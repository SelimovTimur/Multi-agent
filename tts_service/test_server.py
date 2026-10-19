import io
import os
import unittest
import wave
from unittest.mock import patch

try:
    import torch
except ModuleNotFoundError as exc:
    if exc.name != "torch":
        raise
    torch = None

# Синтез по умолчанию идёт через API и при импорте требует ключ: тестам
# хватает фиктивного, настоящих запросов они не делают.
os.environ.setdefault("OPENAI_API_KEY", "test-key")

import server  # noqa: E402


class FakeModel:
    def __init__(self):
        self.call = None

    def apply_tts(self, **kwargs):
        self.call = kwargs
        return torch.linspace(-0.2, 0.2, 4_800)


class TTSServiceTest(unittest.TestCase):
    def test_api_uses_native_openai_key_and_endpoint(self):
        with patch.dict(os.environ, {'TTS_USE_API':'true', 'OPENAI_API_KEY':'native-key'}, clear=True):
            config = server.TTSConfig.from_env()
        self.assertEqual(config.api_base_url, 'https://api.openai.com/v1')
        self.assertEqual(config.api_model, 'gpt-4o-mini-tts')
        self.assertEqual(config.api_key, 'native-key')
        with patch.object(server, 'CONFIG', config), patch.object(server.httpx, 'Client') as client_factory:
            client = client_factory.return_value.__enter__.return_value
            client.post.return_value.content = b'RIFFtest'
            self.assertEqual(server._synthesize_audio_api('Исәнмесез!'), b'RIFFtest')
            self.assertEqual(client.post.call_args.args[0], 'https://api.openai.com/v1/audio/speech')
            self.assertEqual(client.post.call_args.kwargs['headers']['Authorization'], 'Bearer native-key')
            self.assertEqual(client.post.call_args.kwargs['json']['response_format'], 'wav')
            self.assertEqual(client.post.call_count, 1)
            self.assertEqual(client_factory.call_args.kwargs['timeout'].connect, 10)

    def test_api_does_not_fall_back_to_proxy_key(self):
        with patch.dict(os.environ, {'TTS_USE_API':'true', 'PROXYAPI_API_KEY':'old-key'}, clear=True):
            with self.assertRaisesRegex(ValueError, 'OPENAI_API_KEY'):
                server.TTSConfig.from_env()

    def test_connection_timeout_does_not_repeat_synthesis(self):
        with patch.dict(os.environ, {'TTS_USE_API':'true', 'OPENAI_API_KEY':'native-key'}, clear=True):
            config = server.TTSConfig.from_env()
        with patch.object(server, 'CONFIG', config), patch.object(server.httpx, 'Client') as factory:
            client = factory.return_value.__enter__.return_value
            client.post.side_effect = server.httpx.ConnectTimeout('таймаут')
            with self.assertRaisesRegex(RuntimeError, 'ConnectTimeout'):
                server._synthesize_audio_api('Исәнмесез!')
            self.assertEqual(client.post.call_count, 1)

    def test_config_defaults(self):
        names = {
            "TTS_LANGUAGE",
            "TTS_MODEL_ID",
            "TTS_SPEAKER",
            "TTS_SAMPLE_RATE",
            "TTS_DEVICE",
            "TTS_MAX_WORKERS",
            "TTS_MAX_TEXT_LENGTH",
        }
        with patch.dict(os.environ, {}, clear=False):
            for name in names:
                os.environ.pop(name, None)
            config = server.TTSConfig.from_env()

        # 'ru' — ключ каталога Silero, под которым лежит v5_cis_base_nostress,
        # а не язык урока. Раньше здесь стояло 'tat': такого ключа в каталоге
        # нет, и тест закреплял умолчание, с которым модель не загружается.
        self.assertEqual(config.language, "ru")
        self.assertEqual(config.model_id, "v5_cis_base_nostress")
        self.assertEqual(config.speaker, "tat_marat")
        self.assertEqual(config.sample_rate, 48_000)

    @unittest.skipIf(torch is None, "Локальный TTS требует torch; в API-образе его нет")
    def test_synthesize_returns_valid_wav(self):
        config = server.TTSConfig(
            language="tat",
            model_id="v5_cis_base_nostress",
            speaker="tat_marat",
            sample_rate=48_000,
            device="cpu",
            max_workers=1,
            max_text_length=5_000,
        )
        model = FakeModel()

        result = server.synthesize_wav(
            model,
            config,
            "Беренче фраза.\nИкенче фраза.",
        )

        with wave.open(io.BytesIO(result), "rb") as wav:
            self.assertEqual(wav.getframerate(), 48_000)
            self.assertEqual(wav.getnchannels(), 1)
            self.assertEqual(wav.getsampwidth(), 2)
            self.assertEqual(wav.getnframes(), 4_800)
        self.assertEqual(model.call["speaker"], "tat_marat")
        self.assertEqual(model.call["sample_rate"], 48_000)
        self.assertNotIn("text", model.call)
        self.assertEqual(
            model.call["ssml_text"],
            '<speak>\n'
            '    Беренче фраза.\n'
            '    <break time="200ms"/>\n'
            '    Икенче фраза.\n'
            '</speak>',
        )

    def test_build_ssml_ignores_empty_lines_and_escapes_markup(self):
        result = server.build_ssml("  Бер & ике.  \n\nӨч < дүрт.")

        self.assertEqual(
            result,
            '<speak>\n'
            '    Бер &amp; ике.\n'
            '    <break time="200ms"/>\n'
            '    Өч &lt; дүрт.\n'
            '</speak>',
        )

    def test_rejects_unsupported_sample_rate(self):
        with patch.dict(os.environ, {"TTS_SAMPLE_RATE": "44100", "TTS_USE_API": "false"}):
            with self.assertRaisesRegex(ValueError, "TTS_SAMPLE_RATE"):
                server.TTSConfig.from_env()


class SpeakersTest(unittest.TestCase):
    def test_speakers_come_from_the_loaded_model(self):
        class Model:
            speakers = ["tat_marat", "bak_aigul", "chv_ekaterina"]

        self.assertEqual(
            server.speakers_of(Model()),
            ["tat_marat", "bak_aigul", "chv_ekaterina"],
        )

    def test_model_without_speakers_is_not_an_error(self):
        # Проверка здоровья не должна падать из-за того, что сборка модели не
        # объявляет голоса, и не должна падать до загрузки весов.
        self.assertEqual(server.speakers_of(object()), [])
        self.assertEqual(server.speakers_of(None), [])


class InstructionsTest(unittest.TestCase):
    """Тон разговора от учителя доходит до внешнего синтеза (NR-29)."""

    def test_teacher_tone_is_appended_to_the_base_rules(self):
        text = server._instructions("Ты продавец на рынке, говори бодро")
        self.assertIn("Tatar", text)
        self.assertIn("Ты продавец на рынке, говори бодро", text)

    def test_without_tone_instructions_do_not_change(self):
        self.assertEqual(server._instructions(""), server.TTS_INSTRUCTIONS)
        self.assertEqual(server._instructions("   "), server.TTS_INSTRUCTIONS)


if __name__ == "__main__":
    unittest.main()
