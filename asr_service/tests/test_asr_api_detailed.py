"""Клиент OpenAI Audio API: подробное распознавание и совместимость transcribe().

HTTP подменён (asr_fakes.FakeAPI): платный API не вызывается.
"""

import math
from dataclasses import replace

import numpy as np
import pytest

from asr_api import build_prompt, transcribe, transcribe_detailed, transcribe_material
from asr_fakes import logprobs, make_wav, speech_like, to_wav, white_noise_for

CLEAN = {"text": "Мин Казанда яшим.", "logprobs": logprobs(0.99, 0.97, 0.98, 0.99),
         "languages": [], "usage": {"type": "duration", "seconds": 2}}


def test_tatar_request_has_orthography_prompt_and_logprobs(fake_api, config):
    fake_api.reply(CLEAN)
    result = transcribe_detailed(make_wav(), config)

    fields = fake_api.requests[0]
    assert fake_api.urls == ["http://asr.test/v1/audio/transcriptions"]
    assert fields["model"] == b"gpt-transcribe"
    assert fields["response_format"] == b"json"
    assert fields["include[]"] == b"logprobs"
    # language=tt API отвергает (400), язык — только в подсказке.
    assert "language" not in fields
    prompt = fake_api.prompt()
    for letter in "әөүҗңһ":
        assert letter in prompt
    assert "tatar" in prompt and "Tatar Cyrillic only" in prompt
    assert "not Kazakh, not Bashkir and not Turkish" in prompt
    assert "mistakes" in prompt and "do not translate" in prompt
    # Подсказка зависит только от языка: слов урока в ней нет и быть не может.
    assert prompt == build_prompt("tatar")

    assert result.text == "Мин Казанда яшим."
    assert result.language == "tatar"
    assert result.confidence == pytest.approx(math.exp(np.mean(np.log([0.99, 0.97, 0.98, 0.99]))))
    assert result.min_token_prob == pytest.approx(0.97)
    assert result.needs_review is False and result.review_reasons == ()
    assert result.attempts == 1


def test_low_average_confidence_needs_review(fake_api, config):
    # Как у трудного фрагмента живой речи: средняя ≈0.83.
    fake_api.reply({"text": "Иделара өлкә көнчыгыш", "logprobs": logprobs(0.83, 0.83, 0.84)})
    result = transcribe_detailed(make_wav(), config)
    assert result.review_reasons == ("low_confidence",)
    assert result.to_response()["needs_review"] is True


def test_one_doubtful_token_needs_review(fake_api, config):
    # Длинный уверенный ответ с одним сомнительным словом: среднее высокое,
    # а минимум ловит место, где модель могла ослышаться.
    fake_api.reply({"text": "Мин Казанда яшим", "logprobs": logprobs(*[0.999] * 15, 0.2)})
    result = transcribe_detailed(make_wav(), config)
    assert result.confidence > config.review_min_confidence
    assert result.review_reasons == ("low_confidence",)


def test_thresholds_are_taken_from_config(fake_api, config):
    fake_api.reply({"text": "Әйе", "logprobs": logprobs(0.83, 0.83)})
    lenient = replace(config, review_min_confidence=0.8)
    assert transcribe_detailed(make_wav(), lenient).needs_review is False


def test_kazakh_letters_get_one_retry_with_strict_prompt(fake_api, config):
    fake_api.reply(
        {"text": "Мин Қазанда тұрам", "logprobs": logprobs(0.9, 0.95)},
        {"text": "Мин Казанда торам", "logprobs": logprobs(0.97, 0.99)},
    )
    result = transcribe_detailed(make_wav(), config)

    assert len(fake_api.requests) == 2
    strict = fake_api.prompt(1)
    assert strict.startswith(fake_api.prompt(0))
    assert "а ә б в г д е ё ж җ з и й к л м н ң о ө п р с т у ү ф х һ ц ч ш щ ъ ы ь э ю я" in strict
    assert "қ ғ ұ і" in strict and "ҡ ҙ ҫ" in strict
    assert result.text == "Мин Казанда торам"
    assert result.review_reasons == ()
    assert result.attempts == 2
    assert result.confidence == pytest.approx(math.sqrt(0.97 * 0.99))


def test_retry_that_did_not_help_gives_replacement_and_review(fake_api, config):
    fake_api.reply(
        {"text": "Қызым ІШ", "logprobs": logprobs(0.99, 0.99)},
        {"text": "Қызым ҡайта ІШ", "logprobs": logprobs(0.99, 0.99, 0.99)},
    )
    result = transcribe_detailed(make_wav(), config)
    assert len(fake_api.requests) == 2  # ровно один повтор
    assert result.text == "Кызым кайта ЕШ"
    assert result.review_reasons == ("foreign_letters",)
    assert result.needs_review is True


