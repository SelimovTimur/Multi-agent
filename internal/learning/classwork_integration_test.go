package learning

// Интеграционные тесты «Урока в классе» на настоящей базе. Без
// NATIVE_TEST_DATABASE_URL пропускаются, как и остальные тесты с базой.
//
// Классы и уроки — подделки портов: learning о classroom и content знает
// только интерфейсы, и проверяется здесь именно learning — его SQL, его
// транзакции и его правила.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/migrate"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/NATIVE/NATIVE/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool подключается к тестовой базе и доводит схему до последней
// миграции: тест не должен падать оттого, что миграцию забыли применить.
func testPool(t *testing.T) *pg.Pool {
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

// ---------------------------------------------------------------------------
// Подделки портов
// ---------------------------------------------------------------------------

type fakeClasses struct {
	mu      sync.Mutex
	teacher uuid.UUID
	classes map[uuid.UUID]ClassInfo
	members map[uuid.UUID][]ClassMember
}

func (f *fakeClasses) OwnedClass(_ context.Context, actor iam.Actor, classID uuid.UUID) (ClassInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.classes[classID]
	if !ok || actor.UserID != f.teacher {
		return ClassInfo{}, httpx.NotFound("Класс не найден")
	}
	return c, nil
}

func (f *fakeClasses) StudentClasses(_ context.Context, actor iam.Actor) ([]ClassInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ClassInfo
	for id, members := range f.members {
		for _, m := range members {
			if m.ID == actor.StudentID {
				out = append(out, f.classes[id])
			}
		}
	}
	return out, nil
}

func (f *fakeClasses) Roster(ctx context.Context, actor iam.Actor, classID uuid.UUID) ([]ClassMember, error) {
	if _, err := f.OwnedClass(ctx, actor, classID); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ClassMember(nil), f.members[classID]...), nil
}

func (f *fakeClasses) ClassesInfo(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]ClassInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[uuid.UUID]ClassInfo{}
	for _, id := range ids {
		if c, ok := f.classes[id]; ok {
			out[id] = c
		}
	}
	return out, nil
}

type fakeLessons struct {
	versions map[uuid.UUID]VersionInfo
	tasks    map[uuid.UUID][]LessonTask
	// keys — ключ ответов: упражнение → вопрос → верный ответ.
	keys    map[uuid.UUID]map[string]string
	reviews map[uuid.UUID]map[uuid.UUID]ExerciseReview
	words   map[uuid.UUID][]LessonWord
}

func (f *fakeLessons) OwnedVersion(_ context.Context, _ iam.Actor, id uuid.UUID) (VersionInfo, error) {
	v, ok := f.versions[id]
	if !ok {
		return VersionInfo{}, httpx.NotFound("Версия урока не найдена")
	}
	return v, nil
}

