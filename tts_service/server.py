"""Silero TTS microservice for Tatar speech synthesis."""

from __future__ import annotations

import asyncio
import html
# io нужен audio_to_wav: WAV собирается в памяти. Импорт потерялся при
# слиянии — локальный синтез падал с NameError на первой же реплике.
import io
import logging
from tts_settings import DEFAULTS, getenv
import sys
from concurrent.futures import ThreadPoolExecutor
from contextlib import asynccontextmanager
from dataclasses import dataclass
from typing import Any

import httpx
import numpy as np
import soundfile as sf
import uvicorn
from fastapi import FastAPI, HTTPException
from fastapi.responses import Response
from pydantic import BaseModel, Field


logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

SUPPORTED_SAMPLE_RATES = {8_000, 24_000, 48_000}

TTS_INSTRUCTIONS = """Speak naturally in the Tatar language.
Use native Tatar pronunciation.
Do not pronounce the text as Russian.
Pay particular attention to the Tatar letters ә, ө, ү, җ, ң, һ.
Positive and friendly educational narration."""


def _env_int(name: str, minimum: int = 1) -> int:
    raw = getenv(name)
    try:
        value = int(raw)
    except ValueError as exc:
        raise ValueError(f"{name} должен быть целым числом, получено {raw!r}") from exc
    if value < minimum:
        raise ValueError(f"{name} должен быть не меньше {minimum}")
    return value


def _env_bool(name: str, default: bool = False) -> bool:
    raw = getenv(name)
    if raw is None:
        return default
    return raw.strip().lower() in {"1", "true", "yes", "on", "y"}


@dataclass(frozen=True)
class TTSConfig:
    language: str
    model_id: str
    speaker: str
    sample_rate: int
    device: str
    max_workers: int
    max_text_length: int
    use_api: bool = DEFAULTS["TTS_USE_API"] == "true"
    api_base_url: str = DEFAULTS["TTS_API_BASE_URL"]
    api_model: str = DEFAULTS["TTS_API_MODEL"]
    api_key: str = ""
    api_voice: str = DEFAULTS["TTS_API_VOICE"]
    api_response_format: str = DEFAULTS["TTS_API_RESPONSE_FORMAT"]
    api_timeout_seconds: int = int(DEFAULTS["TTS_API_TIMEOUT_SECONDS"])

    @classmethod
    def from_env(cls) -> "TTSConfig":
        use_api = _env_bool("TTS_USE_API")
        api_key = getenv("TTS_API_KEY").strip()
        if not api_key:
            api_key = getenv("OPENAI_API_KEY").strip()
        config = cls(
            # Ключ каталога Silero, а не язык урока: модель v5_cis_base_nostress
            # лежит под 'ru' (latest_silero_models.yml) и уже содержит татарские
            # голоса — сам голос задаётся через TTS_SPEAKER. Умолчание 'tat'
            # каталогу неизвестно, и загрузка модели с ним не находит пакет.
            language=getenv("TTS_LANGUAGE").strip(),
            model_id=getenv("TTS_MODEL_ID").strip(),
            speaker=getenv("TTS_SPEAKER").strip(),
            sample_rate=_env_int("TTS_SAMPLE_RATE"),
            device=getenv("TTS_DEVICE").strip().lower(),
            max_workers=_env_int("TTS_MAX_WORKERS"),
            max_text_length=_env_int("TTS_MAX_TEXT_LENGTH"),
            use_api=use_api,
            api_base_url=getenv("TTS_API_BASE_URL").strip().rstrip("/"),
            api_model=getenv("TTS_API_MODEL").strip(),
            api_key=api_key,
            api_voice=getenv("TTS_API_VOICE").strip(),
            api_response_format=getenv("TTS_API_RESPONSE_FORMAT").strip(),
            api_timeout_seconds=_env_int("TTS_API_TIMEOUT_SECONDS"),
        )
        if config.use_api:
            if not config.api_key:
                raise ValueError("TTS_API_KEY или OPENAI_API_KEY должен быть задан")
            if not config.api_model:
                raise ValueError("TTS_API_MODEL не должен быть пустым")
            if not config.api_response_format:
                raise ValueError("TTS_API_RESPONSE_FORMAT не должен быть пустым")
            if config.api_timeout_seconds <= 0:
                raise ValueError("TTS_API_TIMEOUT_SECONDS должен быть положительным")
            if not config.api_voice:
                raise ValueError("TTS_API_VOICE не должен быть пустым")
            return config
        if not config.language:
            raise ValueError("TTS_LANGUAGE не должен быть пустым")
        if not config.model_id:
            raise ValueError("TTS_MODEL_ID не должен быть пустым")
        if not config.speaker:
            raise ValueError("TTS_SPEAKER не должен быть пустым")
        if config.sample_rate not in SUPPORTED_SAMPLE_RATES:
            supported = ", ".join(str(rate) for rate in sorted(SUPPORTED_SAMPLE_RATES))
            raise ValueError(f"TTS_SAMPLE_RATE должен быть одним из: {supported}")
        if config.device != "auto" and not (
            config.device == "cpu" or config.device.startswith("cuda")
        ):
            raise ValueError("TTS_DEVICE должен быть cpu, cuda, cuda:N или auto")
        return config


