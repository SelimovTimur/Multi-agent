package content

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/migrate"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/NATIVE/NATIVE/migrations"
	"github.com/google/uuid"
)

// migratedPool — тестовая база, доведённая до последней миграции: колонки
// пояснений появились в 0021, и тест не должен зависеть от того, кто первым
// применил миграции.
func migratedPool(t *testing.T) *pg.Pool {
	t.Helper()
	dsn := os.Getenv("NATIVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NATIVE_TEST_DATABASE_URL не задан")
	}
	ctx := context.Background()
	pool, err := pg.Connect(ctx, pg.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	list, err := migrate.Load(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Up(ctx, pool, list); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Пояснения к целям живут в версии, как и сами цели: сохраняются, не
// стираются правкой без них и переезжают в новую версию.
func TestGoalNotesPersistAndSurviveEdits(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	actor := iam.Actor{Kind: "user", UserID: uuid.New(), OrgID: uuid.New()}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM content.lessons WHERE org_id = $1`, actor.OrgID)
	})
	svc := NewService(pool)

	setup := func(notes ...string) *AvatarConfig {
		c := &AvatarConfig{
			Enabled: true, MaxTurns: 4, Saved: true,
			PracticePhrases: []string{"Минем исемем", "Мин Казанда яшим"},
			PracticeGrammar: []string{"Местный падеж"},
		}
		if notes != nil {
			c.PracticePhraseNotes = notes[:2]
			c.PracticeGrammarNotes = notes[2:]
		}
		return c
	}
	lesson, version, err := svc.CreateFromDraft(ctx, actor, DraftContent{
		Title: "Танышу", LangCode: "tatar", Level: "A1", RefinedText: "Минем исемем Алсу.",
		Avatar: setup("Меня зовут", "Я живу в Казани", "-да/-дә"),
	})
	if err != nil {
		t.Fatal(err)
	}
	notesOf := func(versionID uuid.UUID) string {
		t.Helper()
		c, err := svc.TeacherVersion(ctx, actor, versionID)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(c.Avatar.PracticePhraseNotes, "|") + " / " + strings.Join(c.Avatar.PracticeGrammarNotes, "|")
	}
	if got := notesOf(version.ID); got != "Меня зовут|Я живу в Казани / -да/-дә" {
		t.Fatalf("сохранённые пояснения: %q", got)
	}

	// Карта урока переключает блок и присылает списки без пояснений.
	mapEdit := setup()
	mapEdit.Enabled, mapEdit.Saved = false, false
	if _, err := svc.EditVersion(ctx, actor, version.ID, DraftContent{
		Title: "Танышу", LangCode: "tatar", Level: "A1", RefinedText: "Минем исемем Алсу.", Avatar: mapEdit,
	}); err != nil {
		t.Fatal(err)
	}
	if got := notesOf(version.ID); got != "Меня зовут|Я живу в Казани / -да/-дә" {
		t.Errorf("правка без пояснений стёрла их: %q", got)
	}

	// Старый экран настройки поправил одну фразу: её пояснение пропало,
	// остальные на месте.
	oldScreen := setup()
	oldScreen.PracticePhrases = []string{"Минем исемем", "Мин Чаллыда яшим"}
	if _, err := svc.EditVersion(ctx, actor, version.ID, DraftContent{
		Title: "Танышу", LangCode: "tatar", Level: "A1", RefinedText: "Минем исемем Алсу.", Avatar: oldScreen,
	}); err != nil {
		t.Fatal(err)
	}
	if got := notesOf(version.ID); got != "Меня зовут| / -да/-дә" {
		t.Errorf("пояснения после правки одной фразы: %q", got)
	}

	// Новая версия без поля avatar наследует настройку целиком.
	next, err := svc.NewVersionFromDraft(ctx, actor, lesson.ID, DraftContent{
		Title: "Танышу", LangCode: "tatar", Level: "A1", RefinedText: "Минем исемем Алсу. Мин Чаллыда яшим.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := notesOf(next.ID); got != "Меня зовут| / -да/-дә" {
		t.Errorf("новая версия без настройки: %q", got)
	}
	// Новая версия с настройкой, но без пояснений, берёт их у прежней.
	third, err := svc.NewVersionFromDraft(ctx, actor, lesson.ID, DraftContent{
		Title: "Танышу", LangCode: "tatar", Level: "A1", RefinedText: "Минем исемем Алсу.",
		Avatar: &AvatarConfig{Enabled: true, MaxTurns: 4, Saved: true,
			PracticePhrases: []string{"Минем исемем"}, PracticeGrammar: []string{"Местный падеж"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := notesOf(third.ID); got != "Меня зовут / -да/-дә" {
		t.Errorf("новая версия с настройкой без пояснений: %q", got)
	}

	// Ученику пояснения не уходят.
	student, err := svc.StudentContent(ctx, actor.OrgID, third.ID)
	if err != nil {
		t.Fatal(err)
	}
	dto := ToStudentDTO(student).(contentDTO)
	if dto.Avatar.PracticePhraseNotes != nil || dto.Avatar.PracticeGrammarNotes != nil {
		t.Errorf("ученику ушли пояснения: %+v", dto.Avatar)
	}

	// Запись до 0021 (пустые массивы при непустых списках) читается
	// выровненной: у каждой цели есть пояснение, пусть и пустое.
	if _, err := pool.Exec(ctx, `UPDATE content.avatar_configs
		SET practice_phrase_notes = '{}', practice_grammar_notes = '{}'
		WHERE lesson_version_id = $1`, third.ID); err != nil {
		t.Fatal(err)
	}
	c, err := svc.StudentContent(ctx, actor.OrgID, third.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Avatar.PracticePhraseNotes) != 1 || len(c.Avatar.PracticeGrammarNotes) != 1 {
		t.Errorf("старая запись не выровнена: %+v", c.Avatar)
	}
}
