package content

import (
	"context"
	"os"
	"testing"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// Задания урока, собранные из настоящей базы: запросы LessonOutlines и
// ExercisesWithKeys читают пять таблиц схемы content, и ошибка в любом из
// них ломает и экран учителя, и экран ученика разом.
//
// Использует только отдельную тестовую базу с применёнными миграциями.
func TestLessonTasksFromDatabase(t *testing.T) {
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

	pairs := Exercise{Type: ExerciseMatching, Title: "Подбери пару",
		Payload:   []byte(`{"items":[{"phrase":"әни","translation":"мама"},{"phrase":"әти","translation":"папа"}]}`),
		AnswerKey: []byte(`{"әни":"мама","әти":"папа"}`)}
	gap := Exercise{Type: ExerciseGapFill,
		Payload: []byte(`{"questions":[{"id":"q1","prompt":"Минем ___ укытучы.",
			"choices":[{"id":"a","text":"әнием"},{"id":"b","text":"әни"}]}]}`),
		AnswerKey: []byte(`{"q1":"a"}`)}
	custom := Exercise{Type: ExerciseCustom, Payload: []byte(`{"text":"Памятка"}`)}

	_, version, err := svc.CreateFromDraft(ctx, actor, DraftContent{
		Title: "Гаилә — Семья", LangCode: "tatar", Level: "A2", RefinedText: "Минем гаиләм.",
		Objectives: []Objective{{Text: "Назову членов семьи"}, {Text: "Расскажу о семье"}},
		Lexicon:    []LexicalUnit{{Phrase: "әни"}, {Phrase: "әти"}, {Phrase: "апа"}},
		Grammar:    []GrammarTopic{{Title: "Притяжательные аффиксы"}, {Title: "Падежи"}},
		Exercises:  []Exercise{pairs, gap, custom},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Опубликованная версия — та, что назначают классам, и та, что кешируется.
	for _, status := range []string{VersionReady, VersionPublished} {
		if _, err := svc.SetVersionStatus(ctx, actor, version.ID, status, nil, nil); err != nil {
			t.Fatalf("версия → %s: %v", status, err)
		}
	}
	// Второй урок — с убранным разговором и без целей.
	off := false
	_, quiet, err := svc.CreateFromDraft(ctx, actor, DraftContent{
		Title: "Мой день", LangCode: "tatar", Level: "A1",
		Avatar: &AvatarConfig{Enabled: off, MaxTurns: AvatarDefaultTurns},
	})
	if err != nil {
		t.Fatal(err)
	}

	byVersion, err := svc.LessonTasks(ctx, actor.OrgID, []uuid.UUID{version.ID, quiet.ID, version.ID, uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if len(byVersion) != 2 {
		t.Fatalf("версий в ответе %d, ожидали 2: чужих и несуществующих там нет", len(byVersion))
	}
	tasks := byVersion[version.ID]
	keys := make([]string, 0, len(tasks))
	for _, task := range tasks {
		keys = append(keys, task.Key)
	}
	if len(tasks) != 8 || tasks[0].Key != TaskObjectives || tasks[1].Title != "Текст «Гаилә — Семья»" ||
		tasks[2].Title != "Лексика: 3 выражения" || tasks[3].Questions != 2 || tasks[4].Questions != 1 ||
		tasks[4].Title != "Вставь выражение" || tasks[5].Questions != 0 ||
		tasks[6].Title != "Грамматика: 2 темы" || tasks[7].Key != TaskDialog {
		t.Errorf("задания урока: %v\n%+v", keys, tasks)
	}
	if q := byVersion[quiet.ID]; len(q) != 1 || q[0].Key != TaskText {
		t.Errorf("урок без целей, лексики, упражнений и разговора — только текст: %+v", q)
	}

	// Опубликованная версия легла в кеш, черновик — нет: его ещё правят.
	if _, ok := svc.tasks.get(actor.OrgID, version.ID); !ok {
		t.Error("опубликованная версия кешируется")
	}
	if _, ok := svc.tasks.get(actor.OrgID, quiet.ID); ok {
		t.Error("черновик не кешируется")
	}
	again, err := svc.LessonTasks(ctx, actor.OrgID, []uuid.UUID{version.ID})
	if err != nil || len(again[version.ID]) != 8 {
		t.Fatalf("повторное чтение из кеша: %v %v", again, err)
	}
	// Чужой тенант не находит версию даже в кеше.
	if other, err := svc.LessonTasks(ctx, uuid.New(), []uuid.UUID{version.ID}); err != nil || len(other) != 0 {
		t.Errorf("версия чужой организации: %v %v", other, err)
	}

	reviews, err := svc.ExerciseReviews(ctx, actor.OrgID, version.ID)
	if err != nil {
		t.Fatal(err)
	}
	pairReview := reviews[tasks[3].ExerciseID]
	gapReview := reviews[tasks[4].ExerciseID]
	if len(pairReview.Questions) != 2 || pairReview.Questions[1].Expected != "папа" {
		t.Errorf("пары с верными переводами: %+v", pairReview)
	}
	if len(gapReview.Questions) != 1 || gapReview.Questions[0].Expected != "әнием" ||
		gapReview.Questions[0].Choices["b"] != "әни" {
		t.Errorf("тест с верным вариантом словами: %+v", gapReview)
	}
	if other, err := svc.ExerciseReviews(ctx, uuid.New(), version.ID); err != nil || len(other) != 0 {
		t.Errorf("разбор чужой организации: %v %v", other, err)
	}
}