def test_failed_retry_keeps_first_answer(fake_api, config):
    fake_api.reply({"text": "Ғаилә", "logprobs": logprobs(0.99)})
    fake_api.reply({"error": {"message": "upstream timeout"}}, status=502)
    result = transcribe_detailed(make_wav(), config)
    assert result.text == "Гаилә"
    assert result.review_reasons == ("foreign_letters",)
    assert result.attempts == 2


def test_turkish_reply_gets_retry_then_cyrillic_and_review(fake_api, config):
    # Ответ ученика 05.10.2026: «белмим» пришло турецким «Bilmeyim».
    fake_api.reply(
        {"text": "Bilmeyim", "logprobs": logprobs(0.99)},
        {"text": "Bilmeyim", "logprobs": logprobs(0.99)},
    )
    result = transcribe_detailed(make_wav(), config)
    assert len(fake_api.requests) == 2
    assert "ş ç ğ ı" in fake_api.prompt(1)
    # Алфавит исправлен, язык — нет, поэтому ученик проверит текст сам.
    assert result.text == "Билмейим"
    assert result.review_reasons == ("foreign_letters",)


def test_turkish_letters_fixed_by_retry_need_no_review(fake_api, config):
    fake_api.reply(
        {"text": "Anlamadım", "logprobs": logprobs(0.99)},
        {"text": "Аңламадым", "logprobs": logprobs(0.99)},
    )
    result = transcribe_detailed(make_wav(), config)
    assert result.text == "Аңламадым"
    assert result.review_reasons == ()
    assert result.attempts == 2


def test_latin_inside_word_triggers_retry(fake_api, config):
    fake_api.reply(
        {"text": "Mин мәктәптә", "logprobs": logprobs(0.99, 0.99)},
        {"text": "Mин мәктәптә", "logprobs": logprobs(0.99, 0.99)},
    )
    result = transcribe_detailed(make_wav(), config)
    assert len(fake_api.requests) == 2
    assert result.text == "Мин мәктәптә"
    assert result.review_reasons == ("foreign_letters",)


def test_foreign_letters_are_checked_only_for_tatar(fake_api, config):
    fake_api.reply({"text": "Ҡыҙ ҫу эсә", "logprobs": logprobs(0.99, 0.99)})
    result = transcribe_detailed(make_wav(), config, language="bashkir")
    assert len(fake_api.requests) == 1
    assert result.text == "Ҡыҙ ҫу эсә"
    assert result.review_reasons == ()
    assert fake_api.prompt().startswith("The audio is in bashkir.")
    assert "ә" not in fake_api.prompt()


def test_language_argument_selects_short_prompt(fake_api, config):
    fake_api.reply({"text": "I lives in Kazan.", "logprobs": logprobs(0.99, 0.99)})
    result = transcribe_detailed(make_wav(), config, language="english")
    prompt = fake_api.prompt()
    assert prompt.startswith("The audio is in english.")
    assert "Tatar" not in prompt and "ә" not in prompt
    assert "mistakes" in prompt
    assert result.language == "english"
    assert result.text == "I lives in Kazan."
    assert result.review_reasons == ()


def test_default_language_comes_from_config(fake_api, config):
    fake_api.reply(CLEAN, CLEAN)
    transcribe_detailed(make_wav(), config)
    transcribe_detailed(make_wav(), replace(config, api_language="english"))
    assert fake_api.prompt(0) == build_prompt("tatar")
    assert fake_api.prompt(1) == build_prompt("english")


def test_model_without_logprobs_still_recognizes_but_asks_review(fake_api, config):
    fake_api.reply({"error": {"message": "include[]=logprobs is not supported for whisper-1"}}, status=400)
    fake_api.reply({"text": "Мин Казанда яшим"})
    result = transcribe_detailed(make_wav(), config)
    assert "include[]" in fake_api.requests[0]
    assert "include[]" not in fake_api.requests[1]
    assert result.confidence is None
    assert result.review_reasons == ("no_confidence",)


def test_other_api_errors_are_not_retried(fake_api, config):
    fake_api.reply({"error": "rate limit"}, status=429)
    with pytest.raises(RuntimeError, match="rate limit"):
        transcribe_detailed(make_wav(), config)
    assert len(fake_api.requests) == 1


def test_quiet_recording_needs_review(fake_api, config):
    fake_api.reply(CLEAN)
    result = transcribe_detailed(make_wav(0.003), config)
    assert result.review_reasons == ("quiet_audio",)
    assert result.level_dbfs < config.review_quiet_dbfs


