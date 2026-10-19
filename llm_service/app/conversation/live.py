"""Живой режим: разговор урока ведёт сессия GPT Live (договор, раздел 10.1).

Реплику аватара произносит сама сессия, а движок работает рядом: ставит
отметки по ответам ученика и присылает указание на следующий ход (steer),
запоминает настоящие реплики аватара (observe) и отвечает инструментам
делегата сессии — `lesson_progress` (progress) и `check_reply` (check).
Здесь только детерминированный код: указание собирается из состояния, а
проверка реплики — те же проверки правила 6, что у реплики текстового
режима. Модель в живом режиме зовут только отметки хода и перевод реплики
(engine.py).

Указание и тексты проблем — по-английски: GPT Live инструктируется
по-английски. Текст учителя и ученика попадает в них только данными в «»
(см. `_quoted`).
"""

from __future__ import annotations

import re
from collections.abc import Sequence

from .policy import implicit_corrections, uncredited_phrase_goals, uncredited_phrases
from .prompts import CONCRETE_QUESTIONS_EN, approach_text_en, basic_level, language_name_en, question_words
from .schemas import (
    Correction,
    EngineState,
    Goal,
    GoalStatus,
    Leak,
    LiveReply,
    ProgressResponse,
    SteerRecord,
)
from .text import count_questions, evaluation_words

# Указание сессии — не длиннее этого: оно уходит в инструкции голосовой
# модели на каждом ходе, и длинное указание размывает главное.
STEER_LIMIT = 1200

# Политика сессии (раздел 10.3) выполняет последнее «Turn guidance» и
# считает, что оно заменяет прежние, поэтому указание начинается с этих слов.
STEER_HEADER = "Turn guidance (replaces any earlier turn guidance)."

# Длина чужих данных внутри указания. Вместе с постоянным текстом самая
# длинная обязательная часть указания (цель с пояснением и заход) занимает
# около 750 символов — на запретные фразы и верные формы остаётся место.
TITLE_LIMIT = 100
NOTE_LIMIT = 140
PHRASE_LIMIT = 80
FORM_LIMIT = 60

# Реплики аватара в состоянии. Реплика голосовой сессии — пара предложений;
# потолки держат состояние малым, даже если расшифровка пришла склеенной.
REPLY_LIMIT = 2_000
LIVE_REPLIES_LIMIT = 8
LEAKS_LIMIT = 200
# Реплика ссылается на одно из последних указаний (обычно прошлого или
# этого хода), поэтому длинная история указаний не нужна.
STEERS_LIMIT = 8

OPENING_START = ("Start the conversation: greet the learner briefly in {language}, "
                 "then ask your first question.")
OPENING_ROLE = " Name yourself briefly in your lesson role"
OPENING_ROLE_AVOID = ", without the phrases listed below"
OPENING_CONTINUE = ("The conversation is already in progress: do not greet again, "
                    "just continue with your next question.")
FINAL = "This is the last turn: thank briefly in {language} and say goodbye without a question."
# Делегат сессии на приветствии и прощании звал инструменты, и пока шли
# вызовы, голосовая модель успевала заговорить сама — аватар здоровался и
# прощался дважды. Всё нужное для этих ходов уже есть в указании.
NO_TOOLS = "Answer right away, without calling tools: this guidance has everything for this turn."
# Сессия отвечает на последний ответ ученика сразу, раньше, чем приходит
# указание о конце разговора, — и задавала вопрос, на который уже не
# ответят. Поэтому о конце предупреждаем на ход раньше.
LAST_NEXT = ("The learner's next answer is the last one: right after it, do not ask a new "
             "question; thank them briefly in {language} and say goodbye.")
GOAL_PHRASE = ("Lead toward this goal with your question: focus phrase {title}{note}. Ask so that "
               "the learner can naturally answer with this phrase; do not say it yourself. "
               "Ask about the content, never about words or how to say something.")
GOAL_GRAMMAR = ("Lead toward this goal with your question: grammar topic {title}{note}. Ask so "
                "that a natural answer needs this construction; do not name the topic or give the form.")
