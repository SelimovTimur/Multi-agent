"""Текстовые проверки движка разговора.

Всё здесь детерминировано и модель не зовёт: правила договора о цитатах,
повторе за аватаром, утечке фокусных фраз, словах оценки и вопросах
проверяются кодом, а не только просьбой в промпте. Промпт модель может
нарушить, код — нет.
"""

from __future__ import annotations

import re
import unicodedata
from collections.abc import Sequence
from dataclasses import dataclass

# Буквы без цифр и подчёркивания: слово в любом алфавите.
_WORD = re.compile(r"[^\W\d_]+")
_LATIN_WORD = re.compile(r"[a-z]+")
_CYRILLIC = re.compile(r"[а-яё]")

# Латинские двойники татарских букв: их ставят клавиатуры и распознаватели.
# Без замены «Минəм» (латинская ə) не совпало бы с «Минәм». Заодно ё → е:
# в реплике и ответе одно и то же слово бывает записано по-разному.
_LOOKALIKES = str.maketrans({"ə": "ә", "ö": "ө", "ü": "ү", "ё": "е"})

# Место для заполнения во фразе-шаблоне («Минем исемем …», «Мин ___ яшим»)
# и пояснение в скобках: их ученик не обязан произносить дословно, поэтому
# сравнивается только постоянная часть фразы.
_TEMPLATE_SLOT = re.compile(r"…|\.{2,}|_{2,}|\([^)]*\)|\[[^\]]*\]|\{[^}]*\}|<[^>]*>")

# «Фраза — перевод» в названии цели: перевод не часть фразы.
_SPACED_DASH = re.compile(r"\s[-‐‑‒–—―]\s")

_QUOTE_EDGES = "«»\"'“”„‘’`"
_EDGE_PUNCTUATION = " .,!?;:…"
_DASHES = "-‐‑‒–—―"
_DASH_CLASS = "[" + re.escape(_DASHES) + "]"
_SPLIT_LOOSE = re.compile(r"(\s+|" + _DASH_CLASS + ")")


def fold(text: str) -> str:
    """Регистр, форма Юникода и буквы-двойники без изменения остального текста."""
    return unicodedata.normalize("NFC", text).casefold().translate(_LOOKALIKES)


def normalize(text: str) -> str:
    """Слова через один пробел: без регистра, пунктуации и лишних пробелов."""
    text = re.sub(r"[^\w\s]|_", " ", fold(text))
    return " ".join(text.split())


def words(text: str) -> list[str]:
    return _WORD.findall(fold(text))


def contains_words(haystack: str, needle: str) -> bool:
    """Есть ли needle целыми словами внутри haystack (после нормализации)."""
    target = normalize(needle)
    return bool(target) and f" {target} " in f" {normalize(haystack)} "


@dataclass(frozen=True)
class PhrasePattern:
    """Постоянная часть фокусной фразы для сравнения с репликой."""

    parts: tuple[str, ...]

    @classmethod
    def from_title(cls, title: str) -> "PhrasePattern":
        # Учитель мог вписать перевод в название: «Минем исемем … — Меня
        # зовут …». С русской частью фраза не совпала бы ни с одной репликой,
        # и утечка прошла бы незамеченной. Первая часть остаётся всегда:
        # «Мин — укучы» — татарская фраза с тире.
        #
        # У фразы латиницей («I usually … — я обычно …») перевод — любая
        # часть кириллицей, даже без частых русских слов.
        segments = _SPACED_DASH.split(title)
        latin = _mostly_latin(segments[0])
        phrase = " ".join(segments[:1] + [
            part for part in segments[1:]
            if not (_mostly_cyrillic(part) if latin else _looks_foreign(part))])
        parts = (normalize(part) for part in _TEMPLATE_SLOT.split(phrase))
        return cls(tuple(part for part in parts if part))

    def occurs_in(self, text: str) -> bool:
        """Все постоянные части встречаются по порядку и целыми словами.

        Шаблон без постоянной части («…») не сравнивается ни с чем: иначе
        любая реплика считалась бы произнесённой фразой.
        """
        if not self.parts:
            return False
        haystack = f" {normalize(text)} "
        position = 0
        for part in self.parts:
            found = haystack.find(f" {part} ", position)
            if found < 0:
                return False
            # Следующая часть может начаться сразу за этой: ищем от пробела.
            position = found + len(part) + 1
        return True


