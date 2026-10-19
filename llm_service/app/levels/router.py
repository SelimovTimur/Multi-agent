"""HTTP-ручка справочника уровней: /v1/levels/guidance.

Ручка синхронная: справочник лежит в памяти, FastAPI выполнит её в пуле
потоков и не задержит цикл событий.
"""
from __future__ import annotations

from fastapi import APIRouter, HTTPException, Query

from .english import level_guidance, normalize_level

router = APIRouter(prefix="/v1/levels", tags=["levels"])


@router.get("/guidance")
def guidance(
    level: str = Query(max_length=8),
    language: str = Query(default="english", max_length=32),
    topic: str = Query(default="", max_length=5_000),
    max_words: int = Query(default=60, ge=0, le=200),
) -> dict:
    value = normalize_level(level)
    if not value:
        raise HTTPException(status_code=422, detail="Неизвестный уровень: ожидается a1, a2, b1, b2 или c1")
    return {"level": value, "text": level_guidance(value, language, topic_text=topic, max_words=max_words)}