GOAL_NOTE = " (teacher's note: {note})"
# Ученик сказал «не знаю» или «не понял» (text.STUCK): учителя просили
# переспрашивать проще, а не уходить к другой цели.
STUCK_LINE = ("The learner said they do not know or did not understand. Do not move on and do not "
              "explain: ask about the same thing again, more simply, with a choice of two options.")
TOPIC = "Keep the talk on the lesson text with a natural question about its content."
TOPIC_DONE = ("The learner has already used every lesson goal: keep a lively talk about the "
              "lesson text.")
APPROACH = "Approach: {approach}."
APPROACH_CHANGED = ("Earlier attempts at this goal did not work: ask differently and more "
                    "simply. Approach: {approach}.")
AVOID = "Never say these phrases yourself, the learner must say them: {phrases}."
AVOID_MORE = "; and {count} more lesson phrases"
AVOID_ALL = "Never say the lesson's focus phrases yourself, the learner must say them."
LEAKED = "You already said {phrases} yourself earlier: do not say {pronoun} again."
# Неявная правка (правило 6) — без слов «ошибка» и «неверно» даже в самом
# указании: модель, которой назвали ошибку, охотнее о ней говорит.
CORRECTIONS = ("Naturally use {forms} in your reply, for example by echoing the learner's "
               "idea with {pronoun}; do not comment on how the learner spoke.")
# Напоминание идёт в конце каждого указания: предел длины в словах сессия
# держит лучше, чем просьбу «коротко» (prompts.question_words).
# «about the lesson text»: учителя просили, чтобы вопросы шли по тексту урока.
REMINDER = ("Reminder: {language}, {level}, ask about the lesson text, at most two short "
            "sentences, one simple question of at most {words} words{kind}.")
# Начальным уровням — только конкретные вопросы (prompts.CONCRETE_QUESTIONS_EN).
REMINDER_CONCRETE = "; " + CONCRETE_QUESTIONS_EN
REMINDER_FINAL = "Reminder: {language}, {level}, at most two short sentences, no question."

_CONTROL = re.compile(r"[\x00-\x1f\x7f]")
_GUIDANCE = re.compile(r"turn\s+guidance", re.IGNORECASE)


# --- Указание сессии (steer) ---


def build_steer(
    state: EngineState,
    *,
    opening: str | None = None,
    final: bool = False,
    approach_changed: bool = False,
    previous_target: str | None = None,
    heard_since: int | None = None,
    corrections: Sequence[dict[str, object]] = (),
    stuck: bool = False,
) -> str:
    """Указание сессии на следующий ход — из состояния после хода.

    opening — "start" (вступление: поздороваться и задать первый вопрос)
    или "continue" (разговор уже шёл в старом режиме, history). Цель хода,
    заход, незачтённые фразы и верные формы берутся из того же состояния и
    тех же правил, что у реплики текстового режима: меняется только то, кто
    пишет реплику. heard_since — номер, с которого реплики аватара ученик
    только что слышал (прошлый применённый ответ): об утечке в них сессии
    напоминается отдельно, о давних — нет, их и так покрывает список фраз.
    """
    language = language_name_en(state.language)
    forbidden = [goal for goal, _ in uncredited_phrase_goals(state)]
    head = [STEER_HEADER]
    if opening == "start":
        line = OPENING_START.format(language=language)
        if state.role_prompt:
            # «Представься» толкает назвать себя фокусной фразой вроде
            # «Минем исемем …» — поэтому оговорка про список ниже.
            line += OPENING_ROLE + (OPENING_ROLE_AVOID if forbidden else "") + "."
        head.append(line)
    elif opening == "continue":
        head.append(OPENING_CONTINUE)
    if final:
        head.append(FINAL.format(language=language))
    else:
        if stuck:
            head.append(STUCK_LINE)
        head.extend(_goal_lines(state, approach_changed=approach_changed, previous_target=previous_target))
        if state.max_turns and state.last_turn == state.max_turns - 1:
            head.append(LAST_NEXT.format(language=language))
    if opening or final:
        head.append(NO_TOOLS)
    level = f"level {state.level.upper()}" if state.level else "simple everyday words"
    reminder = (REMINDER_FINAL if final else REMINDER).format(
        language=language, level=level, words=question_words(state.level),
        kind=REMINDER_CONCRETE if basic_level(state.level) else "")

    recent = [] if heard_since is None else [leak for leak in state.leaks if leak.turn_no >= heard_since]
    leaked = [goal for goal in forbidden if any(leak.goal_key == goal.key for leak in recent)]
    forms = [str(item["correct"]) for item in corrections]

    def assemble(phrases: int, form_count: int, with_leaked: bool) -> str:
        lines = list(head)
        if forbidden:
            lines.append(_avoid_line(forbidden, phrases))
        if with_leaked and leaked:
            lines.append(LEAKED.format(phrases=_join(goal.title for goal in leaked),
                                       pronoun="them" if len(leaked) > 1 else "it"))
        if form_count:
            lines.append(CORRECTIONS.format(forms=_join(forms[:form_count], limit=FORM_LIMIT),
                                            pronoun="them" if form_count > 1 else "it"))
        lines.append(reminder)
        return "\n".join(lines)

    # Что не помещается, отбрасывается по важности: запретные фразы — жёсткое
    # правило 6, неявная правка — мягкое, напоминание об утечке — повтор
    # уже сказанного. Обязательная часть помещается всегда (см. *_LIMIT).
    phrases = len(forbidden)
    while phrases and len(assemble(phrases, 0, False)) > STEER_LIMIT:
        phrases -= 1
    form_count = len(forms)
    while form_count and len(assemble(phrases, form_count, False)) > STEER_LIMIT:
        form_count -= 1
    with_leaked = bool(leaked) and len(assemble(phrases, form_count, True)) <= STEER_LIMIT
    steer = assemble(phrases, form_count, with_leaked)
    return steer if len(steer) <= STEER_LIMIT else _cut(steer, STEER_LIMIT)


