"""llm_service — движок разговора с аватаром и справочник уровней.

Go-сервер ходит сюда за репликами аватара (/v1/conversation/*) и за
указаниями уровня для голосовой сессии (/v1/levels/guidance).
"""
from __future__ import annotations

from contextlib import asynccontextmanager

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

from .config import Settings
from .conversation.api import router as conversation_router
from .levels.router import router as levels_router
from .provider import OpenAICompatibleProvider, ProviderError
from .telemetry import MetricsMiddleware, Monitor


@asynccontextmanager
async def lifespan(app: FastAPI):
    settings = Settings.from_env()
    provider = OpenAICompatibleProvider(settings)
    app.state.settings = settings
    app.state.provider = provider
    try:
        yield
    finally:
        await provider.close()


app = FastAPI(
    title="NATIVE LLM Service",
    version="1.0.0",
    lifespan=lifespan,
)
app.state.monitor = Monitor()
app.add_middleware(MetricsMiddleware, monitor=app.state.monitor)
app.include_router(conversation_router)
app.include_router(levels_router)


@app.exception_handler(ProviderError)
async def provider_error_handler(_: Request, exc: ProviderError) -> JSONResponse:
    return JSONResponse(status_code=502, content={"detail": str(exc), "code": exc.code})


@app.get("/health")
async def health(request: Request) -> dict[str, object]:
    settings: Settings = request.app.state.settings
    return {
        "status": "ok",
        "provider": settings.provider,
        "model": settings.model,
    }
