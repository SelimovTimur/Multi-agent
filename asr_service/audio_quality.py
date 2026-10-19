"""Дешёвые проверки входа и расшифровки до продолжения сборки."""
from __future__ import annotations

import io
import math
import re
import wave
from dataclasses import dataclass

import numpy as np


class UnclearAudio(ValueError):
    pass


def check_audio(audio_bytes: bytes) -> None:
    # Go нормализует звук в PCM WAV. Не применяем этот тест к неизвестному
    # кодеку: его поддержка и проверка остаются за декодером провайдера.
    try:
        with wave.open(io.BytesIO(audio_bytes), 'rb') as audio:
            width = audio.getsampwidth()
            if audio.getcomptype() != 'NONE' or width not in (1, 2, 3, 4):
                return
            data = audio.readframes(audio.getnframes())
    except (wave.Error, EOFError):
        return
    if not data:
        raise UnclearAudio('В записи нет различимой речи. Проверьте микрофон или выберите другой файл.')
    # Только цифровая тишина и сигнал на уровне квантования. Тихую речь
    # и шум не объявляем тишиной по произвольному порогу громкости.
    def sample(offset):
        value = int.from_bytes(data[offset:offset+width], 'little', signed=width != 1)
        return value - 128 if width == 1 else value
    if all(abs(sample(i)) <= 1 for i in range(0, len(data), width)):
        raise UnclearAudio('В записи тишина: не удалось разобрать речь. Замените запись.')


# Уровень речи меряем по громким кадрам, а не по всей записи: ученик
# нажимает «Стоп» не сразу, и короткий ответ с паузой до и после по среднему
# выглядел бы тихим. 95-й перцентиль 20-мс кадров — это уже голос, но ещё не
# одиночный щелчок кнопки.
_LEVEL_FRAME_SECONDS = 0.02
_LEVEL_PERCENTILE = 95

# Шумовой пол — 5-й перцентиль тех же кадров. В записи с паузами это паузы,
# а в сплошной речи без пауз — самые тихие её кадры: 10-й перцентиль там уже
# попадает в речь и занижает оценку чистой записи (на калибровке сплошная
# речь давала по нему от 13.8 дБ — ниже порога, по 5-му — от 17.2). Пол не
# ниже шума квантования PCM16: цифровой ноль не даёт бесконечного отношения.
_FLOOR_PERCENTILE = 5
_FLOOR_DBFS = -100.0
# Меньше полсекунды кадров — перцентили считать не по чему.
_SNR_MIN_FRAMES = 25

# Клиппинг — плоские вершины: подряд идущие отсчёты у максимума записи (а не
# только у полной шкалы: запись могли срезать и на 0.3), соседние почти равны.
# Срезанная вершина держится так десятки отсчётов на каждом периоде; живая —
# только на самом пике и только раз, а её склон меняется от отсчёта к отсчёту
# заметно больше 0.5% пика. Без требования плоскости ровный тон с плавной
# огибающей и вершины под вещательным ограничителем набирали до 1% «среза».
_CLIP_NEAR_PEAK = 0.98
_CLIP_FLAT_STEP = 0.005
_CLIP_MIN_RUN_SECONDS = 0.00025
# Пик ниже −26 дБ от полной шкалы — грубое квантование: равные целые числа
# на вершине там обычное дело, а не срез. Такая запись и так тихая.
_CLIP_MIN_PEAK = 0.05


@dataclass(frozen=True)
class SignalStats:
    """Оценки записи, по которым решается проверка текста учеником."""

    # Громкие кадры, дБ от полной шкалы.
    level_dbfs: float
    # Громкие кадры против шумового пола, дБ; None — запись слишком короткая.
    snr_db: float | None
    # Доля срезанных отсчётов среди отсчётов речевых кадров, 0…1.
    clip_ratio: float