def _goal_lines(state: EngineState, *, approach_changed: bool, previous_target: str | None) -> list[str]:
    goal = next((item for item in state.goals if item.key == state.target), None)
    if goal is None:
        # Цели нет: либо все закрыты, либо у урока их нет вовсе.
        done = bool(state.goals) and all(entry.credited for entry in state.progress.values())
        return [TOPIC_DONE if done else TOPIC]
    note = GOAL_NOTE.format(note=_quoted(goal.note, NOTE_LIMIT)) if goal.note else ""
    template = GOAL_PHRASE if goal.kind == "phrase" else GOAL_GRAMMAR
    lines = [template.format(title=_quoted(goal.title, TITLE_LIMIT), note=note)]
    if state.approach > 0:
        # Обычный заход (0) указанию не нужен — это и есть «вопрос к цели».
        # Другой заход называем всегда: сессия не помнит, что к этой цели
        # уже заходили иначе. «Не удалось» — только если заход сменился
        # сейчас, а цель осталась той же (другой открытой цели нет).
        approach = approach_text_en(state.approach)
        changed_here = approach_changed and goal.key == previous_target
        lines.append((APPROACH_CHANGED if changed_here else APPROACH).format(approach=approach))
    return lines


def _avoid_line(goals: Sequence[Goal], count: int) -> str:
    if count <= 0:
        return AVOID_ALL
    listed = _join(goal.title for goal in goals[:count])
    if count < len(goals):
        listed += AVOID_MORE.format(count=len(goals) - count)
    return AVOID.format(phrases=listed)


def _join(items, *, limit: int = PHRASE_LIMIT) -> str:
    return "; ".join(_quoted(item, limit) for item in items)


def _quoted(text: str, limit: int) -> str:
    """Чужой текст в указании — данные в «», которые нельзя закрыть.

    Внутри не остаётся «», переводов строк, ограждений <<< >>> политики
    сессии и слов «Turn guidance»: учитель в пояснении или ученик в
    ответе не закроют кавычки и не допишут своё указание — сессия
    выполняет последнее «Turn guidance», и такая строка внутри данных
    выглядела бы новым указанием.
    """
    text = _CONTROL.sub(" ", text)
    text = text.replace("«", '"').replace("»", '"').replace("<<<", " ").replace(">>>", " ")
    text = _GUIDANCE.sub("turn note", text)
    return "«" + _cut(" ".join(text.split()), limit) + "»"


