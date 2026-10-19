"""Решение «показать ученику распознанный текст до отправки».

Распознавание само не знает, ошиблось ли оно. Сигналы, которые есть у нас:
вероятности токенов (logprobs) у API, буквы чужого алфавита в тексте и сам
сигнал записи — громкость, шум, клиппинг. Каждый сигнал даёт свою причину в
review_reasons, а needs_review — есть ли хоть одна. Ошибка распознавания для ученика дороже
лишнего нажатия «Отправить как есть»: она превращается в ошибку, которой он
не делал (Уч-2 в docs/product/avatar-goals/jtbd.md).

Модуль без HTTP и без FastAPI: его одинаково зовут server.py, клиент API и
тесты.
"""

from __future__ import annotations

import math
import re
import unicodedata
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any

from audio_quality import signal_stats

if TYPE_CHECKING:
    from asr_runtime import ASRConfig


REASON_LOW_CONFIDENCE = "low_confidence"
REASON_FOREIGN_LETTERS = "foreign_letters"
REASON_QUIET_AUDIO = "quiet_audio"
REASON_NOISY_AUDIO = "noisy_audio"
REASON_CLIPPED_AUDIO = "clipped_audio"
REASON_NO_CONFIDENCE = "no_confidence"

# Язык приходит полным названием, как его хранит платформа («tatar»);
# коды на случай ручного вызова сервиса.
TATAR_LANGUAGES = frozenset({"tatar", "tt", "tat"})

# Тот же формат, что проверяет Go на /my/transcribe и /ml/transcribe: язык
# попадает в подсказку модели, и произвольная строка там лишняя.
_LANGUAGE_PATTERN = re.compile(r"[a-z]{2,16}")

# Казахские и башкирские буквы, которых нет в татарском алфавите. Модель
# путает близкие тюркские языки, и короткий ответ ученика иногда выходит
# «казахским». У каждой буквы один татарский двойник, поэтому замена
# однозначна.
_FOREIGN_CYRILLIC = {
    "қ": "к", "ғ": "г", "ұ": "у", "і": "е",  # казахские
    "ҡ": "к", "ҙ": "з", "ҫ": "с",  # башкирские
}
_FOREIGN_CYRILLIC.update({k.upper(): v.upper() for k, v in list(_FOREIGN_CYRILLIC.items())})
_FOREIGN_TABLE = str.maketrans(_FOREIGN_CYRILLIC)

# Латинские буквы, на глаз неотличимые от кириллических. Внутри
# кириллического слова ученик их не увидит, а разбор не найдёт такое слово в
# справочнике, поэтому двойник заменяется той же кириллической буквой.
_LATIN_TWINS = {
    "a": "а", "c": "с", "e": "е", "o": "о", "p": "р", "x": "х", "y": "у",
    "h": "һ", "ə": "ә",
    "A": "А", "B": "В", "C": "С", "E": "Е", "H": "Н", "K": "К", "M": "М",
    "O": "О", "P": "Р", "T": "Т", "X": "Х", "Y": "Ү", "Ə": "Ә",
}
# Буквы тюркской латиницы: турецкие, татарского яңалифа и заманәлифа,
# азербайджанские. В английском имени их не бывает, поэтому слово с ними —
# татарская речь, записанная чужим алфавитом (05.10.2026 ответ ученика пришёл
# турецким «Anlamadım»).
_TURKIC_LATIN = {
    "ä": "ә", "ə": "ә", "ö": "ө", "ü": "ү", "ñ": "ң", "ŋ": "ң",
    "ş": "ш", "ç": "ч", "ğ": "г", "ı": "ы",
}
# Татарская латиница по буквам — для слова, целиком записанного латиницей.
_LATIN_TO_TATAR = {
    "a": "а", "b": "б", "c": "җ", "d": "д", "e": "е", "f": "ф", "g": "г",
    "h": "һ", "i": "и", "j": "ж", "k": "к", "l": "л", "m": "м", "n": "н",
    "o": "о", "p": "п", "q": "к", "r": "р", "s": "с", "t": "т", "u": "у",
    "v": "в", "w": "в", "x": "х", "y": "й", "z": "з", **_TURKIC_LATIN,
}
_LATIN_DIGRAPHS = {"ya": "я", "yä": "я", "yu": "ю", "yü": "ю", "yo": "ё"}

_WORD = re.compile(r"[^\W\d_]+")


