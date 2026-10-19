"""Профили CEFR для английского: на что опирается аватар на каждом уровне.

Грамматика и связки — по English Grammar Profile, лексика — по Oxford
3000/5000, длина предложений — по образцам заданий Cambridge. Указания
уходят в промпт голосовой сессии: аватар говорит в пределах уровня
ученика, без упрощения до детского языка.
"""
from __future__ import annotations

from dataclasses import dataclass

LEVELS: tuple[str, ...] = ("a1", "a2", "b1", "b2", "c1")

# Слишком длинные указания вытесняют из промпта материал урока.
MAX_CHARS = 1800

_ENGLISH = {"english", "en", "eng"}


@dataclass(frozen=True)
class Profile:
    sentences: str
    grammar: tuple[str, ...]
    linkers: tuple[str, ...]
    sample: str


PROFILES = {
    "a1": Profile(
        sentences=(
            "одна простая мысль на предложение, прямой порядок слов; простые "
            "предложения соединяются через and, but, or; из придаточных только "
            "причина с because; около 8 слов в предложении"
        ),
        grammar=(
            "to be с именной частью: She is twelve",
            "Present Simple для привычек и фактов",
            "Present Continuous для действия в момент речи",
            "Past Simple только в утвердительной форме",
            "can для умения и просьбы, there is / there are",
        ),
        linkers=("and", "but", "or", "because"),
        sample="My sister is twelve, and she likes music and football.",
    ),
    "a2": Profile(
        sentences=(
            "простые предложения и сложные с одним придаточным when, if, before, "
            "after или because; около 14 слов в предложении"
        ),
        grammar=(
            "Present Perfect для опыта с ever и never",
            "Past Continuous для фона и прерванного действия",
            "be going to для намерений",
            "сравнительная и превосходная степень прилагательных",
            "should для совета, have to и must для обязанности, might для возможности",
        ),
        linkers=("first", "then", "after that", "so", "when"),
        sample="When it rains at the weekend, we usually stay at home and watch films together.",
    ),
    "b1": Profile(
        sentences=(
            "сложные предложения с придаточными уступки, причины, цели и условия "
            "(although, as, so that, if); объяснение причин, мнений и событий; "
            "около 16 слов в предложении"
        ),
        grammar=(
            "условные предложения первого, второго и третьего типа",
            "Present Perfect в противопоставлении Past Simple",
            "Present Perfect Continuous с for и since",
            "Past Perfect",
            "косвенная речь и косвенные вопросы",
        ),
        linkers=("although", "therefore", "in my opinion", "firstly … finally"),
        sample="If I had more free time, I would join a club and learn to play the guitar.",
    ),
    "b2": Profile(
        sentences=(
            "развёрнутые предложения с несколькими придаточными и оборотами "
            "-ing и having done; аргументация, сравнение и вывод"
        ),
        grammar=(
            "пассив в перфектных и длительных временах и с модальными перфектами",
            "модальные перфекты для догадки и сожаления: must have, could have",
            "wish и if only с Past Perfect",
            "определительные с whose и which на всё предложение",
        ),
        linkers=("whereas", "nevertheless", "not only … but also", "to sum up"),
        sample="Having finished her exams, Kate finally had enough time to read novels for pleasure.",
    ),
}
# C1 говорит без ограничений сверху: профиль B2 задаёт нижнюю планку.
PROFILES["c1"] = PROFILES["b2"]


def normalize_level(level: str | None) -> str:
    value = (level or "").strip().casefold()
    return value if value in LEVELS else ""


def level_guidance(level: str, language: str = "english", topic_text: str = "", max_words: int = 60) -> str:
    """Указания уровня для промпта; "" — язык не английский или уровень неизвестен."""
    if (language or "").strip().casefold() not in _ENGLISH:
        return ""
    value = normalize_level(level)
    if not value:
        return ""
    profile = PROFILES[value]
    label = value.upper()
    lines = [
        f"Уровень ученика — {label} по CEFR. Говори в пределах этого уровня, "
        "без упрощения до детского языка.",
        f"Предложения: {profile.sentences}.",
        "Грамматика, которую ученик знает: " + "; ".join(profile.grammar) + ".",
        "Связки: " + ", ".join(profile.linkers) + ".",
        f"Пример фразы этого уровня: {profile.sample}",
    ]
    rank = LEVELS.index(value)
    above = [lvl for lvl in LEVELS[rank + 1:] if PROFILES[lvl] is not profile]
    if above:
        nxt = PROFILES[above[0]]
        lines.append("Не используй конструкции выше уровня ученика, например: " + "; ".join(nxt.grammar[:3]) + ".")
    return "\n".join(lines)[:MAX_CHARS]
