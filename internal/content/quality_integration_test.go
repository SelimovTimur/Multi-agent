package content

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// Проверяем полный путь сохранения: исчезнувшее замечание не возвращается
// после чтения из базы, а точечная правка не меняет ключи заданий.
func TestSelectedRepairPersistence(t *testing.T) {
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
	actor := iam.Actor{Kind: "user", UserID: uuid.New(), OrgID: uuid.New()}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM content.lessons WHERE org_id = $1`, actor.OrgID)
	})
	svc := NewService(pool)
	in := DraftContent{
		Title: "Тест исправления", LangCode: "tatar", Level: "A2", RefinedText: "Мин мәктәпкә барам.",
		QualityWarnings: json.RawMessage(`[{"stage":"keyword_audit","message":"Исправить перевод"},{"stage":"material_review","message":"Оставить текст"}]`),
		Lexicon:         []LexicalUnit{{Phrase: "мәктәпкә", Translation: "в магазин", Origin: OriginTeacher}},
		Grammar:         []GrammarTopic{{Title: "Направление", Example: "мәктәпкә", Origin: OriginTeacher}},
		Objectives:      []Objective{{Text: "Расскажу о школе"}},
		Exercises: []Exercise{{Type: ExerciseGapFill, Payload: json.RawMessage(`{"questions":[{"id":"q1","prompt":"Мин ___ барам."}]}`),
			AnswerKey: json.RawMessage(`{"q1":"A"}`), RawSource: "Исходное задание"}},
	}
	_, version, err := svc.CreateFromDraft(ctx, actor, in)
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.TeacherVersion(ctx, actor, version.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.PreserveContentIDs = true
	in.Lexicon[0].Translation = "в школу"
	in.QualityWarnings = json.RawMessage(`[{"stage":"material_review","message":"Оставить текст"}]`)
	if _, err := svc.EditVersion(ctx, actor, version.ID, in); err != nil {
		t.Fatal(err)
	}
	after, err := svc.TeacherVersion(ctx, actor, version.ID)
	if err != nil {
		t.Fatal(err)
	}
	var warnings []map[string]string
	if err := json.Unmarshal(after.Version.QualityWarnings, &warnings); err != nil || len(warnings) != 1 || warnings[0]["message"] != "Оставить текст" {
		t.Fatalf("оставшееся замечание: %s, %v", after.Version.QualityWarnings, err)
	}
	if after.Lexicon[0].Translation != "в школу" || after.Lexicon[0].ID != before.Lexicon[0].ID || after.Lexicon[0].Origin != OriginTeacher {
		t.Errorf("перевод с прежним идентификатором и происхождением: %+v", after.Lexicon)
	}
	if !reflect.DeepEqual(before.Grammar, after.Grammar) || !reflect.DeepEqual(before.Exercises, after.Exercises) || !reflect.DeepEqual(before.Objectives, after.Objectives) {
		t.Error("точечная правка изменила остальные задания")
	}
	// Старые экраны не присылают замечания: отсутствие поля их не стирает.
	in.QualityWarnings = nil
	if _, err := svc.EditVersion(ctx, actor, version.ID, in); err != nil {
		t.Fatal(err)
	}
	preserved, err := svc.TeacherVersion(ctx, actor, version.ID)
	if err != nil || !reflect.DeepEqual(preserved.Version.QualityWarnings, after.Version.QualityWarnings) {
		t.Fatalf("отсутствующее поле сбросило замечания: %s, %v", preserved.Version.QualityWarnings, err)
	}
	// Несовпавший состав откатывает и изменения текста, и список замечаний.
	in.QualityWarnings = json.RawMessage(`[]`)
	in.Lexicon = nil
	if _, err := svc.EditVersion(ctx, actor, version.ID, in); err == nil {
		t.Fatal("изменение состава принято как точечная правка")
	}
	rolledBack, err := svc.TeacherVersion(ctx, actor, version.ID)
	if err != nil || !reflect.DeepEqual(rolledBack.Version.QualityWarnings, after.Version.QualityWarnings) {
		t.Fatalf("неуспешная правка не откатилась: %s, %v", rolledBack.Version.QualityWarnings, err)
	}
	in.Lexicon = []LexicalUnit{{Phrase: "мәктәпкә", Translation: "в школу"}}
	if _, err := svc.EditVersion(ctx, actor, version.ID, in); err != nil {
		t.Fatal(err)
	}
	cleared, err := svc.TeacherVersion(ctx, actor, version.ID)
	if err != nil || string(cleared.Version.QualityWarnings) != "[]" {
		t.Fatalf("последнее замечание не удалено: %s, %v", cleared.Version.QualityWarnings, err)
	}
}
