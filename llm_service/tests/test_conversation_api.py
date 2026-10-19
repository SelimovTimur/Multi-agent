"""Ручки `/v1/conversation/*` через TestClient: форма JSON и коды ответа.

Форма ответов сверяется с договором (раздел 4) дословно: по нему
параллельно пишется Go-клиент.
"""

from __future__ import annotations

import os
import unittest
from dataclasses import replace
from unittest.mock import patch

# Старт приложения читает настройки из окружения — задаём их до импорта.
os.environ.setdefault("LLM_PROVIDER", "openrouter")
os.environ.setdefault("OPENROUTER_API_KEY", "test-key")

from fastapi.testclient import TestClient

from app.config import Settings
from app.main import app
from app.provider import ProviderError
from test_conversation_engine import GENERIC_REPLY, GOALS, LESSON, ScriptedProvider, mark

START = {
    "language": "tatar", "level": "a2", "refined_text": LESSON,
    "collocations": "Минем исемем — меня зовут", "role_prompt": "",
    "goals": GOALS, "max_turns": 8, "history": [],
}


class ConversationApiTests(unittest.TestCase):
    def setUp(self):
        # Справочник уровней в этих тестах — заглушка порта: проверяется
        # форма ручек, а не уровни.
        patcher = patch("app.conversation.levels_port._load_default", return_value=None)
        patcher.start()
        self.addCleanup(patcher.stop)

    def client(self, provider, **settings) -> TestClient:
        client = TestClient(app)
        client.__enter__()
        self.addCleanup(client.__exit__, None, None, None)
        client.app.state.provider = provider
        if settings:
            client.app.state.settings = replace(client.app.state.settings, **settings)
        return client

    def test_start_turn_and_conflict(self):
        provider = ScriptedProvider(reply=[GENERIC_REPLY],
                                    marks=[{"marks": [mark("p1", "credited", "Минем исемем Марат")]}])
        client = self.client(provider)
        started = client.post("/v1/conversation/start", json=START)
        self.assertEqual(started.status_code, 200, started.text)
        body = started.json()
        self.assertEqual(set(body), {"state", "reply", "reply_ru", "target_goal"})
        self.assertEqual(body["target_goal"], "p1")

        turn = {"state": body["state"],
                "answer": {"turn_no": 1, "text": "Минем исемем Марат.", "input": "voice", "edited": False},
                "history": [{"role": "avatar", "content": body["reply"], "turn_no": 0}],
                "final": False}
        answered = client.post("/v1/conversation/turn", json=turn)
        self.assertEqual(answered.status_code, 200, answered.text)
        body = answered.json()
        self.assertEqual(set(body), {"state", "reply", "reply_ru", "marks", "target_goal", "approach_changed"})
        self.assertEqual(body["marks"], [{"turn_no": 1, "goal_key": "p1", "verdict": "credited",
                                          "error_kind": None, "quote": "Минем исемем Марат",
                                          "correct_form": "", "note_ru": ""}])
        self.assertEqual((body["target_goal"], body["approach_changed"]), ("p2", False))

        repeated = client.post("/v1/conversation/turn", json={**turn, "state": body["state"]})
        self.assertEqual(repeated.status_code, 409)
        self.assertIn("detail", repeated.json())

    def test_invalid_input_is_422(self):
        client = self.client(ScriptedProvider(reply=[GENERIC_REPLY]))
        bad_goal = {**START, "goals": [{"key": "x1", "kind": "phrase", "title": "Сәлам", "note": ""}]}
        mismatched = {**START, "goals": [{"key": "g1", "kind": "phrase", "title": "Сәлам", "note": ""}]}
        duplicated = {**START, "goals": [GOALS[0], GOALS[0]]}
        for payload in (bad_goal, mismatched, duplicated, {**START, "level": "a9"},
                        {**START, "language": "klingon"}):
            response = client.post("/v1/conversation/start", json=payload)
            self.assertEqual(response.status_code, 422, payload)
            self.assertIn("detail", response.json())

        for state in ({}, {"version": 2}, {"version": 1, "language": "tatar"}):
            response = client.post("/v1/conversation/turn", json={
                "state": state, "answer": {"turn_no": 1, "text": "Сәлам"}, "history": [], "final": False})
            self.assertEqual(response.status_code, 422, state)
            self.assertIn("detail", response.json())

    def test_provider_failure_is_502_with_code(self):
        failing = ScriptedProvider(reply=[ProviderError("Провайдер модели не ответил", code="provider_timeout")])
        response = self.client(failing).post("/v1/conversation/start", json=START)
        self.assertEqual(response.status_code, 502)
        self.assertEqual(response.json()["code"], "provider_timeout")

        broken = ScriptedProvider(reply=["{не JSON"])
        response = self.client(broken).post("/v1/conversation/start", json=START)
        self.assertEqual(response.status_code, 502)
        self.assertEqual(response.json()["code"], "provider_error")
        self.assertEqual(len(broken.calls), 2)

    def test_avatar_model_setting_reaches_provider(self):
        provider = ScriptedProvider(reply=[GENERIC_REPLY])
        client = self.client(provider, avatar_model="fast/model")
        self.assertEqual(client.post("/v1/conversation/start", json=START).status_code, 200)
        self.assertEqual(provider.last("reply").kwargs["model"], "fast/model")

    def test_analyze_shape(self):
        provider = ScriptedProvider(analysis=[{"marks": [mark("p1", "credited", "Минем исемем Марат", turn_no=1)]}],
                                    errors=[{"errors": []}])
        response = self.client(provider).post("/v1/conversation/analyze", json={
            "language": "tatar", "level": "a2", "goals": GOALS, "state": None,
            "transcript": [
                {"turn_no": 0, "role": "avatar", "content": "Исәнме! Синең исемең ничек?"},
                {"turn_no": 1, "role": "student", "content": "Минем исемем Марат.", "input": "voice", "edited": False},
            ],
        })
        self.assertEqual(response.status_code, 200, response.text)
        body = response.json()
        self.assertEqual(set(body), {"goals", "marks", "general_errors", "summary", "speech_level"})
        self.assertEqual(body["goals"][0], {"key": "p1", "kind": "phrase", "title": "Минем исемем …",
                                            "note": "Меня зовут …", "credited": True, "credited_turn": 1,
                                            "correct_uses": 1, "errors": 0, "misses": 0})
        self.assertEqual(body["summary"], {
            "credited_phrases": [{"key": "p1", "title": "Минем исемем …"}], "applied_themes": [],
            "phrases_total": 2, "themes_total": 1, "next_time": None})
        self.assertEqual(body["speech_level"], {
            "estimated": "", "shares": {"a1": 0.0, "a2": 0.0, "b1": 0.0, "b2": 0.0, "c1": 0.0},
            "unknown_share": 0.0})


class SettingsTests(unittest.TestCase):
    def test_avatar_model_from_env(self):
        with patch.dict(os.environ, {"OPENROUTER_API_KEY": "k", "AVATAR_MODEL": " fast/model "}, clear=True):
            self.assertEqual(Settings.from_env().avatar_model, "fast/model")
        with patch.dict(os.environ, {"OPENROUTER_API_KEY": "k"}, clear=True):
            self.assertEqual(Settings.from_env().avatar_model, "")


if __name__ == "__main__":
    unittest.main()
