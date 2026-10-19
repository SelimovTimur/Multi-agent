"""Ответы ученика на английском: что считается ответом не на языке урока."""
from __future__ import annotations

import unittest

from app.conversation.text import FOREIGN, NORMAL, SOCIAL, STUCK, PhrasePattern, classify_answer


class EnglishAnswerTests(unittest.TestCase):
    def test_english_answer_is_normal(self):
        self.assertEqual(classify_answer("I usually play football at the weekend", "english"), NORMAL)

    def test_russian_answer_is_foreign(self):
        self.assertEqual(classify_answer("Я обычно играю в футбол", "english"), FOREIGN)

    def test_one_russian_word_keeps_the_answer_english(self):
        self.assertEqual(classify_answer("I like playing футбол with friends", "english"), NORMAL)

    def test_dont_know_is_stuck_in_both_languages(self):
        self.assertEqual(classify_answer("I don't know", "english"), STUCK)
        self.assertEqual(classify_answer("не знаю", "english"), STUCK)

    def test_greeting_is_social(self):
        self.assertEqual(classify_answer("Hello!", "english"), SOCIAL)


class EnglishGoalTitleTests(unittest.TestCase):
    def test_russian_translation_is_dropped_from_the_phrase(self):
        self.assertEqual(PhrasePattern.from_title("at the weekend — на выходных").parts, ("at the weekend",))

    def test_template_slots_stay_parts(self):
        pattern = PhrasePattern.from_title("I'm going to … — я собираюсь …")
        self.assertTrue(pattern.occurs_in("I'm going to visit my granny"))
        self.assertFalse(pattern.occurs_in("I went to my granny"))
