"""Форма ручек `/v1/conversation/*` (договор, разделы 4 и 10.1) и состояния движка.

JSON ручек повторяет договор дословно: по нему параллельно пишется
Go-клиент. Неизвестные поля во входе игнорируются, а не дают 422: Go может
прислать лишний атрибут реплики (например, `input` в истории), и разговор
ученика из-за этого падать не должен.
"""

from __future__ import annotations

from typing import Any, Literal

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    SerializerFunctionWrapHandler,
    field_validator,
    model_serializer,
    model_validator,
)

from ..languages import normalize_language
from .levels_port import LEVELS

Verdict = Literal["credited", "error", "miss"]
ErrorKind = Literal["wrong_form", "misuse", "not_used"]
Visibility = Literal["main", "above_level", "unknown_level"]
# Живой режим (раздел 10.1): "steer" — реплику произносит сессия GPT Live,
# а движок вместо текста реплики отдаёт ей указание на ход.
ReplyMode = Literal["text", "steer"]

STATE_VERSION = 1


class _Input(BaseModel):
    model_config = ConfigDict(extra="ignore")


class _Output(BaseModel):
    model_config = ConfigDict(extra="forbid")


def _language(value: str) -> str:
    return normalize_language(value)


def _level(value: str) -> str:
    level = (value or "").strip().lower()
    if level and level not in LEVELS:
        raise ValueError("level: ожидается a1, a2, b1, b2, c1 или пустая строка")
    return level


class Goal(_Input):
    key: str = Field(pattern=r"^[pg][1-9][0-9]{0,2}$")
    kind: Literal["phrase", "grammar"]
    title: str = Field(max_length=500)
    note: str = Field(default="", max_length=1000)

    @field_validator("title", "note")
    @classmethod
    def strip_text(cls, value: str) -> str:
        return value.strip()

    @model_validator(mode="after")
    def check_consistency(self) -> "Goal":
        if not self.title:
            raise ValueError("у цели должно быть название")
        # Ключ несёт вид цели (договор, раздел 1): p… — фраза, g… — тема.
        if (self.key[0] == "p") != (self.kind == "phrase"):
            raise ValueError("ключ цели не соответствует виду: p… для фраз, g… для тем")
        return self


def _unique_goals(goals: list[Goal]) -> list[Goal]:
    keys = [goal.key for goal in goals]
    if len(keys) != len(set(keys)):
        raise ValueError("ключи целей повторяются")
    return goals


class HistoryItem(_Input):
    role: Literal["student", "avatar"]
    content: str = Field(max_length=10_000)
    turn_no: int = Field(ge=0)


class StartRequest(_Input):
    language: str = Field(min_length=2, max_length=32)
    level: str = Field(default="", max_length=8)
    refined_text: str = Field(default="", max_length=200_000)
    collocations: str = Field(default="", max_length=50_000)
    role_prompt: str = Field(default="", max_length=2_000)
    goals: list[Goal] = Field(default_factory=list, max_length=60)
    max_turns: int = Field(default=8, ge=1, le=50)
    history: list[HistoryItem] = Field(default_factory=list, max_length=200)
    reply_mode: ReplyMode = "text"

    @field_validator("language")
    @classmethod
    def check_language(cls, value: str) -> str:
        return _language(value)

    @field_validator("level")
    @classmethod
    def check_level(cls, value: str) -> str:
        return _level(value)

    @field_validator("goals")
    @classmethod
    def check_goals(cls, value: list[Goal]) -> list[Goal]:
        return _unique_goals(value)


class Answer(_Input):
    turn_no: int = Field(ge=1)
    # Пустой текст — не ошибка входа, а технический сбой записи: такой ход
    # принимается и ничего ученику не засчитывает (правило 2).
    text: str = Field(max_length=4_000)
    input: str = "text"
    edited: bool = False


class TurnRequest(_Input):
    state: dict[str, Any]
    answer: Answer
    history: list[HistoryItem] = Field(default_factory=list, max_length=200)
    final: bool = False
    reply_mode: ReplyMode = "text"


class TranscriptItem(_Input):
    turn_no: int = Field(ge=0)
    role: Literal["student", "avatar"]
    content: str = Field(max_length=10_000)
    input: str = "text"
    edited: bool = False


