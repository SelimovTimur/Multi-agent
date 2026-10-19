"""Порт движка к справочнику уровней `app.levels` (договор, раздел 3).

Справочник пишется отдельно, и движок обращается к нему только через этот
модуль. Пока пакета нет или он падает, порт отдаёт безопасные заглушки:
пустые указания уровня и «уровень не определён». Разговор тогда идёт без
калибровки по справочнику, общие ошибки получают `unknown_level` — ученик
их не видит, учитель видит с пометкой. Это честнее, чем угадывать уровень.

Порт вызывает только публичные функции из договора и только с именованными
аргументами, поэтому после слияния веток работает с настоящим пакетом без
правок.
"""

from __future__ import annotations

import importlib
import logging
from dataclasses import dataclass
from functools import lru_cache
from types import ModuleType
from typing import Any

logger = logging.getLogger("uvicorn.error")

LEVELS: tuple[str, ...] = ("a1", "a2", "b1", "b2", "c1")
_PACKAGE = __name__.rsplit(".", 2)[0] + ".levels"


def level_rank(level: str | None) -> int:
    """"a1"→0 … "c1"→4; пустой или неизвестный уровень → -1 (как в договоре)."""
    value = (level or "").strip().lower()
    return LEVELS.index(value) if value in LEVELS else -1


@dataclass(frozen=True)
class LevelRef:
    """Уровень единицы справочника и то, по чему группируются ошибки."""

    level: str
    source: str
    group_key: str
    group_label: str


@dataclass(frozen=True)
class CatalogEntry:
    id: str
    title_ru: str
    level: str


@lru_cache(maxsize=1)
def _load_default() -> ModuleType | None:
    try:
        return importlib.import_module(_PACKAGE)
    except ModuleNotFoundError as exc:
        if exc.name == _PACKAGE:
            logger.info("levels: пакет %s не установлен, движок работает без справочника", _PACKAGE)
        else:
            logger.warning("levels: пакет %s не загрузился: %s", _PACKAGE, exc)
        return None
    except Exception:
        # Сломанный справочник не должен ронять разговор ученика.
        logger.exception("levels: пакет %s не загрузился", _PACKAGE)
        return None


class LevelsPort:
    def __init__(self, module: Any | None = None):
        self._module = module

    @classmethod
    def default(cls) -> "LevelsPort":
        return cls(_load_default())

    @property
    def available(self) -> bool:
        return self._module is not None

    def _call(self, name: str, *args: Any, **kwargs: Any) -> Any:
        if self._module is None:
            return None
        try:
            return getattr(self._module, name)(*args, **kwargs)
        except Exception:
            logger.warning("levels: %s завершилась ошибкой, уровень не определён", name, exc_info=True)
            return None

    def rank(self, level: str | None) -> int:
        return level_rank(level)

    def guidance(self, level: str, *, language: str, topic_text: str) -> str:
        """Компактные указания уровня для промпта реплики; "" — без калибровки."""
        if not level:
            return ""
        text = self._call("level_guidance", level, language=language, topic_text=topic_text)
        return text.strip() if isinstance(text, str) else ""

    def word(self, token: str, *, language: str) -> LevelRef | None:
        token = token.strip()
        if not token:
            return None
        found = self._call("word_level", token, language=language)
        level = _valid_level(getattr(found, "level", ""))
        if found is None or not level:
            return None
        lemma = str(getattr(found, "lemma", "") or token).strip()
        translation = str(getattr(found, "translation", "") or "").strip()
        return LevelRef(
            level=level,
            source=str(getattr(found, "source", "") or ""),
            group_key="word:" + lemma.casefold(),
            group_label=f"{lemma} — {translation}" if translation else lemma,
        )

    def construction(self, construction_id: str, *, language: str) -> LevelRef | None:
        if not construction_id:
            return None
        found = self._call("construction_by_id", construction_id, language=language)
        level = _valid_level(getattr(found, "level", ""))
        if found is None or not level:
            return None
        return LevelRef(
            level=level,
            source=str(getattr(found, "source", "") or ""),
            group_key=str(getattr(found, "id", "") or construction_id),
            group_label=str(getattr(found, "title_ru", "") or construction_id),
        )

    def catalog(self, *, language: str) -> list[CatalogEntry]:
        """Закрытый список конструкций, из которого модель выбирает id."""
        found = self._call("constructions_catalog", language=language) or []
        entries: list[CatalogEntry] = []
        seen: set[str] = set()
        for item in found:
            cid = str(getattr(item, "id", "") or "")
            level = _valid_level(getattr(item, "level", ""))
            if not cid or not level or cid in seen:
                continue
            seen.add(cid)
            entries.append(CatalogEntry(id=cid, title_ru=str(getattr(item, "title_ru", "") or cid), level=level))
        return entries

    def above_level(self, text: str, *, language: str, level: str) -> list[str]:
        """Слова текста из справочника выше уровня урока (для проверки Уч-0)."""
        if not level or not text.strip():
            return []
        found = self._call("analyze_text", text, language=language, target_level=level)
        words = []
        for item in getattr(found, "above_level", None) or []:
            word = str(getattr(item, "token", "") or getattr(item, "lemma", "") or "").strip()
            if word:
                words.append(word)
        return words

    def speech(self, text: str, *, language: str, target_level: str) -> dict[str, Any]:
        """Уровень речи ученика в форме `speech_level` ответа analyze."""
        empty = {"estimated": "", "shares": {level: 0.0 for level in LEVELS}, "unknown_share": 0.0}
        if not text.strip():
            return empty
        found = self._call("analyze_text", text, language=language, target_level=target_level or None)
        if found is None:
            return empty
        shares = getattr(found, "shares", None) or {}
        try:
            return {
                "estimated": _valid_level(getattr(found, "estimated_level", "")),
                "shares": {level: round(float(shares.get(level, 0.0) or 0.0), 3) for level in LEVELS},
                "unknown_share": round(float(getattr(found, "unknown_share", 0.0) or 0.0), 3),
            }
        except (TypeError, ValueError, AttributeError):
            logger.warning("levels: analyze_text вернула неожиданную форму")
            return empty


def _valid_level(value: Any) -> str:
    level = str(value or "").strip().lower()
    return level if level in LEVELS else ""