def ground_quote(quote: str, text: str) -> str | None:
    """Цитата как точная подстрока ответа или None, если её там нет.

    Код может восстановить регистр, пробелы, вид тире и кавычки по краям —
    это не меняет слов ученика. Буквы и окончания не восстанавливаются:
    цитата с другой формой слова — уже выдумка, и отметка отбрасывается.
    Несколько равных совпадений без учёта регистра — тоже отказ: неясно,
    какое из них имела в виду модель.
    """
    candidate = quote.strip().strip(_QUOTE_EDGES).strip()
    if not candidate or not _WORD.search(candidate):
        return None
    core = candidate.strip(_EDGE_PUNCTUATION)
    for variant in (candidate, core):
        if variant and variant in text:
            return variant
    if not core:
        return None
    matches = list(re.finditer(_loose_pattern(core), text, re.IGNORECASE))
    if len(matches) == 1:
        return matches[0].group()
    return None


def _loose_pattern(core: str) -> str:
    parts: list[str] = []
    for token in _SPLIT_LOOSE.split(core):
        if not token:
            continue
        if token.isspace():
            parts.append(r"\s+")
        elif token in _DASHES:
            parts.append(_DASH_CLASS)
        else:
            parts.append("".join("[её]" if ch in "её" else re.escape(ch) for ch in token))
    return "".join(parts)


def spans(quote: str, text: str) -> list[tuple[int, int]]:
    """Все вхождения точной цитаты в текст."""
    found: list[tuple[int, int]] = []
    if not quote:
        return found
    start = text.find(quote)
    while start >= 0:
        found.append((start, start + len(quote)))
        start = text.find(quote, start + 1)
    return found


def overlaps(first: str, second: str, text: str) -> bool:
    """Пересекаются ли места двух цитат в одном ответе."""
    return any(a < d and c < b for a, b in spans(first, text) for c, d in spans(second, text))


def answer_quote(text: str, limit: int = 120) -> str:
    """Цитата промаха — начало ответа до limit символов.

    Обрезаем по границе слова, чтобы учитель не видел обрубок, но так, что
    результат остаётся точной подстрокой ответа.
    """
    stripped = text.strip()
    if len(stripped) <= limit:
        return stripped
    cut = stripped[:limit]
    space = cut.rfind(" ")
    if space >= limit * 2 // 3:
        cut = cut[:space]
    return cut.rstrip(" ,;:—-")


# --- Классы ответа ученика (правило 2) ---

TECHNICAL = "technical"
STUCK = "stuck"
SOCIAL = "social"
FOREIGN = "foreign"
NORMAL = "normal"

# Приветствия, благодарности и прощания: такой ответ ошибкой по цели не
# бывает. Ответ социальный, только если в нём нет других слов, — «Рәхмәт,
# мин Казанда яшим» уже обычный ответ. Слова вроде «зур», «көн», «яхшы»
# сами по себе бывают ответом по существу, поэтому они допустимы только
# рядом с настоящей формулой вежливости.
SOCIAL_CORE = frozenset(
    """
    сәлам сәламәтме исәнме исәнмесез исәнмесезме исәнмесеңме хәерле рәхмәт
    сау саубул саубулыгыз хуш хушыгыз исәнсау гафу рәхим
    привет здравствуй здравствуйте спасибо пока свидания извините
    hi hello hey thanks thank bye goodbye
    """.split()
)
SOCIAL_FILLERS = frozenset(
    """
    иртә көн кич зур бик күп сезгә сиңа бул булыгыз исән юл итегез ит
    яхшы әйбәт ярыйсы шәп
    добрый доброе день утро вечер большое очень пожалуйста до хорошо нормально
    отлично you ok okay
    """.split()
)

