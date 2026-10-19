# Сгенерировано из settings.py; вручную не редактировать.
import os

DEFAULTS = {
    "OPENROUTER_MODEL": "qwen/qwen3.7-plus",
    "YANDEX_MODEL": "aliceai-llm",
    "AVATAR_MODEL": "",
    "LLM_PROVIDER": "openrouter",
    "OPENROUTER_API_KEY": "",
    "OPENROUTER_BASE_URL": "https://openrouter.ai/api/v1",
    "OPENROUTER_REASONING_ENABLED": "false",
    "YANDEX_FOLDER_ID": "",
    "YANDEX_API_KEY": "",
    "YANDEX_BASE_URL": "https://ai.api.cloud.yandex.net/v1",
    "MAX_CONCURRENT_LLM": "20",
    "LLM_REQUEST_TIMEOUT_SECONDS": "300",
    "LLM_CALL_TIMEOUT_SECONDS": "90",
    "LLM_HTTP_PROXY": "",
    "LLM_BILLING_RATES_JSON": ""
}


def getenv(name, fallback=None):
    return os.environ.get(name, DEFAULTS.get(name, fallback))
