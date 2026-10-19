# Сгенерировано из settings.py; вручную не редактировать.
import os

DEFAULTS = {
    "TTS_API_MODEL": "gpt-4o-mini-tts",
    "TTS_USE_API": "true",
    "TTS_API_BASE_URL": "https://api.openai.com/v1",
    "TTS_API_KEY": "",
    "OPENAI_API_KEY": "",
    "TTS_API_VOICE": "alloy",
    "TTS_API_RESPONSE_FORMAT": "wav",
    "TTS_API_TIMEOUT_SECONDS": "120",
    "TTS_PORT": "8000",
    "TTS_LANGUAGE": "ru",
    "TTS_MODEL_ID": "v5_cis_base_nostress",
    "TTS_SPEAKER": "tat_marat",
    "TTS_SAMPLE_RATE": "48000",
    "TTS_DEVICE": "cpu",
    "TTS_MAX_WORKERS": "1",
    "TTS_MAX_TEXT_LENGTH": "5000"
}


def getenv(name, fallback=None):
    return os.environ.get(name, DEFAULTS.get(name, fallback))
