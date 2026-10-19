package learning

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// Страница урока: доля верных по заданию и одинаковые ошибки, склеенные в
// одну строку с числом. Ушедший из класса ученик не считается — как и во
// всей аналитике.
func TestLessonAnalyticsGluesSameMistakes(t *testing.T) {
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)
	exID := uuid.New()
	task := LessonTask{Key: ExerciseTaskKey(exID), N: 1, Kind: TaskExercise, Title: "Вставь слово",
		Questions: 2, ExerciseID: exID, ItemIDs: []string{"q1", "q2"}}
	dialog := LessonTask{Key: "dialog", N: 2, Kind: TaskDialog, Title: "Диалог"}
	review := ExerciseReview{Questions: []QuestionReview{
		{ItemID: "q1", Prompt: "Мин ___ яшим.", Expected: "Казанда"},
		{ItemID: "q2", Prompt: "Бу ___.", Expected: "урам", Choices: map[string]string{"a": "урам", "b": "юл"}},
	}}

	a := Assignment{ID: uuid.New(), LessonVersionID: uuid.New(), CreatedAt: now.Add(-time.Hour)}
	aigul, timur, kamila, left := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	answers := func(q1, q2 string, ok1, ok2 bool) map[string]GivenAnswer {
		p := exID.String() + ":"
		return map[string]GivenAnswer{p + "q1": {Answer: q1, Correct: ok1}, p + "q2": {Answer: q2, Correct: ok2}}
	}
	c := classwork{
		assignment: a,
		tasks:      []LessonTask{task, dialog},
		members: []ClassMember{
			{ID: aigul, DisplayName: "Айгуль"}, {ID: timur, DisplayName: "Тимур"}, {ID: kamila, DisplayName: "Камила"},
		},
		attempts: []Attempt{
			{ID: uuid.New(), AssignmentID: a.ID, StudentID: aigul, Completed: true, CorrectAnswers: 1, TotalQuestions: 2,
				Answers: answers("Казанга", "a", false, true)},
			{ID: uuid.New(), AssignmentID: a.ID, StudentID: timur, CorrectAnswers: 0, TotalQuestions: 2,
				Answers: answers("казанга ", "b", false, false)},
			// Ушедший: тоже ошибся, но в страницу не попадает.
			{ID: uuid.New(), AssignmentID: a.ID, StudentID: left, CorrectAnswers: 0, TotalQuestions: 2,
				Answers: answers("Казанга", "b", false, false)},
		},
	}

	got := buildLessonAnalytics(c, map[uuid.UUID]ExerciseReview{exID: review}, nil, now)

	if got.Students != 3 || got.Passed != 1 {
		t.Errorf("в классе трое, прошёл один, получили %d и %d", got.Students, got.Passed)
	}
	if p := got.Tasks.Percent(); p == nil || *p != 25 {
		t.Errorf("верных 1 из 4 вопросов — 25 %%, получили %v", p)
	}
	if !got.HasDialog || got.DialogIssued {
		t.Errorf("диалог в уроке есть, но не выдан: %+v", got)
	}
	if len(got.Exercises) != 1 {
		t.Fatalf("письменное задание одно, получили %d", len(got.Exercises))
	}
	ex := got.Exercises[0]
	if ex.Students != 2 || ex.Answered != 4 || ex.Correct != 1 {
		t.Errorf("ответили двое, 4 ответа, 1 верный; получили %+v", ex)
	}
	if len(ex.Mistakes) != 2 {
		t.Fatalf("две разные ошибки, получили %+v", ex.Mistakes)
	}
	top := ex.Mistakes[0]
	if top.Answer != "Казанга" || top.Expected != "Казанда" || top.Count != 2 || len(top.Students) != 2 {
		t.Errorf("«Казанга» и «казанга » — одна ошибка у двоих: %+v", top)
	}
	// Ответ на тест — текст варианта, а не его идентификатор.
	if second := ex.Mistakes[1]; second.Answer != "юл" || second.Count != 1 {
		t.Errorf("вторая ошибка — «юл» один раз: %+v", second)
	}
}
