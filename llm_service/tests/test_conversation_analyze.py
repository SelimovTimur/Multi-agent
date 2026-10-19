"""Разбор после разговора: отметки, общие ошибки, итог ученика.

Справочник уровней подменён модулем с публичным API из договора (раздел 3):
движок обращается к нему только через порт, поэтому так же он будет
работать и с настоящим пакетом `app.levels`.
"""

from __future__ import annotations

import unittest
from types import SimpleNamespace

from app.conversation.engine import ConversationEngine
from app.conversation.levels_port import LevelsPort
from app.conversation.schemas import AnalyzeRequest
from test_conversation_engine import GOALS, GENERIC_REPLY, ScriptedProvider, mark, start_request, turn_request

CATALOG = {
    "locative_da": SimpleNamespace(id="locative_da", title_ru="Местный падеж -да/-дә", level="a1",
                                   source="A1 · Морфология: местный падеж"),
    "future_achak": SimpleNamespace(id="future_achak", title_ru="Будущее время -ачак/-әчәк", level="b1",
                                    source="B1 · Морфология: будущее категорическое"),
}
WORDS = {
    "китапханә": SimpleNamespace(token="китапханә", lemma="китапханә", level="a1", topic="Шәһәр",
                                 translation="библиотека", source="A1 · ШӘҺӘР (Город)"),
}


def fake_levels(calls: list | None = None) -> SimpleNamespace:
    def word_level(token, language="tatar"):
        if calls is not None:
            calls.append(("word_level", token))
        token = token.casefold()
        # Грубое снятие окончания направительного падежа: «китапханәгә» → «китапханә».
        return WORDS.get(token) or WORDS.get(token[:-2])

    return SimpleNamespace(
        LEVELS=("a1", "a2", "b1", "b2", "c1"),
        word_level=word_level,
        construction_by_id=lambda cid, language="tatar": CATALOG.get(cid),
        constructions_catalog=lambda language="tatar": list(CATALOG.values()),
        analyze_text=lambda text, language="tatar", target_level=None: SimpleNamespace(
            estimated_level="a1", shares={"a1": 0.8, "a2": 0.1}, unknown_share=0.1,
            tokens=len(text.split()), above_level=[], constructions=[]),
        level_guidance=lambda level, language="tatar", topic_text="", max_words=60: f"Уровень {level}",
    )


TRANSCRIPT = [
    {"turn_no": 0, "role": "avatar", "content": "Исәнме! Синең исемең ничек?"},
    {"turn_no": 1, "role": "student", "content": "Минем исемем Марат.", "input": "voice", "edited": False},
    {"turn_no": 1, "role": "avatar", "content": "Син кайда яшисең?"},
    {"turn_no": 2, "role": "student", "content": "Мин Казанга яшим, мәктәпкә барачакмын.", "input": "voice", "edited": True},
    {"turn_no": 2, "role": "avatar", "content": "Ә кайда укыйсың?"},
    {"turn_no": 3, "role": "student", "content": "Мин мәктәптә укыйм, аннары мәктәпкә барам.", "input": "text", "edited": False},
]


def general(turn_no, quote, kind, correct="", rule="", key_word="", cid="unknown", label=""):
    return {"turn_no": turn_no, "quote": quote, "type": kind, "correct_form": correct, "rule_ru": rule,
            "key_word": key_word, "construction_id": cid, "label_ru": label}


def analyze_request(state, transcript=TRANSCRIPT, goals=GOALS, level="a2") -> AnalyzeRequest:
    return AnalyzeRequest.model_validate({"language": "tatar", "level": level, "goals": goals,
                                          "state": state, "transcript": transcript})


async def played_state(provider) -> dict:
    """Состояние после трёх ходов: зачёт p1, ошибка формы по g1, ход 3 без отметок."""
    engine = ConversationEngine(provider, levels=LevelsPort(None))
    state = (await engine.start(start_request())).state
    state = (await engine.turn(turn_request(state, 1, "Минем исемем Марат."))).state
    state = (await engine.turn(turn_request(state, 2, "Мин Казанга яшим, мәктәпкә барачакмын."))).state
    return state


