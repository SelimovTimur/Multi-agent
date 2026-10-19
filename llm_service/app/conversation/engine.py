"""Движок разговора: вступление и ходы (договор, разделы 4 и 10.1).

Ход — не больше двух вызовов модели (правило 9): отметки по целям и
реплика аватара. Третий — одна перегенерация реплики, если она нарушила
проверяемые кодом правила. Между вызовами всё решает код: какие отметки
остаются, был ли промах, менять ли заход и к какой цели вести дальше.

В живом режиме (reply_mode="steer") реплику произносит сессия GPT Live:
модель реплики не вызывается, вместо текста движок отдаёт указание на ход
(live.py). Настоящая реплика сессии приходит потом через observe.
"""

from __future__ import annotations

import logging
from collections.abc import Sequence
from dataclasses import dataclass
from typing import Any

from pydantic import ValidationError

from ..provider import LLMProvider
from ..schemas import ChatMessage
from ..validators import looks_like_teacher_injection, sanitize_agent_text, should_refuse_avatar_message
from . import live, prompts
from .analysis import analyze_conversation
from .levels_port import LevelsPort
from .llm import (
    MarkOut,
    MarksOut,
    ModelCaller,
    ReplyOut,
    ReplySentence,
    TranslationOut,
    marks_schema,
    reply_schema,
    translation_schema,
)
from .policy import (
    apply_marks,
    choose_target,
    finalize_marks,
    implicit_corrections,
    lead_to,
    register_attempt,
    stays_on_target,
    uncredited_phrases,
)
from .schemas import (
    STATE_VERSION,
    AnalyzeRequest,
    AnalyzeResponse,
    EngineState,
    GoalProgress,
    HistoryItem,
    ObserveRequest,
    ObserveResponse,
    StartRequest,
    StartResponse,
    TurnRecord,
    TurnRequest,
    TurnResponse,
)
from .text import STUCK, TECHNICAL, PhrasePattern, classify_answer, contains_words, evaluation_words, is_question

logger = logging.getLogger("uvicorn.error")

# Сколько текста урока хранить в состоянии и показывать модели. Разговор
# идёт о теме урока, весь многостраничный текст ему не нужен, а состояние
# хранится в каждой попытке.
LESSON_TEXT_LIMIT = 12_000
PROMPT_TEXT_LIMIT = 6_000
COLLOCATIONS_LIMIT = 4_000
HISTORY_LIMIT = 12
# Перевод реплики живой сессии — подсказка ученику, а не документ:
# склеенную расшифровку длиннее этого переводить незачем.
TRANSLATE_LIMIT = 4_000
TRANSLATION_LIMIT = 4_000


class TurnConflict(Exception):
    """Ход уже применён или разговор закрыт — 409."""


class InvalidState(Exception):
    """Состояние не разбирается или другой версии — 422."""


def parse_state(raw: Any) -> EngineState:
    if not isinstance(raw, dict) or raw.get("version") != STATE_VERSION:
        raise InvalidState("Состояние разговора повреждено или другой версии")
    try:
        return EngineState.model_validate(raw)
    except ValidationError as exc:
        raise InvalidState("Состояние разговора повреждено или другой версии") from exc


@dataclass
class Reply:
    text: str
    ru: str


