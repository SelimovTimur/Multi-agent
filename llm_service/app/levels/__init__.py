"""Справочник уровней английского: профили CEFR A1–B2.

Публичный API тот же, что ждёт порт движка (conversation/levels_port.py):
компактные указания уровня для промптов и уровень слова, текста и
конструкции. Профили дают только указания уровня. Пословного справочника
у английского здесь нет, поэтому уровень слова, текста и конструкции не
определяется: порт отдаёт «уровень не определён», и разговор идёт без
пословной калибровки.
"""
from __future__ import annotations

from .english import LEVELS, level_guidance, normalize_level


def word_level(token: str, *, language: str):
    return None


def construction_by_id(construction_id: str, *, language: str):
    return None


def constructions_catalog(*, language: str) -> list:
    return []


def analyze_text(text: str, *, language: str, target_level: str | None = None):
    return None


__all__ = [
    "LEVELS",
    "analyze_text",
    "construction_by_id",
    "constructions_catalog",
    "level_guidance",
    "normalize_level",
    "word_level",
]
