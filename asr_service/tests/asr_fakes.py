"""Подмены для тестов распознавания: OpenAI Audio API через urllib и WAV-записи.

Платный API в тестах не вызывается: urlopen подменяется, запрос разбирается
как multipart, а ответ берётся из заранее заданной очереди.
"""

import io
import json
import math
import wave
from email.parser import BytesParser
from urllib import error

import numpy as np


RATE = 16000


def speech_like(amplitude: float = 0.1, seconds: float = 1.5, rate: int = RATE) -> np.ndarray:
    """«Речь»: слоги из гармоник 150 Гц с огибающей и паузами, пик = amplitude.

    Ровный тон без пауз для оценки шума неотличим от ровного шума: у него нет
    перепада между громкими кадрами и шумовым полом. У речи этот перепад есть.
    Громкость слогов разная, как у живой речи: максимум записи — один.
    """
    t = np.arange(int(rate * seconds)) / rate
    out = np.zeros_like(t)
    syllable, gap = 0.22, 0.12
    gains = (1.0, 0.75, 0.9, 0.6, 0.85, 0.7)
    start, k = 0.15, 0
    while start + syllable < seconds - 0.1:
        mask = (t >= start) & (t < start + syllable)
        local = t[mask] - start
        envelope = np.sin(np.pi * local / syllable) ** 2
        tone = sum(np.sin(2 * np.pi * 150 * h * local + h) / h for h in range(1, 6))
        out[mask] = gains[k % len(gains)] * envelope * tone
        start += syllable + gap
        k += 1
    peak = np.max(np.abs(out))
    return amplitude * out / peak if peak else out


def white_noise_for(signal: np.ndarray, snr_db: float, seed: int = 0) -> np.ndarray:
    """Белый шум с отношением речь/шум snr_db к мощности слогов (без пауз)."""
    voiced = signal[np.abs(signal) > 0]
    power = float(np.mean(voiced ** 2))
    return np.random.default_rng(seed).standard_normal(len(signal)) * np.sqrt(power / 10 ** (snr_db / 10))


def to_wav(samples: np.ndarray, rate: int = RATE) -> bytes:
    """PCM16 моно, как после ffmpeg в Go."""
    pcm = (np.clip(samples, -1.0, 1.0) * 32767).astype("<i2")
    out = io.BytesIO()
    with wave.open(out, "wb") as f:
        f.setnchannels(1)
        f.setsampwidth(2)
        f.setframerate(rate)
        f.writeframes(pcm.tobytes())
    return out.getvalue()


def make_wav(amplitude: float = 0.1, seconds: float = 1.5, rate: int = RATE) -> bytes:
    """Чистая речеподобная запись PCM16 с пиком amplitude (1.0 — полная шкала)."""
    return to_wav(speech_like(amplitude, seconds, rate), rate)


def logprobs(*probs: float, tokens: list[str] | None = None) -> list[dict]:
    """Поле logprobs ответа API для заданных вероятностей токенов."""
    tokens = tokens or [f"т{i}" for i in range(len(probs))]
    return [
        {"token": token, "logprob": math.log(p), "bytes": list(token.encode())}
        for token, p in zip(tokens, probs)
    ]


class _Response:
    def __init__(self, status: int, body: bytes):
        self.status = status
        self._body = body

    def read(self) -> bytes:
        return self._body

    def __enter__(self):
        return self

    def __exit__(self, *_):
        return False


class FakeAPI:
    """Очередь ответов API и журнал запросов (поля формы → байты)."""

    def __init__(self):
        self.responses: list[tuple[int, dict]] = []
        self.requests: list[dict[str, bytes]] = []
        self.urls: list[str] = []

    def reply(self, *payloads, status: int = 200) -> "FakeAPI":
        self.responses.extend((status, payload) for payload in payloads)
        return self

    def urlopen(self, req, timeout=None):
        content_type = req.get_header("Content-type")
        mime = BytesParser().parsebytes(
            f"Content-Type: {content_type}\r\n\r\n".encode() + req.data
        )
        parts = {
            part.get_param("name", header="content-disposition"): part.get_payload(decode=True)
            for part in mime.get_payload()
        }
        self.requests.append(parts)
        self.urls.append(req.full_url)
        if not self.responses:
            raise AssertionError("лишний запрос к API")
        status, payload = self.responses.pop(0)
        body = json.dumps(payload).encode()
        if status >= 400:
            raise error.HTTPError(req.full_url, status, "error", None, io.BytesIO(body))
        return _Response(status, body)

    def prompt(self, index: int = 0) -> str:
        return self.requests[index]["prompt"].decode()