class SynthesizeRequest(BaseModel):
    text: str = Field(..., description="Text to synthesize")
    # Тон разговора, заданный учителем (NR-29). Silero им не управляет —
    # у локальной модели нет ручки стиля, — а внешний синтез принимает его
    # подсказкой и звучит ближе к роли: рыночный торговец и строгий
    # экзаменатор читают один и тот же текст по-разному.
    style: str = Field(default="", max_length=400)


def resolve_device(requested: str) -> Any:
    """
    Устройство для Silero.

    torch импортируется здесь, а не наверху файла: при TTS_USE_API веса
    не грузятся, синтез идёт внешним API, и образу torch не нужен вовсе —
    полтора гигабайта на сервере, где их просто нет.
    """
    import torch

    if requested == "auto":
        return torch.device("cuda" if torch.cuda.is_available() else "cpu")
    device = torch.device(requested)
    if device.type == "cuda" and not torch.cuda.is_available():
        raise RuntimeError("TTS_DEVICE запрашивает CUDA, но torch.cuda.is_available() == False")
    return device


def load_model(config: TTSConfig) -> tuple[Any, Any]:
    # Import is deliberately delayed: health/unit tooling can import this module
    # without downloading or initializing Silero.
    from silero import silero_tts

    device = resolve_device(config.device)
    logger.info(
        "Загружаю Silero TTS %s (language=%s, speaker=%s, device=%s)",
        config.model_id,
        config.language,
        config.speaker,
        device,
    )
    model, _ = silero_tts(language=config.language, speaker=config.model_id)
    model.to(device)
    if hasattr(model, "eval"):
        model.eval()
    return model, device


def audio_to_wav(audio: Any, sample_rate: int) -> bytes:
    # Тензор бывает только от локальной модели. В API-режиме torch в образе
    # нет вовсе, поэтому проверяем тип, только если пакет уже загружен, —
    # импортировать его здесь значило бы вернуть полтора гигабайта обратно.
    torch = sys.modules.get("torch")
    if torch is not None and isinstance(audio, torch.Tensor):
        samples = audio.detach().to(device="cpu", dtype=torch.float32).numpy()
    else:
        samples = np.asarray(audio, dtype=np.float32)

    samples = np.squeeze(samples)
    if samples.ndim != 1 or samples.size == 0:
        raise RuntimeError(f"Silero вернул аудио неправильной формы: {samples.shape}")

    output = io.BytesIO()
    sf.write(output, samples, sample_rate, format="WAV", subtype="PCM_16")
    return output.getvalue()


def build_ssml(text: str) -> str:
    """Convert newline-separated phrases to Silero SSML with short pauses."""
    phrases = [line.strip() for line in text.splitlines() if line.strip()]
    escaped_phrases = [html.escape(phrase, quote=False) for phrase in phrases]
    separator = '\n    <break time="200ms"/>\n    '
    return f"<speak>\n    {separator.join(escaped_phrases)}\n</speak>"


def synthesize_wav(model: Any, config: TTSConfig, text: str) -> bytes:
    # Локальный синтез — единственное место, где torch действительно нужен.
    import torch

    ssml_text = build_ssml(text)
    with torch.inference_mode():
        audio = model.apply_tts(
            ssml_text=ssml_text,
            speaker=config.speaker,
            sample_rate=config.sample_rate,
        )
    return audio_to_wav(audio, config.sample_rate)


def _instructions(style: str) -> str:
    """Общие правила произношения плюс тон от учителя, если он задан."""
    style = style.strip()
    if not style:
        return TTS_INSTRUCTIONS
    return TTS_INSTRUCTIONS + "\nTone set by the teacher: " + style


def _synthesize_audio_api(text: str, style: str = "") -> bytes:
    if not CONFIG.api_key:
        raise RuntimeError("Ключ API для TTS не найден. Укажите TTS_API_KEY или OPENAI_API_KEY")
    if not CONFIG.api_model:
        raise RuntimeError("TTS_API_MODEL не должен быть пустым")
    if CONFIG.api_timeout_seconds <= 0:
        raise RuntimeError("TTS_API_TIMEOUT_SECONDS должен быть положительным")

    payload = {
        "model": CONFIG.api_model,
        "input": text,
        "voice": CONFIG.api_voice,
        "instructions": _instructions(style),
        "response_format": CONFIG.api_response_format,
    }
    try:
        # Короткий предел подключения не даёт TLS зависнуть на весь бюджет
        # синтеза. POST не повторяем: ответ мог потеряться уже после оплаты.
        with httpx.Client(timeout=httpx.Timeout(CONFIG.api_timeout_seconds, connect=10)) as client:
            resp = client.post(
                f"{CONFIG.api_base_url}/audio/speech",
                json=payload,
                headers={"Authorization": f"Bearer {CONFIG.api_key}"},
            )
            resp.raise_for_status()
            return resp.content
    except httpx.HTTPStatusError as exc:
        raise RuntimeError(f"TTS API вернул статус {exc.response.status_code}") from exc
    except httpx.RequestError as exc:
        raise RuntimeError(f"Ошибка соединения с TTS API: {type(exc).__name__}") from exc


