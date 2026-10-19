"""Ручки движка разговора `/v1/conversation/*` (договор, разделы 4 и 10.1).

Сбой провайдера отдаёт общий обработчик ProviderError из `app/main.py`
(502 с `code`): Go показывает ученику «Аватар не смог ответить» и
сохраняет его ответ для повтора.
"""

from __future__ import annotations

from typing import Any

from fastapi import APIRouter, HTTPException, Request

from . import live
from .engine import ConversationEngine, InvalidState, TurnConflict, parse_state
from .schemas import (
    AnalyzeRequest,
    AnalyzeResponse,
    CheckRequest,
    CheckResponse,
    EngineState,
    ObserveRequest,
    ObserveResponse,
    ProgressRequest,
    ProgressResponse,
    StartRequest,
    StartResponse,
    TurnRequest,
    TurnResponse,
)

router = APIRouter(prefix="/v1/conversation", tags=["conversation"])


def _engine(request: Request) -> ConversationEngine:
    settings = request.app.state.settings
    # Пустая AVATAR_MODEL — основная модель провайдера (правило 9).
    return ConversationEngine(request.app.state.provider,
                              model=getattr(settings, "avatar_model", "") or None)


@router.post("/start", response_model=StartResponse)
async def start(payload: StartRequest, request: Request) -> StartResponse:
    return await _engine(request).start(payload)


@router.post("/turn", response_model=TurnResponse)
async def turn(payload: TurnRequest, request: Request) -> TurnResponse:
    try:
        return await _engine(request).turn(payload)
    except TurnConflict as exc:
        raise HTTPException(status_code=409, detail=str(exc)) from exc
    except InvalidState as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc


@router.post("/analyze", response_model=AnalyzeResponse)
async def analyze(payload: AnalyzeRequest, request: Request) -> AnalyzeResponse:
    return await _engine(request).analyze(payload)


# --- Живой режим (раздел 10.1) ---


def _state(raw: Any) -> EngineState:
    try:
        return parse_state(raw)
    except InvalidState as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc


@router.post("/observe", response_model=ObserveResponse)
async def observe(payload: ObserveRequest, request: Request) -> ObserveResponse:
    try:
        return await _engine(request).observe(payload)
    except InvalidState as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc


# progress и check — инструменты делегата сессии: отвечают из состояния,
# без модели, поэтому провайдер им не нужен.


@router.post("/progress", response_model=ProgressResponse)
async def progress(payload: ProgressRequest) -> ProgressResponse:
    return live.lesson_progress(_state(payload.state))


@router.post("/check", response_model=CheckResponse)
async def check(payload: CheckRequest) -> CheckResponse:
    problems = live.check_reply(_state(payload.state), payload.text, final=payload.final)
    return CheckResponse(ok=not problems, problems=problems)
