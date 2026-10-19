package learning

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type closingLessons struct {
	Lessons
	close func() error
}

func (l closingLessons) CheckAnswer(ctx context.Context, org, version, exercise uuid.UUID, item, answer string) (AnswerCheck, error) {
	if err := l.close(); err != nil {
		return AnswerCheck{}, err
	}
	return l.Lessons.CheckAnswer(ctx, org, version, exercise, item, answer)
}

func TestAnswerRechecksIssueAfterAnswerValidation(t *testing.T) {
	f := newFixture(t)
	a := f.assign(t)
	is := f.issue(t, a, IssueClass, "", ExerciseTaskKey(f.match))
	at := f.start(t, a, f.students[0])
	f.svc.lessons = closingLessons{Lessons: f.lessons, close: func() error {
		_, err := f.svc.FinishIssue(f.ctx, f.teacher, is.Issue.ID)
		return err
	}}
	_, err := f.answer(f.students[0], at, "әни", "мама")
	wantErr(t, err, 409, msgClassFinished)
	current, err := f.svc.store.AttemptByID(f.ctx, f.pool, f.org, at.ID)
	if err != nil || current.TotalAnswers != 0 || current.Score != 0 || len(current.Answers) != 0 {
		t.Fatalf("Ответ записан после закрытия: %+v, %v", current, err)
	}
}

func TestOpenedTaskRechecksIssueAfterStaleGate(t *testing.T) {
	for _, kind := range []string{IssueClass, IssueHome} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			a := f.assign(t)
			due := ""
			if kind == IssueHome {
				due = dateIn(1)
			}
			is := f.issue(t, a, kind, due, TaskDialog)
			at := f.start(t, a, f.students[0])
			if kind == IssueClass {
				if _, err := f.svc.FinishIssue(f.ctx, f.teacher, is.Issue.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.pool.Exec(f.ctx, `UPDATE learning.issues SET due_at=$2 WHERE id=$1`, is.Issue.ID, time.Now().Add(-time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			// Имитирует уже пройденную проверку снимка доски в соседнем запросе.
			err := f.svc.markOpened(f.ctx, f.students[0], at.ID, TaskDialog, is.Issue.ID)
			msg := msgClassFinished
			if kind == IssueHome {
				msg = msgHomeExpired
			}
			wantErr(t, err, 409, msg)
			progress, err := f.svc.store.ProgressOfAttempts(f.ctx, f.pool, f.org, []uuid.UUID{at.ID})
			if err != nil || len(progress) != 0 {
				t.Fatalf("Закрытое задание отмечено открытым: %+v, %v", progress, err)
			}
		})
	}
}