class AnalyzeTests(unittest.IsolatedAsyncioTestCase):
    async def _played(self):
        provider = ScriptedProvider(
            reply=[GENERIC_REPLY],
            marks=[{"marks": [mark("p1", "credited", "Минем исемем Марат")]},
                   {"marks": [mark("g1", "error", "Казанга", "wrong_form", "Казанда",
                                   "На вопрос «где?» — окончание -да")]}],
        )
        return await played_state(provider)

    async def test_general_errors_get_level_visibility_and_no_duplicates(self):
        state = await self._played()
        provider = ScriptedProvider(
            analysis=[{"marks": [mark("g1", "credited", "мәктәптә", turn_no=3)]}],
            errors=[{"errors": [
                general(3, "мәктәпкә барам", "grammar", "мәктәпкә барам", "Направление: -га/-гә",
                        cid="locative_da", label="Падеж"),
                general(2, "барачакмын", "grammar", "барачакмын", "Будущее время на -ачак", cid="future_achak"),
                general(2, "Казанга", "grammar", "Казанда", "Местный падеж", cid="locative_da"),
                general(3, "укыйм", "grammar", "укыйм", "Настоящее время", cid="unknown", label="Настоящее время"),
                general(2, "мәктәпкә", "lexical", "китапханәгә", "Библиотека — китапханә", key_word="китапханә"),
                general(2, "мәктәпкә", "lexical", "китапханәгә", "Повтор того же места", key_word="китапханә"),
                general(1, "Синең исемең", "grammar", "Синең исемең", "Цитата из реплики аватара"),
                general(3, "мәктәптә укыдым", "grammar", "мәктәптә укыйм", "Выдуманная цитата"),
                general(3, "аннары", "lexical", "", ""),
            ]}],
        )
        engine = ConversationEngine(provider, levels=LevelsPort(fake_levels()))
        result = await engine.analyze(analyze_request(state, transcript=TRANSCRIPT + [
            {"turn_no": 3, "role": "avatar", "content": "Рәхмәт! Сау бул!"}]))

        found = [(e.turn_no, e.quote, e.type, e.level, e.visibility, e.group_key) for e in result.general_errors]
        self.assertEqual(found, [
            (2, "мәктәпкә", "lexical", "a1", "main", "word:китапханә"),
            (2, "барачакмын", "grammar", "b1", "above_level", "future_achak"),
            (3, "укыйм", "grammar", "", "unknown_level", "grammar:настоящее_время"),
            (3, "мәктәпкә барам", "grammar", "a1", "main", "locative_da"),
        ])
        by_quote = {e.quote: e for e in result.general_errors}
        self.assertEqual(by_quote["барачакмын"].level_source, "B1 · Морфология: будущее категорическое")
        self.assertEqual(by_quote["мәктәпкә"].group_label, "китапханә — библиотека")
        self.assertEqual(by_quote["укыйм"].level_source, "")
        # Ход 3 в состоянии без отметок: оценён здесь; «Казанга» — уже ошибка по цели.
        self.assertEqual(provider.names(), ["conversation_analysis_marks", "conversation_general_errors"])
        self.assertIn(("g1", "credited", 3), [(m.goal_key, m.verdict, m.turn_no) for m in result.marks])

    async def test_marks_and_goal_reports_from_state(self):
        state = await self._played()
        provider = ScriptedProvider(analysis=[{"marks": []}], errors=[{"errors": []}])
        result = await ConversationEngine(provider, levels=LevelsPort(None)).analyze(analyze_request(state))

        marks = [(m.turn_no, m.goal_key, m.verdict) for m in result.marks]
        self.assertEqual(marks, [(1, "p1", "credited"), (2, "p2", "miss"), (2, "g1", "error"), (3, "g1", "miss")])
        reports = {r.key: r for r in result.goals}
        self.assertEqual((reports["p1"].credited, reports["p1"].credited_turn, reports["p1"].correct_uses),
                         (True, 1, 1))
        self.assertEqual((reports["g1"].errors, reports["g1"].misses, reports["p2"].misses), (1, 1, 1))
        summary = result.summary
        self.assertEqual([item.key for item in summary.credited_phrases], ["p1"])
        self.assertEqual(summary.applied_themes, [])
        self.assertEqual((summary.phrases_total, summary.themes_total), (2, 1))

    async def test_next_time_comes_from_goal_errors_first(self):
        state = await self._played()
        provider = ScriptedProvider(analysis=[{"marks": []}], errors=[{"errors": [
            general(3, "мәктәпкә барам", "grammar", "мәктәпкә барам", "Направление", cid="locative_da")]}])
        result = await ConversationEngine(provider, levels=LevelsPort(fake_levels())).analyze(analyze_request(state))
        self.assertEqual(result.summary.next_time.model_dump(), {
            "source": "goal", "text_ru": "На вопрос «где?» — окончание -да", "example": "Казанда"})

    async def test_next_time_falls_back_to_main_general_error(self):
        provider = ScriptedProvider(errors=[{"errors": [
            general(2, "барачакмын", "grammar", "барачакмын", "Будущее время на -ачак", cid="future_achak"),
            general(3, "мәктәпкә", "grammar", "мәктәптә", "Неверно: после «где?» нужен местный падеж",
                    cid="locative_da"),
            general(2, "Казанга", "grammar", "Казанда", "Где? — окончание -да/-дә", cid="locative_da"),
        ]}])
        result = await ConversationEngine(provider, levels=LevelsPort(fake_levels())).analyze(
            analyze_request(None, goals=[]))
        next_time = result.summary.next_time
        # Ошибка выше уровня урока не выбирается; из самой частой группы —
        # самая свежая, а её подсказка со словом оценки заменена шаблоном.
        self.assertEqual(next_time.model_dump(), {
            "source": "general", "text_ru": "Обрати внимание: Местный падеж -да/-дә.", "example": "мәктәптә"})
        self.assertEqual(provider.names(), ["conversation_general_errors"])

    async def test_next_time_is_null_without_errors(self):
        provider = ScriptedProvider(analysis=[{"marks": []}], errors=[{"errors": [
            general(2, "барачакмын", "grammar", "барачакмын", "Будущее время", cid="future_achak")]}])
        result = await ConversationEngine(provider, levels=LevelsPort(fake_levels())).analyze(
            analyze_request(None))
        self.assertIsNone(result.summary.next_time)

    async def test_null_state_evaluates_every_turn_of_old_conversation(self):
        transcript = [
            {"turn_no": 0, "role": "avatar", "content": "Исәнме! Минем исемем Алсу. Ә синең?"},
            {"turn_no": 1, "role": "student", "content": "Минем исемем Марат."},
            {"turn_no": 1, "role": "avatar", "content": "Син кайда яшисең?"},
            {"turn_no": 2, "role": "student", "content": "Мин Казанда яшим."},
        ]
        provider = ScriptedProvider(
            analysis=[{"marks": [mark("p1", "credited", "Минем исемем Марат", turn_no=1),
                                 mark("p2", "credited", "Мин Казанда яшим", turn_no=2),
                                 mark("g1", "credited", "Казанда", turn_no=2),
                                 mark("g1", "credited", "Казанда", turn_no=1)]}],
            errors=[{"errors": []}],
        )
        result = await ConversationEngine(provider, levels=LevelsPort(None)).analyze(
            analyze_request(None, transcript=transcript))
        # p1 прозвучала у аватара старого режима — повтор не зачитывается;
        # цели реплик неизвестны — промахов нет; цитата не из этого хода — отброшена.
        self.assertEqual([(m.turn_no, m.goal_key, m.verdict) for m in result.marks],
                         [(2, "p2", "credited"), (2, "g1", "credited")])
        schema = provider.last("analysis").kwargs["response_format"]["json_schema"]["schema"]
        item = schema["properties"]["marks"]["items"]["properties"]
        self.assertEqual(item["turn_no"]["enum"], [1, 2])
        self.assertEqual(item["goal_key"]["enum"], ["p1", "p2", "g1"])

    async def test_last_turn_without_avatar_reply_gets_miss_for_its_target(self):
        state = await self._played()
        self.assertEqual(state["target"], "g1")
        provider = ScriptedProvider(analysis=[{"marks": []}], errors=[{"errors": []}])
        result = await ConversationEngine(provider, levels=LevelsPort(None)).analyze(analyze_request(state))
        self.assertIn((3, "g1", "miss"), [(m.turn_no, m.goal_key, m.verdict) for m in result.marks])
        data = provider.data("analysis")
        self.assertEqual((data["turns_to_check"], data["targets"]), ([3], {"3": "g1"}))

    async def test_state_marks_without_matching_quote_are_dropped(self):
        state = await self._played()
        transcript = [dict(item) for item in TRANSCRIPT]
        transcript[1]["content"] = "Исемем Марат."
        provider = ScriptedProvider(analysis=[{"marks": []}], errors=[{"errors": []}])
        result = await ConversationEngine(provider, levels=LevelsPort(None)).analyze(
            analyze_request(state, transcript=transcript))
        self.assertNotIn((1, "p1", "credited"), [(m.turn_no, m.goal_key, m.verdict) for m in result.marks])
        self.assertFalse({r.key: r for r in result.goals}["p1"].credited)

    async def test_broken_state_is_treated_as_old_conversation(self):
        provider = ScriptedProvider(analysis=[{"marks": []}], errors=[{"errors": []}])
        result = await ConversationEngine(provider, levels=LevelsPort(None)).analyze(
            analyze_request({"version": 99}))
        self.assertEqual(provider.data("analysis")["turns_to_check"], [1, 2, 3])
        self.assertEqual(result.marks, [])

    async def test_speech_level_uses_levels_reference(self):
        provider = ScriptedProvider(analysis=[{"marks": []}], errors=[{"errors": []}])
        result = await ConversationEngine(provider, levels=LevelsPort(fake_levels())).analyze(analyze_request(None))
        self.assertEqual(result.speech_level.model_dump(), {
            "estimated": "a1", "shares": {"a1": 0.8, "a2": 0.1, "b1": 0.0, "b2": 0.0, "c1": 0.0},
            "unknown_share": 0.1})

        result = await ConversationEngine(provider, levels=LevelsPort(None)).analyze(analyze_request(None))
        self.assertEqual(result.speech_level.estimated, "")

    async def test_foreign_and_empty_answers_are_not_searched_for_errors(self):
        transcript = [
            {"turn_no": 0, "role": "avatar", "content": "Исәнме!"},
            {"turn_no": 1, "role": "student", "content": "Я не понимаю"},
            {"turn_no": 2, "role": "student", "content": "..."},
        ]
        provider = ScriptedProvider(analysis=[{"marks": []}])
        result = await ConversationEngine(provider, levels=LevelsPort(None)).analyze(
            analyze_request(None, transcript=transcript))
        self.assertEqual(result.general_errors, [])
        self.assertNotIn("conversation_general_errors", provider.names())


if __name__ == "__main__":
    unittest.main()