# Языки урока на латинице: ответ не на языке урока у них — ответ кириллицей.
LATIN_LANGUAGES = frozenset({"english"})

# Особые буквы языков урока: их наличие — надёжный признак, что ответ на
# языке урока, а не по-русски.
LANGUAGE_LETTERS = {
    "tatar": "әөүҗңһ",
    "bashkir": "әөүҡғҫҙһң",
    "chuvash": "ӑӗҫӳ",
    "mari": "ӓӧӱҥӹ",
    "ossetian": "ӕ",
    "udmurt": "ӝӟӥӧӵ",
    "yakut": "ҕҥөһү",
}
_ALL_LANGUAGE_LETTERS = frozenset("".join(LANGUAGE_LETTERS.values()))

# Частые русские слова, которых нет среди татарских. Слова-омонимы
# («да», «без», «кем», «а», «так») сюда не входят: «Мин дә» и «Без
# Казанда» — татарские ответы.
RUSSIAN_MARKERS = frozenset(
    """
    я ты мы вы он она оно они меня тебя его её ее их мне тебе нам вам ему ей им
    мой моя моё мое мои твой твоя наш наша ваш ваша свой это этот эта эти тот та те
    не нет ни что как где когда почему зачем кто куда откуда сколько который какой
    и или но в на с со у к ко по из за для от до про при над под без через после
    перед около вот же бы ли если чтобы потому что тоже также очень уже ещё еще
    только просто можно нельзя надо нужно хочу хочешь хотел могу можешь знаю
    знаешь понимаю понял поняла понятно думаю говорю говорить сказать скажи
    люблю нравится живу живёшь живет живёт зовут учусь работаю есть был была было
    были буду будет сейчас сегодня вчера завтра здесь там тут всё все весь много
    мало больше меньше хорошо плохо давай давайте пожалуйста спасибо
    """.split()
)


# «Не знаю», «не понял», «забыл»: ученик застрял на вопросе. Учителя
# просили, чтобы аватар тогда переспрашивал проще, а не уходил к другой
# цели. Ответ застрявший, только если в нём нет других слов: «Белмим, мин
# бармадым» — уже ответ по существу. Русским словам из STUCK_NEGATED нужно
# «не» рядом: «знаю» само по себе — «я знаю».
STUCK_CORE = frozenset(
    """
    белмим белмием белмейм белмәдем белмибез белмимдер аңламадым аңламыйм аңламадык
    аңлашылмады аңлашылмый оныттым онытым хәтерләмим хәтерләмәдем
    забыл забыла непонятно хз idk
    """.split()
)
STUCK_NEGATED = frozenset("знаю понял поняла понимаю помню know understand".split())
# «don't» слова делят на «don» и «t».
STUCK_NEGATIONS = frozenset("не dont don not".split())
STUCK_FILLERS = frozenset(
    """
    мин әле ну ә э эм хм ой юк гафу итегез ит кабатла кабатлагыз кабатлачы сорау
    сорауны тагын бер бу нәрсә
    я а да нет извините извини простите прости ещё еще раз повторите повтори
    вопрос это что пока совсем честно
    i do t sorry
    """.split()
)


def is_stuck(tokens: Sequence[str]) -> bool:
    """Ответ — только «не знаю» / «не понял» без другого содержания."""
    negated = any(token in STUCK_NEGATIONS for token in tokens)
    core = [token for token in tokens
            if token in STUCK_CORE or (negated and token in STUCK_NEGATED)]
    if not core:
        return False
    allowed = STUCK_CORE | STUCK_NEGATED | STUCK_NEGATIONS | STUCK_FILLERS | SOCIAL_CORE
    return all(token in allowed for token in tokens)


