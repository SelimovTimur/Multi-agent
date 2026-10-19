"""Учёт транспорта и затрат: тексты запросов и ответов сюда не попадают."""
from __future__ import annotations

import asyncio
import base64
import functools
import json
import os
import time
from contextvars import ContextVar
from decimal import Decimal, InvalidOperation

from prometheus_client import CollectorRegistry, Counter, Gauge, Histogram, generate_latest, ProcessCollector, GCCollector

current: ContextVar[RequestMetrics | None] = ContextVar("request_metrics", default=None)
current_stage: ContextVar[str] = ContextVar("metric_stage", default="unstructured")
BUCKETS = (.01, .05, .1, .25, .5, 1, 2, 5, 10, 30, 60, 120, 300, 600)


def number(value):
    if isinstance(value, bool) or value is None:
        return None
    try:
        result = Decimal(str(value))
        return result if result.is_finite() and 0 <= result <= Decimal("1e12") else None
    except (InvalidOperation, ValueError):
        return None


def tokens(value):
    n = number(value)
    return int(n) if n is not None and n == int(n) and n <= 1_000_000_000 else None


class Monitor:
    def __init__(self):
        self.registry = CollectorRegistry()
        ProcessCollector(registry=self.registry)
        GCCollector(registry=self.registry)
        self.requests = Counter("native_python_requests_total", "Результаты HTTP-запросов.", ("method", "route", "status"), registry=self.registry)
        self.duration = Histogram("native_python_request_duration_seconds", "Длительность HTTP-запросов.", ("route",), buckets=BUCKETS, registry=self.registry)
        self.inflight = Gauge("native_python_requests_inflight", "Одновременные запросы.", registry=self.registry)
        self.calls = Counter("native_provider_calls_total", "Фактические попытки обращения к провайдеру.", ("provider", "model", "stage", "outcome"), registry=self.registry)
        self.call_duration = Histogram("native_provider_call_duration_seconds", "Время отдельной попытки провайдера.", ("provider", "model", "stage"), buckets=BUCKETS, registry=self.registry)
        self.stages = Counter("native_llm_stages_total", "Результаты этапов, включая проверку формата ответа.", ("stage", "outcome"), registry=self.registry)
        self.stage_duration = Histogram("native_llm_stage_duration_seconds", "Полное время этапа.", ("stage", "outcome"), buckets=BUCKETS, registry=self.registry)


class RequestMetrics:
    def __init__(self, monitor):
        self.monitor = monitor
        self.billing = {}

    def record(self, provider, model, response, elapsed, outcome):
        usage = {}
        if response is not None:
            try:
                body = response.json(parse_float=Decimal)
                if isinstance(body, dict) and isinstance(body.get("usage"), dict):
                    usage = body["usage"]
            except ValueError:
                pass
        cost = number(usage.get("cost")) if provider == "openrouter" else None
        source, currency = ("api", "USD") if cost is not None else ("unknown", "")
        input_tokens, output_tokens = tokens(usage.get("prompt_tokens")), tokens(usage.get("completion_tokens"))
        # Тариф явно привязан к провайдеру и модели; общий тариф на все модели
        # дал бы правдоподобную, но неверную себестоимость.
        if cost is None and input_tokens is not None and output_tokens is not None:
            try:
                rate = json.loads((os.getenv("LLM_BILLING_RATES_JSON") or "{}"))[f"{provider}:{model}"]
                incoming, outgoing = number(rate.get("input_per_million")), number(rate.get("output_per_million"))
                if incoming is not None and outgoing is not None and rate.get("currency") in ("USD", "RUB"):
                    cost = (input_tokens * incoming + output_tokens * outgoing) / 1_000_000
                    source, currency = "estimated", rate["currency"]
            except (KeyError, ValueError, TypeError, AttributeError):
                pass
        key = (provider, model, source, currency)
        row = self.billing.setdefault(key, {"provider": provider, "model": model, "source": source,
            "currency": currency, "cost": None, "tokens_in": 0, "tokens_out": 0, "calls": 0})
        row["calls"] += 1
        row["tokens_in"] += input_tokens or 0
        row["tokens_out"] += output_tokens or 0
        if cost is not None:
            row["cost"] = format(Decimal(row["cost"] or "0") + cost, "f")
        stage = current_stage.get()
        self.monitor.calls.labels(provider, model, stage, outcome).inc()
        self.monitor.call_duration.labels(provider, model, stage).observe(elapsed)

    def header(self):
        return base64.b64encode(json.dumps(list(self.billing.values()), separators=(",", ":")).encode())


def provider_call(provider, model, response, elapsed, outcome):
    request = current.get()
    if request is not None:
        request.record(provider, model, response, elapsed, outcome)


def stage(name, *, schema_argument=False):
    def decorate(fn):
        @functools.wraps(fn)
        async def measured(*args, **kwargs):
            label = (kwargs.get("schema_name") or (args[3] if len(args) > 3 else name)) if schema_argument else name
            token = current_stage.set(label)
            started, outcome = time.monotonic(), "success"
            try:
                return await fn(*args, **kwargs)
            except asyncio.CancelledError:
                outcome = "cancelled"
                raise
            except Exception:
                outcome = "error"
                raise
            finally:
                request = current.get()
                if request is not None:
                    request.monitor.stages.labels(label, outcome).inc()
                    request.monitor.stage_duration.labels(label, outcome).observe(time.monotonic() - started)
                current_stage.reset(token)
        return measured
    return decorate


class MetricsMiddleware:
    def __init__(self, app, monitor):
        self.app, self.monitor = app, monitor

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http" or scope["path"] == "/health":
            return await self.app(scope, receive, send)
        if scope["path"] == "/metrics":
            await send({"type": "http.response.start", "status": 200,
                        "headers": [(b"content-type", b"text/plain; version=0.0.4; charset=utf-8")]})
            await send({"type": "http.response.body", "body": generate_latest(self.monitor.registry)})
            return
        request = RequestMetrics(self.monitor)
        token = current.set(request)
        started, status = time.monotonic(), 500
        self.monitor.inflight.inc()

        async def measured_send(message):
            nonlocal status
            if message["type"] == "http.response.start":
                status = message["status"]
                message = dict(message)
                message["headers"] = list(message.get("headers", [])) + [(b"x-native-usage", request.header())]
            await send(message)
        try:
            await self.app(scope, receive, measured_send)
        finally:
            route = getattr(scope.get("route"), "path", "unmatched")
            method = scope["method"] if scope["method"] in {"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE"} else "OTHER"
            self.monitor.requests.labels(method, route, str(status)).inc()
            self.monitor.duration.labels(route).observe(time.monotonic() - started)
            self.monitor.inflight.dec()
            current.reset(token)