class ConversationEngine:
    def __init__(
        self,
        provider: LLMProvider,
        *,
        model: str | None = None,
        levels: LevelsPort | None = None,
    ):
        self.levels = levels if levels is not None else LevelsPort.default()
        # Живые ходы — без рассуждений модели: ученик ждёт голосовой ответ,
        # и рассуждения модели не должны его тормозить.
        self.live = ModelCaller(provider, model=model, reasoning_enabled=False)
        self.background = ModelCaller(provider, model=model)

    # --- start ---

    async def start(self, request: StartRequest) -> StartResponse:
        role = request.role_prompt.strip()
        if role and looks_like_teacher_injection(role):
            # Кабинет такую роль не сохранит, но движок не полагается на
            # проверку снаружи: защита от подмены инструкций — и здесь.
            logger.warning("conversation: роль учителя отклонена проверкой на prompt injection")
            role = ""
        goals = []
        for goal in request.goals:
            if goal.note and looks_like_teacher_injection(goal.note):
                logger.warning("conversation: пояснение цели %s отклонено проверкой", goal.key)
                goal = goal.model_copy(update={"note": ""})
            goals.append(goal)
        history = sorted(request.history, key=_history_order)
        state = EngineState(
            version=STATE_VERSION,
            language=request.language,
            level=request.level,
            refined_text=request.refined_text.strip()[:LESSON_TEXT_LIMIT],
            collocations=request.collocations.strip()[:COLLOCATIONS_LIMIT],
            role_prompt=role,
            goals=goals,
            max_turns=request.max_turns,
            last_turn=max((item.turn_no for item in history if item.role == "student"), default=0),
            progress={goal.key: GoalProgress() for goal in goals},
        )
        target = choose_target(state.goals, state.progress)
        lead_to(state, target, turn_no=state.last_turn)
        if request.reply_mode == "steer":
            # Вступление произносит живая сессия по указанию (раздел 10.1):
            # модель реплики не нужна, last_reply придёт из observe.
            live.record_steer(state, 0)
            steer = live.build_steer(state, opening="continue" if history else "start")
            return StartResponse(state=state.model_dump(mode="json"), reply="", reply_ru="",
                                 target_goal=state.target, steer=steer)
        reply = await self._reply(
            state, history=history, answer=None, answer_class=None, suspicious=False,
            start=True, final=False, approach_changed=False, corrections=[],
        )
        state.last_reply = reply.text
        return StartResponse(state=state.model_dump(mode="json"), reply=reply.text,
                             reply_ru=reply.ru, target_goal=state.target)

    # --- turn ---

    async def turn(self, request: TurnRequest) -> TurnResponse:
        state = parse_state(request.state)
        answer = request.answer
        if state.closed:
            raise TurnConflict("Разговор уже завершён")
        if answer.turn_no <= state.last_turn:
            raise TurnConflict("Этот ответ уже применён")
        text = answer.text
        answer_class = classify_answer(text, state.language)
        # Ответ с командами модели или не по уроку модели не показываем
        # вовсе: зачесть по нему нечего, а реплика вернёт разговор к теме.
        suspicious = answer_class != TECHNICAL and should_refuse_avatar_message(text)
        # Текстовый режим — одна last_reply; живой — все реплики сессии
        # после прошлого ответа (правило 1).
        previous_replies = live.heard_replies(state, answer.turn_no)
        previous_reply = " ".join(previous_replies)
        previous_turn = state.last_turn
        target = state.target
        if request.reply_mode == "steer":
            # Сессия отвечает раньше, чем приходит указание: реплика, на
            # которую ответил ученик, могла вести к цели прежнего указания.
            # Промах, смена захода и выбор следующей цели — от неё.
            target = live.reply_target(state, answer.turn_no)
        history = sorted(
            (item for item in request.history
             if not (item.role == "student" and item.turn_no >= answer.turn_no)),
            key=_history_order,
        )

        raw: list[MarkOut] = []
        # «Не знаю» отмечать нечем: промах по цели поставит код.
        if state.goals and answer_class not in (TECHNICAL, STUCK) and not suspicious:
            raw = await self._evaluate(state, text=text, previous_reply=previous_reply,
                                       target=target, history=history)
        marks = finalize_marks(raw, turn_no=answer.turn_no, text=text, answer_class=answer_class,
                               previous_reply=previous_replies, target=target, goals=state.goals)
        apply_marks(state.progress, marks, turn_no=answer.turn_no)
        changed = register_attempt(state.progress, marks, target=target, answer_class=answer_class)
        state.turns.append(TurnRecord(turn_no=answer.turn_no, target=target, marks=marks))
        state.last_turn = answer.turn_no
        live.forget_replies(state)

        next_target = None
        if not request.final:
            if stays_on_target(state.progress, target=target, answer_class=answer_class, changed=changed):
                # «Не знаю»: та же цель, сразу проще — выбор из двух.
                next_target = target
            else:
                next_target = choose_target(state.goals, state.progress,
                                            exclude=[target] if changed and target else [])
        lead_to(state, next_target, turn_no=answer.turn_no)
        corrections = implicit_corrections(marks, uncredited_phrases(state))
        if request.reply_mode == "steer":
            # Правила отметок, промахов, захода и цели — те же, что в
            # текстовом режиме; вместо реплики — указание сессии. Её настоящая
            # реплика на этот ответ станет last_reply через observe, а цель
            # указания запоминается для steer_turn этой реплики.
            state.last_reply = live.latest_reply(state)
            state.closed = request.final
            live.record_steer(state, answer.turn_no)
            steer = live.build_steer(state, final=request.final, approach_changed=changed,
                                     previous_target=target, heard_since=previous_turn,
                                     corrections=corrections, stuck=answer_class == STUCK)
            return TurnResponse(
                state=state.model_dump(mode="json"), reply="", reply_ru="", marks=marks,
                target_goal=state.target, approach_changed=changed, steer=steer,
            )
        reply = await self._reply(
            state, history=history, answer=None if suspicious else text,
            answer_class=answer_class, suspicious=suspicious, start=False,
            final=request.final, approach_changed=changed, corrections=corrections,
        )
        state.last_reply = reply.text
        state.closed = request.final
        return TurnResponse(
            state=state.model_dump(mode="json"), reply=reply.text, reply_ru=reply.ru,
            marks=marks, target_goal=state.target, approach_changed=changed,
        )

    # --- observe (живой режим) ---

    async def observe(self, request: ObserveRequest) -> ObserveResponse:
        """Настоящая реплика аватара из живой сессии (раздел 10.1).

        Состояние меняет только код: last_reply для правила 1 и утечки
        незачтённых фраз. Модель зовётся один раз — перевод для подсказки.
        """
        state = parse_state(request.state)
        text = " ".join(request.reply.split())
        leaked = live.observe_reply(state, turn_no=request.turn_no, reply=text,
                                    steer_turn=request.steer_turn)
        reply_ru = await self._translate(state, text) if request.translate and text else ""
        return ObserveResponse(state=state.model_dump(mode="json"), reply_ru=reply_ru, leaked=leaked)

    # --- analyze ---

    async def analyze(self, request: AnalyzeRequest) -> AnalyzeResponse:
        state = None
        if request.state is not None:
            try:
                state = parse_state(request.state)
            except InvalidState:
                # Отчёт важнее состояния: без него движок оценит все ответы
                # по расшифровке, как у разговора старого режима.
                logger.warning("conversation analyze: состояние не разобрано, разбор по расшифровке")
        return await analyze_conversation(request, state, caller=self.background, levels=self.levels)

    # --- модель ---

    async def _evaluate(
        self,
        state: EngineState,
        *,
        text: str,
        previous_reply: str,
        target: str | None,
        history: Sequence[HistoryItem],
    ) -> list[MarkOut]:
        data = {
            "level": state.level,
            "goals": [goal.model_dump() for goal in state.goals],
            "context": [{"role": item.role, "content": item.content[:500]} for item in history[-4:]],
            "avatar_reply": previous_reply,
            "target_goal": target,
            "answer": text,
        }
        messages = [
            ChatMessage(role="system", content=prompts.marks_system(
                language=state.language, level=state.level, batch=False)),
            ChatMessage(role="user", content=prompts.marks_user(data)),
        ]
        result = await self.live.ask(
            messages, schema=marks_schema([goal.key for goal in state.goals]),
            name="conversation_marks", parse=MarksOut.model_validate,
        )
        return result.marks

    async def _reply(
        self,
        state: EngineState,
        *,
        history: Sequence[HistoryItem],
        answer: str | None,
        answer_class: str | None,
        suspicious: bool,
        start: bool,
        final: bool,
        approach_changed: bool,
        corrections: list[dict[str, object]],
    ) -> Reply:
        forbidden = uncredited_phrases(state)
        goal = next((item for item in state.goals if item.key == state.target), None)
        guidance = self.levels.guidance(state.level, language=state.language,
                                        topic_text=state.refined_text)
        data = {
            "lesson_text": state.refined_text[:PROMPT_TEXT_LIMIT],
            "lesson_phrases": state.collocations,
            "target": goal.model_dump() if goal else None,
            "do_not_say": [title for title, _ in forbidden],
            "corrections": [{"wrong": item["wrong"], "correct": item["correct"]} for item in corrections],
            "history": [{"role": item.role, "content": item.content[:500]}
                        for item in history[-HISTORY_LIMIT:]],
            "answer": answer,
            "turn": state.last_turn,
            "max_turns": state.max_turns,
        }
        instructions = prompts.reply_instructions(
            language=state.language, start=start, continued=start and bool(history), final=final,
            target_kind=goal.kind if goal else None,
            goals_closed=bool(state.goals) and all(item.credited for item in state.progress.values()),
            variant=state.approach,
            approach_changed=approach_changed, has_forbidden=bool(forbidden),
            has_corrections=bool(corrections), answer_class=answer_class, suspicious=suspicious,
        )
        messages = [
            ChatMessage(role="system", content=prompts.reply_system(
                language=state.language, level=state.level, guidance=guidance,
                role=state.role_prompt, final=final)),
            ChatMessage(role="user", content=prompts.reply_user(data=data, instructions=instructions)),
        ]
        sentences = await self._ask_reply(messages)
        problems = reply_problems(sentences, final=final, forbidden=forbidden, corrections=corrections)
        if problems:
            # Одна перегенерация (правила 6 и 9): модель видит, что именно
            # нарушено. Её ответ проверяется тем же кодом.
            logger.info("conversation reply regenerate problems=%s", len(problems))
            retry = [*messages,
                     ChatMessage(role="assistant", content=_sentences_json(sentences)),
                     ChatMessage(role="user", content=prompts.regeneration_request(problems))]
            sentences = await self._ask_reply(retry)
            if reply_problems(sentences, final=final, forbidden=forbidden, corrections=corrections):
                sentences = repair_reply(sentences, final=final, forbidden=forbidden,
                                         language=state.language)
        return Reply(text=" ".join(item.text for item in sentences),
                     ru=" ".join(item.ru for item in sentences if item.ru))

    async def _translate(self, state: EngineState, text: str) -> str:
        messages = [
            ChatMessage(role="system", content=prompts.translation_system(language=state.language)),
            ChatMessage(role="user", content=prompts.translation_user(text[:TRANSLATE_LIMIT])),
        ]

        def parse(data: Any) -> str:
            translation = TranslationOut.model_validate(data).ru
            return " ".join(sanitize_agent_text(translation).split())[:TRANSLATION_LIMIT]

        try:
            # Один короткий вызов без повтора: перевод — только подсказка.
            return await self.live.ask(messages, schema=translation_schema(),
                                       name="conversation_translation", parse=parse, attempts=1)
        except Exception as exc:
            # Сбой перевода не роняет observe: реплика и её отметки важнее,
            # а ученик без перевода увидит текст реплики.
            logger.warning("conversation translation failed: %s", type(exc).__name__)
            return ""

    async def _ask_reply(self, messages: list[ChatMessage]) -> list[ReplySentence]:
        def parse(data: Any) -> list[ReplySentence]:
            reply = ReplyOut.model_validate(data)
            sentences = []
            for item in reply.sentences:
                text = " ".join(sanitize_agent_text(item.text).split())
                if text:
                    sentences.append(ReplySentence(text=text, ru=" ".join(sanitize_agent_text(item.ru).split())))
            if not sentences:
                raise ValueError("пустая реплика")
            return sentences

        return await self.live.ask(messages, schema=reply_schema(), name="conversation_reply", parse=parse)


