# Сгенерировано из settings.py; вручную не редактировать.
import os

DEFAULTS = {
    "ASR_API_MODEL": "gpt-transcribe",
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
    "ASR_REVIEW_MIN_CONFIDENCE": "0.85",
    "ASR_REVIEW_MIN_TOKEN_PROB": "0.25",
    "ASR_REVIEW_QUIET_DBFS": "-40",
    "ASR_REVIEW_MIN_SNR_DB": "15",
    "ASR_REVIEW_MAX_CLIP_RATIO": "0.01"
}


def getenv(name, fallback=None):
    return os.environ.get(name, DEFAULTS.get(name, fallback))