def test_confident_model_on_noisy_recording_still_asks_review(fake_api, config):
    # Как на замере: шум 5 дБ, модель уверена (0.91), а текст испорчен.
    fake_api.reply({"text": "Патрика, үткәндәге өчен савышларыбыз", "logprobs": logprobs(0.95, 0.9, 0.9)})
    speech = speech_like()
    result = transcribe_detailed(to_wav(speech + white_noise_for(speech, 5)), config)
    assert result.confidence > config.review_min_confidence
    assert result.review_reasons == ("noisy_audio",)
    diagnostics = result.to_response()["diagnostics"]
    assert diagnostics["snr_db"] < config.review_min_snr_db
    assert diagnostics["clip_ratio"] == 0.0


def test_confident_model_on_clipped_recording_still_asks_review(fake_api, config):
    fake_api.reply({"text": "Мардиннең мокшавылык", "logprobs": logprobs(0.96, 0.96)})
    # Как в варианте с замера: усиление ×10 и срез на ±0.3.
    result = transcribe_detailed(to_wav(np.clip(speech_like() * 10, -0.3, 0.3)), config)
    assert result.review_reasons == ("clipped_audio",)
    assert result.to_response()["diagnostics"]["clip_ratio"] > config.review_max_clip_ratio


def test_empty_answer_follows_allow_empty(fake_api, config):
    fake_api.reply({"text": ""}, {"text": ""}, {"unexpected": "payload"})
    with pytest.raises(RuntimeError, match="пустой ответ"):
        transcribe_detailed(make_wav(), config)
    assert transcribe_detailed(make_wav(), config, allow_empty=True).text == ""
    with pytest.raises(RuntimeError, match="пустой ответ"):
        transcribe_detailed(make_wav(), config, allow_empty=True)


# --- transcribe(): только текст и язык ---


def test_transcribe_keeps_request_shape_and_tatar_orthography(fake_api, config):
    fake_api.reply({"text": " Сәлам! ", "languages": []}, {"text": "Hello"})
    assert transcribe(b"audio", config) == ("Сәлам!", "tatar")
    fields = fake_api.requests[0]
    assert set(fields) == {"model", "response_format", "prompt", "file"}
    prompt = fake_api.prompt(0)
    assert "Tatar Cyrillic only" in prompt
    # Субтитры — речь носителя: просьбы сохранить ошибки здесь нет.
    assert "mistakes" not in prompt
    assert transcribe(b"audio", replace(config, api_language="english")) == ("Hello", "english")
    assert fake_api.requests[1]["prompt"] == b"The audio is in english. Transcribe it in the original language."


def test_transcribe_does_not_retry_but_writes_tatar_letters(fake_api, config):
    fake_api.reply({"text": "Қазан", "language": "kazakh"})
    assert transcribe(b"audio", config) == ("Казан", "kazakh")
    assert len(fake_api.requests) == 1


def test_transcribe_auto_language_and_empty_text(fake_api, config):
    auto = replace(config, api_language="")
    fake_api.reply({"text": ""}, {"text": "", "languages": [{"code": "tt"}]})
    with pytest.raises(RuntimeError, match="пустой ответ"):
        transcribe(b"audio", auto)
    assert transcribe(b"audio", auto, allow_empty=True) == ("", "tt")
    assert all("prompt" not in fields for fields in fake_api.requests)


def test_provider_failure_with_logprobs_retries_without_them(fake_api, config):
    # Долгая запись с вероятностями токенов: провайдер отвечал пустым 500.
    fake_api.reply({}, status=500)
    fake_api.reply({"text": "Мин Казанда яшим"})
    result = transcribe_detailed(make_wav(), config)
    assert "include[]" in fake_api.requests[0]
    assert "include[]" not in fake_api.requests[1]
    assert result.text == "Мин Казанда яшим"
    assert result.review_reasons == ("no_confidence",)


def test_material_is_recognized_without_logprobs_retries_or_review(fake_api, config):
    fake_api.reply({"text": "Бүгін һава бик яхшы."})
    result = transcribe_material(make_wav(), config, language="tatar")
    assert len(fake_api.requests) == 1
    assert "include[]" not in fake_api.requests[0]
    assert "ә, ө, ү, җ, ң, һ" in fake_api.prompt(0)
    # Казахская і заменена, проверка учеником материалу не нужна.
    assert result.text == "Бүген һава бик яхшы."
    assert result.review_reasons == ()
    assert result.to_response()["needs_review"] is False
