"""Решение «показать ученику распознанный текст» — без HTTP."""

import math

import numpy as np
import pytest

from asr_fakes import logprobs, make_wav, to_wav
from asr_review import (
    confidence_from_logprobs,
    foreign_letters,
    replace_foreign_letters,
    resolve_language,
    review_local,
    review_reasons,
)
from asr_runtime import ASRConfig
from audio_quality import UnclearAudio, check_audio, speech_level_dbfs


def test_confidence_is_geometric_mean_and_minimum_skips_punctuation():
    probs = [0.99, 0.9, 0.95, 0.1, 0.8]
    items = logprobs(*probs, tokens=["Мин", " Каз", "анда", ",", " яшим"])
    confidence, min_prob = confidence_from_logprobs(items)
    expected = math.exp(sum(math.log(p) for p in probs) / len(probs))
    assert confidence == pytest.approx(expected)
    # Запятая с вероятностью 0.1 тянет вниз среднее, но не минимум: сомнение
    # в пунктуации — не повод показывать ученику проверку.
    assert min_prob == pytest.approx(0.8)


@pytest.mark.parametrize("value", [None, [], "x", [{"logprob": "x"}], [{"logprob": float("nan")}], [1]])
def test_missing_or_broken_logprobs_give_no_confidence(value):
    assert confidence_from_logprobs(value) == (None, None)


def test_rounding_above_zero_is_clamped():
    confidence, min_prob = confidence_from_logprobs([{"token": "Әйе", "logprob": 1e-7}])
    assert confidence == 1.0 and min_prob == 1.0


@pytest.mark.parametrize(
    "confidence, min_prob, foreign, level, expected",
    [
        (0.95, 0.6, False, -20.0, ()),
        # Замер трудного фрагмента живой речи: средняя 0.83, минимальная 0.29.
        (0.83, 0.29, False, -20.0, ("low_confidence",)),
        (0.95, 0.2, False, -20.0, ("low_confidence",)),
        (0.95, None, False, -20.0, ()),
        (None, None, False, -20.0, ("no_confidence",)),
        (0.95, 0.6, True, -20.0, ("foreign_letters",)),
        (0.95, 0.6, False, -55.0, ("quiet_audio",)),
        (0.95, 0.6, False, None, ()),
        (0.5, 0.1, True, -55.0, ("low_confidence", "foreign_letters", "quiet_audio")),
    ],
)
def test_review_reasons_by_default_thresholds(confidence, min_prob, foreign, level, expected):
    reasons = review_reasons(
        confidence=confidence,
        min_token_prob=min_prob,
        foreign_letters_left=foreign,
        level_dbfs=level,
        config=ASRConfig(),
    )
    assert reasons == expected


def test_thresholds_come_from_env(monkeypatch):
    monkeypatch.setenv("ASR_REVIEW_MIN_CONFIDENCE", "0.5")
    monkeypatch.setenv("ASR_REVIEW_MIN_TOKEN_PROB", "0.05")
    monkeypatch.setenv("ASR_REVIEW_QUIET_DBFS", "-60")
    monkeypatch.setenv("ASR_REVIEW_MIN_SNR_DB", "6")
    monkeypatch.setenv("ASR_REVIEW_MAX_CLIP_RATIO", "0.2")
    config = ASRConfig.from_env(use_api=False, device="cpu")
    assert (
        config.review_min_confidence, config.review_min_token_prob, config.review_quiet_dbfs,
        config.review_min_snr_db, config.review_max_clip_ratio,
    ) == (0.5, 0.05, -60.0, 6.0, 0.2)
    reasons = review_reasons(
        confidence=0.6, min_token_prob=0.1, foreign_letters_left=False, config=config,
        level_dbfs=-55.0, snr_db=8.0, clip_ratio=0.1,
    )
    assert reasons == ()


def test_default_thresholds_without_env(monkeypatch):
    for name in ("ASR_REVIEW_MIN_CONFIDENCE", "ASR_REVIEW_MIN_TOKEN_PROB", "ASR_REVIEW_QUIET_DBFS",
                 "ASR_REVIEW_MIN_SNR_DB", "ASR_REVIEW_MAX_CLIP_RATIO"):
        monkeypatch.delenv(name, raising=False)
    config = ASRConfig.from_env(use_api=False, device="cpu")
    assert (
        config.review_min_confidence, config.review_min_token_prob, config.review_quiet_dbfs,
        config.review_min_snr_db, config.review_max_clip_ratio,
    ) == (0.85, 0.25, -40.0, 15.0, 0.01)


@pytest.mark.parametrize(
    "snr, clip, expected",
    [
        (40.0, 0.0, ()),
        (14.9, 0.0, ("noisy_audio",)),
        (None, 0.0, ()),  # запись короче полсекунды — шум не оцениваем
        (40.0, 0.011, ("clipped_audio",)),
        (40.0, 0.01, ()),
        (8.0, 0.4, ("noisy_audio", "clipped_audio")),
    ],
)
def test_signal_reasons_by_default_thresholds(snr, clip, expected):
    reasons = review_reasons(
        confidence=0.95, min_token_prob=0.6, foreign_letters_left=False, config=ASRConfig(),
        level_dbfs=-20.0, snr_db=snr, clip_ratio=clip,
    )
    assert reasons == expected


def test_zero_snr_and_full_clip_ratio_switch_checks_off():
    config = ASRConfig(review_min_snr_db=0.0, review_max_clip_ratio=1.0)
    reasons = review_reasons(
        confidence=0.95, min_token_prob=0.6, foreign_letters_left=False, config=config,
        level_dbfs=-20.0, snr_db=0.0, clip_ratio=1.0,
    )
    assert reasons == ()


