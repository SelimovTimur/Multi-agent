package learning

import (
	"net/http"
	"testing"
)

// Опрос пишется одной строкой на попытку, новые ответы дописываются к
// прежним, а чужая попытка для опроса не существует.
func TestFeedbackMergesIntoOneRowPerAttempt(t *testing.T) {
	f := newFixture(t)
	aidar, alsu := f.students[0], f.students[1]
	a := f.assign(t)
	f.issue(t, a, IssueClass, "", TaskText)
	attempt := f.start(t, a, aidar)

	if err := f.svc.SaveFeedback(f.ctx, aidar, attempt.ID,
		AttemptFeedback{Lesson: RatingBad, LessonWhy: "скучно", Unclear: "слова"}); err != nil {
		t.Fatal(err)
	}
	// Позже, после разговора в домашней работе, — только про аватара.
	if err := f.svc.SaveFeedback(f.ctx, aidar, attempt.ID,
		AttemptFeedback{Avatar: RatingGood}); err != nil {
		t.Fatal(err)
	}
	var rows int
	var lesson, avatar, lessonWhy, avatarWhy, unclear string
	err := f.pool.QueryRow(f.ctx, `SELECT count(*) OVER (), lesson, avatar, lesson_why, avatar_why, unclear
		FROM learning.attempt_feedback WHERE attempt_id = $1`, attempt.ID).
		Scan(&rows, &lesson, &avatar, &lessonWhy, &avatarWhy, &unclear)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 || lesson != RatingBad || avatar != RatingGood || lessonWhy != "скучно" ||
		avatarWhy != "" || unclear != "слова" {
		t.Errorf("строк %d, ответы %q %q %q %q %q — второй ответ должен дописаться к первому",
			rows, lesson, avatar, lessonWhy, avatarWhy, unclear)
	}

	err = f.svc.SaveFeedback(f.ctx, alsu, attempt.ID, AttemptFeedback{Lesson: RatingGood})
	wantErr(t, err, http.StatusNotFound, "Урок не найден")
}