func (f *fakeLessons) VersionsInfo(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]VersionInfo, error) {
	out := map[uuid.UUID]VersionInfo{}
	for _, id := range ids {
		if v, ok := f.versions[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

func (f *fakeLessons) StudentContent(context.Context, uuid.UUID, uuid.UUID) (any, error) {
	return map[string]any{"ok": true}, nil
}

func (f *fakeLessons) CheckAnswer(_ context.Context, _, _, exerciseID uuid.UUID, itemID, value string) (AnswerCheck, error) {
	key, ok := f.keys[exerciseID]
	if !ok {
		return AnswerCheck{}, httpx.NotFound("Упражнение не найдено")
	}
	expected := key[itemID]
	out := AnswerCheck{Correct: expected != "" && expected == value, Expected: expected, Kind: "matching"}
	if out.Correct {
		out.Score = 10
	}
	return out, nil
}

func (f *fakeLessons) QuestionCount(_ context.Context, _, versionID uuid.UUID) (int, error) {
	n := 0
	for _, t := range f.tasks[versionID] {
		n += t.Questions
	}
	return n, nil
}

func (f *fakeLessons) LessonTasks(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID][]LessonTask, error) {
	out := map[uuid.UUID][]LessonTask{}
	for _, id := range ids {
		if tasks, ok := f.tasks[id]; ok {
			out[id] = tasks
		}
	}
	return out, nil
}

func (f *fakeLessons) ExerciseReviews(_ context.Context, _, versionID uuid.UUID) (map[uuid.UUID]ExerciseReview, error) {
	return f.reviews[versionID], nil
}

func (f *fakeLessons) LessonWords(_ context.Context, _, versionID uuid.UUID) ([]LessonWord, error) {
	return f.words[versionID], nil
}

type fakeMaterial struct{}

func (m fakeMaterial) TeacherAvatarBrief(ctx context.Context, actor iam.Actor, versionID uuid.UUID) (AvatarBrief, error) {
	return m.AvatarBriefFor(ctx, actor.OrgID, versionID)
}

func (fakeMaterial) AvatarBriefFor(context.Context, uuid.UUID, uuid.UUID) (AvatarBrief, error) {
	return AvatarBrief{Enabled: true, MaxTurns: 8, Language: "tatar"}, nil
}

// ---------------------------------------------------------------------------
// Класс с уроком
// ---------------------------------------------------------------------------

type fixture struct {
	ctx      context.Context
	pool     *pg.Pool
	svc      *Service
	org      uuid.UUID
	teacher  iam.Actor
	students []iam.Actor
	classID  uuid.UUID
	classes  *fakeClasses
	lessons  *fakeLessons
	// Урок: текст, сопоставление на два вопроса, «свой текст», диалог.
	versionID uuid.UUID
	lessonID  uuid.UUID
	match     uuid.UUID
	custom    uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureOnPool(t, testPool(t))
}

func newFixtureOnPool(t *testing.T, pool *pg.Pool) *fixture {
	t.Helper()
	f := &fixture{
		ctx: context.Background(), pool: pool,
		org: uuid.New(), classID: uuid.New(), versionID: uuid.New(), lessonID: uuid.New(),
		match: uuid.New(), custom: uuid.New(),
	}
	f.teacher = iam.Actor{Kind: "user", UserID: uuid.New(), OrgID: f.org, Role: "teacher"}
	members := []ClassMember{
		{ID: uuid.New(), DisplayName: "Айдар", PublicTag: "#KZ4F"},
		{ID: uuid.New(), DisplayName: "Алсу", PublicTag: "#M2QP"},
	}
	for _, m := range members {
		f.students = append(f.students, iam.Actor{Kind: "student", StudentID: m.ID, OrgID: f.org})
	}
	f.classes = &fakeClasses{
		teacher: f.teacher.UserID,
		classes: map[uuid.UUID]ClassInfo{f.classID: {ID: f.classID, Title: "7А"}},
		members: map[uuid.UUID][]ClassMember{f.classID: members},
	}
	f.lessons = &fakeLessons{
		versions: map[uuid.UUID]VersionInfo{f.versionID: {
			ID: f.versionID, LessonID: f.lessonID, Version: 1, LessonTitle: "Гаилә — Семья",
			Level: "A2", Status: "published",
		}},
		tasks: map[uuid.UUID][]LessonTask{f.versionID: {
			{Key: TaskText, N: 1, Kind: TaskText, TypeLabel: "Текст", Title: "Текст «Гаилә»"},
			{Key: ExerciseTaskKey(f.match), N: 2, Kind: TaskExercise, Type: "matching", TypeLabel: "Упражнение",
				Title: "Подбери пару", Questions: 2, ExerciseID: f.match, ItemIDs: []string{"әни", "әти"}},
			{Key: ExerciseTaskKey(f.custom), N: 3, Kind: TaskExercise, Type: "custom", TypeLabel: "Упражнение",
				Title: "Свой текст", ExerciseID: f.custom},
			{Key: TaskDialog, N: 4, Kind: TaskDialog, TypeLabel: "Диалог", Title: "Разговор с аватаром"},
		}},
		keys: map[uuid.UUID]map[string]string{f.match: {"әни": "мама", "әти": "папа"}},
		reviews: map[uuid.UUID]map[uuid.UUID]ExerciseReview{f.versionID: {
			f.match: {Questions: []QuestionReview{
				{ItemID: "әни", Prompt: "әни", Expected: "мама"},
				{ItemID: "әти", Prompt: "әти", Expected: "папа"},
			}},
		}},
		words: map[uuid.UUID][]LessonWord{f.versionID: {
			{Phrase: "әни", Translation: "мама"}, {Phrase: "әти", Translation: "папа"},
		}},
	}
	f.svc = NewService(pool, f.classes, f.lessons).WithSchoolZone(msk)
	f.svc.material = fakeMaterial{}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM learning.assignments WHERE org_id = $1`, f.org)
		_, _ = pool.Exec(ctx, `DELETE FROM learning.presence WHERE org_id = $1`, f.org)
	})
	return f
}

func (f *fixture) assign(t *testing.T) Assignment {
	t.Helper()
	a, err := f.svc.Assign(f.ctx, f.teacher, AssignInput{LessonVersionID: f.versionID, ClassID: f.classID})
	if err != nil {
		t.Fatalf("назначить урок: %v", err)
	}
	return a
}

func (f *fixture) issue(t *testing.T, a Assignment, kind, due string, keys ...string) IssueView {
	t.Helper()
	v, err := f.svc.IssueTasks(f.ctx, f.teacher, a.ID, IssueInput{Kind: kind, TaskKeys: keys, DueDate: due})
	if err != nil {
		t.Fatalf("выдать %v: %v", keys, err)
	}
	return v
}

func (f *fixture) start(t *testing.T, a Assignment, student iam.Actor) Attempt {
	t.Helper()
	at, _, err := f.svc.StartLesson(f.ctx, student, a.ID)
	if err != nil {
		t.Fatalf("начать урок: %v", err)
	}
	return at
}

func (f *fixture) answer(student iam.Actor, attempt Attempt, item, value string) (AnswerResult, error) {
	ex := f.match
	return f.svc.RecordAnswer(f.ctx, student, AnswerInput{
		AttemptID: attempt.ID, ExerciseID: &ex, ItemID: item, Answer: value,
	})
}

// dateIn — дата через days дней по времени школы, для due_date.
func dateIn(days int) string { return time.Now().In(msk).AddDate(0, 0, days).Format(dueDateLayout) }

// wantConflict — ошибка с кодом и текстом из контракта.
func wantErr(t *testing.T, err error, status int, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("ожидали %d %q, а отказа нет", status, msg)
	}
	he := httpx.AsError(err)
	if he.Status != status || (msg != "" && he.Message != msg && he.Fields["task_keys"] != msg &&
		he.Fields["due_date"] != msg) {
		t.Fatalf("ожидали %d %q, получили %d %q %v (%v)", status, msg, he.Status, he.Message, he.Fields, err)
	}
}

// ---------------------------------------------------------------------------
// Тесты
// ---------------------------------------------------------------------------

// Сквозной сценарий стори: назначить, выдать на уроке, сделать, завершить,
// задать на дом, доделать — и увидеть это в таблице, панели, карточке и у
// ученика.
func TestClassworkFlow(t *testing.T) {
	f := newFixture(t)
	aidar := f.students[0]
	a := f.assign(t)

	// Назначенный урок закрыт: у ученика его нет, открыть нельзя.
	if a.State != StateActive || a.OpensAt != nil || a.ClosesAt != nil {
		t.Errorf("назначение без сроков: %+v", a)
	}
	lessons, err := f.svc.MyLessons(f.ctx, aidar, uuid.Nil)
	if err != nil || len(lessons) != 0 {
		t.Fatalf("ученик не видит невыданный урок: %d %v", len(lessons), err)
	}
	_, _, err = f.svc.StartLesson(f.ctx, aidar, a.ID)
	wantErr(t, err, http.StatusForbidden, msgNothingIssued)
	rows, err := f.svc.ClassAssignments(f.ctx, f.teacher, f.classID, "")
	if err != nil || len(rows) != 1 || rows[0].Summary.TasksTotal != 4 || rows[0].Summary.TasksIssued != 0 {
		t.Fatalf("вкладка «Уроки» — выдано 0 из 4: %+v %v", rows, err)
	}

	// На уроке — текст и упражнение.
	class := f.issue(t, a, IssueClass, "", ExerciseTaskKey(f.match), TaskText)
	if !class.Open || class.Started || class.DoneStudents != 0 || class.Issue.DueAt == nil ||
		!class.Issue.DueAt.Equal(classDueAt(time.Now(), msk)) {
		t.Fatalf("выдача на уроке: %+v", class)
	}
	if len(class.TaskKeys) != 2 || class.TaskKeys[0] != TaskText {
		t.Errorf("задания выдачи по порядку урока: %v", class.TaskKeys)
	}

	lessons, err = f.svc.MyLessons(f.ctx, aidar, uuid.Nil)
	if err != nil || len(lessons) != 1 {
		t.Fatalf("урок появился у ученика: %v", err)
	}
	if s := lessons[0].ClassSection; s == nil || s.Tasks != 2 || s.Left != 2 || s.Begun || lessons[0].Attempt != nil {
		t.Errorf("«Сейчас на уроке · 2 задания»: %+v", s)
	}

	attempt := f.start(t, a, aidar)
	section, err := f.svc.SectionTasks(f.ctx, aidar, attempt.ID, IssueClass)
	if err != nil || !section.Open || len(section.Tasks) != 2 {
		t.Fatalf("раздел на уроке: %+v %v", section, err)
	}

	if err := f.svc.OpenTask(f.ctx, aidar, attempt.ID, TaskText); err != nil {
		t.Fatalf("открыть текст: %v", err)
	}
	if err := f.svc.OpenTask(f.ctx, aidar, attempt.ID, TaskText); err != nil {
		t.Fatalf("повторное открытие — не ошибка: %v", err)
	}
	live, err := f.svc.Live(f.ctx, f.teacher, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if live.Students[0].Cells[TaskText].State != CellOpened || !live.Issues[0].Started {
		t.Errorf("учитель видит «открыл»: %+v, начата=%v", live.Students[0].Cells, live.Issues[0].Started)
	}

	if err := f.svc.TaskDone(f.ctx, aidar, attempt.ID, TaskText); err != nil {
		t.Fatalf("текст прочитан: %v", err)
	}
	err = f.svc.TaskDone(f.ctx, aidar, attempt.ID, ExerciseTaskKey(f.match))
	wantErr(t, err, http.StatusConflict, "Задание засчитывается по ответам")

	if res, err := f.answer(aidar, attempt, "әни", "мама"); err != nil || !res.Correct {
		t.Fatalf("верный ответ: %+v %v", res, err)
	}
	if res, err := f.answer(aidar, attempt, "әни", "папа"); err != nil || !res.Repeated || !res.Correct {
		t.Fatalf("повторный ответ не меняет счёт: %+v %v", res, err)
	}
	if res, err := f.answer(aidar, attempt, "әти", "мама"); err != nil || res.Correct {
		t.Fatalf("неверный ответ: %+v %v", res, err)
	}

	live, err = f.svc.Live(f.ctx, f.teacher, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	cell := live.Students[0].Cells[ExerciseTaskKey(f.match)]
	if cell.State != CellDone || cell.Correct != 1 || cell.Total != 2 || live.Issues[0].DoneStudents != 1 {
		t.Errorf("упражнение сделано 1/2: %+v, сделали %d", cell, live.Issues[0].DoneStudents)
	}
	if live.Students[1].Cells[TaskText].State != CellNone {
		t.Errorf("Алсу не начинала: %+v", live.Students[1].Cells)
	}

	panel, err := f.svc.StudentPanel(f.ctx, f.teacher, a.ID, aidar.StudentID)
	if err != nil || len(panel.Tasks) != 2 {
		t.Fatalf("панель ученика: %+v %v", panel, err)
	}
	ex := panel.Tasks[1]
	if ex.Cell.State != CellDone || ex.Minutes == nil || len(ex.Questions) != 2 {
		t.Fatalf("упражнение в панели: %+v", ex)
	}
	if ex.Questions[1] != (AnsweredQuestion{Prompt: "әти", Answer: "мама", Correct: false, Expected: "папа"}) {
		t.Errorf("неверный ответ словами и верный рядом: %+v", ex.Questions[1])
	}
	if _, err := f.svc.StudentPanel(f.ctx, f.teacher, a.ID, uuid.New()); httpx.AsError(err).Status != http.StatusNotFound {
		t.Errorf("чужой ученик — 404: %v", err)
	}

	// Завершить работу на уроке и задать оставшееся на дом.
	finished, err := f.svc.FinishIssue(f.ctx, f.teacher, class.Issue.ID)
	if err != nil || finished.Open || finished.Issue.ClosedAt == nil {
		t.Fatalf("работа на уроке завершена: %+v %v", finished, err)
	}
	_, err = f.svc.FinishIssue(f.ctx, f.teacher, class.Issue.ID)
	wantErr(t, err, http.StatusConflict, msgClassDone)
	section, err = f.svc.SectionTasks(f.ctx, aidar, attempt.ID, IssueClass)
	if err != nil || section.Open || section.ClosedReason != ReasonClassFinished {
		t.Errorf("ученик видит «Учитель завершил работу на уроке»: %+v %v", section, err)
	}

	home := f.issue(t, a, IssueHome, dateIn(2), ExerciseTaskKey(f.custom), TaskDialog)
	if !home.Open || home.Issue.DueAt == nil || home.Issue.DueAt.In(msk).Format("15:04:05") != "23:59:59" {
		t.Fatalf("домашка до конца дня: %+v", home)
	}
	_, err = f.svc.FinishIssue(f.ctx, f.teacher, home.Issue.ID)
	wantErr(t, err, http.StatusConflict, msgFinishOnlyClass)
	_, err = f.svc.ExtendIssue(f.ctx, f.teacher, class.Issue.ID, dateIn(3))
	wantErr(t, err, http.StatusConflict, msgExtendOnlyHome)

	lessons, _ = f.svc.MyLessons(f.ctx, aidar, uuid.Nil)
	if l := lessons[0]; l.ClassSection != nil || l.HomeSection == nil || l.HomeSection.Tasks != 2 ||
		l.TasksDone != 2 || l.Finished {
		t.Errorf("«Домашняя работа · 2 задания»: %+v", l)
	}

	// Доделать дома: «свой текст» и разговор — и урок завершён.
	if err := f.svc.TaskDone(f.ctx, aidar, attempt.ID, ExerciseTaskKey(f.custom)); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.TaskDone(f.ctx, aidar, attempt.ID, TaskDialog); err != nil {
		t.Fatal(err)
	}
	state, err := f.svc.AttemptState(f.ctx, aidar, attempt.ID)
	if err != nil || state.FinishedAt == nil || !state.Completed {
		t.Fatalf("все задания сделаны — попытка завершена: %+v %v", state, err)
	}

	card, err := f.svc.StudentCardFor(f.ctx, f.teacher, f.classID, aidar.StudentID)
	if err != nil || len(card.Lessons) != 1 || card.Summary.Assigned != 1 || card.Summary.Passed != 1 {
		t.Fatalf("карточка ученика: %+v %v", card, err)
	}
	h := card.Lessons[0]
	if h.Assignment.ID != a.ID || h.Attempt == nil || h.TasksTotal != 4 || h.TasksDone != 4 || len(h.Issues) != 2 ||
		h.Issues[0].Issue.Kind != IssueClass || h.Issues[0].Tasks[1].State != CardDone ||
		*h.Issues[0].Tasks[1].Correct != 1 || *h.Issues[0].Tasks[1].Total != 2 {
		t.Errorf("история урока с выдачами: %+v", h)
	}

	// Алсу урок не открывала: строка урока есть, попытки и счёта нет.
	alsuCard, err := f.svc.StudentCardFor(f.ctx, f.teacher, f.classID, f.students[1].StudentID)
	if err != nil || len(alsuCard.Lessons) != 1 || alsuCard.Lessons[0].Attempt != nil || alsuCard.Summary.Passed != 0 {
		t.Errorf("урок, к которому ученик не приступал: %+v %v", alsuCard, err)
	}
	// Не ученик этого класса — 404, а не пустая карточка.
	_, err = f.svc.StudentCardFor(f.ctx, f.teacher, f.classID, uuid.New())
	wantErr(t, err, http.StatusNotFound, "Ученик не найден")

	// Таблица результатов: клетка Айдара совпадает со строкой урока в его
	// карточке, Алсу урок не открывала, разговора не было ни у кого.
	an, err := f.svc.ClassAnalyticsFor(f.ctx, f.teacher, f.classID)
	if err != nil || an.Assigned != 1 || an.Expected != 2 || an.Passed != 1 ||
		len(an.Lessons) != 1 || an.Lessons[0].AssignmentID != a.ID || len(an.Students) != 2 {
		t.Fatalf("аналитика класса: %+v %v", an, err)
	}
	rc := an.Students[0].Cells[0]
	if !rc.Started || !rc.Completed || rc.Correct != h.Attempt.CorrectAnswers ||
		rc.Questions != h.Attempt.TotalQuestions || rc.Conversation != nil {
		t.Errorf("клетка Айдара %+v, в карточке %+v", rc, h.Attempt)
	}
	if an.Students[1].Cells[0].Started || an.Students[1].Tasks.Percent() != nil {
		t.Errorf("Алсу урок не открывала: %+v", an.Students[1])
	}
	// Чужой класс — та же 404, что и у карточки.
	_, err = f.svc.ClassAnalyticsFor(f.ctx, iam.Actor{Kind: "user", UserID: uuid.New(), OrgID: f.org}, f.classID)
	wantErr(t, err, http.StatusNotFound, "Класс не найден")

	// Страница урока в аналитике: сводка — те же числа, что в столбце
	// таблицы; чужой учитель урока не видит.
	la, err := f.svc.LessonAnalyticsFor(f.ctx, f.teacher, a.ID)
	if err != nil || la.Students != 2 || la.Passed != an.Passed || la.Tasks != an.Lessons[0].Tasks ||
		la.Conversation != an.Lessons[0].Conversation || la.Class.ID != f.classID {
		t.Fatalf("страница урока: %+v %v", la, err)
	}
	_, err = f.svc.LessonAnalyticsFor(f.ctx, iam.Actor{Kind: "user", UserID: uuid.New(), OrgID: f.org}, a.ID)
	wantErr(t, err, http.StatusNotFound, "Назначение не найдено")

	// Трудные слова: отвечал один Айдар, а трудное слово — от двух
	// ошибившихся. Блок пуст, но это пустой список, а не ошибка.
	hw, err := f.svc.ClassHardWords(f.ctx, f.teacher, f.classID, uuid.Nil)
	if err != nil || hw == nil || len(hw) != 0 {
		t.Fatalf("трудные слова: %+v %v", hw, err)
	}
	// Страница урока (#68) просит только свой урок — и тоже пустой список.
	if hw, err = f.svc.ClassHardWords(f.ctx, f.teacher, f.classID, a.ID); err != nil || hw == nil || len(hw) != 0 {
		t.Fatalf("трудные слова урока: %+v %v", hw, err)
	}
	_, err = f.svc.ClassHardWords(f.ctx, iam.Actor{Kind: "user", UserID: uuid.New(), OrgID: f.org}, f.classID, uuid.Nil)
	wantErr(t, err, http.StatusNotFound, "Класс не найден")

	rows, _ = f.svc.ClassAssignments(f.ctx, f.teacher, f.classID, "")
	if s := rows[0].Summary; s.TasksIssued != 4 || s.ClassOpen || s.Home == nil || !s.Home.Open {
		t.Errorf("строка урока: %+v", s)
	}

	// «Результаты урока». На уроке всё сделано, верных половина — одна звезда
	// из двух. Дома всё сделано, а разговора по целям не было — тоже одна из
	// двух: упражнений с вопросами в домашке нет, звезды «80 %» у неё нет.
	res, err := f.svc.LessonResults(f.ctx, aidar, a.ID)
	if err != nil || res.Attempt == nil || len(res.Issues) != 2 || res.Class.ID != f.classID {
		t.Fatalf("результаты урока: %+v %v", res, err)
	}
	if earned, total := lessonStars(res.Issues); earned != 2 || total != 4 {
		t.Errorf("звёзды урока %d из %d, ожидали 2 из 4", earned, total)
	}
	if got := res.Issues[0]; got.Kind != IssueClass || !got.Closed || got.Pct() != 50 ||
		len(got.Stars) != 2 || !got.Stars[0].On || got.Stars[1].On {
		t.Errorf("выдача на уроке: %+v", got)
	}
	if got := res.Issues[1]; got.Kind != IssueHome || got.Words == nil || got.Words.State != WordsNone ||
		len(got.Stars) != 2 || !got.Stars[0].On || got.Stars[1].Kind != StarWords || got.Stars[1].On {
		t.Errorf("домашка с разговором без разбора: %+v", got)
	}
	lessons, _ = f.svc.MyLessons(f.ctx, aidar, uuid.Nil)
	if s := lessons[0].HomeSection; s == nil || s.Left != 0 || len(s.Stars) != 2 {
		t.Errorf("сделанная домашка со звёздами: %+v", s)
	}
	// Алсу урок не открывала: результаты есть, попытки нет.
	other, err := f.svc.LessonResults(f.ctx, f.students[1], a.ID)
	if err != nil || other.Attempt != nil || len(other.Issues) != 2 || other.Issues[0].Done != 0 {
		t.Errorf("результаты без попытки: %+v %v", other, err)
	}
}

// Каждое задание выдаётся один раз: две вкладки учителя, выдающие одно и
// то же одновременно, не дают двух выдач.
func TestIssueSameTaskConcurrently(t *testing.T) {
	f := newFixture(t)
	a := f.assign(t)

	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.svc.IssueTasks(f.ctx, f.teacher, a.ID,
				IssueInput{Kind: IssueClass, TaskKeys: []string{TaskText, TaskDialog}})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		wantErr(t, err, http.StatusUnprocessableEntity, msgTaskTaken)
	}
	if ok != 1 {
		t.Fatalf("выдач прошло %d, ожидали ровно одну", ok)
	}

	// И без проверки в коде: первичный ключ держит правило сам.
	is := func() Issue {
		return Issue{ID: uuid.New(), OrgID: f.org, AssignmentID: a.ID, Kind: IssueClass,
			OpenedAt: time.Now(), CreatedBy: f.teacher.UserID}
	}
	results := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = f.svc.store.CreateIssue(f.ctx, f.pool, is(), []string{ExerciseTaskKey(f.custom)})
		}()
	}
	wg.Wait()
	ok = 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrTaskTaken):
			t.Fatalf("неожиданная ошибка: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("в обход проверки прошло %d выдач одного задания", ok)
	}
	var rows int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM learning.issues WHERE assignment_id = $1`,
		a.ID).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("выдач в базе %d, ожидали 2: %v", rows, err)
	}
}

