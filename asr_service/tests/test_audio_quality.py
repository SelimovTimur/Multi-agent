import io
import wave
import pytest
from audio_quality import check_audio, check_transcript, UnclearAudio


def wav(data):
    out = io.BytesIO()
    with wave.open(out, 'wb') as f:
        f.setnchannels(1)
        f.setsampwidth(2)
        f.setframerate(16000)
        f.writeframes(data)
    return out.getvalue()


def test_digital_silence_is_refused_before_asr():
    with pytest.raises(UnclearAudio, match='тишина'):
        check_audio(wav(b'\x00\x00' * 16000))


def test_quiet_signal_is_not_mislabeled_as_silence():
    check_audio(wav(b'\x08\x00\xf8\xff' * 8000))


def test_looped_transcript_is_refused_but_normal_repetition_is_allowed():
    with pytest.raises(UnclearAudio, match='зациклилось'):
        check_transcript('тавыш, тавыш, тавыш, тавыш, тавыш, тавыш, тавыш')
    check_transcript('Әйе, әйе. Мин сезне ишетәм.')
