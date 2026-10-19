"""Указания уровня /v1/levels/guidance через TestClient."""
from __future__ import annotations

import os
import unittest

# Старт приложения читает настройки из окружения — задаём их до импорта.
os.environ.setdefault("LLM_PROVIDER", "openrouter")
os.environ.setdefault("OPENROUTER_API_KEY", "test-key")

from fastapi.testclient import TestClient

from app.levels import level_guidance
from app.main import app


class LevelsApiTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.client_context = TestClient(app)
        cls.client = cls.client_context.__enter__()

    @classmethod
    def tearDownClass(cls):
        cls.client_context.__exit__(None, None, None)

    def test_guidance_names_the_level_and_its_grammar(self):
        response = self.client.get("/v1/levels/guidance", params={"level": "a2", "language": "english"})
        self.assertEqual(response.status_code, 200)
        body = response.json()
        self.assertEqual(body["level"], "a2")
        self.assertIn("A2 по CEFR", body["text"])
        self.assertIn("Present Perfect для опыта", body["text"])

    def test_unknown_level_is_rejected(self):
        response = self.client.get("/v1/levels/guidance", params={"level": "z9"})
        self.assertEqual(response.status_code, 422)

    def test_other_language_gets_no_guidance(self):
        response = self.client.get("/v1/levels/guidance", params={"level": "a1", "language": "tatar"})
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()["text"], "")


class GuidanceTests(unittest.TestCase):
    def test_every_level_has_guidance(self):
        for level in ("a1", "a2", "b1", "b2", "c1"):
            with self.subTest(level=level):
                self.assertIn(level.upper(), level_guidance(level, "english"))

    def test_a1_warns_about_the_next_level(self):
        text = level_guidance("a1", "english")
        self.assertIn("Не используй конструкции выше уровня ученика", text)
        self.assertIn("Present Perfect", text.split("Не используй")[1])

    def test_top_level_has_no_ceiling(self):
        self.assertNotIn("Не используй", level_guidance("c1", "english"))