// Домашняя работа по уроку одна.
func TestOneHomeIssuePerAssignment(t *testing.T) {
	f := newFixture(t)
	a := f.assign(t)
	f.issue(t, a, IssueHome, dateIn(1), TaskText)
	_, err := f.svc.IssueTasks(f.ctx, f.teacher, a.ID,
		IssueInput{Kind: IssueHome, TaskKeys: []string{TaskDialog}, DueDate: dateIn(1)})
	wantErr(t, err, http.StatusConflict, msgHomeExists)

	// Две домашки одновременно, на разные задания, — проходит одна.
	b := newFixture(t)
	a2 := b.assign(t)
	keys := [][]string{{TaskText}, {TaskDialog}, {ExerciseTaskKey(b.match)}, {ExerciseTaskKey(b.custom)}}
	errs := make([]error, len(keys))
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = b.svc.IssueTasks(b.ctx, b.teacher, a2.ID,
				IssueInput{Kind: IssueHome, TaskKeys: keys[i], DueDate: dateIn(1)})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		wantErr(t, err, http.StatusConflict, msgHomeExists)
	}
	if ok != 1 {
		t.Fatalf("домашек прошло %d", ok)
	}
}

func TestIssueValidation(t *testing.T) {
	f := newFixture(t)
	a := f.assign(t)
	cases := []struct {
		in     IssueInput
		status int
		msg    string
	}{
		{IssueInput{Kind: IssueClass}, 422, msgNoTasks},
		{IssueInput{Kind: IssueClass, TaskKeys: []string{"ex:" + uuid.NewString()}}, 422, msgNoSuchTask},
		{IssueInput{Kind: IssueHome, TaskKeys: []string{TaskText}}, 422, msgDueRequired},
		{IssueInput{Kind: IssueHome, TaskKeys: []string{TaskText}, DueDate: dateIn(-1)}, 422, msgDueBeforeToday},
		{IssueInput{Kind: IssueHome, TaskKeys: []string{TaskText}, DueDate: "03.10"}, 422, msgDueFormat},
		{IssueInput{Kind: "later", TaskKeys: []string{TaskText}}, 422, ""},
	}
	for _, c := range cases {
		_, err := f.svc.IssueTasks(f.ctx, f.teacher, a.ID, c.in)
		wantErr(t, err, c.status, c.msg)
	}
	// Срок сегодня — можно: до конца сегодняшнего дня.
	f.issue(t, a, IssueHome, dateIn(0), TaskText)

	// Чужой учитель не видит назначения вовсе.
	stranger := iam.Actor{Kind: "user", UserID: uuid.New(), OrgID: f.org}
	_, err := f.svc.IssueTasks(f.ctx, stranger, a.ID, IssueInput{Kind: IssueClass, TaskKeys: []string{TaskDialog}})
	wantErr(t, err, http.StatusNotFound, "")
	_, err = f.svc.Live(f.ctx, stranger, a.ID)
	wantErr(t, err, http.StatusNotFound, "")

	// Чужой ученик не видит чужую попытку.
	attempt := f.start(t, a, f.students[0])
	_, err = f.svc.SectionTasks(f.ctx, f.students[1], attempt.ID, "")
	wantErr(t, err, http.StatusNotFound, "")
	_, err = f.svc.SectionTasks(f.ctx, f.students[0], attempt.ID, "later")
	wantErr(t, err, http.StatusBadRequest, "")
	err = f.svc.OpenTask(f.ctx, f.students[0], attempt.ID, "нет-такого")
	wantErr(t, err, http.StatusNotFound, "")
}

