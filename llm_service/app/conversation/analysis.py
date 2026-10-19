"""Разбор после разговора: `POST /v1/conversation/analyze`.

Отметки по целям берутся из состояния — их уже поставили ходы. Ходы без
отметок (сбой модели на последнем ответе, разговор старого режима,
`state=null`) оцениваются здесь одним вызовом. Общие ошибки ищутся один
раз, вторым вызовом; их уровень и видимость определяет код по справочнику
уровней, а не модель.
"""

from __future__ import annotations

import logging
from collections.abc import Sequence

from ..schemas import ChatMessage
from ..validators import should_refuse_avatar_message
from . import prompts
from .levels_port import LevelRef, LevelsPort
from .llm import GeneralErrorsOut, MarkOut, MarksOut, ModelCaller, errors_schema, marks_schema
from .live import reply_target
from .policy import finalize_marks, goal_reports, pick_next_time, sort_marks, visibility
from .schemas import (
    AnalyzeRequest,
    AnalyzeResponse,
    EngineState,
    GeneralError,
    Goal,
    GoalRef,
    Mark,
    SpeechLevel,
    Summary,
    TranscriptItem,
)
from .text import (
    FOREIGN,
    STUCK,
    TECHNICAL,
    answer_quote,
    classify_answer,
    corrected_words,
    ground_quote,
    normalize,
    overlaps,
)

logger = logging.getLogger("uvicorn.error")

GENERAL_ERRORS_LIMIT = 40
CONTENT_LIMIT = 2_000
HIDDEN_ANSWER = "[ответ не по теме урока]"


def _order(item: TranscriptItem) -> tuple[int, int]:
    return item.turn_no, 0 if item.role == "student" else 1


async def analyze_conversation(
    request: AnalyzeRequest,
    state: EngineState | None,
    *,
    caller: ModelCaller,
    levels: LevelsPort,
) -> AnalyzeResponse:
    goals = list(request.goals)
    keys = {goal.key for goal in goals}
    entries = sorted(request.transcript, key=_order)

    students: dict[int, str] = {}
    # Реплики аватара перед каждым ответом — для правила 1. В живом режиме
    # на один ответ сессия может сказать несколько реплик: повтор за любой
    # из них — не зачёт. Нет реплик между ответами — остаются прежние.
    previous_avatar: dict[int, list[str]] = {}
    heard: list[str] = []
    after_student = True
    for item in entries:
        if item.role == "avatar":
            if after_student:
                heard, after_student = [], False
            heard.append(item.content)
        elif item.turn_no >= 1 and item.turn_no not in students:
            students[item.turn_no] = item.content
            previous_avatar[item.turn_no] = list(heard)
            after_student = True
    classes = {turn: classify_answer(text, request.language) for turn, text in students.items()}
    hidden = {turn for turn, text in students.items()
              if classes[turn] != TECHNICAL and should_refuse_avatar_message(text)}
    transcript = [
        {"turn_no": item.turn_no, "role": item.role,
         "content": HIDDEN_ANSWER if item.role == "student" and item.turn_no in hidden
         else item.content[:CONTENT_LIMIT]}
        for item in entries
    ]

    marks: list[Mark] = []
    evaluated: set[int] = set()
    if state is not None:
        for record in state.turns:
            text = students.get(record.turn_no)
            if text is None:
                # Отметка без ответа в расшифровке никуда не ведёт учителя.
                continue
            evaluated.add(record.turn_no)
            for mark in record.marks:
                if mark.goal_key in keys:
                    regrounded = _reground(mark, text)
                    if regrounded is not None:
                        marks.append(regrounded)

    pending = [turn for turn in sorted(students) if turn not in evaluated]
    targets: dict[int, str] = {}
    if state is not None and not state.closed:
        # Последний ответ, на который аватар не успел ответить: реплика,
        # к которой он относится, вела к state.target, а в живом режиме —
        # к цели указания, по которому её сказала сессия (как в turn).
        target = reply_target(state, state.last_turn + 1)
        if target in keys:
            targets[state.last_turn + 1] = target
    if pending:
        checkable = [turn for turn in pending if classes[turn] != TECHNICAL and turn not in hidden]
        raw: dict[int, list[MarkOut]] = {turn: [] for turn in pending}
        if goals and checkable:
            for item in await _evaluate_turns(request, goals, transcript, checkable, targets, caller):
                if item.turn_no in checkable:
                    raw[item.turn_no].append(item)
        for turn in pending:
            marks.extend(finalize_marks(
                raw[turn], turn_no=turn, text=students[turn], answer_class=classes[turn],
                previous_reply=previous_avatar[turn], target=targets.get(turn), goals=goals,
            ))
    marks = sort_marks(marks, goals)

    answers = {turn: text for turn, text in students.items()
               if classes[turn] not in (TECHNICAL, FOREIGN, STUCK) and turn not in hidden}
    general = await _general_errors(request, goals, transcript, answers, marks, caller, levels)
    reports = goal_reports(goals, marks)
    credited = {report.key for report in reports if report.credited}
    phrases = [goal for goal in goals if goal.kind == "phrase"]
    themes = [goal for goal in goals if goal.kind == "grammar"]
    summary = Summary(
        credited_phrases=[GoalRef(key=goal.key, title=goal.title) for goal in phrases if goal.key in credited],
        applied_themes=[GoalRef(key=goal.key, title=goal.title) for goal in themes if goal.key in credited],
        phrases_total=len(phrases),
        themes_total=len(themes),
        next_time=pick_next_time(goals, reports, marks, general),
    )
    speech = levels.speech("\n".join(answers[turn] for turn in sorted(answers)),
                           language=request.language, target_level=request.level)
    return AnalyzeResponse(goals=reports, marks=marks, general_errors=general,
                           summary=summary, speech_level=SpeechLevel(**speech))


