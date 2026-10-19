from __future__ import annotations

from llm_settings import DEFAULTS, getenv
from dataclasses import dataclass


def _env_bool(name: str, default: bool = False) -> bool:
    value = getenv(name)
    if value is None:
        return default
    return value.strip().lower() in {"1", "true", "yes", "on"}


def _env_int(name: str, minimum: int = 1) -> int:
    value = int(getenv(name))
    if value < minimum:
        raise ValueError(f"{name} must be at least {minimum}")
    return value


@dataclass(frozen=True)
class Settings:
    provider: str
    openrouter_key: str
    openrouter_base_url: str
    openrouter_model: str
    openrouter_reasoning_enabled: bool
    yandex_folder_id: str
    yandex_api_key: str
    yandex_base_url: str
    max_concurrent_requests: int
    request_timeout_seconds: int
    # Какая модель в каталоге Яндекса. Кроме Алисы там доступны открытые
    # модели (Qwen, gpt-oss) — они отвечают на том же OpenAI-совместимом
    # пути, поэтому смена сводится к переменной.
    yandex_model: str = DEFAULTS["YANDEX_MODEL"]
    # Общий бюджет одного вызова, включая очередь и сетевые повторы.
    call_timeout_seconds: int = int(DEFAULTS["LLM_CALL_TIMEOUT_SECONDS"])
    # Явный транспортный прокси для локальной сети/VPN; ключ идёт в OpenRouter.
    http_proxy: str = DEFAULTS["LLM_HTTP_PROXY"]
    # Модель движка разговора с аватаром (/v1/conversation/*). Пусто —
    # основная модель. Отдельную задают, когда разговору нужна модель
    # быстрее или дешевле основной.
    avatar_model: str = DEFAULTS["AVATAR_MODEL"]

    @classmethod
    def from_env(cls) -> "Settings":
        settings = cls(
            provider=getenv("LLM_PROVIDER").strip().lower(),
            openrouter_key=getenv("OPENROUTER_API_KEY").strip(),
            openrouter_base_url=getenv("OPENROUTER_BASE_URL").rstrip("/"),
            openrouter_model=getenv("OPENROUTER_MODEL").strip(),
            openrouter_reasoning_enabled=_env_bool("OPENROUTER_REASONING_ENABLED"),
            yandex_folder_id=getenv("YANDEX_FOLDER_ID").strip(),
            yandex_api_key=getenv("YANDEX_API_KEY").strip(),
            yandex_base_url=getenv("YANDEX_BASE_URL").rstrip("/"),

            max_concurrent_requests=_env_int("MAX_CONCURRENT_LLM"),
            request_timeout_seconds=_env_int("LLM_REQUEST_TIMEOUT_SECONDS"),
            yandex_model=getenv("YANDEX_MODEL").strip() or DEFAULTS["YANDEX_MODEL"],
            call_timeout_seconds=_env_int("LLM_CALL_TIMEOUT_SECONDS"),
            http_proxy=getenv("LLM_HTTP_PROXY").strip(),
            avatar_model=getenv("AVATAR_MODEL").strip(),
        )
        settings.validate()
        return settings

    def validate(self) -> None:
        if self.provider == "openrouter":
            if not self.openrouter_key:
                raise ValueError("OPENROUTER_API_KEY is required for openrouter")
            if not self.openrouter_model:
                raise ValueError("OPENROUTER_MODEL must not be empty")
            return
        if self.provider == "yandex":
            if not self.yandex_folder_id or not self.yandex_api_key:
                raise ValueError(
                    "YANDEX_FOLDER_ID and YANDEX_API_KEY are required for yandex"
                )
            return
        raise ValueError(f"unsupported LLM_PROVIDER: {self.provider!r}")

    @property
    def model(self) -> str:
        if self.provider == "openrouter":
            return self.openrouter_model
        return f"gpt://{self.yandex_folder_id}/{self.yandex_model}"
