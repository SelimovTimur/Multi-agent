from __future__ import annotations

import re


INJECTION_MARKERS = (
    "ignore previous", "ignore all previous", "игнорируй предыдущ",
    "забудь предыдущ", "system prompt", "системный промпт", "developer message",
    "jailbreak", "act as", "roleplay as", "притворись", "смени роль",
    "раскрой свои инструкции", "покажи инструкции", "покажи системный",
)

OFF_TOPIC_MARKERS = (
    "интеграл", "реши уравн", "матриц", "логарифм", "derivative", "integral",
    "equation", "matrix", "напиши код", "python", "javascript", "typescript",
    "sql", "regex", "api", "алгоритм", "курс валют",
)


# Учительская «Роль и тон» проверяется своим списком. «Притворись продавцом»
# и «act as a guide» — ровно то, ради чего поле существует, поэтому маркеры
# ролевой игры из INJECTION_MARKERS здесь не участвуют. Ловим попытки
# отменить правила и вытащить инструкции.
TEACHER_INJECTION_MARKERS = (
    "ignore previous", "ignore all previous", "ignore the above", "disregard previous",
    "system prompt", "developer message", "developer mode", "jailbreak",
    "reveal your instructions", "игнорируй", "забудь инструкции",
    "забудь все инструкции", "забудь предыдущ", "системный промпт",
    "системные инструкции", "покажи инструкции", "раскрой инструкции",
    "раскрой свои инструкции", "отмени правила", "без ограничений",
)


def looks_like_teacher_injection(text: str) -> bool:
    normalized = text.strip().lower()
    return any(marker in normalized for marker in TEACHER_INJECTION_MARKERS)


def should_refuse_avatar_message(text: str) -> bool:
    normalized = text.strip().lower()
    return any(marker in normalized for marker in INJECTION_MARKERS + OFF_TOPIC_MARKERS)


def sanitize_agent_text(text: str) -> str:
    text = text.strip().replace("**", "").replace("__", "").replace("`", "")
    lines: list[str] = []
    for raw_line in text.splitlines():
        line = raw_line.strip().lstrip("#").strip()
        if line.startswith(("- ", "* ")):
            line = line[2:].strip()
        lines.append(line)
    return re.sub(r"\n{3,}", "\n\n", "\n".join(lines)).strip()


def validate_legacy_exercise(text: str) -> None:
    if "|||" not in text:
        raise ValueError("exercise does not contain the ||| answer separator")
    answer = text.split("|||", 1)[1]
    if "{" not in answer or "}" not in answer:
        raise ValueError("exercise does not contain a JSON answer key")