def _reground(mark: Mark, text: str) -> Mark | None:
    """Цитата отметки из состояния — снова точная подстрока ответа (правило 5)."""
    if mark.verdict == "miss":
        if mark.quote and mark.quote in text:
            return mark
        quote = answer_quote(text)
        return mark.model_copy(update={"quote": quote}) if quote else None
    quote = ground_quote(mark.quote, text)
    return mark.model_copy(update={"quote": quote}) if quote else None


async def _evaluate_turns(
    request: AnalyzeRequest,
    goals: Sequence[Goal],
    transcript: list[dict],
    checkable: list[int],
    targets: dict[int, str],
    caller: ModelCaller,
) -> list[MarkOut]:
    data = {
        "level": request.level,
        "goals": [goal.model_dump() for goal in goals],
        "transcript": transcript,
        "turns_to_check": checkable,
        "targets": {str(turn): targets[turn] for turn in checkable if turn in targets},
    }
    messages = [
        ChatMessage(role="system", content=prompts.marks_system(
            language=request.language, level=request.level, batch=True)),
        ChatMessage(role="user", content=prompts.marks_user(data)),
    ]
    result = await caller.ask(
        messages, schema=marks_schema([goal.key for goal in goals], turn_numbers=checkable),
        name="conversation_analysis_marks", parse=MarksOut.model_validate,
    )
    return result.marks


async def _general_errors(
    request: AnalyzeRequest,
    goals: Sequence[Goal],
    transcript: list[dict],
    answers: dict[int, str],
    marks: Sequence[Mark],
    caller: ModelCaller,
    levels: LevelsPort,
) -> list[GeneralError]:
    if not answers:
        return []
    language = request.language
    catalog = levels.catalog(language=language)
    data = {
        "level": request.level,
        "goals": [{"key": goal.key, "kind": goal.kind, "title": goal.title} for goal in goals],
        "goal_marks": [{"turn_no": mark.turn_no, "goal_key": mark.goal_key,
                        "verdict": mark.verdict, "quote": mark.quote}
                       for mark in marks if mark.verdict != "miss"],
        "transcript": transcript,
        "constructions": [{"id": entry.id, "title": entry.title_ru, "level": entry.level}
                          for entry in catalog],
    }
    messages = [
        ChatMessage(role="system", content=prompts.errors_system(language=language, level=request.level)),
        ChatMessage(role="user", content=prompts.errors_user(data)),
    ]
    result = await caller.ask(
        messages, schema=errors_schema(sorted(answers), [entry.id for entry in catalog]),
        name="conversation_general_errors", parse=GeneralErrorsOut.model_validate,
    )
    known_ids = {entry.id for entry in catalog}
    found: list[GeneralError] = []
    seen: set[tuple[int, str]] = set()
    for item in result.errors:
        text = answers.get(item.turn_no)
        if text is None or item.type not in ("lexical", "grammar"):
            continue  # реплики аватара и чужие ходы не оцениваются (правило 7)
        quote = ground_quote(item.quote, text)
        if quote is None:
            continue
        correct = item.correct_form.strip()[:300]
        rule = item.rule_ru.strip()[:300]
        if not correct and not rule:
            continue  # у общей ошибки должна быть верная форма или правило
        if any(mark.turn_no == item.turn_no and mark.verdict != "miss"
               and overlaps(quote, mark.quote, text) for mark in marks):
            continue  # это место уже отмечено по цели — не дублируем
        identity = (item.turn_no, normalize(quote))
        if identity in seen:
            continue
        seen.add(identity)
        ref = _level_ref(item.type, item.construction_id, item.key_word, quote, correct,
                         known_ids, language, levels)
        level = ref.level if ref else ""
        group_key, group_label = (ref.group_key, ref.group_label) if ref else _fallback_group(
            item.type, item.label_ru, item.key_word, correct or quote)
        found.append(GeneralError(
            turn_no=item.turn_no, quote=quote, type=item.type, correct_form=correct,
            rule_ru=rule, level=level, level_source=ref.source if ref else "",
            visibility=visibility(level, request.level),
            group_key=group_key, group_label=group_label,
        ))
        if len(found) >= GENERAL_ERRORS_LIMIT:
            break
    found.sort(key=lambda error: (error.turn_no, answers[error.turn_no].find(error.quote)))
    return found


def _level_ref(
    kind: str,
    construction_id: str,
    key_word: str,
    quote: str,
    correct: str,
    known_ids: set[str],
    language: str,
    levels: LevelsPort,
) -> LevelRef | None:
    """Уровень общей ошибки по справочнику (правило 7).

    Грамматика — только по id из закрытого каталога: «не знаю» и id не из
    каталога дают «уровень не определён». Лексика — по верному слову; если
    модель его не назвала, по словам верной формы, которых не было в цитате:
    совпадающие слова («мин») к ошибке отношения не имеют, и их уровень
    приписал бы ошибке чужой уровень.
    """
    if kind == "grammar":
        cid = construction_id.strip()
        if cid and cid != "unknown" and cid in known_ids:
            return levels.construction(cid, language=language)
        return None
    for word in [key_word.strip(), *corrected_words(quote, correct)]:
        if word:
            ref = levels.word(word, language=language)
            if ref is not None:
                return ref
    return None


def _fallback_group(kind: str, label: str, key_word: str, correct: str) -> tuple[str, str]:
    label = " ".join(label.split())[:80]
    if kind == "grammar":
        label = label or "Грамматика"
        return "grammar:" + normalize(label).replace(" ", "_")[:60], label
    base = key_word.strip() or correct
    return "word:" + normalize(base).replace(" ", "_")[:60], label or base[:80]