def _read_pcm(audio_bytes: bytes) -> tuple[np.ndarray | None, int]:
    """PCM WAV → моно float в [-1, 1]. Чужой формат — None: не гадаем."""
    try:
        with wave.open(io.BytesIO(audio_bytes), 'rb') as audio:
            width = audio.getsampwidth()
            channels = max(1, audio.getnchannels())
            rate = audio.getframerate()
            if audio.getcomptype() != 'NONE' or width not in (1, 2, 3, 4):
                return None, 0
            data = audio.readframes(audio.getnframes())
    except (wave.Error, EOFError):
        return None, 0
    data = data[:len(data) - len(data) % (width * channels)]
    if width == 1:
        samples = (np.frombuffer(data, np.uint8).astype(np.float64) - 128) / 128
    elif width == 2:
        samples = np.frombuffer(data, '<i2').astype(np.float64) / 32768
    elif width == 3:
        raw = np.frombuffer(data, np.uint8).reshape(-1, 3).astype(np.int32)
        value = raw[:, 0] | (raw[:, 1] << 8) | (raw[:, 2] << 16)
        samples = ((value ^ 0x800000) - 0x800000).astype(np.float64) / 2 ** 23
    else:
        samples = np.frombuffer(data, '<i4').astype(np.float64) / 2 ** 31
    if channels > 1:
        samples = samples.reshape(-1, channels).mean(axis=1)
    return samples, rate


def _dbfs(value: float) -> float:
    return 20 * math.log10(max(value, 1e-10))


def _clip_ratio(samples: np.ndarray, rate: int, frame_rms: np.ndarray, hop: int) -> float:
    peak = float(np.max(np.abs(samples)))
    if peak < _CLIP_MIN_PEAK:
        return 0.0
    small_step = np.abs(np.diff(samples)) <= _CLIP_FLAT_STEP * peak
    flat = np.zeros(len(samples), dtype=bool)
    flat[1:] |= small_step
    flat[:-1] |= small_step
    plateau = ((np.abs(samples) >= _CLIP_NEAR_PEAK * peak) & flat).astype(np.int8)
    edges = np.flatnonzero(np.diff(np.concatenate(([0], plateau, [0]))))
    runs = edges[1::2] - edges[::2]
    min_run = max(3, round(rate * _CLIP_MIN_RUN_SECONDS))
    clipped = int(runs[runs >= min_run].sum())
    # Делим на отсчёты речи, а не всей записи: тишина до и после ответа не
    # должна разбавлять срез. Речевой кадр — не тише −20 дБ от самого громкого.
    active = int(np.count_nonzero(frame_rms >= frame_rms.max() * 0.1)) * hop
    return min(1.0, clipped / active) if active else 0.0


def signal_stats(audio_bytes: bytes) -> SignalStats | None:
    """Уровень, оценка отношения речь/шум и доля клиппинга записи.

    Считается по тем же PCM, что уходят в распознавание. None — запись не
    PCM WAV или пустая: тогда о сигнале ничего не утверждаем, решает
    уверенность распознавания.
    """
    samples, rate = _read_pcm(audio_bytes)
    if samples is None or not samples.size or rate <= 0:
        return None
    hop = max(1, int(rate * _LEVEL_FRAME_SECONDS))
    count = len(samples) // hop
    frames = samples[:count * hop].reshape(count, hop) if count else samples[None, :]
    rms = np.sqrt(np.mean(frames ** 2, axis=1))
    level = _dbfs(float(np.percentile(rms, _LEVEL_PERCENTILE)))
    snr = None
    if len(rms) >= _SNR_MIN_FRAMES:
        floor = max(_dbfs(float(np.percentile(rms, _FLOOR_PERCENTILE))), _FLOOR_DBFS)
        snr = max(0.0, level - floor)
    return SignalStats(
        level_dbfs=level,
        snr_db=snr,
        clip_ratio=_clip_ratio(samples, rate, rms, len(samples) if not count else hop),
    )


def speech_level_dbfs(audio_bytes: bytes) -> float | None:
    """Уровень громкой части записи в дБ от полной шкалы (0 — максимум).

    None — запись не PCM WAV или пустая: тогда о громкости ничего не
    утверждаем, решает уверенность распознавания.
    """
    stats = signal_stats(audio_bytes)
    return None if stats is None else stats.level_dbfs


def check_transcript(text: str) -> None:
    if not text.strip():
        raise UnclearAudio('Не удалось разобрать речь. Замените запись.')
    if re.search(r'\b([^\W\d_]+)(?:[\s,;.!?]+\1){5,}\b', text, re.I):
        raise UnclearAudio('Распознавание зациклилось на повторяющемся фрагменте. Проверьте качество записи.')