def _cut(text: str, limit: int) -> str:
    """Обрезать по границе слова: обрывок слова сбивает модель сильнее хвоста."""
    if len(text) <= limit:
        return text
    cut = text[: limit - 1]
    space = cut.rfind(" ")
    if space > limit // 2:
        cut = cut[:space]
    return cut.rstrip(" ,;:—-") + "…"


# --- Реплики аватара (observe) и правило 1 ---


def heard_replies(state: EngineState, turn_no: int) -> list[str]:
    """Реплики аватара, на которые отвечает ответ turn_no (правило 1).

    Текстовый режим — одна last_reply. Живой — ещё и все реплики сессии
    после прошлого применённого ответа: на один ответ сессия может сказать
    несколько реплик, и повтор за любой из них — не зачёт. Реплика,
    присланная раньше своего хода (сессия уже ответила на ответ turn_no, а
    он ещё не применён), сюда не входит: она прозвучала после ответа.
    """
    texts = [item.text for item in state.live_replies if state.last_turn <= item.turn_no < turn_no]
    if state.last_reply:
        texts.append(state.last_reply)
    return list(dict.fromkeys(texts))


def record_steer(state: EngineState, turn_no: int) -> None:
    """Запомнить цель указания хода turn_no (0 — вступление).

    Сессия отвечает ученику сразу, а указание приходит через несколько
    секунд, поэтому реплика часто звучит по прежнему указанию. Клиент
    сообщает в observe номер указания, дошедшего до сессии (steer_turn), и
    по этой истории реплика узнаёт, к какой цели она вела на самом деле.
    """
    state.steers.append(SteerRecord(turn_no=turn_no, target=state.target))
    del state.steers[:-STEERS_LIMIT]


def reply_target(state: EngineState, turn_no: int) -> str | None:
    """Цель, к которой вела последняя реплика сессии перед ответом turn_no.

    По ней ставится промах (правило 3), а от него считаются смена захода и
    следующая цель. Указание реплики неизвестно (нет steer_turn или номер
    не из истории) — цель последнего указания, как без живого режима.
    Цель указания уже закрыта — промаха нет: ученик употребил её, пока
    сессия ещё вела к ней по прежнему указанию, а промах — неудача по
    открытой цели.
    """
    heard = [item for item in state.live_replies if state.last_turn <= item.turn_no < turn_no]
    if not heard or heard[-1].steer_turn is None:
        return state.target
    target = heard[-1].target
    if target is not None and state.progress[target].credited:
        return None
    return target


def forget_replies(state: EngineState) -> None:
    """После применённого ответа старые реплики правилу 1 уже не нужны."""
    state.live_replies = [item for item in state.live_replies if item.turn_no >= state.last_turn]


def latest_reply(state: EngineState) -> str:
    """last_reply после хода живого режима.

    Реплику сессия скажет сама, и известна она станет только из observe;
    пока её нет — пусто, чтобы правило 1 не сверялось с репликой, на которую
    ученик уже ответил. Если сессия ответила раньше, чем ход применён, её
    реплика уже лежит в состоянии.
    """
    found = [item.text for item in state.live_replies if item.turn_no == state.last_turn]
    return found[-1] if found else ""