def _history_order(item: HistoryItem) -> tuple[int, int]:
    # Ответ ученика k идёт перед репликой аватара k (договор, раздел 1).
    return item.turn_no, 0 if item.role == "student" else 1


def _sentences_json(sentences: Sequence[ReplySentence]) -> str:
    return ReplyOut(sentences=list(sentences)).model_dump_json()


def reply_problems(
    sentences: Sequence[ReplySentence],
    *,
    final: bool,
    forbidden: Sequence[tuple[str, PhrasePattern]],
    corrections: Sequence[dict[str, object]],
) -> list[str]:
    """Нарушения правила 6, которые проверяет код."""
    text = " ".join(item.text for item in sentences)
    translation = " ".join(item.ru for item in sentences)
    problems = []
    for title, pattern in forbidden:
        if pattern.occurs_in(text):
            problems.append(f"произнесена фраза «{title}» из do_not_say — её должен сказать ученик")
    found = evaluation_words(text) + evaluation_words(translation)
    if found:
        problems.append("есть слова оценки речи ученика (" + ", ".join(sorted(set(found))) +
                        ") — убери их и не оценивай ответ")
    questions = sum(1 for item in sentences if is_question(item.text))
    if final and questions:
        problems.append("в прощании есть вопрос — попрощайся без вопросов")
    elif not final and questions != 1:
        problems.append(f"вопросов {questions}, а нужен ровно один, в конце")
    for item in corrections:
        if not any(contains_words(text, word) for word in item["words"]):
            problems.append("не употреблена верная форма из corrections")
            break
    return problems