// Отменить выдачу можно, только пока её никто не начал.
func TestCancelRefusedAfterStudentStarted(t *testing.T) {
	f := newFixture(t)
	a := f.assign(t)
	started := f.issue(t, a, IssueClass, "", TaskText)
	idle := f.issue(t, a, IssueClass, "", TaskDialog)

	attempt := f.start(t, a, f.students[0])
	if err := f.svc.OpenTask(f.ctx, f.students[0], attempt.ID, TaskText); err != nil {
		t.Fatal(err)
	}
	err := f.svc.CancelIssue(f.ctx, f.teacher, started.Issue.ID)
	wantErr(t, err, http.StatusConflict, msgIssueStarted)

	if err := f.svc.CancelIssue(f.ctx, f.teacher, idle.Issue.ID); err != nil {
		t.Fatalf("никем не начатая выдача отменяется: %v", err)
	}
	// Задания отменённой выдачи снова «не выданы» и выдаются заново.
	err = f.svc.OpenTask(f.ctx, f.students[0], attempt.ID, TaskDialog)
	wantErr(t, err, http.StatusConflict, msgNotIssued)
	again := f.issue(t, a, IssueClass, "", TaskDialog)
	if again.Started {
		t.Error("новая выдача не начата")
	}
	err = f.svc.CancelIssue(f.ctx, f.teacher, idle.Issue.ID)
	wantErr(t, err, http.StatusNotFound, "")
}

