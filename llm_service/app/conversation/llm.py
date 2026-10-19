"""Вызовы модели движка: схемы ответов, разбор и одна повторная попытка.

Схема для провайдера строгая (перечисления ключей целей, номеров ходов и
конструкций), а разбор у нас мягкий: незнакомый ключ или лишнее поле не
роняют ход, а отбрасываются кодом. Повтор — только когда ответ вообще не
разбирается как JSON нужной формы; второй такой ответ — ProviderError, и
Go показывает ученику «Аватар не смог ответить», сохранив его ответ.
"""

from __future__ import annotations

import json
import logging
import re
import time
from collections.abc import Callable, Sequence
from typing import Any, TypeVar

from pydantic import BaseModel, ConfigDict, Field, ValidationError, field_validator

from ..provider import LLMProvider, ProviderError
from ..schemas import ChatMessage
from .prompts import RETRY_INVALID_JSON

logger = logging.getLogger("uvicorn.error")
T = TypeVar("T")

ERROR_KINDS = ("wrong_form", "misuse", "not_used")


class _Lenient(BaseModel):
    model_config = ConfigDict(extra="ignore")


class ReplySentence(_Lenient):
    text: str
    ru: str = ""

    @field_validator("text", "ru")
    @classmethod
    def single_line(cls, value: str) -> str:
        return " ".join(value.split())


class ReplyOut(_Lenient):
    sentences: list[ReplySentence] = Field(min_length=1, max_length=3)

    @field_validator("sentences")
    @classmethod
    def not_empty(cls, value: list[ReplySentence]) -> list[ReplySentence]:
        if not any(sentence.text for sentence in value):
            raise ValueError("пустая реплика")
        return value


class MarkOut(_Lenient):
    turn_no: int | None = None
    goal_key: str
    verdict: str
    error_kind: str | None = None
    quote: str
    correct_form: str = ""
    note_ru: str = ""


class MarksOut(_Lenient):
    marks: list[MarkOut]


class GeneralErrorOut(_Lenient):
    turn_no: int
    quote: str
    type: str
    correct_form: str = ""
    rule_ru: str = ""
    key_word: str = ""
    construction_id: str = "unknown"
    label_ru: str = ""


class GeneralErrorsOut(_Lenient):
    errors: list[GeneralErrorOut]


class TranslationOut(_Lenient):
    ru: str


def _object(properties: dict[str, Any]) -> dict[str, Any]:
    return {
        "type": "object",
        "properties": properties,
        "required": list(properties),
        "additionalProperties": False,
    }


def reply_schema() -> dict[str, Any]:
    sentence = _object({"text": {"type": "string"}, "ru": {"type": "string"}})
    return _object({"sentences": {"type": "array", "items": sentence, "minItems": 1, "maxItems": 3}})


def marks_schema(goal_keys: list[str], turn_numbers: list[int] | None = None) -> dict[str, Any]:
    properties: dict[str, Any] = {}
    if turn_numbers is not None:
        properties["turn_no"] = {"type": "integer", "enum": turn_numbers}
    properties.update({
        "goal_key": {"type": "string", "enum": goal_keys},
        "verdict": {"type": "string", "enum": ["credited", "error"]},
        # Пустая строка вместо null: строковое перечисление понимают все
        # провайдеры строгой схемы, а anyOf с null — не все.
        "error_kind": {"type": "string", "enum": [*ERROR_KINDS, ""]},
        "quote": {"type": "string"},
        "correct_form": {"type": "string"},
        "note_ru": {"type": "string"},
    })
    return _object({"marks": {"type": "array", "items": _object(properties)}})


def translation_schema() -> dict[str, Any]:
    return _object({"ru": {"type": "string"}})


def errors_schema(turn_numbers: list[int], construction_ids: list[str]) -> dict[str, Any]:
    item = _object({
        "turn_no": {"type": "integer", "enum": turn_numbers},
        "quote": {"type": "string"},
        "type": {"type": "string", "enum": ["lexical", "grammar"]},
        "correct_form": {"type": "string"},
        "rule_ru": {"type": "string"},
        "key_word": {"type": "string"},
        "construction_id": {"type": "string", "enum": [*construction_ids, "unknown"]},
        "label_ru": {"type": "string"},
    })
    return _object({"errors": {"type": "array", "items": item}})


_FENCE = re.compile(r"^\s*```(?:json)?\s*(.*?)\s*```\s*$", re.S)


def _json_payload(content: str) -> Any:
    # Часть моделей оборачивает JSON в ```json даже при строгой схеме:
    # это не повод тратить повторный вызов.
    match = _FENCE.match(content)
    return json.loads(match.group(1) if match else content)


class ModelCaller:
    def __init__(
        self,
        provider: LLMProvider,
        *,
        model: str | None = None,
        reasoning_enabled: bool | None = None,
    ):
        self.provider = provider
        self.model = model or None
        self.reasoning_enabled = reasoning_enabled

    async def ask(
        self,
        messages: Sequence[ChatMessage],
        *,
        schema: dict[str, Any],
        name: str,
        parse: Callable[[Any], T],
        attempts: int = 2,
    ) -> T:
        """Вызов модели со строгой схемой; attempts=1 — без повтора при сбое формата."""
        kwargs: dict[str, Any] = {
            "response_format": {
                "type": "json_schema",
                "json_schema": {"name": name, "strict": True, "schema": schema},
            },
        }
        if self.model:
            kwargs["model"] = self.model
        if self.reasoning_enabled is not None:
            kwargs["reasoning_enabled"] = self.reasoning_enabled
        pending = list(messages)
        for attempt in range(1, attempts + 1):
            started = time.monotonic()
            try:
                result = await self.provider.complete(pending, **kwargs)
            finally:
                logger.info("conversation stage=%s attempt=%s elapsed_seconds=%.2f",
                            name, attempt, time.monotonic() - started)
            try:
                return parse(_json_payload(result.content))
            except (ValueError, TypeError, ValidationError) as exc:
                logger.warning("conversation stage=%s invalid_output=%s attempt=%s",
                               name, type(exc).__name__, attempt)
                if attempt == attempts:
                    raise ProviderError(
                        f"Модель вернула ответ с нарушением схемы ({name})"
                    ) from exc
                pending = [*pending, ChatMessage(role="user", content=RETRY_INVALID_JSON)]
        raise AssertionError("unreachable")
