"""Контракт общего каталога: окружение, пустые значения и изоляция ключей."""
import importlib.util
import os
from pathlib import Path
import unittest
from unittest.mock import patch

import settings


class SettingsTests(unittest.TestCase):
    def test_generated_files_are_current(self):
        for path, expected in settings.generated_files().items():
            with self.subTest(path=str(path.relative_to(settings.ROOT))):
                self.assertEqual(path.read_text(), expected)

    def test_env_overrides_models_without_replacing_explicit_empty_values(self):
        for service, model in (("llm", "OPENROUTER_MODEL"), ("asr", "ASR_API_MODEL"), ("tts", "TTS_API_MODEL")):
            spec = importlib.util.spec_from_file_location(
                service + "_settings_test", settings.ROOT / f"{service}_service/{service}_settings.py")
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            with patch.dict(os.environ, {}, clear=True):
                self.assertEqual(module.getenv(model), settings.MODELS[model])
            with patch.dict(os.environ, {model: "test-model", "AVATAR_MODEL": ""}, clear=True):
                self.assertEqual(module.getenv(model), "test-model")
                if service == "llm":
                    self.assertEqual(module.getenv("AVATAR_MODEL"), "")

    def test_credentials_are_empty_and_scoped_to_their_service(self):
        for defaults in settings.SERVICES.values():
            for name, value in defaults.items():
                if name.endswith(("API_KEY", "PASSWORD")):
                    self.assertEqual(value, "", name)
        self.assertNotIn("OPENROUTER_API_KEY", settings.ASR)
        self.assertNotIn("DATABASE_URL", settings.LLM)
        self.assertNotIn("OPENAI_API_KEY", settings.LLM)

    def test_speech_goes_through_api_in_english(self):
        self.assertEqual(settings.ASR["ASR_USE_API"], "true")
        self.assertEqual(settings.ASR["ASR_API_LANGUAGE"], "english")
        self.assertEqual(settings.TTS["TTS_USE_API"], "true")


if __name__ == "__main__":
    unittest.main()
