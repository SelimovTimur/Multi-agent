"""Проверки самого сигнала: тихо, шумно, срезано — на синтетической речи."""

import io
import wave

import numpy as np
import pytest

from asr_fakes import speech_like, to_wav, white_noise_for
from asr_review import build_recognition
from asr_runtime import ASRConfig
from audio_quality import signal_stats


def confident(audio: bytes):
    """Модель уверена: остаются только причины, которые дал сам сигнал."""
    return build_recognition(
        text="Мин Казанда яшим", language="tatar", confidence=0.95, min_token_prob=0.9,
        foreign_letters_left=False, audio_bytes=audio, config=ASRConfig(),
    )


SPEECH = speech_like(0.3)


def test_clean_speech_has_no_signal_reasons():
    result = confident(to_wav(SPEECH))
    assert result.review_reasons == ()
    assert result.snr_db > 40
    assert result.clip_ratio == 0.0


def test_white_noise_5db_is_noisy():
    result = confident(to_wav(SPEECH + white_noise_for(SPEECH, 5)))
    assert result.review_reasons == ("noisy_audio",)
    assert result.snr_db < 15


@pytest.mark.parametrize("gain, ceiling", [(10, 0.3), (10, 1.0), (4, 0.6)])
def test_plateau_clipping_is_found_relative_to_record_max(gain, ceiling):
    # Срез ищется у максимума записи, а не только у полной шкалы: в варианте
    # с замера пик обрезан на 0.3.
    result = confident(to_wav(np.clip(SPEECH * gain, -ceiling, ceiling)))
    assert "clipped_audio" in result.review_reasons
    assert result.clip_ratio > 0.05


def test_quiet_recording_stays_quiet_only():
    result = confident(to_wav(SPEECH * 10 ** (-30 / 20)))
    assert result.review_reasons == ("quiet_audio",)
    # Громкость не меняет ни оценку шума, ни клиппинг.
    assert result.snr_db > 40
    assert result.clip_ratio == 0.0


def test_loud_speech_near_full_scale_is_not_clipping():
    assert confident(to_wav(speech_like(0.99))).review_reasons == ()


def test_smooth_tone_peaks_are_not_clipping():
    # Ровный тон с плавной огибающей держится у максимума на многих периодах,
    # но вершина не плоская. Без требования плоскости он набирал около 1%
    # «среза», а на −30 дБ грубое квантование переваливало порог.
    t = np.arange(16000 * 3) / 16000
    gate = np.sin(2 * np.pi * 2 * t) > 0
    tone = 0.3 * gate * np.sin(2 * np.pi * 180 * t) * (1 + 0.3 * np.sin(2 * np.pi * 7 * t))
    for gain in (1.0, 10 ** (-30 / 20)):
        assert signal_stats(to_wav(tone * gain)).clip_ratio < 0.001


def test_steady_noise_without_speech_is_noisy():
    noise = np.random.default_rng(3).standard_normal(16000) * 0.05
    assert confident(to_wav(noise)).review_reasons == ("noisy_audio",)


def test_short_or_foreign_audio_gives_no_signal_verdict():
    short = signal_stats(to_wav(speech_like(0.3, seconds=0.4)))
    assert short is not None and short.snr_db is None
    assert signal_stats(b"ID3 not a wav") is None
    result = confident(b"ID3 not a wav")
    assert result.review_reasons == ()
    assert result.level_dbfs is None and result.snr_db is None and result.clip_ratio is None


def _wav(samples: np.ndarray, width: int, channels: int = 1) -> bytes:
    if width == 1:
        data = (np.clip(samples, -1, 1) * 127 + 128).astype(np.uint8).tobytes()
    elif width == 3:
        ints = (np.clip(samples, -1, 1) * (2 ** 23 - 1)).astype("<i4")
        data = b"".join(int(v).to_bytes(3, "little", signed=True) for v in ints)
    else:
        data = (np.clip(samples, -1, 1) * (2 ** 31 - 1)).astype("<i4").tobytes()
    if channels == 2:
        frame = width
        data = b"".join(data[i:i + frame] * 2 for i in range(0, len(data), frame))
    out = io.BytesIO()
    with wave.open(out, "wb") as f:
        f.setnchannels(channels)
        f.setsampwidth(width)
        f.setframerate(16000)
        f.writeframes(data)
    return out.getvalue()


@pytest.mark.parametrize("width, channels", [(1, 1), (3, 1), (4, 1), (3, 2)])
def test_other_pcm_formats_give_the_same_estimates(width, channels):
    reference = signal_stats(to_wav(SPEECH))
    stats = signal_stats(_wav(SPEECH, width, channels))
    assert stats.level_dbfs == pytest.approx(reference.level_dbfs, abs=0.5)
    assert stats.clip_ratio == pytest.approx(0.0, abs=0.002)
    clipped = signal_stats(_wav(np.clip(SPEECH * 10, -0.3, 0.3), width, channels))
    assert clipped.clip_ratio > 0.05
