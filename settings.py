"""Общие настройки проекта. Меняйте значения здесь, затем: python3 settings.py --sync.

Compose подставляет .env и окружение поверх этих значений. Python-сервисы и
Go используют сгенерированные копии; --check запрещает расхождение с ними.
Генерация не читает .env и никогда не записывает реальные ключи в репозиторий.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path


# Все модели API, включая живой разговор и его помощника.
MODELS = {
    "OPENROUTER_MODEL": "qwen/qwen3.7-plus",
    "ASR_API_MODEL": "gpt-transcribe",
    "TTS_API_MODEL": "gpt-4o-mini-tts",
    "LIVE_MODEL": "gpt-live-1",
    "LIVE_BACKEND_MODEL": "gpt-5.4",
    "YANDEX_MODEL": "aliceai-llm",
    # Движок разговора с аватаром (/v1/conversation). Пусто — OPENROUTER_MODEL.
    "AVATAR_MODEL": "",
}

LLM = {
    **{key: MODELS[key] for key in (
        "OPENROUTER_MODEL", "YANDEX_MODEL", "AVATAR_MODEL")},
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
    "LLM_BILLING_RATES_JSON": "",
}

ASR = {
    "ASR_API_MODEL": MODELS["ASR_API_MODEL"],
    "ASR_USE_API": "true",
    "ASR_API_BASE_URL": "https://api.openai.com/v1",
    "ASR_API_KEY": "",
    "OPENAI_API_KEY": "",
    "ASR_API_LANGUAGE": "english",
    "ASR_API_TIMEOUT_SECONDS": "120",
    "ASR_PORT": "8000",
    "ASR_DEVICE": "cpu",
    "ASR_MODEL_FILE": "api",
    "ASR_CHUNK_SECONDS": "20",
    "ASR_MAX_WORKERS": "2",
    # Когда показывать ученику распознанный текст на проверку
    # (docs/product/avatar-goals/asr.md): уверенность модели, тишина, шум, срез.
    "ASR_REVIEW_MIN_CONFIDENCE": "0.85",
    "ASR_REVIEW_MIN_TOKEN_PROB": "0.25",
    "ASR_REVIEW_QUIET_DBFS": "-40",
    "ASR_REVIEW_MIN_SNR_DB": "15",
    "ASR_REVIEW_MAX_CLIP_RATIO": "0.01",
}

TTS = {
    "TTS_API_MODEL": MODELS["TTS_API_MODEL"],
    "TTS_USE_API": "true",
    "TTS_API_BASE_URL": "https://api.openai.com/v1",
    "TTS_API_KEY": "",
    "OPENAI_API_KEY": "",
    "TTS_API_VOICE": "alloy",
    "TTS_API_RESPONSE_FORMAT": "wav",
    "TTS_API_TIMEOUT_SECONDS": "120",
    "TTS_PORT": "8000",
    "TTS_LANGUAGE": "ru",  # Ключ каталога Silero, не язык урока.
    "TTS_MODEL_ID": "v5_cis_base_nostress",
    "TTS_SPEAKER": "tat_marat",
    "TTS_SAMPLE_RATE": "48000",
    "TTS_DEVICE": "cpu",
    "TTS_MAX_WORKERS": "1",
    "TTS_MAX_TEXT_LENGTH": "5000",
}

LIVE = {
    "LIVE_MODEL": MODELS["LIVE_MODEL"],
    "LIVE_BACKEND_MODEL": MODELS["LIVE_BACKEND_MODEL"],
    "LIVE_API_BASE_URL": "https://api.openai.com/v1",
    "LIVE_VOICE": "delta",
    "LIVE_REASONING_EFFORT": "medium",
    # Инструменты делегата живой сессии урока (internal/live/conversation.go):
    # по умолчанию выключены — давали двойные приветствия и прощания.
    "LIVE_CONVERSATION_TOOLS": "false",
}

# Настройки Go API: база, авторизация, почта, хранение, лимиты и пулы.
# Пустые значения для двух флагов ниже сохраняют выбор по APP_ENV:
# AUTH_COOKIE_SECURE и DB_MIGRATE_ON_START.
API = {
    "PORT": "8080",
    "METRICS_ADDR": "127.0.0.1:9091",
    "LLM_SERVICE_URL": "http://llm:8000",
    "ASR_SERVICE_URL": "http://asr:8000",
    "TTS_SERVICE_URL": "http://tts:8000",
    "MAX_CONCURRENT_ASR": "4",
    "MAX_CONCURRENT_TTS": "2",
    "OPENAI_API_KEY": "",
    "APP_ENV": "dev",
    "LOG_FORMAT": "",
    "SMTP_FROM_NAME": "На родном",
    "LOG_LEVEL": "info",
    "AUTH_SESSION_COOKIE": "nr_session",
    "AUTH_COOKIE_DOMAIN": "",
    "AUTH_STUDENT_COOKIE": "nr_student",
    "PUBLIC_URL": "http://localhost:3000",
    "SMTP_PORT": "587",
    "SMTP_TIMEOUT_SEC": "20",
    "DB_MAX_CONNS": "20",
    "DB_MIN_CONNS": "2",
    "DB_CONN_LIFETIME_MIN": "30",
    "DB_CONN_IDLE_MIN": "5",
    "DB_CONNECT_TIMEOUT_SEC": "10",
    "DB_STATEMENT_TIMEOUT_SEC": "30",
    "AUTH_SESSION_TTL_DAYS": "30",
    "AUTH_STUDENT_TTL_DAYS": "180",
    "AUTH_VERIFY_TTL_HOURS": "24",
    "AUTH_RESET_TTL_MIN": "60",
    "AUTH_APPROVE_TTL_DAYS": "30",
    "AUTH_LOGIN_MAX_ATTEMPTS": "5",
    "AUTH_LOGIN_WINDOW_MIN": "15",
    "AUTH_ARGON_TIME": "3",
    "AUTH_ARGON_MEMORY_KB": "65536",
    "AUTH_ARGON_THREADS": "2",
    "AUTH_ARGON_KEYLEN": "32",
    "ML_LLM_CHAT_CONCURRENCY": "16",
    "ML_LLM_BUILD_CONCURRENCY": "8",
    "ML_ASR_CONCURRENCY": "4",
    "ML_TTS_CONCURRENCY": "2",
    "ML_MEDIA_CONCURRENCY": "4",
    "ML_ACQUIRE_TIMEOUT_SEC": "45",
    "ML_COST_IN_MICRO_PER_TOKEN": "90",
    "ML_COST_OUT_MICRO_PER_TOKEN": "360",
    "LLM_SERVICE_TIMEOUT_SECONDS": "900",
    "SMTP_HOST": "",
    "SMTP_FROM": "",
    "SMTP_USERNAME": "",
    "SMTP_PASSWORD": "",
    "DATABASE_URL": "",
    "AUTH_AUTO_APPROVE_EMAILS": "",
    "AUTH_APPROVER_EMAILS": "",
    "TRUSTED_PROXIES": "",
    "SCHOOL_TZ": "Europe/Moscow",
    "AUTH_COOKIE_SECURE": "",
    "DB_MIGRATE_ON_START": "",
    "DB_REQUIRE_VERSION": "true",
    "API_READ_TIMEOUT_SECONDS": "30",
    "API_WRITE_TIMEOUT_SECONDS": "960",
    "API_SHUTDOWN_TIMEOUT_SECONDS": "30",
    "MAX_AUDIO_SECONDS": "300",
}

SERVICES = {"api": {**API, **LIVE}, "llm": LLM, "asr": ASR, "tts": TTS}
ROOT = Path(__file__).resolve().parent


def generated_files() -> dict[Path, str]:
    header = "# Сгенерировано из settings.py; вручную не редактировать.\n"
    outputs = {}
    for service in ("llm", "asr", "tts"):
        defaults = json.dumps(SERVICES[service], ensure_ascii=False, indent=4)
        outputs[ROOT / f"{service}_service/{service}_settings.py"] = (
            header + "import os\n\nDEFAULTS = " + defaults + "\n\n\n"
            "def getenv(name, fallback=None):\n"
            "    return os.environ.get(name, DEFAULTS.get(name, fallback))\n"
        )

    # Go остаётся самостоятельным бинарником: Python при его запуске не нужен.
    go = ["// Code generated by settings.py; DO NOT EDIT.", "package settings", "",
          "// Default возвращает значение из общего каталога настроек.",
          "func Default(name string) string {", "\tswitch name {"]
    for name, value in sorted(SERVICES["api"].items()):
        go += [f"\tcase {json.dumps(name)}:", f"\t\treturn {json.dumps(value, ensure_ascii=False)}"]
    go += ["\tdefault:", '\t\treturn ""', "\t}", "}", ""]
    outputs[ROOT / "internal/platform/settings/defaults_generated.go"] = "\n".join(go)

    # У каждого контейнера только его настройки и ключи, не весь .env.
    compose = [header.rstrip(), "services:"]
    for service, defaults in SERVICES.items():
        compose += [f"  {service}:", "    environment:"]
        for name, value in defaults.items():
            # '-' сохраняет явное пустое значение, например AVATAR_MODEL=.
            compose.append(f"      {name}: " + json.dumps("${" + name + "-" + value + "}", ensure_ascii=False))
    outputs[ROOT / "settings.compose.yml"] = "\n".join(compose) + "\n"
    policy = json.loads((ROOT / "internal/live/session_policy.json").read_text())
    policy["model"] = LIVE["LIVE_MODEL"]
    policy["audio"]["output"]["voice"] = LIVE["LIVE_VOICE"]
    policy["delegation"]["responses"]["model"] = LIVE["LIVE_BACKEND_MODEL"]
    policy["delegation"]["responses"]["reasoning"]["effort"] = LIVE["LIVE_REASONING_EFFORT"]
    outputs[ROOT / "internal/live/session.json"] = json.dumps(policy, ensure_ascii=False, indent=2) + "\n"
    return outputs


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--sync", action="store_true", help="Обновить копии настроек для сервисов")
    action.add_argument("--check", action="store_true", help="Проверить, что копии соответствуют settings.py")
    args = parser.parse_args()
    stale = []
    for path, expected in generated_files().items():
        if path.exists() and path.read_text() == expected:
            continue
        if args.sync:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(expected)
        else:
            stale.append(str(path.relative_to(ROOT)))
    if stale:
        parser.exit(1, "Обновите настройки: python3 settings.py --sync\n" + "\n".join(stale) + "\n")
    print("Настройки синхронизированы." if args.sync else "Настройки согласованы.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
