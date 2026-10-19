"""Повторы при временных отказах провайдера.

Провайдер иногда отвечает 5xx или обрывает соединение без причины. Для
учителя это выглядит как «урок не собрался» на ровном месте, поэтому такие
отказы обязаны переживаться повтором. А вот 4xx повторять бессмысленно —
это ошибка в нашем запросе, и вторая попытка её не исправит.
"""

import unittest
import asyncio
from dataclasses import replace

import httpx

from app.config import Settings
from app.provider import OpenAICompatibleProvider, ProviderError
from app.schemas import ChatMessage


def settings() -> Settings:
    return Settings(
        provider="openrouter",
        openrouter_key="key",
        openrouter_base_url="https://example.test/v1",
        openrouter_model="test-model",
        openrouter_reasoning_enabled=False,
        yandex_folder_id="",
        yandex_api_key="",
        yandex_base_url="https://yandex.test/v1",
        max_concurrent_requests=2,
        request_timeout_seconds=5,
    )


def ok_response() -> httpx.Response:
    return httpx.Response(200, json={"choices": [{"message": {"content": "готово"}}]})


class RetryTests(unittest.IsolatedAsyncioTestCase):
    async def test_insufficient_balance_is_not_retried(self):
        calls = 0
        async def handler(request):
            nonlocal calls
            calls += 1
            return httpx.Response(402, text="private billing details")
        with self.assertRaises(ProviderError) as failure:
            await self._run(handler)
        self.assertEqual(calls, 1)
        self.assertEqual(failure.exception.code, 'provider_balance')
        self.assertNotIn('private', str(failure.exception))

    async def test_read_timeout_does_not_repeat_generation(self):
        calls = 0
        async def handler(request):
            nonlocal calls
            calls += 1
            raise httpx.ReadTimeout("", request=request)
        with self.assertRaisesRegex(ProviderError, "не прислал ответ"):
            await self._run(handler)
        self.assertEqual(calls, 1)

    async def test_total_budget_stops_slow_request(self):
        async def handler(request):
            await asyncio.sleep(1)
            return ok_response()
        async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
            provider = OpenAICompatibleProvider(replace(settings(), call_timeout_seconds=0.02), client)
            with self.assertRaisesRegex(ProviderError, "не ответил за"):
                await provider.complete([ChatMessage(role="user", content="тест")])

    async def _run(self, handler) -> tuple:
        client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
        provider = OpenAICompatibleProvider(settings(), client=client)
        # Пауза между попытками не нужна тесту — только их количество.
        provider_module = __import__("app.provider", fromlist=["provider"])
        original = provider_module.RETRY_BASE_DELAY
        provider_module.RETRY_BASE_DELAY = 0
        try:
            return await provider.complete([ChatMessage(role="user", content="тест")])
        finally:
            provider_module.RETRY_BASE_DELAY = original
            await client.aclose()

    async def test_recovers_after_transient_5xx(self):
        calls = {"n": 0}

        async def handler(_: httpx.Request) -> httpx.Response:
            calls["n"] += 1
            if calls["n"] == 1:
                return httpx.Response(502, text="bad gateway")
            return ok_response()

        result = await self._run(handler)
        self.assertEqual(result.content, "готово")
        self.assertEqual(calls["n"], 2, "второй попытки не было")

    async def test_recovers_after_network_error(self):
        calls = {"n": 0}

        async def handler(request: httpx.Request) -> httpx.Response:
            calls["n"] += 1
            if calls["n"] == 1:
                raise httpx.ConnectError("соединение оборвалось", request=request)
            return ok_response()

        result = await self._run(handler)
        self.assertEqual(result.content, "готово")
        self.assertEqual(calls["n"], 2)

    async def test_gives_up_after_three_attempts(self):
        calls = {"n": 0}

        async def handler(_: httpx.Request) -> httpx.Response:
            calls["n"] += 1
            return httpx.Response(503, text="unavailable")

        with self.assertRaises(ProviderError):
            await self._run(handler)
        self.assertEqual(calls["n"], 3, "число попыток должно быть ограничено")

    async def test_does_not_retry_client_errors(self):
        calls = {"n": 0}

        async def handler(_: httpx.Request) -> httpx.Response:
            calls["n"] += 1
            return httpx.Response(400, text="bad request")

        with self.assertRaises(ProviderError):
            await self._run(handler)
        self.assertEqual(calls["n"], 1, "4xx повторять бессмысленно")

    async def test_retries_rate_limit(self):
        calls = {"n": 0}

        async def handler(_: httpx.Request) -> httpx.Response:
            calls["n"] += 1
            if calls["n"] < 3:
                return httpx.Response(429, text="slow down")
            return ok_response()

        result = await self._run(handler)
        self.assertEqual(result.content, "готово")
        self.assertEqual(calls["n"], 3)