@pytest.mark.parametrize(
    "name, value",
    [
        ("ASR_REVIEW_MIN_CONFIDENCE", "высокая"),
        ("ASR_REVIEW_MIN_CONFIDENCE", "1.5"),
        ("ASR_REVIEW_MIN_TOKEN_PROB", "nan"),
        ("ASR_REVIEW_QUIET_DBFS", "10"),
        ("ASR_REVIEW_MIN_SNR_DB", "-3"),
        ("ASR_REVIEW_MAX_CLIP_RATIO", "2"),
    ],
)
def test_broken_threshold_stops_startup(monkeypatch, name, value):
    monkeypatch.setenv(name, value)
    with pytest.raises(ValueError, match=name):
        ASRConfig.from_env(use_api=False, device="cpu")


def test_kazakh_bashkir_and_mixed_latin_letters_are_found():
    assert foreign_letters("Мин Қазанда тұрам, ҡыҙ ҫу іш") == ["Қ", "ұ", "ҡ", "ҙ", "ҫ", "і"]
    # Латиница внутри кириллического слова подозрительна, целое латинское
    # слово — нет: ученик мог сказать «Google».
    assert foreign_letters("Мин Google кулланам, мәктәпta") == ["t", "a"]
    assert foreign_letters("Әйе, мин мәктәптә укыйм. Һәр көн җырлыйм, аңлыйм.") == []


def test_replacement_keeps_case():
    assert replace_foreign_letters("Қазан ғаилә Ұл Ішле ҡыҙ ҪУ ғ") == "Казан гаилә Ул Ешле кыз СУ г"


def test_latin_inside_cyrillic_word_becomes_cyrillic():
    # Двойники («M», «h») — той же буквой на экране, заметная латиница —
    # по татарской латинице. Целое латинское имя в кириллической реплике
    # остаётся: ученик мог сказать «Google».
    text = replace_foreign_letters("Mәктәп hәм мәктәпtә Google")
    assert text == "Мәктәп һәм мәктәптә Google"
    assert foreign_letters(text) == []


def test_turkic_latin_and_latin_replies_are_foreign():
    # Буквы тюркской латиницы чужие и в отдельном слове.
    assert foreign_letters("Мин mäktäptä укыйм") == ["ä"]
    assert replace_foreign_letters("Мин mäktäptä укыйм") == "Мин мәктәптә укыйм"
    # Реплика латиницей почти целиком — татарская речь чужим алфавитом.
    assert foreign_letters("Bilmeyim") == ["B", "i", "l", "m", "e", "y"]
    assert replace_foreign_letters("Bilmeyim") == "Билмейим"
    assert replace_foreign_letters("Anlamadım, İdel") == "Анламадым, Идел"
    assert replace_foreign_letters("eş yaxşı") == "эш яхшы"


@pytest.mark.parametrize("value, expected", [(None, "tatar"), ("", "tatar"), ("english", "english")])
def test_language_from_form_or_default(value, expected):
    assert resolve_language(value, " Tatar ") == expected


@pytest.mark.parametrize("value", ["Tatar", "t", "tatar1", "tat ar", "x" * 17, "татар", "en-US"])
def test_bad_language_is_rejected(value):
    with pytest.raises(ValueError, match="язык"):
        resolve_language(value, "tatar")


def test_local_model_never_claims_confidence():
    result = review_local("Мин Казанда яшим", "tatar", make_wav(0.1), ASRConfig())
    assert result.confidence is None
    assert result.needs_review is True
    assert result.review_reasons == ("no_confidence",)
    body = result.to_response()
    assert body["confidence"] is None and body["needs_review"] is True
    assert body["review_reasons"] == ["no_confidence"]


def test_local_tatar_output_gets_replacement_but_bashkir_model_keeps_its_letters():
    tatar = review_local("Қазан", "tatar", make_wav(0.1), ASRConfig())
    assert tatar.text == "Казан"
    assert tatar.review_reasons == ("no_confidence", "foreign_letters")
    bashkir = review_local("ҡыҙ", "bashkir", make_wav(0.1), ASRConfig())
    assert bashkir.text == "ҡыҙ"
    assert bashkir.review_reasons == ("no_confidence",)


def test_quiet_recording_is_review_not_refusal():
    quiet = make_wav(0.003)
    check_audio(quiet)  # не отказ
    assert speech_level_dbfs(quiet) < -40
    result = review_local("Әйе", "tatar", quiet, ASRConfig())
    assert result.review_reasons == ("no_confidence", "quiet_audio")
    # Цифровая тишина по-прежнему отказ.
    with pytest.raises(UnclearAudio, match="тишина"):
        check_audio(make_wav(0.0))


def test_speech_level_ignores_pauses_around_short_answer():
    rate = 16000
    tone = 0.1 * np.sin(2 * np.pi * 220 * np.arange(rate) / rate)
    padded = np.zeros(rate * 10)
    padded[rate:2 * rate] = tone
    # Секунда ответа в десяти секундах записи — не «тихая запись».
    assert speech_level_dbfs(to_wav(padded)) == pytest.approx(speech_level_dbfs(to_wav(tone)), abs=0.5)
    assert speech_level_dbfs(b"not a wav") is None


def test_response_shape_matches_contract():
    body = review_local("Әйе", "tatar", make_wav(0.1), ASRConfig()).to_response()
    assert list(body)[:5] == ["text", "language", "confidence", "needs_review", "review_reasons"]
    assert set(body["diagnostics"]) == {"min_token_prob", "attempts", "level_dbfs", "snr_db", "clip_ratio"}
