from __future__ import annotations

import asyncio
import logging
import time
from collections.abc import Sequence
from typing import Any, Protocol

import httpx

from .telemetry import provider_call
from .config import Settings
from .schemas import ChatMessage, LLMResult


class ProviderError(RuntimeError):
    def __init__(self, message: str, code: str = "provider_error"):
        super().__init__(message)
        self.code = code


# Сколько раз пробовать при временном отказе провайдера и с какой начальной
# паузой. Три попытки с паузами 0.5 и 1 секунда: этого хватает, чтобы
# пережить короткий сбой, и мало, чтобы не растянуть отказ на минуты.
RETRY_ATTEMPTS = 3
RETRY_BASE_DELAY = 0.5
logger = logging.getLogger("uvicorn.error")


class LLMProvider(Protocol):
    async def complete(
        self,
        messages: Sequence[ChatMessage],
        *,
        response_format: dict[str, Any] | None = None,
        tools: list[dict[str, Any]] | None = None,
        model: str | None = None,
        reasoning_max_tokens: int | None = None,
        reasoning_enabled: bool | None = None,
    ) -> LLMResult: ...


class OpenAICompatibleProvider:
    def __init__(self, settings: Settings, client: httpx.AsyncClient | None = None):
        self.settings = settings
        self._client = client or httpx.AsyncClient(
            timeout=httpx.Timeout(settings.request_timeout_seconds, connect=10, pool=10),
            limits=httpx.Limits(max_connections=settings.max_concurrent_requests,
                               max_keepalive_connections=settings.max_concurrent_requests,
                               keepalive_expiry=120),
            proxy=settings.http_proxy or None,
            trust_env=False,
        )
        self._owns_client = client is None
        self._semaphore = asyncio.Semaphore(settings.max_concurrent_requests)

    async def close(self) -> None:
        if self._owns_client:
            await self._client.aclose()

    async def complete(
        self,
        messages: Sequence[ChatMessage],
        *,
        response_format: dict[str, Any] | None = None,
        tools: list[dict[str, Any]] | None = None,
        model: str | None = None,
        reasoning_max_tokens: int | None = None,
        reasoning_enabled: bool | None = None,
    ) -> LLMResult:
        body: dict[str, Any] = {
            # model позволяет сходить не основной моделью, а специальной —
            # например поисковой для исследования темы.
            "model": model or self.settings.model,
            "messages": [message.model_dump() for message in messages],
        }
        if response_format is not None:
            body["response_format"] = response_format
        if tools:
            body["tools"] = tools
            body["max_tool_calls"] = sum(tool.get("parameters", {}).get("max_uses", 1) for tool in tools)
            # Веб-запрос также возвращает предложения урока и ссылки для
            # каждого факта; прежнего бюджета только на URL не хватает.
            body["max_tokens"] = 8192

        if self.settings.provider == "openrouter":
            url = f"{self.settings.openrouter_base_url}/chat/completions"
            headers = {"Authorization": f"Bearer {self.settings.openrouter_key}"}
            body["reasoning"] = {
                "enabled": self.settings.openrouter_reasoning_enabled if reasoning_enabled is None else reasoning_enabled
            }
            if reasoning_max_tokens is not None:
                body["reasoning"] = {"enabled": True, "max_tokens": reasoning_max_tokens}
            if response_format is not None:
                body["provider"] = {"require_parameters": True}
        else:
            if tools:
                raise ProviderError("web tools are unavailable for the yandex provider")
            url = f"{self.settings.yandex_base_url}/chat/completions"
            headers = {
                "Authorization": f"Api-Key {self.settings.yandex_api_key}",
                "x-folder-id": self.settings.yandex_folder_id,
            }

        started = time.monotonic()
        try:
            async with asyncio.timeout(self.settings.call_timeout_seconds):
                async with self._semaphore:
                    response = await self._post_with_retry(url, headers, body)
        except TimeoutError as exc:
            raise ProviderError(
                f"Провайдер модели не ответил за {self.settings.call_timeout_seconds} секунд. "
                "Попробуйте позже.", code="provider_timeout"
            ) from exc
        finally:
            logger.info("provider model=%s elapsed_seconds=%.2f", body["model"], time.monotonic()-started)

        try:
            payload = response.json()
        except ValueError as exc:
            raise ProviderError("Провайдер вернул некорректный JSON", code="provider_response") from exc
        if not isinstance(payload, dict):
            raise ProviderError("Провайдер вернул неверный формат ответа", code="provider_response")
        choices = payload.get("choices") or []
        if not choices:
            raise ProviderError("LLM returned no choices")
        if not isinstance(choices, list) or not isinstance(choices[0], dict):
            raise ProviderError("Провайдер вернул неверный формат вариантов ответа", code="provider_response")
        message = choices[0].get("message") or {}
        usage = payload.get("usage") or {}
        if not isinstance(message, dict) or not isinstance(usage, dict):
            raise ProviderError("Провайдер вернул неверный формат сообщения", code="provider_response")
        details = usage.get("completion_tokens_details") or {}
        if not isinstance(details, dict):
            details = {}
        logger.info("provider model=%s prompt_tokens=%s completion_tokens=%s reasoning_tokens=%s",
                    body["model"], usage.get("prompt_tokens"), usage.get("completion_tokens"),
                    details.get("reasoning_tokens"))
        content = message.get("content")
        if not isinstance(content, str) or not content.strip():
            raise ProviderError("LLM returned empty content")
        lowered = content.lower()
        refusal_markers = (
            "не могу обсуждать эту тему",
            "не могу обработать",
            "не могу помочь с этим",
        )
        if any(marker in lowered for marker in refusal_markers):
            raise ProviderError(f"LLM refused the request: {content}")
        return LLMResult(
            content=content,
            annotations=message.get("annotations") or [],
            usage=usage,
        )

    async def _post_with_retry(
        self, url: str, headers: dict[str, str], body: dict[str, Any]
    ) -> httpx.Response:
        """Запрос к провайдеру с повторами на временные отказы.

        Провайдер иногда отвечает 5xx или обрывает соединение без всякой
        причины: наблюдалось, что один и тот же запрос падал за несколько
        секунд, а его точный повтор проходил. Без повтора ученик видел бы
        «аватар не смог ответить» на ровном месте.

        Повторяем только то, что имеет смысл повторять: 5xx, 429 и сетевые
        ошибки. 4xx — это наша ошибка в запросе, и повтор её не исправит.
        """
        last_error: Exception | None = None
        for attempt in range(RETRY_ATTEMPTS):
            started = time.monotonic()
            response = None
            outcome = "network_error"
            try:
                response = await self._client.post(url, headers=headers, json=body)
                outcome = str(response.status_code)
                response.raise_for_status()
                return response
            except httpx.HTTPStatusError as exc:
                status = exc.response.status_code
                error_kind = f"HTTP_{status}"
                if status == 402:
                    raise ProviderError(
                        "Недостаточно средств на балансе провайдера генерации.",
                        code="provider_balance",
                    ) from exc
                if status < 500 and status != 429:
                    raise ProviderError(
                        f"LLM returned {status}: {exc.response.text}"
                    ) from exc
                last_error = ProviderError(
                    f"LLM returned {status}: {exc.response.text}"
                )
            except httpx.ReadTimeout as exc:
                outcome = "timeout"
                # Запрос уже отправлен: повтор после долгого ожидания может
                # запустить вторую платную генерацию того же материала.
                raise ProviderError("Провайдер модели не прислал ответ вовремя. Попробуйте позже.",
                                    code="provider_timeout") from exc
            except httpx.HTTPError as exc:
                if isinstance(exc, httpx.TimeoutException):
                    outcome = "timeout"
                error_kind = type(exc).__name__
                last_error = ProviderError(
                    f"Не удалось связаться с провайдером модели ({type(exc).__name__}). "
                    "Проверьте подключение и повторите позже.", code="provider_connection"
                )

            except asyncio.CancelledError:
                outcome = "cancelled"
                raise
            finally:
                provider_call(self.settings.provider, body["model"], response, time.monotonic()-started, outcome)

            logger.warning("provider model=%s attempt=%s elapsed_seconds=%.2f error=%s",
                           body["model"], attempt+1, time.monotonic()-started,
                           error_kind)

            if attempt < RETRY_ATTEMPTS - 1:
                # Пауза растёт, чтобы не добивать провайдера, которому и так
                # плохо. Лишняя секунда ожидания дешевле потерянной реплики.
                await asyncio.sleep(RETRY_BASE_DELAY * (2**attempt))

        assert last_error is not None
        raise last_error
