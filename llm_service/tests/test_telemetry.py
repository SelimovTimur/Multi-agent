"""Учёт не смешивает запросы, повторы, отсутствие цены и бесплатный вызов."""
import asyncio
import base64
import json
from unittest.mock import patch

import httpx
import pytest
from fastapi import FastAPI

from app.telemetry import Monitor, RequestMetrics, MetricsMiddleware, provider_call, current, stage
from app.provider import OpenAICompatibleProvider, ProviderError
from app.schemas import ChatMessage
from tests.test_provider import settings


def rows(request):
    return json.loads(base64.b64decode(request.header()))


def test_cost_decimal_zero_unknown_and_invalid_rate(monkeypatch):
    request = RequestMetrics(Monitor())
    monkeypatch.setenv('LLM_BILLING_RATES_JSON', '{"openrouter:test":{"input_per_million":1,"output_per_million":2,"currency":"USD"}}')
    for cost in [0, '0.000000001', '0.0025']:
        request.record('openrouter', 'test', httpx.Response(200, json={'usage': {'cost': cost}}), .1, '200')
    assert rows(request)[0]['cost'] == '0.002500001'
    assert rows(request)[0]['calls'] == 3
    request.record('openrouter', 'test', httpx.Response(200, json={'usage': {'cost': 'NaN', 'prompt_tokens': -1, 'completion_tokens': 'bad'}}), .1, '200')
    assert rows(request)[1]['source'] == 'unknown'
    assert rows(request)[1]['cost'] is None
    request.record('openrouter', 'test', httpx.Response(200, json={'usage': {'prompt_tokens': 100, 'completion_tokens': 50}}), .1, '200')
    assert rows(request)[2]['source'] == 'estimated'
    assert rows(request)[2]['cost'] == '0.0002'
    monkeypatch.setenv('LLM_BILLING_RATES_JSON', '{"openrouter:test": []}')
    request.record('openrouter', 'test', httpx.Response(200, json={'usage': {'prompt_tokens': 1, 'completion_tokens': 1}}), .1, '200')
    assert rows(request)[1]['calls'] == 2


def test_api_decimal_is_not_rounded_through_float():
    request = RequestMetrics(Monitor())
    request.record('openrouter', 'test', httpx.Response(200, text='{"usage":{"cost":0.12345678901234567890}}'), .1, '200')
    assert rows(request)[0]['cost'] == '0.12345678901234567890'


@pytest.mark.asyncio
async def test_failed_attempts_and_paid_invalid_response_are_counted():
    request = RequestMetrics(Monitor())
    token = current.set(request)
    count = 0
    async def answer(_):
        nonlocal count
        count += 1
        return httpx.Response(429 if count == 1 else 200, json={'usage': {'cost': '0.01'}})
    try:
        async with httpx.AsyncClient(transport=httpx.MockTransport(answer)) as client:
            provider = OpenAICompatibleProvider(settings(), client=client)
            with patch('app.provider.RETRY_BASE_DELAY', 0), pytest.raises(ProviderError):
                await provider.complete([ChatMessage(role='user', content='Пример')])
    finally:
        current.reset(token)
    assert rows(request)[0]['calls'] == 2
    assert rows(request)[0]['cost'] == '0.02'
    assert request.monitor.registry.get_sample_value('native_provider_calls_total', {'provider': 'openrouter', 'model': settings().model, 'stage': 'unstructured', 'outcome': '429'}) == 1


@pytest.mark.asyncio
async def test_parallel_requests_and_stages_are_isolated():
    app, monitor = FastAPI(), Monitor()
    app.add_middleware(MetricsMiddleware, monitor=monitor)
    @stage('test')
    async def call(model):
        await asyncio.sleep(0)
        provider_call('openrouter', model, httpx.Response(200, json={'usage': {'cost': 0}}), .01, '200')
    @app.get('/test/{model}')
    async def route(model: str):
        await asyncio.gather(call(model), call(model))
        return {'ok': True}
    async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url='http://test') as client:
        responses = await asyncio.gather(client.get('/test/one'), client.get('/test/two'))
        for response, model in zip(responses, ['one', 'two']):
            billing = json.loads(base64.b64decode(response.headers['X-Native-Usage']))
            assert len(billing) == 1 and billing[0]['model'] == model
            assert billing[0]['cost'] == '0' and billing[0]['calls'] == 2
        assert monitor.inflight._value.get() == 0
        assert current.get() is None
        assert monitor.registry.get_sample_value('native_python_requests_total', {'method': 'GET', 'route': '/test/{model}', 'status': '200'}) == 2
