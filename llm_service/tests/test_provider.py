from __future__ import annotations

import json
import unittest
from dataclasses import replace
from unittest.mock import patch

import httpx

from app.config import Settings
from app.provider import OpenAICompatibleProvider, ProviderError
from app.schemas import ChatMessage


def settings() -> Settings:
    return Settings(
        provider="openrouter",
        openrouter_key="secret",
        openrouter_base_url="https://proxy.example/openrouter/v1",
        openrouter_model="qwen/qwen3.7-plus",
        openrouter_reasoning_enabled=False,
        yandex_folder_id="",
        yandex_api_key="",
        yandex_base_url="https://yandex.example/v1",
        max_concurrent_requests=2,
        request_timeout_seconds=30,
    )


class ProviderTests(unittest.IsolatedAsyncioTestCase):
    async def test_malformed_transport_response_is_provider_error(self):
        for raw in ['<html>Ошибка прокси</html>', '[]', '{"choices":[null]}',
                    '{"choices":{"message":{}}}', '{"choices":[{"message":"ok"}]}',
                    '{"choices":[{"message":{"content":"ok"}}],"usage":"broken"}']:
            with self.subTest(raw=raw):
                async with httpx.AsyncClient(transport=httpx.MockTransport(
                    lambda request: httpx.Response(200, text=raw))) as client:
                    provider = OpenAICompatibleProvider(settings(), client=client)
                    with self.assertRaises(ProviderError) as caught:
                        await provider.complete([ChatMessage(role="user", content="test")])
                    self.assertEqual(caught.exception.code, "provider_response")

    async def test_explicit_proxy_and_keepalive_are_configured(self):
        with patch('app.provider.httpx.AsyncClient') as client_factory:
            OpenAICompatibleProvider(replace(settings(), http_proxy='http://127.0.0.1:10809'))
        options = client_factory.call_args.kwargs
        self.assertEqual(options['proxy'], 'http://127.0.0.1:10809')
        self.assertFalse(options['trust_env'])
        self.assertEqual(options['limits'].keepalive_expiry, 120)
        self.assertEqual(options['limits'].max_keepalive_connections, settings().max_concurrent_requests)
        self.assertEqual(options['timeout'].connect, 10)

    async def test_forwards_structured_output_and_server_tools(self):
        captured = {}

        async def handler(request: httpx.Request) -> httpx.Response:
            captured["authorization"] = request.headers.get("authorization")
            captured["body"] = json.loads(request.content)
            return httpx.Response(
                200,
                json={
                    "choices": [{"message": {"content": '{"ok":true}'}}],
                    "usage": {"output_tokens": 3},
                },
            )

        client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
        provider = OpenAICompatibleProvider(settings(), client=client)
        response_format = {
            "type": "json_schema",
            "json_schema": {
                "name": "test",
                "strict": True,
                "schema": {
                    "type": "object",
                    "properties": {"ok": {"type": "boolean"}},
                    "required": ["ok"],
                    "additionalProperties": False,
                },
            },
        }
        tools = [{"type": "openrouter:web_search"}]
        try:
            result = await provider.complete(
                [ChatMessage(role="user", content="test")],
                response_format=response_format,
                tools=tools,
            )
        finally:
            await client.aclose()
        self.assertEqual(result.content, '{"ok":true}')
        self.assertEqual(captured["authorization"], "Bearer secret")
        self.assertEqual(captured["body"]["response_format"], response_format)
        self.assertEqual(captured["body"]["tools"], tools)
        self.assertTrue(captured["body"]["provider"]["require_parameters"])
        self.assertEqual(captured["body"]["reasoning"], {"enabled": False})
        self.assertEqual(captured["body"]["max_tool_calls"], 1)
        self.assertEqual(captured["body"]["max_tokens"], 8192)

    async def test_explicit_reasoning_and_search_model_are_not_forced_off(self):
        captured = []
        async def handler(request):
            captured.append(json.loads(request.content))
            return httpx.Response(200, json={"choices": [{"message": {"content": "ok"}}]})
        async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
            enabled = OpenAICompatibleProvider(replace(settings(), openrouter_reasoning_enabled=True), client=client)
            await enabled.complete([ChatMessage(role="user", content="test")])
            search = OpenAICompatibleProvider(settings(), client=client)
            await search.complete([ChatMessage(role="user", content="test")], model="qwen/qwen3.7-plus")
            await search.complete([ChatMessage(role="user", content="grammar")], reasoning_max_tokens=1024)
        self.assertEqual(captured[0]["reasoning"], {"enabled": True})
        self.assertEqual(captured[1]["reasoning"], {"enabled": False})
        self.assertEqual(captured[2]["reasoning"], {"enabled": True, "max_tokens": 1024})
        self.assertTrue(all("reasoning_effort" not in body for body in captured))


if __name__ == "__main__":
    unittest.main()
