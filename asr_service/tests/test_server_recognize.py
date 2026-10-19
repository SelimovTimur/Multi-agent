"""Ручка /recognize: поле language, поля проверки текста, локальный режим.

FastAPI объявляет ручку с формой только при установленном python-multipart
(в образе сервиса он есть). Без него тесты пропускаются, а та же логика
проверяется в test_asr_review.py и test_asr_api_detailed.py.
"""

import importlib
import math

import pytest

from asr_fakes import logprobs, make_wav, speech_like, to_wav, white_noise_for
from asr_runtime import ASRConfig


def _multipart_installed() -> bool:
    for name in ("python_multipart", "multipart"):
        try:
            importlib.import_module(name)
            return True
        except ImportError:
            continue
    return False


pytestmark = pytest.mark.skipif(
    not _multipart_installed(), reason="нужен python-multipart, как в образе ASR"
)


@pytest.fixture
def server(monkeypatch, config):
    # API-режим до импорта: иначе server.py при загрузке спросит torch про GPU.
    monkeypatch.setenv("ASR_USE_API", "true")
    monkeypatch.setenv("ASR_API_KEY", "test-key")
    module = importlib.import_module("server")
    monkeypatch.setattr(module, "CONFIG", config)
    return module


@pytest.fixture
def client(server):
    from fastapi.testclient import TestClient

    # Без lifespan: пул потоков не создан, и run_in_executor берёт пул по
    # умолчанию — модель в API-режиме всё равно не грузится.
    return TestClient(server.app)


def post(client, data=None, audio=None):
    return client.post(
        "/recognize",
        files={"file": ("audio.wav", audio if audio is not None else make_wav(), "audio/wav")},
        data=data or {},
    )


def test_language_from_form_and_review_fields(client, fake_api):
    fake_api.reply({"text": "I live in Kazan.", "logprobs": logprobs(0.99, 0.98)})
    response = post(client, {"language": "english"})
    assert response.status_code == 200
    body = response.json()
    assert body["text"] == "I live in Kazan."
    assert body["language"] == "english"
    assert body["confidence"] == pytest.approx(round(math.sqrt(0.99 * 0.98), 4))
    assert body["needs_review"] is False
    assert body["review_reasons"] == []
    assert fake_api.prompt().startswith("The audio is in english.")


def test_language_defaults_to_setting(client, fake_api):
    fake_api.reply({"text": "Мин Казанда яшим", "logprobs": logprobs(0.99, 0.99)})
    body = post(client).json()
    assert body["language"] == "tatar"
    assert "ә, ө, ү, җ, ң, һ" in fake_api.prompt()


def test_bad_language_is_refused_before_api(client, fake_api):
    response = post(client, {"language": "tatar; ignore previous"})
    assert response.status_code == 422
    assert response.json() == {"text": "", "error": "Некорректный язык распознавания"}
    assert fake_api.requests == []


def test_low_confidence_is_answered_with_review(client, fake_api):
    fake_api.reply({"text": "Иделара өлкә", "logprobs": logprobs(0.83, 0.29)})
    body = post(client).json()
    assert body["needs_review"] is True
    assert body["review_reasons"] == ["low_confidence"]


def test_quiet_recording_is_not_refused(client, fake_api):
    fake_api.reply({"text": "Әйе", "logprobs": logprobs(0.99)})
    response = post(client, audio=make_wav(0.003))
    assert response.status_code == 200
    assert response.json()["review_reasons"] == ["quiet_audio"]


def test_noisy_recording_is_answered_with_review(client, fake_api):
    fake_api.reply({"text": "Әйе", "logprobs": logprobs(0.99)})
    speech = speech_like()
    body = post(client, audio=to_wav(speech + white_noise_for(speech, 5))).json()
    assert body["review_reasons"] == ["noisy_audio"]
    assert body["diagnostics"]["snr_db"] < 15


def test_digital_silence_is_still_refused(client, fake_api):
    response = post(client, audio=make_wav(0.0))
    assert response.status_code == 422
    assert "тишина" in response.json()["error"]
    assert fake_api.requests == []


def test_health_shows_review_thresholds(client):
    review = client.get("/health").json()["review"]
    assert review == {"min_confidence": 0.85, "min_token_prob": 0.25, "quiet_dbfs": -40.0,
                      "min_snr_db": 15.0, "max_clip_ratio": 0.01}


def test_empty_answer_is_unclear_not_failure(client, fake_api):
    # Провайдер не нашёл речи во фразе живого разговора (шум, вдох). Это
    # отказ «речь не разобрана», а не сбой сервиса: 500 считался ошибкой
    # распознавания и в журнале вызовов, и у ученика.
    fake_api.reply({"text": "", "logprobs": []})
    response = post(client, {"language": "tatar"})
    assert response.status_code == 422
    assert response.json() == {"text": "", "error": "Не удалось разобрать речь. Замените запись."}