def observe_reply(state: EngineState, *, turn_no: int, reply: str, steer_turn: int | None = None) -> list[str]:
    """Запомнить настоящую реплику аватара; вернуть ключи незачтённых
    фокусных фраз, которые в ней прозвучали."""
    text = " ".join(reply.split())
    if not text:
        return []
    if turn_no == 0 and not state.turns:
        # Вступление. Разговор, начатый с history (старый режим), движок
        # ведёт не с нуля, а с последнего ответа истории — вступление
        # отвечает на него.
        turn_no = state.last_turn
    leaked = [goal.key for goal, pattern in uncredited_phrase_goals(state) if pattern.occurs_in(text)]
    known = {(leak.turn_no, leak.goal_key) for leak in state.leaks}
    for key in leaked:
        if (turn_no, key) not in known and len(state.leaks) < LEAKS_LIMIT:
            state.leaks.append(Leak(turn_no=turn_no, goal_key=key))
    if turn_no < state.last_turn:
        # Реплика на уже пройденный ответ пришла поздно: ученик ответил
        # после неё, и его ответ уже оценён. Следующий ответ отвечает не на
        # неё — для правила 1 она не нужна. Утечка всё равно отмечена.
        return leaked
    if turn_no == state.last_turn and state.last_reply:
        # last_reply написал текстовый режим (разговор перешёл в живую
        # сессию): ученик слышал и её, правило 1 должно её помнить.
        _remember(state, turn_no, state.last_reply[:REPLY_LIMIT], None)
    # Неизвестный номер указания — как без поля: цель реплики не знаем.
    steer = next((item for item in reversed(state.steers) if item.turn_no == steer_turn), None)
    _remember(state, turn_no, text[:REPLY_LIMIT], steer)
    if turn_no == state.last_turn:
        state.last_reply = text[:REPLY_LIMIT]
    return leaked


def _remember(state: EngineState, turn_no: int, text: str, steer: SteerRecord | None) -> None:
    for item in state.live_replies:
        if item.turn_no == turn_no and item.text == text:
            # Повтор той же реплики (Go повторил запрос) не дублируется;
            # указание дописывается, если раньше его не знали.
            if steer is not None and item.steer_turn is None:
                item.steer_turn, item.target = steer.turn_no, steer.target
            return
    state.live_replies.append(LiveReply(
        turn_no=turn_no, text=text,
        steer_turn=steer.turn_no if steer else None, target=steer.target if steer else None,
    ))
    del state.live_replies[:-LIVE_REPLIES_LIMIT]


# --- Инструменты делегата: progress и check ---


def last_corrections(state: EngineState) -> list[dict[str, object]]:
    """Верные формы для неявной правки — из отметок последнего хода, по
    тем же правилам, что у реплики текстового режима."""
    if not state.turns or state.turns[-1].turn_no != state.last_turn:
        return []
    return implicit_corrections(state.turns[-1].marks, uncredited_phrases(state))


def lesson_progress(state: EngineState) -> ProgressResponse:
    return ProgressResponse(
        goals=[GoalStatus(key=goal.key, kind=goal.kind, title=goal.title, note=goal.note,
                          status="closed" if state.progress[goal.key].credited else "open")
               for goal in state.goals],
        target_goal=state.target,
        avoid_phrases=[goal.title for goal, _ in uncredited_phrase_goals(state)],
        corrections=[Correction(quote=str(item["wrong"]), correct_form=str(item["correct"]))
                     for item in last_corrections(state)],
        answers_left=max(0, state.max_turns - state.last_turn),
        finished=state.closed or state.last_turn >= state.max_turns,
    )


def check_reply(state: EngineState, text: str, *, final: bool) -> list[str]:
    """Черновик реплики сессии — теми же проверками, что реплика текстового
    режима (правило 6): незачтённая фокусная фраза, слова оценки, число
    вопросов. Тексты короткие и по-английски: их читает делегат сессии.

    После применённого final разговор закрыт, и единственная оставшаяся
    реплика — прощание: вопросов в ней быть не должно, даже если запрос
    пришёл с final=false (у инструмента check_reply есть только text).
    """
    final = final or state.closed
    if not text.strip():
        return ["The reply is empty."]
    problems = [f"Do not say {_quoted(goal.title, PHRASE_LIMIT)}: the learner must say it."
                for goal, pattern in uncredited_phrase_goals(state) if pattern.occurs_in(text)]
    found = sorted(set(evaluation_words(text)))
    if found:
        problems.append("Remove evaluation words (" + ", ".join(_quoted(word, 40) for word in found) +
                        "): do not judge the learner's speech.")
    questions = count_questions(text)
    if final and questions:
        problems.append(f"This is the goodbye: no questions (found {questions}).")
    elif not final and questions != 1:
        problems.append(f"Ask exactly one question (found {questions}).")
    return problems
