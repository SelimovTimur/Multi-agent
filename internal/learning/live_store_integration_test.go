package learning

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// Использует только отдельную тестовую базу с применёнными миграциями.
func TestLiveTurnRetryIsAtomic(t *testing.T) {
	dsn := os.Getenv("NATIVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NATIVE_TEST_DATABASE_URL не задан")
	}
	ctx := context.Background()
	pool, err := pg.Connect(ctx, pg.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	org, assignment, attempt, student := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO learning.assignments(id,org_id,lesson_version_id,class_id,created_by) VALUES($1,$2,$3,$4,$5)`, assignment, org, uuid.New(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM learning.assignments WHERE id=$1`, assignment)
	_, err = pool.Exec(ctx, `INSERT INTO learning.attempts(id,org_id,assignment_id,student_id) VALUES($1,$2,$3,$4)`, attempt, org, assignment, student)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM learning.dialog_turns WHERE attempt_id=$1`, attempt)
	issueID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO learning.issues(id,org_id,assignment_id,kind,created_by) VALUES($1,$2,$3,'class',$4)`, issueID, org, assignment, uuid.New()); err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	turn := DialogTurn{ID: uuid.New(), OrgID: org, AttemptID: attempt, StudentID: student, Role: RoleStudent, Content: "Мин китап укыйм.", ClientMsgID: uuid.NewString()}
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.RecordLiveTurn(ctx, pool, turn, 2, issueID); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	turns, err := store.DialogTurns(ctx, pool, org, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].Content != turn.Content {
		t.Fatalf("повтор реплики: %+v", turns)
	}
	var taken int
	if err := pool.QueryRow(ctx, `SELECT avatar_turns FROM learning.attempts WHERE id=$1`, attempt).Scan(&taken); err != nil {
		t.Fatal(err)
	}
	if taken != 1 {
		t.Fatalf("счётчик после повторов: %d", taken)
	}
	turn.ID = uuid.New()
	turn.ClientMsgID = uuid.NewString()
	turn.Role = RoleAvatar
	turn.Content = "Нинди китап?"
	if got, err := store.RecordLiveTurn(ctx, pool, turn, 2, issueID); err != nil || got != 1 {
		t.Fatalf("аватар изменил счётчик: %d %v", got, err)
	}
	turn.ID = uuid.New()
	turn.ClientMsgID = uuid.NewString()
	turn.Role = RoleStudent
	if got, err := store.RecordLiveTurn(ctx, pool, turn, 2, issueID); err != nil || got != 2 {
		t.Fatalf("вторая реплика: %d %v", got, err)
	}
	turn.ID = uuid.New()
	turn.ClientMsgID = uuid.NewString()
	if _, err := store.RecordLiveTurn(ctx, pool, turn, 2, issueID); err == nil {
		t.Fatal("превышен лимит")
	}
	turns, err = store.DialogTurns(ctx, pool, org, attempt)
	if err != nil || len(turns) != 3 {
		t.Fatalf("история: %d %v", len(turns), err)
	}
	// Реплики аватара не двигают счётчик ученика, но также занимают журнал.
	turn.Role = RoleAvatar
	for range 3 {
		turn.ID, turn.ClientMsgID = uuid.New(), uuid.NewString()
		if _, err := store.RecordLiveTurn(ctx, pool, turn, 2, issueID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RecordLiveTurn(ctx, pool, turn, 2, issueID); err != nil {
		t.Fatalf("Повтор у предела журнала: %v", err)
	}
	turn.ID, turn.ClientMsgID = uuid.New(), uuid.NewString()
	if _, err := store.RecordLiveTurn(ctx, pool, turn, 2, issueID); !errors.Is(err, errDialogEnded) {
		t.Fatalf("Журнал без лимита для аватара: %v", err)
	}
	if _, err := store.FinishIssue(ctx, pool, org, issueID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordLiveTurn(ctx, pool, turn, 2, issueID); !errors.Is(err, ErrClassFinished) {
		t.Fatalf("Реплика после закрытия: %v", err)
	}
}