@dataclass(frozen=True)
class Recognition:
    """Подробный результат распознавания."""

    text: str
    language: str
    # exp(среднего logprob); None — модель вероятностей не дала.
    confidence: float | None = None
    # Вероятность самого сомнительного токена-слова.
    min_token_prob: float | None = None
    review_reasons: tuple[str, ...] = ()
    # Сколько раз распознавали запись: 2 — был повтор из-за чужих букв
    # (даже если сам повтор сорвался — запрос всё равно ушёл).
    attempts: int = 1
    # Сигнал записи (audio_quality.signal_stats): уровень громкой части в дБ
    # от полной шкалы, отношение речь/шум в дБ, доля срезанных отсчётов.
    level_dbfs: float | None = None
    snr_db: float | None = None
    clip_ratio: float | None = None

    @property
    def needs_review(self) -> bool:
        return bool(self.review_reasons)

    def to_response(self) -> dict[str, Any]:
        """Ответ /recognize: поля договора и служебные замеры для калибровки."""
        return {
            "text": self.text,
            "language": self.language,
            "confidence": _round(self.confidence),
            "needs_review": self.needs_review,
            "review_reasons": list(self.review_reasons),
            # Go эти замеры не пробрасывает: они нужны, чтобы подобрать пороги
            # скриптом benchmarks/review_check.py.
            "diagnostics": {
                "min_token_prob": _round(self.min_token_prob),
                "attempts": self.attempts,
                "level_dbfs": _round(self.level_dbfs, 1),
                "snr_db": _round(self.snr_db, 1),
                "clip_ratio": _round(self.clip_ratio),
            },
        }


def _round(value: float | None, digits: int = 4) -> float | None:
    return None if value is None else round(value, digits)


def resolve_language(requested: str | None, default: str) -> str:
    """Язык распознавания: поле формы, а без него — ASR_API_LANGUAGE.

    Неверное значение — ValueError: язык попадает в подсказку модели.
    """
    if requested is None or requested == "":
        return (default or "").strip().lower()
    if not _LANGUAGE_PATTERN.fullmatch(requested):
        raise ValueError("Некорректный язык распознавания")
    return requested


def is_tatar(language: str) -> bool:
    return language.strip().lower() in TATAR_LANGUAGES


def _script(ch: str) -> str:
    return unicodedata.name(ch, "").split(" ", 1)[0]


def _mixed_word(word: str) -> bool:
    scripts = {_script(ch) for ch in word}
    return "CYRILLIC" in scripts and "LATIN" in scripts


def _lower(ch: str) -> str:
    # «İ».lower() в Python — две кодовые точки (i и точка сверху).
    return "i" if ch == "İ" else ch.lower()


def _turkic(ch: str) -> bool:
    return _lower(ch) in _TURKIC_LATIN or ch == "İ"


def _mostly_latin(text: str) -> bool:
    """Реплика записана латиницей, а не содержит латинское имя."""
    letters = [ch for ch in text if ch.isalpha()]
    latin = sum(1 for ch in letters if _script(ch) == "LATIN")
    return bool(letters) and latin * 2 > len(letters)


def foreign_letters(text: str) -> list[str]:
    """Буквы чужого алфавита в татарском тексте — без повторов, по порядку.

    Чужие всегда: казахские и башкирские буквы, буквы тюркской латиницы
    (ş, ä, ı…) и латиница внутри кириллического слова. Целое латинское слово
    без тюркских букв (имя, «Google») чужим не считается, если реплика в
    остальном кириллическая: ученик мог его и сказать. Реплика, записанная
    латиницей почти целиком, — татарская речь чужим алфавитом.
    """
    found: dict[str, None] = {}
    for ch in text:
        if ch in _FOREIGN_CYRILLIC or _turkic(ch):
            found[ch] = None
    whole = _mostly_latin(text)
    for match in _WORD.finditer(text):
        word = match.group()
        if whole or _mixed_word(word):
            for ch in word:
                if _script(ch) == "LATIN":
                    found[ch] = None
    return list(found)


def _case(src: str, dst: str) -> str:
    return dst.upper() if src.isupper() else dst


def _in_cyrillic_word(ch: str) -> str:
    # Внутри кириллического слова двойник — та же буква на экране, прочая
    # латиница — по татарской латинице: «мәктәпtә» → «мәктәптә».
    if _script(ch) != "LATIN":
        return ch
    if ch in _LATIN_TWINS:
        return _LATIN_TWINS[ch]
    low = _lower(ch)
    return _case(ch, _LATIN_TO_TATAR[low]) if low in _LATIN_TO_TATAR else ch


def _transliterate(word: str) -> str:
    out = []
    i = 0
    while i < len(word):
        pair = word[i:i + 2].lower()
        if pair in _LATIN_DIGRAPHS:
            out.append(_case(word[i], _LATIN_DIGRAPHS[pair]))
            i += 2
            continue
        ch = word[i]
        low = _lower(ch)
        if low == "e" and i == 0:
            out.append(_case(ch, "э"))  # eş → эш
        elif low in _LATIN_TO_TATAR:
            out.append(_case(ch, _LATIN_TO_TATAR[low]))
        else:
            out.append(ch)
        i += 1
    return "".join(out)


def replace_foreign_letters(text: str) -> str:
    """Татарский текст только татарской кириллицей, регистр сохраняется.

    Казахские и башкирские буквы — их татарскими двойниками, латиница внутри
    кириллического слова — кириллицей, слово тюркской латиницей и реплика,
    почти целиком записанная латиницей, — транслитерацией: «Anlamadım» →
    «Анламадым». Транслитерация исправляет алфавит, а не язык: турецкое
    «Bilmeyim» станет «Билмейим», а не «белмим», — поэтому такой текст
    по-прежнему уходит ученику на проверку (foreign_letters_left).
    """
    text = text.translate(_FOREIGN_TABLE)
    whole = _mostly_latin(text)

    def fix(match: re.Match[str]) -> str:
        word = match.group()
        if _mixed_word(word):
            return "".join(_in_cyrillic_word(ch) for ch in word)
        if "LATIN" in {_script(ch) for ch in word} and (whole or any(_turkic(ch) for ch in word)):
            return _transliterate(word)
        return word

    return _WORD.sub(fix, text)


