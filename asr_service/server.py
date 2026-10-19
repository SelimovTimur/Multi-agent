"""
ASR-микросервис: распознавание ответа ученика через API распознавания
(OpenAI-совместимый /audio/transcriptions).
"""

import os
from asr_settings import getenv
import logging
import asyncio
from concurrent.futures import ThreadPoolExecutor
from contextlib import asynccontextmanager
from typing import Optional

from fastapi import FastAPI, UploadFile, File, Form, HTTPException
from fastapi.responses import JSONResponse
import uvicorn

from asr_runtime import ASRConfig
from asr_api import transcribe_detailed
from asr_review import Recognition, resolve_language
from audio_quality import check_audio, check_transcript, UnclearAudio

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

CONFIG = ASRConfig.from_env()

executor = None


def _api_recognize_answer(audio_bytes: bytes, language: str) -> Recognition:
    """Ответ ученика: с уверенностью и решением о проверке текста.

    Пустой текст — не сбой: в фразе не нашлось речи (шум, вдох). Его
    отклоняет check_transcript ответом 422, как неразборчивую запись.
    """
    return transcribe_detailed(audio_bytes, CONFIG, language=language, allow_empty=True)


@asynccontextmanager
async def lifespan(app: FastAPI):
    global executor
    logger.info("Инициализирую ASR: %s, модель %s", CONFIG.api_base_url, CONFIG.api_model)
    # Запрос к API блокирует — он идёт в пуле потоков, чтобы FastAPI мог
    # обрабатывать другие запросы.
    executor = ThreadPoolExecutor(max_workers=CONFIG.max_workers)
    yield
    logger.info("Останавливаю ASR-сервис")
    if executor is not None:
        executor.shutdown(wait=False)


app = FastAPI(title="ASR Service", lifespan=lifespan)


@app.post("/recognize")
async def recognize(
    file: UploadFile = File(...),
    language: Optional[str] = Form(None),
):
    """
    POST /recognize — принимает WAV-файл, возвращает распознанный текст.

    Go API вызывает этот эндпоинт из asr.go:
        POST http://asr-service:8000/recognize
        Content-Type: multipart/form-data
        Body: file=audio.wav, language=english (необязательно)

    Ответ: {"text": "…", "language": "english", "confidence": 0.93,
            "needs_review": false, "review_reasons": [], "diagnostics": {…}}

    needs_review=true — интерфейс показывает ученику распознанный текст до
    отправки (docs/product/avatar-goals/asr.md).
    """
    if not file.filename:
        raise HTTPException(status_code=400, detail="Файл не указан")
    try:
        lang = resolve_language(language, CONFIG.api_language)
    except ValueError as exc:
        return JSONResponse(status_code=422, content={"text": "", "error": str(exc)})

    try:
        audio_bytes = await file.read()
        if len(audio_bytes) == 0:
            raise HTTPException(status_code=400, detail="Пустой файл")

        check_audio(audio_bytes)

        logger.info(f"Распознаю {file.filename} ({len(audio_bytes)} байт, язык {lang or 'auto'})")

        # Запускаем распознавание в пуле потоков чтобы не блокировать FastAPI
        loop = asyncio.get_running_loop()
        result = await loop.run_in_executor(executor, _api_recognize_answer, audio_bytes, lang)

        check_transcript(result.text)
        # Текст ответа в лог не пишем: это речь ученика.
        logger.info(
            "Распознано: %d символов, уверенность %s, запросов %d, проверка %s",
            len(result.text),
            "нет" if result.confidence is None else f"{result.confidence:.3f}",
            result.attempts,
            ",".join(result.review_reasons) or "не нужна",
        )
        return JSONResponse(result.to_response())

    except UnclearAudio as exc:
        return JSONResponse(status_code=422, content={"text": "", "error": str(exc)})
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Ошибка распознавания: {e}", exc_info=True)
        return JSONResponse(
            status_code=500,
            content={"text": "", "error": str(e)}
        )


@app.get("/health")
async def health():
    """Проверка здоровья — для Docker healthcheck."""
    return {
        "status": "ok",
        "model": CONFIG.api_model,
        "language": CONFIG.api_language,
        # Действующие пороги проверки текста — чтобы сверять калибровку.
        "review": {
            "min_confidence": CONFIG.review_min_confidence,
            "min_token_prob": CONFIG.review_min_token_prob,
            "quiet_dbfs": CONFIG.review_quiet_dbfs,
            "min_snr_db": CONFIG.review_min_snr_db,
            "max_clip_ratio": CONFIG.review_max_clip_ratio,
        },
    }


if __name__ == "__main__":
    port = int(getenv("ASR_PORT"))
    uvicorn.run(app, host="0.0.0.0", port=port, workers=1)
