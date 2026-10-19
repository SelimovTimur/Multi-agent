"""Детерминированные правила движка (договор, раздел 4).

Модель только судит: какие цели ученик употребил и как, какие у него общие
ошибки. Всё, что можно посчитать, считает этот модуль: проверку цитат,
непризнание повтора за аватаром, промахи, смену захода, выбор следующей
цели, видимость общих ошибок и «одну вещь на следующий раз». Так отметки
учителю подтверждены словами ученика, а разговор одинаково ведёт себя на
одинаковых данных.
"""

from __future__ import annotations

from collections import Counter
from collections.abc import Iterable, Sequence

from .levels_port import level_rank
from .llm import ERROR_KINDS, MarkOut
from .schemas import EngineState, GeneralError, Goal, GoalProgress, GoalReport, Mark, NextTime
from .text import (
    NORMAL,
    STUCK,
    TECHNICAL,
    PhrasePattern,
    answer_quote,
    contains_words,
    corrected_words,
    evaluation_words,
    ground_quote,
    normalize,
)

VERDICT_ORDER = {"credited": 0, "error": 1, "miss": 2}
KIND_ORDER = {kind: index for index, kind in enumerate(ERROR_KINDS)}


def repeated_from_avatar(goal: Goal, quote: str, previous_reply: str) -> bool:
    """Правило 1: употребление прозвучало в предыдущей реплике аватара.

    Для фразы сравнивается её постоянная часть; для любой цели — сама
    цитата целыми словами. «Мин дә Казанда» после «Мин Казанда яшим» —
    повтор, а не самостоятельное употребление.
    """
    if not previous_reply.strip():
        return False
    if goal.kind == "phrase" and PhrasePattern.from_title(goal.title).occurs_in(previous_reply):
        return True
    return contains_words(previous_reply, quote)


def finalize_marks(
    raw: Iterable[MarkOut],
    *,
    turn_no: int,
    text: str,
    answer_class: str,
    previous_reply: str | Sequence[str],
    target: str | None,
    goals: Sequence[Goal],
) -> list[Mark]:
    """Отметки хода из суждения модели — только то, что подтверждено кодом.

    previous_reply — реплика аватара перед ответом или, в живом режиме,
    все его реплики после прошлого ответа ученика: сессия может сказать
    на один ответ несколько реплик, и повтор за любой из них — не зачёт.
    """
    heard = [previous_reply] if isinstance(previous_reply, str) else list(previous_reply)
    by_key = {goal.key: goal for goal in goals}
    kept: list[Mark] = []
    seen: dict[tuple[str, str], int] = {}
    for item in raw:
        goal = by_key.get(item.goal_key)
        if goal is None or item.verdict not in ("credited", "error"):
            continue
        if item.verdict == "error":
            # Правило 2: приветствие, «спасибо», ответ не на языке урока и
            # технический сбой ошибкой по цели не считаются.
            if answer_class != NORMAL or item.error_kind not in ERROR_KINDS:
                continue
        quote = ground_quote(item.quote, text)
        if quote is None:
            continue  # правило 5: выдуманная цитата — отметки нет
        if item.verdict == "credited" and any(
            repeated_from_avatar(goal, quote, reply) for reply in heard
        ):
            continue
        error = item.verdict == "error"
        mark = Mark(
            turn_no=turn_no,
            goal_key=goal.key,
            verdict=item.verdict,
            error_kind=item.error_kind if error else None,
            quote=quote,
            correct_form=item.correct_form.strip()[:300] if error else "",
            note_ru=item.note_ru.strip()[:300] if error else "",
        )
        identity = (goal.key, normalize(quote))
        if identity in seen:
            # Зачёт и ошибка на одном месте — модель противоречит себе.
            # Оставляем ошибку: ложный зачёт учителю вреднее пропущенного.
            index = seen[identity]
            if kept[index].verdict == "credited" and error:
                kept[index] = mark
            continue
        seen[identity] = len(kept)
        kept.append(mark)
    if target in by_key and answer_class != TECHNICAL:
        attempted = any(mark.goal_key == target for mark in kept)
        if not attempted:
            # Правило 3: ответ на реплику, которая вела к цели, без её
            # употребления. Ответ с ошибкой по этой цели — тоже неудачная
            # попытка, но её уже показывает отметка error: второй отметкой
            # промаха её не дублируем.
            kept.append(Mark(turn_no=turn_no, goal_key=target, verdict="miss",
                             quote=answer_quote(text)))
    return kept


def apply_marks(progress: dict[str, GoalProgress], marks: Iterable[Mark], *, turn_no: int) -> None:
    for mark in marks:
        entry = progress.get(mark.goal_key)
        if entry is None:
            continue
        if mark.verdict == "credited":
            entry.correct_uses += 1
            if not entry.credited:
                entry.credited = True
                entry.credited_turn = turn_no
        elif mark.verdict == "error":
            entry.errors += 1
        else:
            entry.misses += 1


