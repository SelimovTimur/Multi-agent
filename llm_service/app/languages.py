"""Языки, на которых идёт разговор с аватаром.

Код языка урока («english») приходит из Go вместе с материалом урока.
Неизвестный язык — ошибка запроса, а не молчаливый разговор «на каком-то».

Уроки MVP английские. Татарский — второй язык движка: на нём написаны
тесты логики разговора (tests/), и разговор на нём работает так же.
"""
from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class LanguageSpec:
    code: str
    russian_name: str
    native_name: str


LANGUAGES = {
    "english": LanguageSpec("english", "английский", "English"),
    "tatar": LanguageSpec("tatar", "татарский", "татар теле"),
}

ALIASES = {
    "en": "english", "eng": "english", "англ": "english", "английский": "english",
    "tt": "tatar", "tat": "tatar", "татар": "tatar", "татарский": "tatar",
}


def normalize_language(value: str) -> str:
    normalized = value.strip().lower()
    normalized = ALIASES.get(normalized, normalized)
    if normalized not in LANGUAGES:
        raise ValueError(f"unsupported language: {value!r}")
    return normalized


# Короткая памятка нормы языка для промпта аватара: что модель чаще всего
# нарушает, разговаривая со школьником.
LANGUAGE_GRAMMAR_GUARDS = {
    "english": """
Пиши на естественном современном английском (British или American — один
вариант на весь разговор, без смешения написаний). Соблюдай согласование
подлежащего и сказуемого, артикли a/an/the, порядок слов SVO, вспомогательные
do/does/did в вопросах и отрицаниях. Не калькируй русский синтаксис и не
вставляй русские слова в английские реплики.
""",
    "tatar": """
Татарский текст — только татарской кириллицей с буквами ә, ө, ү, җ, ң, һ.
Никакой латиницы, турецких букв и букв казахского и башкирского алфавитов;
не используй казахские, турецкие и башкирские слова вместо татарских.
Соблюдай татарскую литературную норму: порядок SOV и сказуемое в конце,
определение перед существительным, послелоги после имени, сингармонизм.
Не калькируй русский синтаксис.
""",
}
