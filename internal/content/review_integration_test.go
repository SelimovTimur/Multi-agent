package content

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

func reviewFixture(t *testing.T) (*Service, iam.Actor, DraftContent, LessonVersion) {
	t.Helper()
	dsn := os.Getenv("NATIVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NATIVE_TEST_DATABASE_URL не задан")
	}
	pool, err := pg.Connect(context.Background(), pg.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	actor := iam.Actor{Kind: "user", UserID: uuid.New(), OrgID: uuid.New()}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM content.lessons WHERE org_id=$1`, actor.OrgID)
	})
	in := DraftContent{Title: "Проверка версий", LangCode: "tatar", Level: "A2", RefinedText: "Текст",
		Objectives: []Objective{{Text: "Цель урока"}},
		Lexicon:    []LexicalUnit{{Phrase: "сүз", Translation: "слово"}},
		Grammar:    []GrammarTopic{{Title: "Правило"}},
		Exercises:  []Exercise{{Type: ExerciseGapFill, Payload: json.RawMessage(`{"questions":[{"id":"q1","prompt":"Вопрос"}]}`), AnswerKey: json.RawMessage(`{"q1":"a"}`)}}}
	svc := NewService(pool)
	_, version, err := svc.CreateFromDraft(context.Background(), actor, in)
	if err != nil {
		t.Fatal(err)
	}
	return svc, actor, in, version
}

func TestNewVersionCopiesContentWithNewIDs(t *testing.T) {
	svc, actor, in, version := reviewFixture(t)
	ctx := context.Background()
	before, err := svc.TeacherVersion(ctx, actor, version.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.Objectives = nil // Так сохраняют экраны правки текста: цели наследуются.
	newVersion, err := svc.NewVersionFromDraft(ctx, actor, version.LessonID, in)
	if err != nil {
		t.Fatal(err)
	}
	after, err := svc.TeacherVersion(ctx, actor, newVersion.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Objectives) != 1 || after.Objectives[0].Text != before.Objectives[0].Text || after.Objectives[0].ID == before.Objectives[0].ID {
		t.Fatalf("Цели не скопированы с новым ID: %+v", after.Objectives)
	}
	if in.Lexicon[0].ID != uuid.Nil || in.Grammar[0].ID != uuid.Nil || in.Exercises[0].ID != uuid.Nil {
		t.Fatal("Создание изменило переданный черновик")
	}
	// ID из сохранённой версии тоже никогда не переезжают в новую.
	in.Lexicon, in.Grammar, in.Exercises, in.Objectives = before.Lexicon, before.Grammar, before.Exercises, before.Objectives
	if _, err := svc.NewVersionFromDraft(ctx, actor, version.LessonID, in); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateFromDraft(ctx, actor, in); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentVersionNumbers(t *testing.T) {
	svc, actor, in, version := reviewFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const count = 8
	start := make(chan struct{})
	results := make(chan LessonVersion, count)
	errors := make(chan error, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			v, err := svc.NewVersionFromDraft(ctx, actor, version.LessonID, in)
			if err != nil {
				errors <- err
			} else {
				results <- v
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	seen := map[int]bool{}
	for v := range results {
		if seen[v.Version] {
			t.Errorf("Повтор номера %d", v.Version)
		}
		seen[v.Version] = true
	}
	for n := 2; n <= count+1; n++ {
		if !seen[n] {
			t.Errorf("Нет версии %d", n)
		}
	}
}

func TestArchivedVersionStaysImmutable(t *testing.T) {
	svc, actor, in, version := reviewFixture(t)
	ctx := context.Background()
	for _, status := range []string{VersionReady, VersionPublished, VersionArchived} {
		if _, err := svc.SetVersionStatus(ctx, actor, version.ID, status, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	in.RefinedText = "Подмена"
	if _, err := svc.EditVersion(ctx, actor, version.ID, in); err == nil || httpx.AsError(err).Status != 409 {
		t.Fatalf("Правка архивированной версии: %v", err)
	}
	for _, field := range []string{"refined_text", "note", "quality_warnings"} {
		value := "'Подмена'"
		if field == "quality_warnings" {
			value = "'[{\"message\":\"Подмена\"}]'::jsonb"
		}
		_, err := svc.pool.Exec(ctx, `UPDATE content.lesson_versions SET `+field+`=`+value+` WHERE id=$1 AND org_id=$2`, version.ID, actor.OrgID)
		if !pg.IsRestrictViolation(err) {
			t.Fatalf("База разрешила изменение %s: %v", field, err)
		}
	}
}

func TestPublishChecksContentAfterConcurrentEdit(t *testing.T) {
	svc, actor, _, version := reviewFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := svc.SetVersionStatus(ctx, actor, version.ID, VersionReady, nil, nil); err != nil {
		t.Fatal(err)
	}
	tx, err := svc.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = svc.store.LockVersion(ctx, tx, actor.OrgID, version.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE content.exercises SET payload='{"questions":[]}' WHERE lesson_version_id=$1`, version.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := svc.SetVersionStatus(ctx, actor, version.ID, VersionPublished, nil, nil)
		done <- err
	}()
	// Ждём именно блокировки публикации, чтобы проверка воспроизводила гонку,
	// а не полагалась на скорость планировщика и произвольный sleep.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := svc.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%content.lesson_versions%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Публикация не дождалась блокировки")
		case <-ticker.C:
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err == nil || httpx.AsError(err).Status != 409 {
		t.Fatalf("Публикация пустого упражнения: %v", err)
	}
	current, err := svc.store.VersionByID(ctx, svc.pool, actor.OrgID, version.ID)
	if err != nil || current.Status != VersionReady {
		t.Fatalf("Статус после отказа: %s, %v", current.Status, err)
	}
}

type assignedVersion struct{}

func (assignedVersion) HasVersionAssignments(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}
func (assignedVersion) ClassesForLessons(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID][]ClassRef, error) {
	return nil, nil
}

func TestPreviouslyAssignedReadyVersionCannotBeEdited(t *testing.T) {
	svc, actor, in, version := reviewFixture(t)
	if _, err := svc.SetVersionStatus(context.Background(), actor, version.ID, VersionReady, nil, nil); err != nil {
		t.Fatal(err)
	}
	svc.WithAssignments(assignedVersion{})
	if _, err := svc.EditVersion(context.Background(), actor, version.ID, in); err == nil || httpx.AsError(err).Status != 409 {
		t.Fatalf("Правка назначенной ready-версии: %v", err)
	}
}