def register_attempt(
    progress: dict[str, GoalProgress],
    marks: Sequence[Mark],
    *,
    target: str | None,
    answer_class: str,
) -> bool:
    """Итог попытки по цели реплики. True — пора заходить иначе (правило 3).

    Неудача — промах или ошибка по цели: в обоих случаях зачтённого
    употребления нет. После второй неудачи подряд заход меняется, счётчик
    подряд обнуляется: к цели можно вернуться позже и снова дать ей две
    попытки уже новым заходом.

    «Не знаю» с первой попытки (answer_class STUCK) меняет заход сразу — на
    выбор из двух — и оставляет счётчик подряд: вторая попытка та же цель,
    но проще (stays_on_target). Не вышло и она — дальше как обычно.
    """
    if target is None or target not in progress or answer_class == TECHNICAL:
        return False
    entry = progress[target]
    if any(mark.goal_key == target and mark.verdict == "credited" for mark in marks):
        entry.streak = 0
        return False
    entry.failed += 1
    entry.streak += 1
    if answer_class == STUCK and entry.streak == 1:
        entry.variant = choice_variant(entry.variant)
        return True
    if entry.streak < 2:
        return False
    entry.variant += 1
    entry.streak = 0
    return True


# Номер захода «вопрос с выбором из двух» (prompts.APPROACHES[2]): заходы
# с первого идут по кругу 1, 2, 3, поэтому это номера 2, 5, 8…
CHOICE_APPROACH = 2


def choice_variant(variant: int) -> int:
    """Ближайший следующий заход «выбор из двух» после variant."""
    following = variant + 1
    while following <= 0 or 1 + (following - 1) % 3 != CHOICE_APPROACH:
        following += 1
    return following


def stays_on_target(
    progress: dict[str, GoalProgress],
    *,
    target: str | None,
    answer_class: str,
    changed: bool,
) -> bool:
    """Ученик сказал «не знаю» с первой попытки: следующая реплика — та же
    цель, проще (register_attempt уже сменил заход)."""
    if not changed or answer_class != STUCK or target is None or target not in progress:
        return False
    entry = progress[target]
    return not entry.credited and entry.streak == 1


def choose_target(
    goals: Sequence[Goal],
    progress: dict[str, GoalProgress],
    *,
    exclude: Iterable[str] = (),
) -> str | None:
    """Правило 4: следующая цель реплики.

    Сначала открытые цели, к которым ещё не вели, — по порядку урока; затем
    открытая с меньшим числом неудач (при равенстве — с меньшим числом
    заходов, потом та, к которой вели давнее). Каждая реплика ведёт к новой
    цели, пока такие есть, поэтому при N ≥ числа целей каждая цель получает
    хотя бы один заход. Цель из exclude (две неудачи подряд) пропускается,
    если есть другие открытые. Все цели закрыты — None, разговор по теме.
    """
    open_goals = [goal for goal in goals if not progress[goal.key].credited]
    if not open_goals:
        return None
    excluded = set(exclude)
    pool = [goal for goal in open_goals if goal.key not in excluded] or open_goals
    fresh = [goal for goal in pool if progress[goal.key].approaches == 0]
    if fresh:
        return fresh[0].key
    order = {goal.key: index for index, goal in enumerate(goals)}
    return min(pool, key=lambda goal: (
        progress[goal.key].failed,
        progress[goal.key].approaches,
        progress[goal.key].last_led,
        order[goal.key],
    )).key


def lead_to(state: EngineState, target: str | None, *, turn_no: int) -> None:
    """Записать, что следующая реплика аватара ведёт к target."""
    state.target = target
    state.approach = 0
    if target is None:
        return
    entry = state.progress[target]
    entry.approaches += 1
    entry.last_led = turn_no
    state.approach = entry.variant


def uncredited_phrase_goals(state: EngineState) -> list[tuple[Goal, PhrasePattern]]:
    """Незачтённые фокусные фразы с их целями — по ключам живой режим
    сообщает, какую фразу аватар всё-таки произнёс."""
    found = []
    for goal in state.goals:
        if goal.kind != "phrase" or state.progress[goal.key].credited:
            continue
        pattern = PhrasePattern.from_title(goal.title)
        if pattern.parts:
            found.append((goal, pattern))
    return found


def uncredited_phrases(state: EngineState) -> list[tuple[str, PhrasePattern]]:
    """Фокусные фразы, которые аватар пока не произносит (правило 6)."""
    return [(goal.title, pattern) for goal, pattern in uncredited_phrase_goals(state)]