// Отмена и открытие задания в одну секунду: либо отмена увидит начатое и
// откажет, либо открытие упрётся в отменённую выдачу. Вдвоём не проходят.
func TestCancelRacesWithOpen(t *testing.T) {
	f := newFixture(t)
	a := f.assign(t)
	student := f.students[0]
	// Урок открывается ученику, когда по нему выдано хоть что-то.
	f.issue(t, a, IssueClass, "", TaskText)
	attempt := f.start(t, a, student)
	for range 15 {
		is := f.issue(t, a, IssueClass, "", TaskDialog)
		var cancelErr, openErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); cancelErr = f.svc.CancelIssue(f.ctx, f.teacher, is.Issue.ID) }()
		go func() { defer wg.Done(); openErr = f.svc.OpenTask(f.ctx, student, attempt.ID, TaskDialog) }()
		wg.Wait()

		var orphans int
		if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM learning.task_progress
			WHERE attempt_id = $1 AND task_key = 'dialog' AND issue_id IS NULL`, attempt.ID).Scan(&orphans); err != nil {
			t.Fatal(err)
		}
		if orphans != 0 {
			t.Fatal("прогресс остался без выдачи")
		}
		switch {
		case cancelErr == nil && openErr == nil:
			t.Fatal("выдачу отменили, а задание открылось")
		case cancelErr == nil:
			wantErr(t, openErr, http.StatusConflict, msgNotIssued)
		default:
			wantErr(t, cancelErr, http.StatusConflict, msgIssueStarted)
			// Выдача начата — дальше её не отменить; для следующего круга
			// убираем её вместе с прогрессом руками.
			if _, err := f.pool.Exec(f.ctx, `DELETE FROM learning.task_progress WHERE attempt_id = $1`, attempt.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.CancelIssue(f.ctx, f.teacher, is.Issue.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Ответ принимается, только пока задание открыто, — с причиной словами.
func TestAnswerRefusedWhenTaskClosed(t *testing.T) {
	f := newFixture(t)
	a := f.assign(t)
	student := f.students[0]
	f.issue(t, a, IssueClass, "", TaskText)
	attempt := f.start(t, a, student)

	_, err := f.answer(student, attempt, "әни", "мама")
	wantErr(t, err, http.StatusConflict, msgNotIssued)

	class2 := f.issue(t, a, IssueClass, "", ExerciseTaskKey(f.match))
	if _, err := f.answer(student, attempt, "әни", "мама"); err != nil {
		t.Fatalf("открытое задание принимает ответ: %v", err)
	}
	if _, err := f.svc.FinishIssue(f.ctx, f.teacher, class2.Issue.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.answer(student, attempt, "әти", "папа")
	wantErr(t, err, http.StatusConflict, msgClassFinished)
	err = f.svc.OpenTask(f.ctx, student, attempt.ID, ExerciseTaskKey(f.match))
	wantErr(t, err, http.StatusConflict, msgClassFinished)

	// Ответ, данный до завершения, сохранился.
	state, _ := f.svc.AttemptState(f.ctx, student, attempt.ID)
	if len(state.Answers) != 1 {
		t.Errorf("ответы до завершения сохраняются: %+v", state.Answers)
	}
}

// Продление после срока снова открывает задания ученикам.
func TestExtendAfterExpiryReopens(t *testing.T) {
	f := newFixture(t)
	a := f.assign(t)
	student := f.students[0]
	home := f.issue(t, a, IssueHome, dateIn(1), TaskText)
	attempt := f.start(t, a, student)

	// Срок прошёл три дня назад.
	if _, err := f.pool.Exec(f.ctx, `UPDATE learning.issues SET due_at = now() - interval '3 days'
		WHERE id = $1`, home.Issue.ID); err != nil {
		t.Fatal(err)
	}
	err := f.svc.OpenTask(f.ctx, student, attempt.ID, TaskText)
	wantErr(t, err, http.StatusConflict, msgHomeExpired)
	section, _ := f.svc.SectionTasks(f.ctx, student, attempt.ID, IssueHome)
	if section.Open || section.ClosedReason != ReasonHomeExpired {
		t.Errorf("срок домашки прошёл: %+v", section)
	}

	_, err = f.svc.ExtendIssue(f.ctx, f.teacher, home.Issue.ID, dateIn(-1))
	wantErr(t, err, http.StatusUnprocessableEntity, msgDueBeforeToday)
	extended, err := f.svc.ExtendIssue(f.ctx, f.teacher, home.Issue.ID, dateIn(3))
	if err != nil || !extended.Open {
		t.Fatalf("продлённая домашка открыта: %+v %v", extended, err)
	}
	if err := f.svc.OpenTask(f.ctx, student, attempt.ID, TaskText); err != nil {
		t.Fatalf("задание снова открыто ученику: %v", err)
	}
	_, err = f.svc.ExtendIssue(f.ctx, f.teacher, home.Issue.ID, dateIn(2))
	wantErr(t, err, http.StatusUnprocessableEntity, msgDueNotLater)
}

// Урок назначается классу один раз — любой своей версией.
func TestAssignSameLessonOnce(t *testing.T) {
	f := newFixture(t)
	second := uuid.New()
	f.lessons.versions[second] = VersionInfo{ID: second, LessonID: f.lessonID, Version: 2, Status: "published"}
	f.lessons.tasks[second] = f.lessons.tasks[f.versionID]
	a := f.assign(t)

	_, err := f.svc.Assign(f.ctx, f.teacher, AssignInput{LessonVersionID: second, ClassID: f.classID})
	wantErr(t, err, http.StatusConflict, "Этот урок уже назначен классу")
	_, err = f.svc.Assign(f.ctx, f.teacher, AssignInput{LessonVersionID: f.versionID, ClassID: f.classID})
	wantErr(t, err, http.StatusConflict, "Этот урок уже назначен классу")

	draft := uuid.New()
	f.lessons.versions[draft] = VersionInfo{ID: draft, LessonID: uuid.New(), Status: "draft"}
	_, err = f.svc.Assign(f.ctx, f.teacher, AssignInput{LessonVersionID: draft, ClassID: f.classID})
	wantErr(t, err, http.StatusConflict, "")
	ready := uuid.New()
	f.lessons.versions[ready] = VersionInfo{ID: ready, LessonID: uuid.New(), Status: "ready"}
	_, err = f.svc.Assign(f.ctx, f.teacher, AssignInput{LessonVersionID: ready, ClassID: f.classID})
	wantErr(t, err, http.StatusConflict, "")

	// Ученик успел открыть урок — снятие закрывает и назначение, и выдачи.
	f.issue(t, a, IssueHome, dateIn(1), TaskText)
	f.start(t, a, f.students[0])
	if res, err := f.svc.Unassign(f.ctx, f.teacher, a.ID); err != nil || res != "closed" {
		t.Fatalf("снять урок с класса: %s %v", res, err)
	}
	live, err := f.svc.Live(f.ctx, f.teacher, a.ID)
	if err != nil || live.Issues[0].Open || live.Issues[0].Issue.ClosedAt == nil {
		t.Errorf("снятый урок — выдачи закрыты: %+v %v", live.Issues, err)
	}
	lessons, _ := f.svc.MyLessons(f.ctx, f.students[0], uuid.Nil)
	if len(lessons) != 1 || !lessons[0].Finished || lessons[0].HomeSection != nil {
		t.Errorf("снятый урок у ученика — в «Завершённых»: %+v", lessons)
	}
	_, err = f.svc.IssueTasks(f.ctx, f.teacher, a.ID, IssueInput{Kind: IssueClass, TaskKeys: []string{TaskDialog}})
	wantErr(t, err, http.StatusConflict, msgLessonRemoved)

	// После снятия урок можно назначить снова, другой версией.
	if _, err := f.svc.Assign(f.ctx, f.teacher, AssignInput{LessonVersionID: second, ClassID: f.classID}); err != nil {
		t.Fatalf("снятый урок назначается снова: %v", err)
	}
}

// «В сети» пишется не на каждый опрос: строка обновляется не чаще раза в 20
// секунд.
func TestPresenceIsThrottled(t *testing.T) {
	f := newFixture(t)
	student := f.students[0]
	seen := func() time.Time {
		var at time.Time
		if err := f.pool.QueryRow(f.ctx, `SELECT seen_at FROM learning.presence WHERE student_id = $1`,
			student.StudentID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	f.svc.SeenStudent(f.ctx, student)
	first := seen()
	f.svc.SeenStudent(f.ctx, student)
	if !seen().Equal(first) {
		t.Error("отметка младше 20 секунд не переписывается")
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE learning.presence SET seen_at = now() - interval '30 seconds'
		WHERE student_id = $1`, student.StudentID); err != nil {
		t.Fatal(err)
	}
	stale := seen()
	f.svc.SeenStudent(f.ctx, student)
	if !seen().After(stale) {
		t.Error("отметка старше 20 секунд обновляется")
	}
	f.svc.SeenStudent(f.ctx, f.teacher) // учитель — не ученик, отметки нет

	a := f.assign(t)
	f.issue(t, a, IssueClass, "", TaskText)
	live, err := f.svc.Live(f.ctx, f.teacher, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range live.Students {
		if st.Member.ID == student.StudentID && (!st.Online || st.LastSeenAt == nil) {
			t.Errorf("ученик в сети: %+v", st)
		}
		if st.Member.ID != student.StudentID && (st.Online || st.LastSeenAt != nil) {
			t.Errorf("ученик не заходил: %+v", st)
		}
	}
}

func isolatedMigrationPool(t *testing.T) *pg.Pool {
	t.Helper()
	admin := testPool(t)
	name := "native_learning_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	ctx := context.Background()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+quoted); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(os.Getenv("NATIVE_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	rawPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool := &pg.Pool{Pool: rawPool}
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
