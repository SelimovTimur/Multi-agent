"""Детерминированные правила движка без модели: текст, выбор цели, уровни."""

from __future__ import annotations

import unittest
from types import SimpleNamespace
from unittest.mock import patch

from app.conversation import levels_port
from app.conversation.levels_port import LevelsPort, level_rank
from app.conversation.policy import choose_target, pick_next_time, register_attempt, visibility
from app.conversation.schemas import GeneralError, Goal, GoalProgress, GoalReport, Mark
from app.conversation.text import (
    FOREIGN,
    NORMAL,
    SOCIAL,
    STUCK,
    TECHNICAL,
    PhrasePattern,
    answer_quote,
    classify_answer,
    corrected_words,
    evaluation_words,
    ground_quote,
    normalize,
)


class TextTests(unittest.TestCase):
    def test_template_phrase_compares_constant_part(self):
        pattern = PhrasePattern.from_title("Минем исемем …")
        self.assertEqual(pattern.parts, ("минем исемем",))
        self.assertTrue(pattern.occurs_in("Исәнме! МИНЕМ, исемем Алсу."))
        self.assertFalse(pattern.occurs_in("Синең исемең ничек?"))
        self.assertFalse(pattern.occurs_in("Минем исемемне беләсеңме?"))

        split = PhrasePattern.from_title("Мин ___ яшим (я живу в …)")
        self.assertEqual(split.parts, ("мин", "яшим"))
        self.assertTrue(split.occurs_in("Мин дә Казанда яшим!"))
        self.assertFalse(split.occurs_in("Яшим мин."))
        self.assertFalse(PhrasePattern.from_title("…").occurs_in("что угодно"))

    def test_translation_inside_title_is_not_part_of_phrase(self):
        self.assertEqual(PhrasePattern.from_title("Минем исемем … — Меня зовут …").parts, ("минем исемем",))
        self.assertEqual(PhrasePattern.from_title("Хәлләр ничек? – Как дела?").parts, ("хәлләр ничек",))
        self.assertEqual(PhrasePattern.from_title("Мин — укучы").parts, ("мин укучы",))
        self.assertTrue(PhrasePattern.from_title("Мин — укучы").occurs_in("Әйе, мин укучы."))

    def test_quote_must_be_exact_substring(self):
        answer = "Мин Мәктәптә укыйм — бик кызык!"
        self.assertEqual(ground_quote("Мәктәптә укыйм", answer), "Мәктәптә укыйм")
        self.assertEqual(ground_quote("«мәктәптә  укыйм.»", answer), "Мәктәптә укыйм")
        self.assertEqual(ground_quote("укыйм - бик", answer), "укыйм — бик")
        self.assertIsNone(ground_quote("мәктәптә укыдым", answer))
        self.assertIsNone(ground_quote("…", answer))
        # Без точного совпадения два равных места без учёта регистра — отказ.
        self.assertIsNone(ground_quote("МИН", "Мин дә, мин дә"))
        self.assertEqual(ground_quote("мин", "Мин дә, мин дә"), "мин")

    def test_miss_quote_is_answer_prefix_up_to_120_chars(self):
        text = "Мин " + "бик " * 40 + "яратам."
        quote = answer_quote(text)
        self.assertLessEqual(len(quote), 120)
        self.assertTrue(text.startswith(quote))
        self.assertFalse(quote.endswith(" "))
        self.assertEqual(answer_quote("  Белмим.  "), "Белмим.")

    def test_answer_classes(self):
        cases = {
            "": TECHNICAL, " ... ": TECHNICAL, "123": TECHNICAL,
            "Исәнме!": SOCIAL, "Зур рәхмәт!": SOCIAL, "Рәхмәт, сау бул!": SOCIAL, "Спасибо": SOCIAL,
            "Я не знаю": STUCK, "Меня зовут Марат": FOREIGN, "I don't know": STUCK,
            "Мин Казанда яшим": NORMAL, "Белмим": STUCK, "Белмим, мин бармадым": NORMAL, "Зур": NORMAL, "Яхшы": NORMAL,
            "Рәхмәт, мин Казанда яшим": NORMAL, "Без мәктәптә укыйбыз": NORMAL, "Ул кем?": NORMAL,
        }
        for text, expected in cases.items():
            with self.subTest(text=text):
                self.assertEqual(classify_answer(text, "tatar"), expected)

    def test_evaluation_words(self):
        self.assertEqual(evaluation_words("Дөрес түгел! Бу хата."), ["дөрес", "хата"])
        self.assertEqual(evaluation_words("Неправильно, ты ошибся. Верно так."),
                         ["неправильно", "ошибся", "верно"])
        self.assertEqual(evaluation_words("Синең хатының кайда эшли? Наверное, в школе."), [])

    def test_normalize_and_corrected_words(self):
        self.assertEqual(normalize("  Минəм — ИСЕМЕМ!! "), "минәм исемем")
        self.assertEqual(corrected_words("Мин Казанга яшим", "Мин Казанда яшим"), ["казанда"])
        self.assertEqual(corrected_words("Казанда", "Казанда"), [])