def classify_answer(text: str, language: str) -> str:
    """Класс ответа: technical, stuck, social, foreign или normal.

    technical — в ответе нет ни одного слова (пустая или сбойная запись);
    stuck — только «не знаю» или «не понял», на любом языке;
    social — только приветствие, благодарность или прощание;
    foreign — ответ не на языке урока (по-русски или латиницей).
    """
    tokens = words(text)
    if not tokens:
        return TECHNICAL
    if is_stuck(tokens):
        return STUCK
    if any(token in SOCIAL_CORE for token in tokens) and all(
        token in SOCIAL_CORE or token in SOCIAL_FILLERS for token in tokens
    ):
        return SOCIAL
    if _is_foreign(fold(text), tokens, language):
        return FOREIGN
    return NORMAL


def _looks_foreign(segment: str) -> bool:
    """Часть названия цели по-русски или латиницей (без букв языков урока)."""
    tokens = words(segment)
    folded = fold(segment)
    if not tokens or any(ch in _ALL_LANGUAGE_LETTERS for ch in folded):
        return False
    return _is_foreign(folded, tokens, "")


def _mostly_latin(segment: str) -> bool:
    tokens = words(segment)
    return bool(tokens) and sum(1 for t in tokens if _LATIN_WORD.fullmatch(t)) * 2 > len(tokens)


def _mostly_cyrillic(segment: str) -> bool:
    tokens = words(segment)
    return bool(tokens) and sum(1 for t in tokens if _CYRILLIC.search(t)) * 2 >= len(tokens)


def _is_foreign(folded: str, tokens: list[str], language: str) -> bool:
    if language in LATIN_LANGUAGES:
        # Английский ответ с русским словом-другим — всё ещё ответ на
        # языке урока; чужой — когда кириллицей не меньше половины слов.
        return sum(1 for token in tokens if _CYRILLIC.search(token)) * 2 >= len(tokens)
    letters = LANGUAGE_LETTERS.get(language, "")
    if letters and any(ch in letters for ch in folded):
        return False
    latin = sum(1 for token in tokens if re.fullmatch(r"[a-z]+", token))
    if latin * 2 >= len(tokens):
        return True
    russian = sum(1 for token in tokens if token in RUSSIAN_MARKERS)
    return russian * 2 >= len(tokens)


# --- Реплика аватара (правило 6) ---

# Слова оценки речи ученика. Сравниваем начала слов: «ошибся», «неверно»,
# «правильнее», «хаталы», «ялгышасың», «дөрес түгел» — одна и та же оценка.
# «хата» не задевает «хатын» (жена): у той другая четвёртая буква.
EVALUATION_STEMS = ("ошиб", "неверн", "неправильн", "правильн", "хата", "ялгыш", "дөрес")
EVALUATION_WORDS = frozenset({"верно"})


def evaluation_words(text: str) -> list[str]:
    return [
        token for token in words(text)
        if token in EVALUATION_WORDS or token.startswith(EVALUATION_STEMS)
    ]


def is_question(sentence: str) -> bool:
    return "?" in sentence


# Конец вопросительного предложения: «?», «??», «?!».
_QUESTION_END = re.compile(r"\?[?!]*")


def count_questions(text: str) -> int:
    """Вопросы в сплошном тексте реплики.

    Реплику живой сессии на предложения никто не делил, поэтому считаем
    концы вопросительных предложений: «Ничек?? Кайда?!» — два вопроса, как
    два предложения с «?» у реплики текстового режима.
    """
    return len(_QUESTION_END.findall(text))


def corrected_words(wrong: str, correct: str) -> list[str]:
    """Слова верной формы, которых не было в неверной цитате.

    По ним проверяется неявная правка: «Казанга» → «Казанда» даёт
    «казанда»; совпадающие слова не доказывают, что правка прозвучала.
    """
    before = set(normalize(wrong).split())
    return [token for token in normalize(correct).split() if token not in before]