def implicit_corrections(
    marks: Sequence[Mark],
    forbidden: Sequence[tuple[str, PhrasePattern]],
    *,
    limit: int = 2,
) -> list[dict[str, object]]:
    """Верные формы для неявной правки в следующей реплике.

    Только ошибки формы с известной верной формой. Если верная форма —
    незачтённая фокусная фраза, правка не нужна: аватар не должен
    произносить её за ученика (правило 6 сильнее).
    """
    corrections: list[dict[str, object]] = []
    for mark in marks:
        if mark.verdict != "error" or mark.error_kind != "wrong_form" or not mark.correct_form:
            continue
        if any(pattern.occurs_in(mark.correct_form) for _, pattern in forbidden):
            continue
        needed = corrected_words(mark.quote, mark.correct_form)
        if not needed:
            continue
        corrections.append({"wrong": mark.quote, "correct": mark.correct_form, "words": needed})
        if len(corrections) >= limit:
            break
    return corrections


# --- Разбор после разговора ---


def visibility(error_level: str, lesson_level: str) -> str:
    """Раздел 1 договора: main, above_level или unknown_level.

    Если неизвестен уровень урока, сравнить не с чем — ошибка получает
    unknown_level: ученик её не видит, учитель видит с пометкой.
    """
    error_rank = level_rank(error_level)
    lesson_rank = level_rank(lesson_level)
    if error_rank < 0 or lesson_rank < 0:
        return "unknown_level"
    return "main" if error_rank <= lesson_rank else "above_level"


def goal_reports(goals: Sequence[Goal], marks: Sequence[Mark]) -> list[GoalReport]:
    reports = []
    for goal in goals:
        own = [mark for mark in marks if mark.goal_key == goal.key]
        credited = [mark.turn_no for mark in own if mark.verdict == "credited"]
        reports.append(GoalReport(
            key=goal.key,
            kind=goal.kind,
            title=goal.title,
            note=goal.note,
            credited=bool(credited),
            credited_turn=min(credited) if credited else None,
            correct_uses=len(credited),
            errors=sum(1 for mark in own if mark.verdict == "error"),
            misses=sum(1 for mark in own if mark.verdict == "miss"),
        ))
    return reports


def sort_marks(marks: Iterable[Mark], goals: Sequence[Goal]) -> list[Mark]:
    order = {goal.key: index for index, goal in enumerate(goals)}
    return sorted(marks, key=lambda mark: (
        mark.turn_no, order.get(mark.goal_key, len(order)), VERDICT_ORDER[mark.verdict]))


def _clean_hint(text: str) -> str:
    # Итог ученика — без слов оценки: подсказка модели с «неверно» заменяется
    # шаблоном, а не показывается.
    text = " ".join(text.split())
    return "" if not text or evaluation_words(text) else text[:200]


def pick_next_time(
    goals: Sequence[Goal],
    reports: Sequence[GoalReport],
    marks: Sequence[Mark],
    general_errors: Sequence[GeneralError],
) -> NextTime | None:
    """Правило 8: одна вещь на следующий раз.

    Сначала ошибки по целям: незакрытая цель важнее закрытой, ошибка формы с
    верной формой — понятнее остальных, чаще ошибались — важнее, свежая —
    важнее старой. Нет ошибок по целям — общие ошибки уровня урока
    (visibility=main): сначала самая частая группа. Ничего нет — None.
    """
    by_key = {goal.key: goal for goal in goals}
    report = {item.key: item for item in reports}
    order = {goal.key: index for index, goal in enumerate(goals)}
    errors = [mark for mark in marks if mark.verdict == "error" and mark.goal_key in by_key]
    if errors:
        best = min(errors, key=lambda mark: (
            report[mark.goal_key].credited,
            KIND_ORDER.get(mark.error_kind or "", len(KIND_ORDER)),
            not mark.correct_form,
            -report[mark.goal_key].errors,
            -mark.turn_no,
            order[mark.goal_key],
        ))
        goal = by_key[best.goal_key]
        text = _clean_hint(best.note_ru)
        if not text:
            text = (f"Попробуй сказать сам фразу «{goal.title}»." if goal.kind == "phrase"
                    else f"Обрати внимание на тему «{goal.title}».")
        return NextTime(source="goal", text_ru=text, example=best.correct_form[:200])
    main = [error for error in general_errors if error.visibility == "main"]
    if not main:
        return None
    groups = Counter(error.group_key for error in main)
    best_error = min(main, key=lambda error: (
        -groups[error.group_key], error.type != "grammar", -error.turn_no))
    text = _clean_hint(best_error.rule_ru) or f"Обрати внимание: {best_error.group_label}."
    return NextTime(source="general", text_ru=text, example=best_error.correct_form[:200])
