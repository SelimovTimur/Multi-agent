package learning

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// Проверка релиза нашла три способа получить цифры, которых нет: пролистать
// урок кнопкой «Дальше», ответить на один вопрос из пяти и открыть
// пройденный урок, чтобы ответить ещё раз. Всё это — про одну и ту же
// арифметику попытки, и проверяется она здесь.

func TestPassedNeedsAnswersNotClicks(t *testing.T) {
	finished := time.Now()
	cases := []struct {
		name     string
		attempt  Attempt
		expected bool
	}{
		{
			name: "пролистал до конца, не ответив ни разу",
			attempt: Attempt{
				TotalQuestions: 5, Answers: map[string]GivenAnswer{}, FinishedAt: &finished,
			},
			expected: false,
		},
		{
			name: "ответил на один вопрос из пяти",
			attempt: Attempt{
				TotalQuestions: 5, Answers: answers(1), FinishedAt: &finished,
			},
			expected: false,
		},
		{
			name: "ответил на половину",
			attempt: Attempt{
				TotalQuestions: 4, Answers: answers(2), FinishedAt: &finished,
			},
			expected: true,
		},
		{
			name: "урок без заданий — засчитываем сам факт прохождения",
			attempt: Attempt{
				TotalQuestions: 0, Answers: map[string]GivenAnswer{}, FinishedAt: &finished,
			},
			expected: true,
		},
		{
			name: "ответил на всё, но урок не завершил",
			attempt: Attempt{
				TotalQuestions: 2, Answers: answers(2),
			},
			expected: false,
		},
	}

	for _, c := range cases {
		if got := c.attempt.Passed(); got != c.expected {
			t.Errorf("%s: пройден=%v, ожидали %v", c.name, got, c.expected)
		}
	}
}

// Ключ ответа различает вопросы разных заданий: номера вопросов в них
// совпадают, и без упражнения в ключе ответ на одно закрывал бы другое.
func TestAnswerKeySeparatesExercises(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	if answerKey(&first, "q_1") == answerKey(&second, "q_1") {
		t.Error("одинаковые номера вопросов в разных заданиях дали один ключ")
	}
	if answerKey(&first, "q_1") == answerKey(&first, "q_2") {
		t.Error("разные вопросы одного задания дали один ключ")
	}
	// Ответ без упражнения тоже бывает: шаг урока без проверки ключом.
	if answerKey(nil, "q_1") == "" {
		t.Error("ответ без упражнения должен иметь ключ")
	}
}

// Доля верных считается от вопросов урока. Ученик, ответивший на два
// вопроса из шестнадцати, не должен выглядеть лучше того, кто ответил на все.
func TestPercentIsCountedFromQuestionsNotAnswers(t *testing.T) {
	diligent := percent(12, 16) // ответил на все шестнадцать, двенадцать верно
	lazy := percent(2, 16)      // ответил на два, оба верно
	if lazy >= diligent {
		t.Errorf("ленивый %d%% против старательного %d%% — считаем не от того", lazy, diligent)
	}
	if percent(2, 2) != 100 {
		t.Error("ответивший на все вопросы верно должен видеть сто процентов")
	}
}

func answers(n int) map[string]GivenAnswer {
	out := make(map[string]GivenAnswer, n)
	for i := 0; i < n; i++ {
		out[string(rune('a'+i))] = GivenAnswer{Answer: "x", Correct: true}
	}
	return out
}