CONFIG = TTSConfig.from_env()
model: Any | None = None
# Устройство известно только после загрузки модели, а её тип — torch.device.
# Аннотация здесь строкой: в API-режиме torch не импортируется вовсе.
resolved_device: Any | None = None
executor: ThreadPoolExecutor | None = None


@asynccontextmanager
async def lifespan(_: FastAPI):
    global model, resolved_device, executor
    executor = ThreadPoolExecutor(
        max_workers=CONFIG.max_workers,
        thread_name_prefix="silero-tts",
    )
    if CONFIG.use_api:
        logger.info(
            "TTS API-режим активен: model=%s voice=%s",
            CONFIG.api_model,
            CONFIG.api_voice,
        )
    else:
        model, resolved_device = load_model(CONFIG)
    try:
        yield
    finally:
        logger.info("Останавливаю TTS-сервис")
        if executor is not None:
            executor.shutdown(wait=False, cancel_futures=True)
        executor = None
        model = None
        resolved_device = None


app = FastAPI(title="TTS Service", version="1.0.0", lifespan=lifespan)


@app.post(
    "/synthesize",
    response_class=Response,
    responses={200: {"content": {"audio/wav": {}}}},
)
async def synthesize(request: SynthesizeRequest) -> Response:
    text = request.text.strip()
    if not text:
        raise HTTPException(status_code=400, detail="Текст не должен быть пустым")
    if len(text) > CONFIG.max_text_length:
        raise HTTPException(
            status_code=413,
            detail=f"Текст длиннее допустимых {CONFIG.max_text_length} символов",
        )
    if executor is None:
        raise HTTPException(status_code=503, detail="TTS-сервис ещё не готов")
    if (not CONFIG.use_api) and model is None:
        raise HTTPException(status_code=503, detail="TTS-модель ещё не готова")

    try:
        loop = asyncio.get_running_loop()
        if CONFIG.use_api:
            wav = await loop.run_in_executor(executor, _synthesize_audio_api, text, request.style)
        else:
            wav = await loop.run_in_executor(executor, synthesize_wav, model, CONFIG, text)
    except Exception as exc:
        logger.exception("Ошибка синтеза речи")
        raise HTTPException(status_code=500, detail=f"Ошибка синтеза речи: {exc}") from exc

    response_format = CONFIG.api_response_format if CONFIG.use_api else "wav"
    media_type = f"audio/{response_format}" if response_format != "wav" else "audio/wav"
    return Response(
        content=wav,
        media_type=media_type,
        headers={
            "Content-Disposition": 'inline; filename="speech.wav"',
            "X-TTS-Speaker": CONFIG.api_voice if CONFIG.use_api else CONFIG.speaker,
            "X-TTS-Mode": "api" if CONFIG.use_api else "local",
            "X-TTS-Model": CONFIG.api_model if CONFIG.use_api else CONFIG.model_id,
        },
    )


def speakers_of(model: Any | None) -> list[str]:
    """
    Голоса, которые есть в загруженной модели.

    Узнать их иначе неоткуда: в каталоге `latest_silero_models.yml` лежат
    только пакеты, а имена голосов — внутри самого пакета. Поле `speakers`
    объявляет не всякая сборка, и его отсутствие — не повод отвечать отказом
    на проверку здоровья: пустой список честнее ошибки.
    """
    names = getattr(model, "speakers", None) or []
    return [str(name) for name in names]


@app.get("/health")
async def health() -> dict[str, object]:
    ready = executor is not None and (CONFIG.use_api or model is not None)
    return {
        "status": "ok" if ready else "starting",
        "ready": ready,
        "mode": "api" if CONFIG.use_api else "local",
        "language": CONFIG.language,
        "model": CONFIG.api_model if CONFIG.use_api else CONFIG.model_id,
        "speaker": CONFIG.api_voice if CONFIG.use_api else CONFIG.speaker,
        # Пакет v5_cis_base_nostress держит голоса сразу нескольких наших
        # языков, поэтому смена голоса — одно имя отсюда, без пересборки образа.
        "speakers": [] if CONFIG.use_api else speakers_of(model),
        "sample_rate": CONFIG.sample_rate if not CONFIG.use_api else None,
        "device": str(resolved_device or CONFIG.device),
    }


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=_env_int("TTS_PORT"), workers=1)