def tatar_script(text: str) -> str:
    """Расшифровка татарской речи — татарской кириллицей (без проверки)."""
    return replace_foreign_letters(text) if foreign_letters(text) else text


def _is_punctuation(token: str) -> bool:
    stripped = token.strip()
    return not stripped or all(unicodedata.category(ch)[0] in "PZ" for ch in stripped)


def confidence_from_logprobs(logprobs: Any) -> tuple[float | None, float | None]:
    """Уверенность и вероятность самого сомнительного токена.

    Уверенность — exp(среднего logprob) по всем токенам, то есть среднее
    геометрическое вероятностей: один провальный токен тянет её вниз сильнее,
    чем обычное среднее. Минимум считается только по токенам со словами:
    сомнение модели, ставить ли запятую, — не повод показывать ученику
    проверку текста.
    """
    if not isinstance(logprobs, list):
        return None, None
    values: list[float] = []
    word_values: list[float] = []
    for item in logprobs:
        if not isinstance(item, dict):
            continue
        value = item.get("logprob")
        if isinstance(value, bool) or not isinstance(value, (int, float)) or math.isnan(value):
            continue
        # Округление у провайдера даёт +1e-7; -inf — «вероятность ноль».
        value = max(min(float(value), 0.0), -100.0)
        values.append(value)
        token = item.get("token")
        if not (isinstance(token, str) and _is_punctuation(token)):
            word_values.append(value)
    if not values:
        return None, None
    confidence = math.exp(sum(values) / len(values))
    min_prob = math.exp(min(word_values)) if word_values else None
    return confidence, min_prob


def review_reasons(
    *,
    confidence: float | None,
    min_token_prob: float | None,
    foreign_letters_left: bool,
    config: ASRConfig,
    level_dbfs: float | None = None,
    snr_db: float | None = None,
    clip_ratio: float | None = None,
) -> tuple[str, ...]:
    """Причины показать текст ученику. Пусто — ответ уходит сразу.

    Сигнал записи проверяется отдельно от уверенности: на замере модель
    уверенно (0.86–0.96) выдавала испорченный текст и на шуме 5 дБ, и на
    срезанной записи. Ни одна сигнальная причина не отказ: речь в записи
    есть, но модель на ней ошибается молча.
    """
    reasons: list[str] = []
    if confidence is None:
        reasons.append(REASON_NO_CONFIDENCE)
    elif confidence < config.review_min_confidence or (
        min_token_prob is not None and min_token_prob < config.review_min_token_prob
    ):
        reasons.append(REASON_LOW_CONFIDENCE)
    if foreign_letters_left:
        reasons.append(REASON_FOREIGN_LETTERS)
    # Цифровую тишину отсекает check_audio ещё до распознавания.
    if level_dbfs is not None and level_dbfs < config.review_quiet_dbfs:
        reasons.append(REASON_QUIET_AUDIO)
    if snr_db is not None and snr_db < config.review_min_snr_db:
        reasons.append(REASON_NOISY_AUDIO)
    if clip_ratio is not None and clip_ratio > config.review_max_clip_ratio:
        reasons.append(REASON_CLIPPED_AUDIO)
    return tuple(reasons)


def build_recognition(
    *,
    text: str,
    language: str,
    confidence: float | None,
    min_token_prob: float | None,
    foreign_letters_left: bool,
    audio_bytes: bytes,
    config: ASRConfig,
    attempts: int = 1,
) -> Recognition:
    """Итог распознавания: текст, уверенность и сигнал той же записи."""
    stats = signal_stats(audio_bytes)
    level = snr = clip = None
    if stats is not None:
        level, snr, clip = stats.level_dbfs, stats.snr_db, stats.clip_ratio
    return Recognition(
        text=text,
        language=language,
        confidence=confidence,
        min_token_prob=min_token_prob,
        review_reasons=review_reasons(
            confidence=confidence,
            min_token_prob=min_token_prob,
            foreign_letters_left=foreign_letters_left,
            config=config,
            level_dbfs=level,
            snr_db=snr,
            clip_ratio=clip,
        ),
        attempts=attempts,
        level_dbfs=level,
        snr_db=snr,
        clip_ratio=clip,
    )


def review_local(text: str, language: str, audio_bytes: bytes, config: ASRConfig) -> Recognition:
    """Результат локальной модели: вероятностей у неё нет, проверка нужна всегда."""
    foreign = is_tatar(language) and bool(foreign_letters(text))
    if foreign:
        # Повторить с подсказкой локальной модели нельзя — сразу замена.
        text = replace_foreign_letters(text)
    return build_recognition(
        text=text,
        language=language,
        confidence=None,
        min_token_prob=None,
        foreign_letters_left=foreign,
        audio_bytes=audio_bytes,
        config=config,
    )
