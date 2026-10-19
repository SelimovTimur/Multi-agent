"""Ручки живого режима `/v1/conversation/*` через TestClient (договор, раздел 10.1).

Форма ответов сверяется с договором дословно: по нему параллельно пишутся
Go-клиент и инструменты делегата сессии.
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

from app.main import app
from app.provider import ProviderError
from test_conversation_engine import GENERIC_REPLY, GOALS, LESSON, mark
from test_conversation_live import LiveProvider

START = {
    "language": "tatar", "level": "a2", "refined_text": LESSON,
    "collocations": "Минем исемем — меня зовут", "role_prompt": "",
    "goals": GOALS, "max_turns": 8, "history": [],
}
BROKEN_STATES = ({}, {"version": 2}, {"version": 1, "language": "tatar"})


class LiveApiTests(unittest.TestCase):
    def setUp(self):
        # Справочник уровней — заглушка порта: проверяется форма ручек.
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

    def test_start_and_turn_in_steer_mode(self):
        provider = LiveProvider(marks=[{"marks": [mark("p1", "credited", "Минем исемем Марат")]}])
        client = self.client(provider)
        started = client.post("/v1/conversation/start", json={**START, "reply_mode": "steer"})
        self.assertEqual(started.status_code, 200, started.text)
        body = started.json()
        self.assertEqual(list(body), ["state", "reply", "reply_ru", "target_goal", "steer"])
        self.assertEqual((body["reply"], body["reply_ru"], body["target_goal"]), ("", "", "p1"))
        self.assertTrue(body["steer"].startswith("Turn guidance"))
        self.assertEqual(provider.calls, [])

        turn = {"state": body["state"], "reply_mode": "steer", "final": False,
                "answer": {"turn_no": 1, "text": "Минем исемем Марат.", "input": "voice", "edited": False},
                "history": [{"role": "avatar", "content": "Исәнме! Синең исемең ничек?", "turn_no": 0}]}
        answered = client.post("/v1/conversation/turn", json=turn)
        self.assertEqual(answered.status_code, 200, answered.text)
        body = answered.json()
        self.assertEqual(list(body), ["state", "reply", "reply_ru", "marks", "target_goal",
                                      "approach_changed", "steer"])
        self.assertEqual((body["reply"], body["reply_ru"]), ("", ""))
        self.assertEqual(body["marks"], [{"turn_no": 1, "goal_key": "p1", "verdict": "credited",
                                          "error_kind": None, "quote": "Минем исемем Марат",
                                          "correct_form": "", "note_ru": ""}])
        self.assertIn("focus phrase «Мин … яшим»", body["steer"])
        self.assertEqual(provider.names(), ["conversation_marks"])

        repeated = client.post("/v1/conversation/turn", json={**turn, "state": body["state"]})
        self.assertEqual(repeated.status_code, 409)

    def test_text_mode_has_no_steer(self):
        responses = []
        for extra in ({}, {"reply_mode": "text"}):
            client = self.client(LiveProvider(reply=[GENERIC_REPLY]))
            response = client.post("/v1/conversation/start", json={**START, **extra})
            self.assertEqual(response.status_code, 200, response.text)
            responses.append(response.content)
        self.assertEqual(responses[0], responses[1])
        self.assertNotIn(b'"steer"', responses[0])
        self.assertNotIn(b"live_replies", responses[0])

    def test_unknown_reply_mode_is_422(self):
        client = self.client(LiveProvider(reply=[GENERIC_REPLY]))
        response = client.post("/v1/conversation/start", json={**START, "reply_mode": "voice"})
        self.assertEqual(response.status_code, 422)

    def test_observe_progress_and_check(self):
        provider = LiveProvider(translation=[{"ru": "Привет! Меня зовут Алсу. А как тебя зовут?"}])
        client = self.client(provider)
        state = client.post("/v1/conversation/start", json={**START, "reply_mode": "steer"}).json()["state"]

        observed = client.post("/v1/conversation/observe", json={
            "state": state, "turn_no": 0, "reply": "Исәнме! Минем исемем Алсу. Ә синең исемең ничек?",
            "translate": True})
        self.assertEqual(observed.status_code, 200, observed.text)
        body = observed.json()
        self.assertEqual(list(body), ["state", "reply_ru", "leaked"])
        self.assertEqual((body["reply_ru"], body["leaked"]),
                         ("Привет! Меня зовут Алсу. А как тебя зовут?", ["p1"]))
        self.assertEqual(body["state"]["last_reply"], "Исәнме! Минем исемем Алсу. Ә синең исемең ничек?")
        state = body["state"]

        progress = client.post("/v1/conversation/progress", json={"state": state})
        self.assertEqual(progress.status_code, 200, progress.text)
        self.assertEqual(progress.json(), {
            "goals": [
                {"key": "p1", "kind": "phrase", "title": "Минем исемем …", "note": "Меня зовут …", "status": "open"},
                {"key": "p2", "kind": "phrase", "title": "Мин … яшим", "note": "Я живу в …", "status": "open"},
                {"key": "g1", "kind": "grammar", "title": "Местный падеж -да/-дә",
                 "note": "Где? — Казанда, мәктәптә", "status": "open"},
            ],
            "target_goal": "p1",
            "avoid_phrases": ["Минем исемем …", "Мин … яшим"],
            "corrections": [],
            "answers_left": 8,
            "finished": False,
        })

        check = client.post("/v1/conversation/check", json={"state": state, "text": "Әйбәт! Ә син кайда укыйсың?"})
        self.assertEqual(check.status_code, 200, check.text)
        self.assertEqual(check.json(), {"ok": True, "problems": []})
        check = client.post("/v1/conversation/check", json={
            "state": state, "text": "Дөрес, минем исемем Алсу.", "final": False})
        self.assertEqual(check.json(), {"ok": False, "problems": [
            "Do not say «Минем исемем …»: the learner must say it.",
            "Remove evaluation words («дөрес»): do not judge the learner's speech.",
            "Ask exactly one question (found 0).",
        ]})
        # progress и check модель не зовут: был только перевод в observe.
        self.assertEqual(provider.names(), ["conversation_translation"])

    def test_observe_takes_steer_turn(self):
        client = self.client(LiveProvider())
        state = client.post("/v1/conversation/start", json={**START, "reply_mode": "steer"}).json()["state"]
        self.assertEqual(state["steers"], [{"turn_no": 0, "target": "p1"}])
        observed = client.post("/v1/conversation/observe", json={
            "state": state, "turn_no": 0, "reply": "Исәнме! Ничек яшисең?", "translate": False, "steer_turn": 0})
        self.assertEqual(observed.status_code, 200, observed.text)
        self.assertEqual(observed.json()["state"]["live_replies"], [
            {"turn_no": 0, "text": "Исәнме! Ничек яшисең?", "steer_turn": 0, "target": "p1"}])
        negative = client.post("/v1/conversation/observe", json={
            "state": state, "turn_no": 0, "reply": "Исәнме!", "steer_turn": -1})
        self.assertEqual(negative.status_code, 422)

    def test_broken_state_is_422(self):
        client = self.client(LiveProvider())
        requests = {
            "observe": lambda state: {"state": state, "turn_no": 0, "reply": "Исәнме!", "translate": True},
            "progress": lambda state: {"state": state},
            "check": lambda state: {"state": state, "text": "Исәнме?"},
            "turn": lambda state: {"state": state, "answer": {"turn_no": 1, "text": "Сәлам"},
                                   "reply_mode": "steer"},
        }
        for name, body in requests.items():
            for state in BROKEN_STATES:
                with self.subTest(handler=name, state=state):
                    response = client.post(f"/v1/conversation/{name}", json=body(state))
                    self.assertEqual(response.status_code, 422)
                    self.assertIn("detail", response.json())

    def test_translation_uses_avatar_model_and_failure_is_not_an_error(self):
        provider = LiveProvider(translation=[{"ru": "Привет!"}])
        client = self.client(provider, avatar_model="fast/model")
        state = client.post("/v1/conversation/start", json={**START, "reply_mode": "steer"}).json()["state"]
        observed = client.post("/v1/conversation/observe",
                               json={"state": state, "turn_no": 0, "reply": "Исәнме!"})
        self.assertEqual(observed.json()["reply_ru"], "Привет!")
        self.assertEqual(provider.last("translation").kwargs["model"], "fast/model")

        failing = LiveProvider(translation=[ProviderError("Провайдер модели не ответил", code="provider_timeout")])
        client = self.client(failing)
        observed = client.post("/v1/conversation/observe",
                               json={"state": state, "turn_no": 0, "reply": "Исәнме!"})
        self.assertEqual(observed.status_code, 200, observed.text)
        self.assertEqual(observed.json()["reply_ru"], "")
        self.assertEqual(observed.json()["state"]["last_reply"], "Исәнме!")


if __name__ == "__main__":
    unittest.main()
