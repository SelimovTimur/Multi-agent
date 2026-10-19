import io
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import asr_api  # noqa: E402
from asr_api import tatar_script  # noqa: E402


def test_kazakh_letters_become_tatar():
    # Реплика ученика с экрана разговора 05.10.2026.
    assert tatar_script("біртағынсын қарашым") == "бертагынсын карашым"


def test_whole_latin_reply_becomes_cyrillic():
    assert tatar_script("Bilmeyim") == "Билмейим"
    assert tatar_script("Anlamadım") == "Анламадым"


def test_latin_name_inside_cyrillic_text_is_kept():
    assert tatar_script("Мин YouTube карыйм") == "Мин YouTube карыйм"


def test_tatar_text_is_unchanged():
    text = "Җәй көне һава җылы, ә мин өйдә утырам."
    assert tatar_script(text) == text


class _Config:
    api_key = "test"
    api_model = "gpt-transcribe"
    api_timeout_seconds = 5
    api_base_url = "https://example.test/v1"

    def __init__(self, language):
        self.api_language = language


def _fake_urlopen(sent, reply):
    class Response(io.BytesIO):
        status = 200

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

    def urlopen(req, timeout):
        sent.append(req.data)
        return Response(json.dumps({"text": reply}).encode())
    return urlopen


def test_tatar_request_uses_cyrillic_prompt_and_normalizes(monkeypatch):
    sent = []
    monkeypatch.setattr(asr_api.request, "urlopen", _fake_urlopen(sent, "Bilmeyim"))
    text, language = asr_api.transcribe(b"RIFF", _Config("tatar"))
    assert text == "Билмейим"
    assert language == "tatar"
    assert "Tatar Cyrillic only".encode() in sent[0]


def test_other_language_keeps_text_as_is(monkeypatch):
    sent = []
    monkeypatch.setattr(asr_api.request, "urlopen", _fake_urlopen(sent, "Hello there"))
    text, _ = asr_api.transcribe(b"RIFF", _Config("english"))
    assert text == "Hello there"
    assert b"The audio is in english." in sent[0]