class AnalyzeRequest(_Input):
    language: str = Field(min_length=2, max_length=32)
    level: str = Field(default="", max_length=8)
    goals: list[Goal] = Field(default_factory=list, max_length=60)
    state: dict[str, Any] | None = None
    transcript: list[TranscriptItem] = Field(default_factory=list, max_length=400)

    @field_validator("language")
    @classmethod
    def check_language(cls, value: str) -> str:
        return _language(value)

    @field_validator("level")
    @classmethod
    def check_level(cls, value: str) -> str:
        return _level(value)

    @field_validator("goals")
    @classmethod
    def check_goals(cls, value: list[Goal]) -> list[Goal]:
        return _unique_goals(value)


class Mark(_Output):
    turn_no: int = Field(ge=0)
    goal_key: str
    verdict: Verdict
    error_kind: ErrorKind | None = None
    quote: str
    correct_form: str = ""
    note_ru: str = ""


class _WithSteer(_Output):
    """Ответ start и turn: поле steer есть только в режиме steer.

    В текстовом режиме его в JSON нет вовсе, а не null: ответ остаётся байт
    в байт прежним (раздел 10.1), и Go текстового режима его не видит.
    Сериализатор без аннотации возврата — так схема OpenAPI остаётся схемой
    модели, а не «любой объект».
    """

    @model_serializer(mode="wrap")
    def _omit_text_mode_steer(self, handler: SerializerFunctionWrapHandler):
        data = handler(self)
        if isinstance(data, dict) and data.get("steer") is None:
            data.pop("steer", None)
        return data


class StartResponse(_WithSteer):
    state: dict[str, Any]
    reply: str
    reply_ru: str
    target_goal: str | None
    steer: str | None = None


class TurnResponse(_WithSteer):
    state: dict[str, Any]
    reply: str
    reply_ru: str
    marks: list[Mark]
    target_goal: str | None
    approach_changed: bool
    steer: str | None = None


# --- Живой режим: наблюдение реплики и инструменты делегата (раздел 10.1) ---


class ObserveRequest(_Input):
    state: dict[str, Any]
    turn_no: int = Field(ge=0)
    reply: str = Field(max_length=10_000)
    translate: bool = True
    # Номер хода, чьё указание уже дошло до сессии к началу реплики:
    # 0 — вступление, нет поля — последнее указание.
    steer_turn: int | None = Field(default=None, ge=0)


class ObserveResponse(_Output):
    state: dict[str, Any]
    reply_ru: str
    leaked: list[str]


class ProgressRequest(_Input):
    state: dict[str, Any]


class GoalStatus(_Output):
    key: str
    kind: Literal["phrase", "grammar"]
    title: str
    note: str
    status: Literal["open", "closed"]


class Correction(_Output):
    quote: str
    correct_form: str


class ProgressResponse(_Output):
    goals: list[GoalStatus]
    target_goal: str | None
    avoid_phrases: list[str]
    corrections: list[Correction]
    answers_left: int
    finished: bool


class CheckRequest(_Input):
    state: dict[str, Any]
    text: str = Field(max_length=10_000)
    final: bool = False


class CheckResponse(_Output):
    ok: bool
    problems: list[str]


class GoalReport(_Output):
    key: str
    kind: Literal["phrase", "grammar"]
    title: str
    note: str
    credited: bool
    credited_turn: int | None
    correct_uses: int
    errors: int
    misses: int


class GeneralError(_Output):
    turn_no: int
    quote: str
    type: Literal["lexical", "grammar"]
    correct_form: str
    rule_ru: str
    level: str
    level_source: str
    visibility: Visibility
    group_key: str
    group_label: str


class GoalRef(_Output):
    key: str
    title: str


class NextTime(_Output):
    source: Literal["goal", "general"]
    text_ru: str
    example: str


class Summary(_Output):
    credited_phrases: list[GoalRef]
    applied_themes: list[GoalRef]
    phrases_total: int
    themes_total: int
    next_time: NextTime | None


class SpeechLevel(_Output):
    estimated: str
    shares: dict[str, float]
    unknown_share: float


class AnalyzeResponse(_Output):
    goals: list[GoalReport]
    marks: list[Mark]
    general_errors: list[GeneralError]
    summary: Summary
    speech_level: SpeechLevel


# --- Состояние движка ---
#
# Go хранит его как непрозрачный JSON и возвращает без изменений. Поле
# version меняется при несовместимой правке формы: движок старой версии не
# должен молча неверно прочитать новое состояние (и наоборот).


class _State(BaseModel):
    model_config = ConfigDict(extra="forbid")