def goals(*keys: str) -> list[Goal]:
    return [Goal(key=key, kind="phrase" if key[0] == "p" else "grammar", title=key) for key in keys]


class TargetTests(unittest.TestCase):
    def test_fresh_goals_first_in_lesson_order(self):
        items = goals("p1", "p2", "g1")
        progress = {goal.key: GoalProgress() for goal in items}
        self.assertEqual(choose_target(items, progress), "p1")
        progress["p1"].approaches = 1
        self.assertEqual(choose_target(items, progress), "p2")
        progress["p2"].credited = True
        self.assertEqual(choose_target(items, progress), "g1")

    def test_then_fewest_failures_then_fewest_approaches_then_oldest(self):
        items = goals("p1", "p2", "g1")
        progress = {
            "p1": GoalProgress(approaches=2, failed=2, last_led=4),
            "p2": GoalProgress(approaches=2, failed=1, last_led=5),
            "g1": GoalProgress(approaches=1, failed=1, last_led=6),
        }
        self.assertEqual(choose_target(items, progress), "g1")
        progress["g1"].approaches = 2
        self.assertEqual(choose_target(items, progress), "p2")
        self.assertEqual(choose_target(items, progress, exclude=["p2"]), "g1")

    def test_excluded_goal_is_kept_when_it_is_the_only_open_one(self):
        items = goals("p1", "p2")
        progress = {"p1": GoalProgress(approaches=2, failed=2), "p2": GoalProgress(credited=True)}
        self.assertEqual(choose_target(items, progress, exclude=["p1"]), "p1")

    def test_all_closed_means_topic_talk(self):
        items = goals("p1")
        self.assertIsNone(choose_target(items, {"p1": GoalProgress(credited=True)}))
        self.assertIsNone(choose_target([], {}))

    def test_two_failures_in_a_row_change_approach(self):
        progress = {"p1": GoalProgress()}
        miss = [Mark(turn_no=1, goal_key="p1", verdict="miss", quote="Белмим")]
        self.assertFalse(register_attempt(progress, miss, target="p1", answer_class=NORMAL))
        self.assertTrue(register_attempt(progress, miss, target="p1", answer_class=NORMAL))
        self.assertEqual((progress["p1"].variant, progress["p1"].streak, progress["p1"].failed), (1, 0, 2))
        # Технический сбой — не попытка; зачёт обнуляет счёт подряд.
        self.assertFalse(register_attempt(progress, [], target="p1", answer_class=TECHNICAL))
        self.assertEqual(progress["p1"].failed, 2)
        register_attempt(progress, miss, target="p1", answer_class=NORMAL)
        credited = [Mark(turn_no=4, goal_key="p1", verdict="credited", quote="Минем исемем")]
        self.assertFalse(register_attempt(progress, credited, target="p1", answer_class=NORMAL))
        self.assertEqual(progress["p1"].streak, 0)


def report(key: str, *, credited=False, errors=0) -> GoalReport:
    return GoalReport(key=key, kind="phrase" if key[0] == "p" else "grammar", title=key, note="",
                      credited=credited, credited_turn=1 if credited else None,
                      correct_uses=int(credited), errors=errors, misses=0)


def general(turn_no, visibility_value, group="locative_da", kind="grammar", rule="Правило", correct="Форма"):
    return GeneralError(turn_no=turn_no, quote="x", type=kind, correct_form=correct, rule_ru=rule,
                        level="a1", level_source="", visibility=visibility_value,
                        group_key=group, group_label="Группа")