def repair_reply(
    sentences: Sequence[ReplySentence],
    *,
    final: bool,
    forbidden: Sequence[tuple[str, PhrasePattern]],
    language: str,
) -> list[ReplySentence]:
    """Последний рубеж после неудачной перегенерации — без новых вызовов.

    Убираем предложения с незачтённой фразой и словами оценки, в прощании —
    вопросы, в обычной реплике — лишние вопросы (оставляем последний: он и
    ведёт к цели). Если не осталось ничего — проверенная запасная реплика.
    """
    def clean(item: ReplySentence) -> bool:
        return (not any(pattern.occurs_in(item.text) for _, pattern in forbidden)
                and not evaluation_words(item.text) and not evaluation_words(item.ru))

    kept = [item for item in sentences if clean(item)]
    if final:
        kept = [item for item in kept if not is_question(item.text)]
    else:
        questions = [index for index, item in enumerate(kept) if is_question(item.text)]
        kept = [item for index, item in enumerate(kept)
                if not is_question(item.text) or index == questions[-1]]
    if kept:
        if len(kept) < len(sentences):
            logger.warning("conversation reply repaired kept=%s of=%s", len(kept), len(sentences))
        else:
            # Остались мягкие нарушения (нет вопроса, нет неявной правки):
            # кодом их не исправить, а реплика без них допустима.
            logger.info("conversation reply accepted with soft violations")
        return kept
    for candidate in prompts.FALLBACK_REPLIES.get(language, {}).get("final" if final else "turn", []):
        fallback = [ReplySentence(text=text, ru=ru) for text, ru in candidate]
        if all(clean(item) for item in fallback):
            logger.warning("conversation reply fallback used")
            return fallback
    # Для языка без запасных реплик лучше ответ с нарушением, чем молчание
    # аватара посреди разговора: ход ученика уже засчитан.
    logger.warning("conversation reply kept with violations language=%s", language)
    return list(sentences)