class GoalProgress(_State):
    credited: bool = False
    credited_turn: int | None = None
    correct_uses: int = Field(default=0, ge=0)
    errors: int = Field(default=0, ge=0)
    misses: int = Field(default=0, ge=0)
    # Сколько реплик аватара вели к цели (заходы).
    approaches: int = Field(default=0, ge=0)
    # Неудачные ответы на реплики, которые вели к цели: промахи и ошибки
    # по ней. По этому числу выбирается цель (правило 4).
    failed: int = Field(default=0, ge=0)
    # Неудачные попытки подряд с последнего зачёта или смены захода
    # (правило 3: после двух — другой заход или другая цель).
    streak: int = Field(default=0, ge=0)
    # Номер захода: 0 — обычный, дальше — другие типы вопроса.
    variant: int = Field(default=0, ge=0)
    # Номер хода, на котором к цели вели в последний раз; -1 — не вели.
    last_led: int = Field(default=-1, ge=-1)


class TurnRecord(_State):
    turn_no: int = Field(ge=1)
    # Цель реплики аватара, на которую отвечал ученик.
    target: str | None
    marks: list[Mark]


class LiveReply(_State):
    """Реплика аватара из живой сессии, присланная через observe."""

    # Номер ответа ученика, на который она отвечает; 0 — вступление.
    turn_no: int = Field(ge=0)
    text: str = Field(max_length=4_000)
    # Указание, по которому сессия говорила реплику, и его цель. Номера нет —
    # указание неизвестно, и промах уходит цели последнего указания.
    steer_turn: int | None = Field(default=None, ge=0)
    target: str | None = None


class SteerRecord(_State):
    """Указание живой сессии: номер хода (0 — вступление) и его цель."""

    turn_no: int = Field(ge=0)
    target: str | None


class Leak(_State):
    """Аватар живой сессии произнёс незачтённую фокусную фразу."""

    turn_no: int = Field(ge=0)
    goal_key: str


class EngineState(_State):
    version: Literal[1] = STATE_VERSION
    language: str
    level: str
    refined_text: str
    collocations: str
    # Роль уже прошла проверку на подмену инструкций.
    role_prompt: str
    goals: list[Goal]
    max_turns: int = Field(ge=1)
    # Последний применённый ответ ученика; 0 — только вступление.
    last_turn: int = Field(default=0, ge=0)
    # Цель последней реплики аватара; None — разговор по теме урока.
    target: str | None = None
    approach: int = Field(default=0, ge=0)
    last_reply: str = ""
    progress: dict[str, GoalProgress]
    turns: list[TurnRecord] = Field(default_factory=list)
    # Применён ход с final=true: дальше разговор не продолжается.
    closed: bool = False
    # Живой режим (раздел 10.1). Реплики аватара, услышанные после последнего
    # применённого ответа ученика (и присланные раньше своего хода), — по
    # ним правило 1 в следующем ответе, а не по одной last_reply: сессия
    # может сказать на один ответ несколько реплик.
    live_replies: list[LiveReply] = Field(default_factory=list)
    # Незачтённые фокусные фразы, которые сессия всё-таки произнесла.
    leaks: list[Leak] = Field(default_factory=list)
    # Последние указания сессии: по номеру из observe реплика узнаёт цель,
    # к которой она вела на самом деле.
    steers: list[SteerRecord] = Field(default_factory=list)

    @model_validator(mode="after")
    def check_consistency(self) -> "EngineState":
        keys = [goal.key for goal in self.goals]
        if len(keys) != len(set(keys)) or set(self.progress) != set(keys):
            raise ValueError("цели и прогресс состояния не совпадают")
        if self.target is not None and self.target not in self.progress:
            raise ValueError("цель реплики не входит в цели урока")
        if any(leak.goal_key not in self.progress for leak in self.leaks):
            raise ValueError("фраза утечки не входит в цели урока")
        led = [item.target for item in [*self.steers, *self.live_replies]]
        if any(target is not None and target not in self.progress for target in led):
            raise ValueError("цель указания не входит в цели урока")
        return self

    @model_serializer(mode="wrap")
    def _omit_empty_live_fields(self, handler: SerializerFunctionWrapHandler):
        # Пустые поля живого режима в JSON не пишутся: состояние текстового
        # режима остаётся прежним байт в байт, и при откате его читает
        # движок без живого режима (у состояния extra="forbid").
        data = handler(self)
        if isinstance(data, dict):
            for key in ("live_replies", "leaks", "steers"):
                if not data.get(key):
                    data.pop(key, None)
        return data