class LevelTests(unittest.TestCase):
    def test_visibility(self):
        self.assertEqual(visibility("a1", "a2"), "main")
        self.assertEqual(visibility("a2", "a2"), "main")
        self.assertEqual(visibility("b1", "a2"), "above_level")
        self.assertEqual(visibility("", "a2"), "unknown_level")
        self.assertEqual(visibility("a1", ""), "unknown_level")
        self.assertEqual([level_rank(v) for v in ("a1", "C1", "", "x")], [0, 4, -1, -1])

    def test_next_time_prefers_open_goal_wrong_form(self):
        items = goals("p1", "g1")
        reports = [report("p1", credited=True, errors=1), report("g1", errors=1)]
        marks = [
            Mark(turn_no=2, goal_key="p1", verdict="error", error_kind="wrong_form", quote="a",
                 correct_form="Минем исемем Марат", note_ru="Скажи «минем исемем»"),
            Mark(turn_no=1, goal_key="g1", verdict="error", error_kind="not_used", quote="b",
                 correct_form="Казанда", note_ru="Где? — -да"),
        ]
        chosen = pick_next_time(items, reports, marks, [general(1, "main")])
        self.assertEqual((chosen.source, chosen.example), ("goal", "Казанда"))

    def test_next_time_from_main_general_errors_only(self):
        chosen = pick_next_time([], [], [], [general(1, "above_level", group="b"), general(2, "unknown_level"),
                                             general(3, "main", group="a", correct="Казанда")])
        self.assertEqual((chosen.source, chosen.text_ru, chosen.example), ("general", "Правило", "Казанда"))
        self.assertIsNone(pick_next_time([], [], [], [general(1, "above_level")]))


class LevelsPortTests(unittest.TestCase):
    def test_missing_package_gives_safe_stubs(self):
        port = LevelsPort(None)
        self.assertFalse(port.available)
        self.assertEqual(port.guidance("a2", language="tatar", topic_text="Текст"), "")
        self.assertIsNone(port.word("китап", language="tatar"))
        self.assertIsNone(port.construction("locative_da", language="tatar"))
        self.assertEqual(port.catalog(language="tatar"), [])
        self.assertEqual(port.speech("Мин", language="tatar", target_level="a2")["estimated"], "")

    def test_failing_package_gives_safe_stubs(self):
        def broken(*args, **kwargs):
            raise RuntimeError("справочник сломан")

        port = LevelsPort(SimpleNamespace(level_guidance=broken, word_level=broken, construction_by_id=broken,
                                          constructions_catalog=broken, analyze_text=broken))
        self.assertEqual(port.guidance("a2", language="tatar", topic_text=""), "")
        self.assertIsNone(port.word("китап", language="tatar"))
        self.assertEqual(port.catalog(language="tatar"), [])
        self.assertEqual(port.speech("Мин", language="tatar", target_level="a2")["shares"]["a1"], 0.0)

    def test_contract_api_is_called_with_keywords(self):
        calls = []

        def guidance(level, language="tatar", topic_text="", max_words=60):
            calls.append((level, language, topic_text))
            return " Грамматика A1–A2 "

        port = LevelsPort(SimpleNamespace(level_guidance=guidance))
        self.assertEqual(port.guidance("a2", language="tatar", topic_text="Казан"), "Грамматика A1–A2")
        self.assertEqual(calls, [("a2", "tatar", "Казан")])
        self.assertEqual(port.guidance("", language="tatar", topic_text="Казан"), "")

    def test_above_level_words(self):
        found = SimpleNamespace(above_level=[SimpleNamespace(token="барачакмын", lemma="бару")])
        port = LevelsPort(SimpleNamespace(analyze_text=lambda text, language="tatar", target_level=None: found))
        self.assertEqual(port.above_level("Мин барачакмын", language="tatar", level="a2"), ["барачакмын"])
        self.assertEqual(port.above_level("Мин барачакмын", language="tatar", level=""), [])
        self.assertEqual(LevelsPort(None).above_level("Мин", language="tatar", level="a2"), [])

    def test_default_port_imports_levels_package_lazily(self):
        levels_port._load_default.cache_clear()
        self.addCleanup(levels_port._load_default.cache_clear)
        fake = SimpleNamespace(level_guidance=lambda *a, **k: "указания")
        with patch("importlib.import_module", return_value=fake) as importer:
            port = LevelsPort.default()
        importer.assert_called_once_with("app.levels")
        self.assertTrue(port.available)
        self.assertEqual(port.guidance("a1", language="tatar", topic_text=""), "указания")


if __name__ == "__main__":
    unittest.main()
