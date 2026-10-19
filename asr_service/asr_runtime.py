"""
Общие конфиги и базовые утилиты для ASR-моделей.

Этот модуль нужен, чтобы server.py и eval использовали один и тот же
набор настроек и одинаковую базовую обработку аудио.
"""

from __future__ import annotations

import io
import os
from asr_settings import DEFAULTS, getenv
from dataclasses import dataclass, replace

import numpy as np
import soundfile as sf


DEFAULT_MODEL_FILE = DEFAULTS["ASR_MODEL_FILE"]
DEFAULT_CHUNK_SECONDS = int(DEFAULTS["ASR_CHUNK_SECONDS"])
DEFAULT_MAX_WORKERS = int(DEFAULTS["ASR_MAX_WORKERS"])

# Пороги, после которых ученик видит распознанный текст до отправки.
# Замеры на gpt-transcribe: чистые короткие фразы дают среднюю вероятность
# токена 0.90–1.00 и минимальную 0.5–1.0, трудная живая речь — 0.83 и 0.29.
# Тихая речь — уровень громких кадров записи ниже порога в дБ от полной шкалы.
DEFAULT_REVIEW_MIN_CONFIDENCE = float(DEFAULTS["ASR_REVIEW_MIN_CONFIDENCE"])
DEFAULT_REVIEW_MIN_TOKEN_PROB = float(DEFAULTS["ASR_REVIEW_MIN_TOKEN_PROB"])
DEFAULT_REVIEW_QUIET_DBFS = float(DEFAULTS["ASR_REVIEW_QUIET_DBFS"])
# Сигнал записи (калибровка — docs/product/avatar-goals/asr.md): белый шум
# 5 дБ даёт оценку 8–14.5 дБ, чистая речь — от 18, телефон в тихом классе —
# от 31. Срез у чистых записей — 0%, под вещательным ограничителем — до 0.2%,
# клиппинг браузера после Opus — от 2.5%.
DEFAULT_REVIEW_MIN_SNR_DB = float(DEFAULTS["ASR_REVIEW_MIN_SNR_DB"])
DEFAULT_REVIEW_MAX_CLIP_RATIO = float(DEFAULTS["ASR_REVIEW_MAX_CLIP_RATIO"])


def default_device() -> str:
    """
    Возвращает дефолтный device, как и раньше в server.py.

    torch импортируется внутри функции: в режиме внешнего распознавания
    (ASR_USE_API) локальная модель не загружается вовсе, и тащить в
    образ полтора гигабайта torch ради одной проверки видеокарты незачем.
    """
    import torch

    if torch.cuda.is_available():
        return "cuda"
    return "cpu"


def _env_bool(name: str, default: bool = False) -> bool:
    raw = getenv(name)
    if raw is None:
        return default
    return raw.strip().lower() in {"1", "true", "yes", "on", "y"}


def _env_int(name: str, minimum: int = 1) -> int:
    raw = getenv(name)
    try:
        value = int(raw)
    except ValueError as exc:
        raise ValueError(f"{name} должен быть целым числом, получено {raw!r}") from exc
    if value < minimum:
        raise ValueError(f"{name} должен быть не меньше {minimum}")
    return value


def _env_float(name: str, default: float, minimum: float, maximum: float) -> float:
    raw = (getenv(name) or "").strip()
    if not raw:
        return default
    try:
        value = float(raw)
    except ValueError as exc:
        raise ValueError(f"{name} должен быть числом, получено {raw!r}") from exc
    # NaN не проходит ни одно сравнение и молча выключил бы проверку.
    if not minimum <= value <= maximum:
        raise ValueError(f"{name} должен быть в пределах от {minimum} до {maximum}")
    return value


@dataclass(frozen=True)
class ASRConfig:
    # Умолчание — строка, а не вызов: значение поля датакласса вычисляется
    # при импорте модуля, и вызов здесь тянул бы torch всегда.
    device: str = "cpu"
    chunk_seconds: int = DEFAULT_CHUNK_SECONDS
    max_workers: int = DEFAULT_MAX_WORKERS
    model_file: str = DEFAULT_MODEL_FILE
    use_api: bool = DEFAULTS["ASR_USE_API"] == "true"
    api_base_url: str = DEFAULTS["ASR_API_BASE_URL"]
    api_model: str = DEFAULTS["ASR_API_MODEL"]
    api_key: str = ""
    api_language: str = DEFAULTS["ASR_API_LANGUAGE"]
    api_timeout_seconds: int = int(DEFAULTS["ASR_API_TIMEOUT_SECONDS"])
    review_min_confidence: float = DEFAULT_REVIEW_MIN_CONFIDENCE
    review_min_token_prob: float = DEFAULT_REVIEW_MIN_TOKEN_PROB
    review_quiet_dbfs: float = DEFAULT_REVIEW_QUIET_DBFS
    review_min_snr_db: float = DEFAULT_REVIEW_MIN_SNR_DB
    review_max_clip_ratio: float = DEFAULT_REVIEW_MAX_CLIP_RATIO

    @classmethod
    def from_env(cls, **overrides: object) -> "ASRConfig":
        """Читает настройки из env и позволяет точечно переопределить их из CLI."""
        use_api = _env_bool("ASR_USE_API")
        if "ASR_USE_API" not in os.environ and "ASR_USE_PROXYAPI" in os.environ:
            use_api = _env_bool("ASR_USE_PROXYAPI")
        api_key = getenv("ASR_API_KEY").strip()
        if not api_key:
            api_key = getenv("OPENAI_API_KEY").strip()
        cfg = cls(
            # Устройство выбираем только когда модель действительно грузится.
            device=(
                "cpu" if use_api else (getenv("ASR_DEVICE") or default_device())
            ),
            chunk_seconds=_env_int("ASR_CHUNK_SECONDS"),
            max_workers=_env_int("ASR_MAX_WORKERS"),
            model_file=getenv("ASR_MODEL_FILE"),
            use_api=use_api,
            api_base_url=getenv("ASR_API_BASE_URL").strip(),
            api_model=getenv("ASR_API_MODEL").strip(),
            api_key=api_key,
            api_language=getenv("ASR_API_LANGUAGE").strip(),
            api_timeout_seconds=_env_int("ASR_API_TIMEOUT_SECONDS"),
            review_min_confidence=_env_float(
                "ASR_REVIEW_MIN_CONFIDENCE", DEFAULT_REVIEW_MIN_CONFIDENCE, 0.0, 1.0
            ),
            review_min_token_prob=_env_float(
                "ASR_REVIEW_MIN_TOKEN_PROB", DEFAULT_REVIEW_MIN_TOKEN_PROB, 0.0, 1.0
            ),
            review_quiet_dbfs=_env_float(
                "ASR_REVIEW_QUIET_DBFS", DEFAULT_REVIEW_QUIET_DBFS, -120.0, 0.0
            ),
            # 0 выключает проверку шума, 1 — проверку клиппинга.
            review_min_snr_db=_env_float(
                "ASR_REVIEW_MIN_SNR_DB", DEFAULT_REVIEW_MIN_SNR_DB, 0.0, 100.0
            ),
            review_max_clip_ratio=_env_float(
                "ASR_REVIEW_MAX_CLIP_RATIO", DEFAULT_REVIEW_MAX_CLIP_RATIO, 0.0, 1.0
            ),
        )
        # CLI выбирает режим явно, поэтому проверяем уже итоговую конфигурацию.
        for key, value in overrides.items():
            if value is not None:
                cfg = replace(cfg, **{key: value})
        if cfg.use_api:
            if not cfg.api_key:
                raise ValueError("ASR_API_KEY или OPENAI_API_KEY должен быть задан")
            if not cfg.api_model:
                raise ValueError("ASR_API_MODEL не должен быть пустым")
            if cfg.api_timeout_seconds <= 0:
                raise ValueError("ASR_API_TIMEOUT_SECONDS должен быть положительным")
        return cfg


def read_audio_bytes(audio_bytes: bytes) -> tuple[np.ndarray, int]:
    """Читает WAV-байты так же, как раньше делал server.py."""
    audio, sample_rate = sf.read(io.BytesIO(audio_bytes), dtype="float32")
    return normalize_audio(audio), sample_rate


def normalize_audio(audio: np.ndarray) -> np.ndarray:
    """
    Приводит аудио к float32 mono.

    Стерео → моно делаем так же, как в текущем server.py: среднее по axis=1.
    """
    audio = np.asarray(audio, dtype="float32")
    if len(audio.shape) > 1:
        audio = np.mean(audio, axis=1)
    return audio


class BaseASRModel:
    """Базовый класс для конкретных реализаций ASR-моделей."""

    def __init__(self, config: ASRConfig):
        self.config = config

    def transcribe_bytes(self, audio_bytes: bytes) -> str:
        audio, sample_rate = read_audio_bytes(audio_bytes)
        return self.transcribe_array(audio, sample_rate)

    def transcribe_array(self, audio: np.ndarray, sample_rate: int) -> str:
        raise NotImplementedError
