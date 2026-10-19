package learning

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Правила «Урока в классе» — это то, что учитель видит в таблице и чему
// верит: «не сделал» у ученика, который сделал, или «открыто» у закрытого
// задания — ошибка не в логах, а в разговоре учителя с родителями.

var msk = mustZone("Europe/Moscow")

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ptr[T any](v T) *T { return &v }

// testLesson — урок «Гаилә» в миниатюре: цели, текст, два упражнения,
// «свой текст» и диалог.
func testLesson() ([]LessonTask, uuid.UUID, uuid.UUID) {
	ex1, ex2 := uuid.New(), uuid.New()
	custom := uuid.New()
	return []LessonTask{
		{Key: TaskObjectives, N: 1, Kind: TaskObjectives, Title: "Цели урока"},
		{Key: TaskText, N: 2, Kind: TaskText, Title: "Текст «Гаилә»"},
		{Key: ExerciseTaskKey(ex1), N: 3, Kind: TaskExercise, Type: "matching", Title: "Подбери пару",
			Questions: 2, ExerciseID: ex1, ItemIDs: []string{"әни", "әти"}},
		{Key: ExerciseTaskKey(ex2), N: 4, Kind: TaskExercise, Type: "gap_fill", Title: "Вставь слово",
			Questions: 3, ExerciseID: ex2, ItemIDs: []string{"q1", "q2", "q3"}},
		{Key: ExerciseTaskKey(custom), N: 5, Kind: TaskExercise, Type: "custom", Title: "Свой текст",
			ExerciseID: custom},
		{Key: TaskDialog, N: 6, Kind: TaskDialog, Title: "Разговор с аватаром"},
	}, ex1, ex2
}

func issue(kind string, opened time.Time, due *time.Time, keys ...string) Issue {
	return Issue{ID: uuid.New(), Kind: kind, OpenedAt: opened, CreatedAt: opened, DueAt: due, TaskKeys: keys}
}

// ---------------------------------------------------------------------------
// Открытость выдачи и сроки
// ---------------------------------------------------------------------------

func TestIssueOpen(t *testing.T) {
	now := at("2026-09-29T10:30:00+03:00")
	before, after := now.Add(-time.Hour), now.Add(time.Hour)
	cases := []struct {
		name string
		is   Issue
		want bool
	}{
		{"на уроке до конца дня", Issue{OpenedAt: before, DueAt: &after}, true},
		{"учитель нажал «Завершить»", Issue{OpenedAt: before, DueAt: &after, ClosedAt: &before}, false},
		{"срок прошёл", Issue{OpenedAt: before, DueAt: &before}, false},
		{"ровно в момент срока уже закрыта", Issue{OpenedAt: before, DueAt: &now}, false},
		{"перенесённое назначение без срока", Issue{OpenedAt: before, Legacy: true}, true},
		{"перенесённое назначение откроется позже", Issue{OpenedAt: after, Legacy: true}, false},
	}
	for _, c := range cases {
		if got := c.is.Open(now); got != c.want {
			t.Errorf("%s: open=%v, ожидали %v", c.name, got, c.want)
		}
	}
}

// Сутки — школьные: в контейнере UTC, и «до конца дня» по Гринвичу закрыло
// бы урок в три часа ночи по Москве.
func TestDueDatesFollowSchoolZone(t *testing.T) {
	// 10:15 по Москве — урок идёт.
	if got := classDueAt(at("2026-09-29T07:15:00Z"), msk); !got.Equal(at("2026-09-29T23:59:59+03:00")) {
		t.Errorf("выдача на уроке до %v, ожидали 23:59:59 того же дня по Москве", got)
	}
	// 01:30 по Москве 30-го — по UTC ещё 29-е, но школьный день уже 30-й.
	if got := classDueAt(at("2026-09-29T22:30:00Z"), msk); !got.Equal(at("2026-09-30T23:59:59+03:00")) {
		t.Errorf("после полуночи по Москве срок %v, ожидали конец 30.09", got)
	}

	day, ok := parseDueDate("2026-10-03", msk)
	if !ok {
		t.Fatal("дата срока не разобралась")
	}
	if got := endOfDay(day, msk); got.Format(time.RFC3339) != "2026-10-03T23:59:59+03:00" {
		t.Errorf("срок домашки %s, ожидали 2026-10-03T23:59:59+03:00", got.Format(time.RFC3339))
	}
	for _, bad := range []string{"", "03.10.2026", "2026-13-01", "2026-10-03T10:00:00Z"} {
		if _, ok := parseDueDate(bad, msk); ok {
			t.Errorf("%q не должна разбираться как дата срока", bad)
		}
	}

	now := at("2026-09-29T23:30:00+03:00")
	if beforeToday(mustDay("2026-09-29"), now, msk) {
		t.Error("сегодняшний день — допустимый срок")
	}
	if !beforeToday(mustDay("2026-09-28"), now, msk) {
		t.Error("вчерашний день — недопустимый срок")
	}
}

func mustDay(s string) time.Time {
	d, ok := parseDueDate(s, msk)
	if !ok {
		panic(s)
	}
	return d
}

// ---------------------------------------------------------------------------
// Доска урока: что выдано и где
// ---------------------------------------------------------------------------

// Перенесённое назначение — «*», все задания урока. Учитель видит их как
// обычную выдачу на дом со списком заданий, и ничего выдать повторно нельзя.
func TestLegacyIssueCoversWholeLesson(t *testing.T) {
	tasks, _, _ := testLesson()
	now := at("2026-09-29T10:00:00+03:00")
	legacy := issue(IssueHome, now.Add(-48*time.Hour), nil, allTasksKey)
	legacy.Legacy = true
	b := newBoard(tasks, []Issue{legacy}, now, false)

	if got := b.issuedCount(); got != len(tasks) {
		t.Fatalf("выдано %d из %d, ожидали всё", got, len(tasks))
	}
	keys := b.issueKeys(0)
	if len(keys) != len(tasks) || keys[0] != TaskObjectives || keys[len(keys)-1] != TaskDialog {
		t.Errorf("задания перенесённой выдачи %v, ожидали весь урок по порядку", keys)
	}
	if msg := checkTaskKeys(b, []string{TaskText}); msg != msgTaskTaken {
		t.Errorf("повторная выдача из перенесённого урока: %q", msg)
	}
	if !b.open[0] {
		t.Error("перенесённая выдача без срока открыта")
	}
}

// Ключи выдачи — в порядке урока, а не в том, в каком их отметил учитель
// или вернула база.
func TestIssueKeysFollowLessonOrder(t *testing.T) {
	tasks, ex1, _ := testLesson()
	now := at("2026-09-29T10:00:00+03:00")
	is := issue(IssueClass, now, nil, TaskDialog, ExerciseTaskKey(ex1), TaskText, "ex:удалённое")
	b := newBoard(tasks, []Issue{is}, now, false)
	want := []string{TaskText, ExerciseTaskKey(ex1), TaskDialog}
	if got := b.issueKeys(0); !slices.Equal(got, want) {
		t.Errorf("ключи %v, ожидали %v", got, want)
	}
	if b.issuedCount() != 3 {
		t.Errorf("ключ, которого нет в уроке, не считается выданным: %d", b.issuedCount())
	}
}

// Выдачи — по времени, старые первыми: так их показывает блок «Выдачи».
func TestIssuesSortedByOpening(t *testing.T) {
	tasks, _, _ := testLesson()
	now := at("2026-10-01T10:00:00+03:00")
	late := issue(IssueHome, now.Add(-time.Hour), ptr(now.Add(time.Hour)), TaskDialog)
	early := issue(IssueClass, now.Add(-48*time.Hour), ptr(now.Add(-24*time.Hour)), TaskText)
	b := newBoard(tasks, []Issue{late, early}, now, false)
	if b.issues[0].ID != early.ID || b.issues[1].ID != late.ID {
		t.Error("выдачи должны идти по времени выдачи")
	}
}

func TestCheckTaskKeys(t *testing.T) {
	tasks, ex1, _ := testLesson()
	now := at("2026-09-29T10:00:00+03:00")
	b := newBoard(tasks, []Issue{issue(IssueClass, now, nil, TaskText)}, now, false)
	cases := []struct {
		keys []string
		want string
	}{
		{nil, msgNoTasks},
		{[]string{"нет-такого"}, msgNoSuchTask},
		{[]string{ExerciseTaskKey(ex1), TaskText}, msgTaskTaken},
		{[]string{ExerciseTaskKey(ex1), TaskDialog}, ""},
	}
	for _, c := range cases {
		if got := checkTaskKeys(b, c.keys); got != c.want {
			t.Errorf("%v: %q, ожидали %q", c.keys, got, c.want)
		}
	}
	if got := uniqueKeys([]string{" text ", "text", "", "dialog"}); !slices.Equal(got, []string{"text", "dialog"}) {
		t.Errorf("повторы и пустые ключи: %v", got)
	}
}

// Отказ ученику называет причину: «учитель завершил» и «срок прошёл» — это
// разные разговоры с учителем.
func TestGateExplainsWhyTaskIsClosed(t *testing.T) {
	tasks, ex1, ex2 := testLesson()
	now := at("2026-10-04T10:00:00+03:00")
	finished := issue(IssueClass, now.Add(-time.Hour), ptr(now.Add(time.Hour)), TaskText)
	finished.ClosedAt = ptr(now.Add(-time.Minute))
	expired := issue(IssueHome, now.Add(-72*time.Hour), ptr(now.Add(-time.Hour)), ExerciseTaskKey(ex1))
	open := issue(IssueClass, now.Add(-time.Minute), ptr(now.Add(time.Hour)), TaskDialog)
	b := newBoard(tasks, []Issue{finished, expired, open}, now, false)

	cases := []struct {
		key  string
		want string
	}{
		{TaskText, msgClassFinished},
		{ExerciseTaskKey(ex1), msgHomeExpired},
		{ExerciseTaskKey(ex2), msgNotIssued},
		{TaskDialog, ""},
	}
	for _, c := range cases {
		err := b.gate(c.key)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: открытое задание закрыто: %v", c.key, err)
		case c.want != "" && (err == nil || httpx.AsError(err).Message != c.want ||
			httpx.AsError(err).Status != http.StatusConflict):
			t.Errorf("%s: %v, ожидали 409 %q", c.key, err, c.want)
		}
	}

	// Урок сняли с класса — закрыто всё, что бы ни было записано в выдачах.
	removed := newBoard(tasks, []Issue{open}, now, true)
	if removed.anyOpen() || removed.gate(TaskDialog) == nil || !removed.allClosed() {
		t.Error("у снятого урока не может быть открытых заданий")
	}
}

// Перенесённое назначение с открытием в будущем ещё не открыто, но и не
// закрыто: задания впереди, а не «не сделаны», и урок не «завершён».
func TestFutureLegacyIssueIsNeitherOpenNorClosed(t *testing.T) {
	tasks, ex1, _ := testLesson()
	now := at("2026-10-01T10:00:00+03:00")
	future := issue(IssueHome, now.Add(48*time.Hour), nil, allTasksKey)
	future.Legacy = true
	b := newBoard(tasks, []Issue{future}, now, false)

	if b.anyOpen() || b.allClosed() {
		t.Fatalf("открыта=%v, закрыта=%v — ожидали ни то ни другое", b.anyOpen(), b.allClosed())
	}
	if err := b.gate(TaskText); err == nil || httpx.AsError(err).Message != msgNotIssued {
		t.Errorf("задание ещё впереди, а не «срок прошёл»: %v", err)
	}
	match := tasks[2]
	if c := cellOf(match, newWork(nil, nil).status(match), b.closed[0]); c.State != CellNone {
		t.Errorf("клетка будущей выдачи — «—», а не «не сделал»: %s", c.State)
	}
	if got := studentLesson(Assignment{State: StateScheduled}, tasks, []Issue{future}, nil, nil, ConversationWords{}, time.UTC, now); got.Finished || got.HomeSection != nil {
		t.Errorf("запланированный урок не в «Завершённых» и не в домашке: %+v", got)
	}
	attempt := Attempt{Answers: answersFor(ex1, map[string]bool{})}
	if row := cardRow(attempt, Assignment{State: StateScheduled}, tasks, []Issue{future}, nil, now); row.Issues[0].Tasks[0].State != CardPending {
		t.Errorf("в карточке — «пока не сделано»: %+v", row.Issues[0].Tasks[0])
	}
}

// ---------------------------------------------------------------------------
// Работа ученика и клетки таблицы
// ---------------------------------------------------------------------------

func answersFor(ex uuid.UUID, items map[string]bool) map[string]GivenAnswer {
	out := map[string]GivenAnswer{}
	for id, ok := range items {
		out[ex.String()+":"+id] = GivenAnswer{Answer: "x", Correct: ok}
	}
	return out
}

func TestStatusCountsOnlyRealQuestions(t *testing.T) {
	tasks, ex1, _ := testLesson()
	match := tasks[2]

	// Ответ на несуществующий вопрос упражнение не закрывает: иначе его
	// «сделать» можно, отправив два ответа на что угодно.
	w := newWork(&Attempt{Answers: answersFor(ex1, map[string]bool{"x1": true, "x2": true})}, nil)
	if st := w.status(match); st.Done || st.Answered != 0 {
		t.Errorf("ответы на чужие вопросы засчитаны: %+v", st)
	}

	w = newWork(&Attempt{Answers: answersFor(ex1, map[string]bool{"әни": false})}, nil)
	st := w.status(match)
	if st.Done || !st.Opened || st.Answered != 1 || st.Correct != 0 {
		t.Errorf("начатое упражнение: %+v", st)
	}

	w = newWork(&Attempt{Answers: answersFor(ex1, map[string]bool{"әни": true, "әти": true})}, nil)
	if st := w.status(match); !st.Done || st.Correct != 2 {
		t.Errorf("отвечены все вопросы — упражнение сделано: %+v", st)
	}
}

func TestCellStatesFollowContract(t *testing.T) {
	tasks, ex1, _ := testLesson()
	match, text, custom := tasks[2], tasks[1], tasks[4]
	opened := at("2026-09-29T10:20:00+03:00")
	done := at("2026-09-29T10:24:10+03:00")

	cases := []struct {
		name  string
		task  LessonTask
		work  studentWork
		open  bool
		state string
	}{
		{"не открывал, выдача открыта", match, newWork(nil, nil), true, CellNone},
		{"не открывал, выдача закрыта", match, newWork(nil, nil), false, CellMiss},
		{"упражнение открыто", match, newWork(nil, []TaskProgress{{TaskKey: match.Key, OpenedAt: opened}}), true, CellDoing},
		{"упражнение начато", match, newWork(&Attempt{Answers: answersFor(ex1, map[string]bool{"әни": true})}, nil), true, CellDoing},
		{"начато, но выдачу завершили", match, newWork(&Attempt{Answers: answersFor(ex1, map[string]bool{"әни": true})}, nil), false, CellMiss},
		{"упражнение сделано", match, newWork(&Attempt{Answers: answersFor(ex1, map[string]bool{"әни": true, "әти": false})}, nil), false, CellDone},
		{"текст открыт", text, newWork(nil, []TaskProgress{{TaskKey: TaskText, OpenedAt: opened}}), true, CellOpened},
		{"свой текст открыт", custom, newWork(nil, []TaskProgress{{TaskKey: custom.Key, OpenedAt: opened}}), true, CellOpened},
		{"текст сделан", text, newWork(nil, []TaskProgress{{TaskKey: TaskText, OpenedAt: opened, DoneAt: &done}}), true, CellDone},
		{"текст открыт, выдача закрыта", text, newWork(nil, []TaskProgress{{TaskKey: TaskText, OpenedAt: opened}}), false, CellMiss},
	}
	for _, c := range cases {
		cell := cellOf(c.task, c.work.status(c.task), !c.open)
		if cell.State != c.state {
			t.Errorf("%s: %q, ожидали %q", c.name, cell.State, c.state)
		}
		if cell.Total != c.task.Questions {
			t.Errorf("%s: всего %d, ожидали %d", c.name, cell.Total, c.task.Questions)
		}
	}

	// «2/5» в жёлтой рамке — отвечено и верно считаются по попытке.
	cell := cellOf(match, newWork(&Attempt{Answers: answersFor(ex1, map[string]bool{"әни": false})}, nil).status(match), false)
	if cell.Answered != 1 || cell.Correct != 0 || cell.Total != 2 {
		t.Errorf("клетка начатого упражнения: %+v", cell)
	}
}

func TestMinutesRoundUp(t *testing.T) {
	opened := at("2026-09-29T10:20:00+03:00")
	cases := []struct {
		done time.Duration
		want *int
	}{
		{4*time.Minute + 10*time.Second, ptr(5)},
		{3 * time.Minute, ptr(3)},
		{20 * time.Second, ptr(1)},
	}
	for _, c := range cases {
		done := opened.Add(c.done)
		st := taskStatus{Done: true, OpenedAt: &opened, DoneAt: &done}
		if got := st.minutes(); got == nil || *got != *c.want {
			t.Errorf("%v: %v минут, ожидали %d", c.done, got, *c.want)
		}
	}
	if (taskStatus{OpenedAt: &opened}).minutes() != nil {
		t.Error("у несделанного задания минут нет")
	}
	// Задание сделано до выдачи по заданиям: времени задания нет, выдумывать
	// его из времени урока целиком нельзя.
	if (taskStatus{Done: true}).minutes() != nil {
		t.Error("без собственного времени минут нет")
	}
}

// Урок, пройденный до выката, остаётся пройденным: «*» с отметкой «сделано»
// закрывает все задания, и ученику не пишут «осталось 5 заданий».
func TestLegacyProgressMarker(t *testing.T) {
	tasks, _, _ := testLesson()
	started := at("2026-09-10T10:00:00+03:00")
	finished := at("2026-09-10T10:30:00+03:00")

	done := newWork(&Attempt{}, []TaskProgress{{TaskKey: allTasksKey, OpenedAt: started, DoneAt: &finished}})
	if !done.allDone(tasks) {
		t.Error("пройденный до выката урок — все задания сделаны")
	}
	for _, task := range tasks {
		if done.status(task).minutes() != nil {
			t.Errorf("%s: минуты урока целиком не приписываются заданию", task.Key)
		}
	}

	begun := newWork(&Attempt{}, []TaskProgress{{TaskKey: allTasksKey, OpenedAt: started}})
	if begun.allDone(tasks) {
		t.Error("начатый до выката урок не сделан")
	}
	if st := begun.status(tasks[1]); !st.Opened || st.Done {
		t.Errorf("начатый урок — задания открыты, но не сделаны: %+v", st)
	}
}

// ---------------------------------------------------------------------------
// «Мои занятия» и раздел прохождения
// ---------------------------------------------------------------------------

func TestStudentLessonSections(t *testing.T) {
	tasks, ex1, ex2 := testLesson()
	now := at("2026-09-29T10:30:00+03:00")
	a := Assignment{ID: uuid.New(), State: StateActive}
	endOfToday := classDueAt(now, msk)
	homeDue := at("2026-10-03T23:59:59+03:00")

	first := issue(IssueClass, now.Add(-15*time.Minute), &endOfToday, TaskText, ExerciseTaskKey(ex1))
	second := issue(IssueClass, now.Add(-5*time.Minute), &endOfToday, TaskObjectives)
	home := issue(IssueHome, now.Add(-time.Minute), &homeDue, ExerciseTaskKey(ex2), TaskDialog)

	doneAt := now.Add(-10 * time.Minute)
	attempt := &Attempt{ID: uuid.New(), Answers: answersFor(ex1, map[string]bool{"әни": true})}
	progress := []TaskProgress{{TaskKey: TaskText, OpenedAt: now.Add(-14 * time.Minute), DoneAt: &doneAt}}

	got := studentLesson(a, tasks, []Issue{first, second, home}, attempt, progress, ConversationWords{}, time.UTC, now)
	if got.TasksTotal != 6 || got.TasksDone != 1 {
		t.Errorf("заданий %d, сделано %d; ожидали 6 и 1", got.TasksTotal, got.TasksDone)
	}
	// Две открытые выдачи на уроке — один раздел. Цели урока — вводный
	// экран, не задание: в разделе два задания, осталось одно.
	if s := got.ClassSection; s == nil || s.Tasks != 2 || s.Left != 1 || !s.Begun {
		t.Errorf("раздел «на уроке»: %+v", s)
	}
	if s := got.HomeSection; s == nil || s.Tasks != 2 || s.Left != 2 || s.Begun || s.DueAt == nil || !s.DueAt.Equal(homeDue) {
		t.Errorf("раздел «на дом»: %+v", s)
	}
	if got.Finished {
		t.Error("выдано не всё — урок не завершён")
	}

	// Завершили работу на уроке и срок домашки прошёл.
	first.ClosedAt, second.ClosedAt = &now, &now
	later := homeDue.Add(time.Hour)
	got = studentLesson(a, tasks, []Issue{first, second, home}, attempt, progress, ConversationWords{}, time.UTC, later)
	if got.ClassSection != nil || got.HomeSection != nil {
		t.Errorf("закрытые разделы не показываются: %+v / %+v", got.ClassSection, got.HomeSection)
	}
	if got.Finished {
		t.Error("невыданное «свой текст» — урок ещё не в «Завершённых»")
	}

	// Выдано всё, и всё закрыто — «Завершённые».
	last := issue(IssueClass, later.Add(-time.Hour), ptr(later.Add(-time.Minute)), ExerciseTaskKey(tasks[4].ExerciseID))
	got = studentLesson(a, tasks, []Issue{first, second, home, last}, attempt, progress, ConversationWords{}, time.UTC, later)
	if !got.Finished {
		t.Error("все задания выданы и все выдачи закрыты — урок завершён")
	}

	// Снятый с класса урок больше ничего не принесёт — тоже «Завершённые».
	removed := a
	removed.State = StateClosed
	if got := studentLesson(removed, tasks, []Issue{first}, attempt, progress, ConversationWords{}, time.UTC, now); !got.Finished {
		t.Error("снятый урок завершён")
	}
}

func TestSectionTasksAndClosedReasons(t *testing.T) {
	tasks, ex1, ex2 := testLesson()
	now := at("2026-09-29T10:30:00+03:00")
	endOfToday := classDueAt(now, msk)
	homeDue := at("2026-10-03T23:59:59+03:00")
	class := issue(IssueClass, now.Add(-time.Minute), &endOfToday, ExerciseTaskKey(ex1), TaskText)
	home := issue(IssueHome, now.Add(-time.Minute), &homeDue, ExerciseTaskKey(ex2))
	w := newWork(&Attempt{Answers: answersFor(ex1, map[string]bool{"әни": true})}, nil)

	got := buildSection(IssueClass, newBoard(tasks, []Issue{class, home}, now, false), w, false, now)
	if !got.Open || got.ClosedReason != "" || got.DueAt == nil || !got.DueAt.Equal(endOfToday) {
		t.Fatalf("открытый раздел на уроке: %+v", got)
	}
	if len(got.Tasks) != 2 || got.Tasks[0].Task.Key != TaskText || got.Tasks[1].Task.Key != ExerciseTaskKey(ex1) {
		t.Errorf("задания раздела по порядку урока: %+v", got.Tasks)
	}
	if got.Tasks[1].Answered != 1 || got.Tasks[1].Done {
		t.Errorf("прогресс упражнения в разделе: %+v", got.Tasks[1])
	}

	all := buildSection("", newBoard(tasks, []Issue{class, home}, now, false), w, false, now)
	if len(all.Tasks) != 3 || all.ClosedReason != "" {
		t.Errorf("без раздела — все открытые задания: %+v", all)
	}

	class.ClosedAt = &now
	got = buildSection(IssueClass, newBoard(tasks, []Issue{class, home}, now, false), w, false, now)
	if got.Open || got.ClosedReason != ReasonClassFinished || len(got.Tasks) != 0 {
		t.Errorf("учитель завершил работу на уроке: %+v", got)
	}

	later := homeDue.Add(time.Second)
	got = buildSection(IssueHome, newBoard(tasks, []Issue{class, home}, later, false), w, false, later)
	if got.Open || got.ClosedReason != ReasonHomeExpired || got.DueAt == nil {
		t.Errorf("срок домашки прошёл: %+v", got)
	}

	// Раздела не было вовсе — причина не называется.
	got = buildSection(IssueHome, newBoard(tasks, []Issue{class}, now, false), w, false, now)
	if got.Open || got.ClosedReason != "" {
		t.Errorf("домашки не было: %+v", got)
	}
	// Урок сняли с класса — это не «учитель завершил урок».
	got = buildSection(IssueClass, newBoard(tasks, []Issue{class}, now, true), w, true, now)
	if got.ClosedReason != "" {
		t.Errorf("снятый урок: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Экран учителя
// ---------------------------------------------------------------------------

func TestBuildLive(t *testing.T) {
	tasks, ex1, _ := testLesson()
	now := at("2026-09-29T10:30:00+03:00")
	endOfToday := classDueAt(now, msk)
	class := issue(IssueClass, now.Add(-10*time.Minute), &endOfToday, TaskText, ExerciseTaskKey(ex1))
	idle := issue(IssueClass, now.Add(-5*time.Minute), &endOfToday, TaskDialog)

	aidar := ClassMember{ID: uuid.New(), DisplayName: "Айдар", PublicTag: "#KZ4F"}
	alsu := ClassMember{ID: uuid.New(), DisplayName: "алсу", PublicTag: "#M2QP"}
	timur := ClassMember{ID: uuid.New(), DisplayName: "Тимур", PublicTag: "#V5GK"}
	gone := uuid.New() // ушёл из класса, но успел начать

	aidarAttempt := Attempt{ID: uuid.New(), StudentID: aidar.ID,
		Answers: answersFor(ex1, map[string]bool{"әни": true, "әти": true})}
	alsuAttempt := Attempt{ID: uuid.New(), StudentID: alsu.ID}
	goneAttempt := Attempt{ID: uuid.New(), StudentID: gone}
	textDone := now.Add(-2 * time.Minute)
	progress := []TaskProgress{
		{AttemptID: aidarAttempt.ID, TaskKey: TaskText, IssueID: &class.ID, OpenedAt: now.Add(-9 * time.Minute), DoneAt: &textDone},
		{AttemptID: aidarAttempt.ID, TaskKey: ExerciseTaskKey(ex1), IssueID: &class.ID, OpenedAt: now.Add(-8 * time.Minute), DoneAt: &textDone},
		{AttemptID: alsuAttempt.ID, TaskKey: TaskText, IssueID: &class.ID, OpenedAt: now.Add(-time.Minute)},
		{AttemptID: goneAttempt.ID, TaskKey: TaskDialog, IssueID: &idle.ID, OpenedAt: now.Add(-time.Minute)},
	}
	c := classwork{
		assignment: Assignment{ID: uuid.New(), State: StateActive},
		tasks:      tasks,
		issues:     []Issue{idle, class},
		members:    []ClassMember{timur, alsu, aidar},
		attempts:   []Attempt{aidarAttempt, alsuAttempt, goneAttempt},
		progress:   progress,
		presence: map[uuid.UUID]time.Time{
			aidar.ID: now.Add(-30 * time.Second),
			timur.ID: now.Add(-3 * time.Minute),
		},
	}
	v := buildLive(c, now)

	if names := []string{v.Students[0].Member.DisplayName, v.Students[1].Member.DisplayName,
		v.Students[2].Member.DisplayName}; !slices.Equal(names, []string{"Айдар", "алсу", "Тимур"}) {
		t.Errorf("ученики по имени без учёта регистра: %v", names)
	}
	aidarRow, alsuRow, timurRow := v.Students[0], v.Students[1], v.Students[2]
	if !aidarRow.Online || timurRow.Online || alsuRow.Online || alsuRow.LastSeenAt != nil {
		t.Errorf("в сети — за последние 2 минуты: %v %v %v", aidarRow.Online, timurRow.Online, alsuRow.Online)
	}
	if len(aidarRow.Cells) != 3 {
		t.Errorf("клетки только по выданным заданиям: %v", aidarRow.Cells)
	}
	if aidarRow.Cells[ExerciseTaskKey(ex1)].State != CellDone || aidarRow.Cells[TaskText].State != CellDone {
		t.Errorf("Айдар всё сделал: %+v", aidarRow.Cells)
	}
	if alsuRow.Cells[TaskText].State != CellOpened || alsuRow.Cells[ExerciseTaskKey(ex1)].State != CellNone {
		t.Errorf("Алсу открыла текст: %+v", alsuRow.Cells)
	}

	if v.Issues[0].Issue.ID != class.ID || v.Issues[1].Issue.ID != idle.ID {
		t.Fatal("выдачи по времени выдачи")
	}
	if !v.Issues[0].Started || v.Issues[0].DoneStudents != 1 {
		t.Errorf("первая выдача: начата=%v, сделали=%d", v.Issues[0].Started, v.Issues[0].DoneStudents)
	}
	// Ушедший из класса начал разговор: выдачу уже не отменить, но в «сделали
	// из 3» он не входит.
	if !v.Issues[1].Started || v.Issues[1].DoneStudents != 0 {
		t.Errorf("вторая выдача: начата=%v, сделали=%d", v.Issues[1].Started, v.Issues[1].DoneStudents)
	}
	if v.HasHome {
		t.Error("домашки нет")
	}
	for _, lt := range v.Tasks {
		switch lt.Key {
		case TaskText:
			if lt.IssueID == nil || *lt.IssueID != class.ID || lt.DoneStudents != 1 {
				t.Errorf("текст: %+v", lt)
			}
		case TaskObjectives:
			if lt.IssueID != nil || lt.DoneStudents != 0 {
				t.Errorf("цели не выданы: %+v", lt)
			}
		}
	}
}

func TestAnsweredQuestionsInWords(t *testing.T) {
	tasks, _, ex2 := testLesson()
	gap := tasks[3]
	review := ExerciseReview{Questions: []QuestionReview{
		{ItemID: "q1", Prompt: "Минем ___ укытучы.", Expected: "әнием",
			Choices: map[string]string{"a": "әнием", "b": "әни"}},
		{ItemID: "q2", Prompt: "Безнең ___ зур.", Expected: "гаиләбез",
			Choices: map[string]string{"a": "гаиләбез", "b": "гаилә"}},
		{ItemID: "q3", Prompt: "Аның ___ бар.", Choices: map[string]string{"a": "абыйсы"}},
	}}
	answers := map[string]GivenAnswer{
		ex2.String() + ":q1": {Answer: "b", Correct: false, Expected: "a"},
		ex2.String() + ":q3": {Answer: "a", Correct: true, Expected: "a"},
	}
	got := answeredQuestions(gap, review, answers)
	if len(got) != 2 {
		t.Fatalf("только отвеченные вопросы: %+v", got)
	}
	if got[0] != (AnsweredQuestion{Prompt: "Минем ___ укытучы.", Answer: "әни", Correct: false, Expected: "әнием"}) {
		t.Errorf("ответ словами, а не идентификатором варианта: %+v", got[0])
	}
	// Ключа на вопрос нет — верный ответ берётся из того, что видел ученик.
	if got[1].Expected != "абыйсы" || got[1].Answer != "абыйсы" {
		t.Errorf("ответ без ключа: %+v", got[1])
	}
}

func TestCardRow(t *testing.T) {
	tasks, ex1, ex2 := testLesson()
	now := at("2026-10-01T10:00:00+03:00")
	class := issue(IssueClass, at("2026-09-29T10:15:00+03:00"), ptr(at("2026-09-29T23:59:59+03:00")),
		TaskText, ExerciseTaskKey(ex1))
	class.ClosedAt = ptr(at("2026-09-29T10:32:00+03:00"))
	homeDue := at("2026-10-03T23:59:59+03:00")
	home := issue(IssueHome, at("2026-09-29T10:33:00+03:00"), &homeDue, ExerciseTaskKey(ex2), TaskDialog)

	attempt := Attempt{ID: uuid.New(), Answers: answersFor(ex1, map[string]bool{"әни": true, "әти": true})}
	got := cardRow(attempt, Assignment{ID: uuid.New(), State: StateActive}, tasks, []Issue{home, class}, nil, now)

	if got.TasksTotal != 6 || got.TasksDone != 1 {
		t.Errorf("«X из 8 заданий»: %d из %d", got.TasksDone, got.TasksTotal)
	}
	if len(got.Issues) != 2 || got.Issues[0].Issue.ID != class.ID {
		t.Fatalf("группы по выдачам, по времени: %+v", got.Issues)
	}
	text, match := got.Issues[0].Tasks[0], got.Issues[0].Tasks[1]
	if text.State != CardMissed || text.Correct != nil || text.Total != nil {
		t.Errorf("текст не сделан, выдача закрыта: %+v", text)
	}
	if match.State != CardDone || *match.Correct != 2 || *match.Total != 2 || match.N != 3 {
		t.Errorf("упражнение 2/2: %+v", match)
	}
	if st := got.Issues[1].Tasks[0].State; st != CardPending || !got.Issues[1].Open {
		t.Errorf("домашка открыта — «пока не сделано»: %s", st)
	}
}

func TestSummaryOfLessonRow(t *testing.T) {
	tasks, ex1, _ := testLesson()
	now := at("2026-09-29T10:30:00+03:00")
	endOfToday := classDueAt(now, msk)
	homeDue := at("2026-10-03T23:59:59+03:00")
	b := newBoard(tasks, []Issue{
		issue(IssueClass, now, &endOfToday, TaskText, ExerciseTaskKey(ex1)),
		issue(IssueHome, now, &homeDue, TaskDialog),
	}, now, false)
	s := summarize(b)
	if s.TasksTotal != 6 || s.TasksIssued != 3 || !s.ClassOpen || s.Legacy {
		t.Errorf("строка урока: %+v", s)
	}
	if s.Home == nil || !s.Home.Open || !s.Home.DueAt.Equal(homeDue) {
		t.Errorf("домашка в строке урока: %+v", s.Home)
	}
	if s := summarize(newBoard(tasks, nil, now, false)); s.Home != nil || s.TasksIssued != 0 || s.ClassOpen {
		t.Errorf("назначенный урок — «выдано 0 из 6»: %+v", s)
	}
}

// Ключ упражнения в пути — «ex:<uuid>», и клиент кодирует двоеточие.
func TestTaskKeyParamDecodesColon(t *testing.T) {
	id := uuid.New()
	var got string
	r := chi.NewRouter()
	r.Post("/my/attempts/{attemptID}/tasks/{taskKey}/open", func(w http.ResponseWriter, req *http.Request) {
		got = taskKeyParam(req)
	})
	for _, path := range []string{
		"/my/attempts/" + uuid.NewString() + "/tasks/ex%3A" + id.String() + "/open",
		"/my/attempts/" + uuid.NewString() + "/tasks/ex:" + id.String() + "/open",
	} {
		got = ""
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
		if got != ExerciseTaskKey(id) {
			t.Errorf("%s → %q, ожидали %q", path, got, ExerciseTaskKey(id))
		}
	}
}

// Маршруты «Урока в классе» поднимаются вместе с остальными и каждый объявил
// право: иначе сервис не стартует (Guard.Verify), а chi падает на
// конфликтующих именах параметров — узнавать это лучше здесь, чем на выкате.
func TestClassworkRoutesMount(t *testing.T) {
	guard := iam.NewGuard(nil, config.AuthConfig{})
	r := chi.NewRouter()
	NewAPI(nil, guard).Mount(r)
	if err := guard.Verify(r); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /assignments/{assignmentID}/live",
		"POST /assignments/{assignmentID}/issues",
		"GET /assignments/{assignmentID}/students/{studentID}",
		"POST /issues/{issueID}/finish",
		"POST /issues/{issueID}/extend",
		"DELETE /issues/{issueID}",
		"GET /my/attempts/{attemptID}/tasks",
		"POST /my/attempts/{attemptID}/tasks/{taskKey}/open",
		"POST /my/attempts/{attemptID}/tasks/{taskKey}/done",
		"GET /my/lessons/{assignmentID}/results",
	}
	got := map[string]bool{}
	_ = chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got[method+" "+route] = true
		return nil
	})
	for _, route := range want {
		if !got[route] {
			t.Errorf("нет маршрута %s", route)
		}
	}
}

func TestStoreErrorsAreDistinct(t *testing.T) {
	for _, err := range []error{ErrTaskTaken, ErrHomeTaken, ErrIssueGone, ErrIssueStarted} {
		if errors.Is(err, ErrNotFound) {
			t.Errorf("%v не должна совпадать с ErrNotFound", err)
		}
	}
}

func TestAssembleCard(t *testing.T) {
	tasks, ex1, _ := testLesson()
	now := at("2026-10-01T10:00:00+03:00")
	versionID := uuid.New()
	older := Assignment{ID: uuid.New(), LessonVersionID: versionID, State: StateActive}
	newer := Assignment{ID: uuid.New(), LessonVersionID: versionID, State: StateActive}
	seen := at("2026-09-30T12:00:00+03:00")
	attempt := Attempt{
		ID: uuid.New(), AssignmentID: older.ID, Completed: true,
		CorrectAnswers: 3, TotalAnswers: 3, TotalQuestions: 4, LastSeenAt: seen,
		Answers: answersFor(ex1, map[string]bool{"әни": true}),
	}
	report, _ := json.Marshal(reportSnapshot{Analysis: json.RawMessage(
		`{"goals":[{"key":"a","credited":true},{"key":"b","credited":false},{"key":"c","credited":true}]}`)})
	convs := []ConversationRow{{AttemptID: attempt.ID, Turns: 6, ReportStatus: ReportReady, Report: report}}

	got := assembleCard([]Assignment{newer, older}, []Attempt{attempt},
		map[uuid.UUID]VersionInfo{versionID: {LessonTitle: "Минем гаиләм"}},
		map[uuid.UUID][]LessonTask{versionID: tasks}, nil, nil, convs, now)

	if len(got.Lessons) != 2 || got.Lessons[0].Assignment.ID != newer.ID {
		t.Fatalf("все уроки класса в порядке назначений: %+v", got.Lessons)
	}
	if l := got.Lessons[0]; l.Attempt != nil || l.Conversation != nil || l.TasksTotal != len(tasks) {
		t.Errorf("не открытый урок — строка без попытки и счёта: %+v", l)
	}
	l := got.Lessons[1]
	if l.Attempt == nil || l.Attempt.ID != attempt.ID || l.Version.Name() != "Минем гаиләм" {
		t.Fatalf("открытый урок с попыткой: %+v", l)
	}
	if c := l.Conversation; c == nil || !c.Ready || c.Credited != 2 || c.Goals != 3 {
		t.Errorf("итог разговора «2 из 3 целей»: %+v", c)
	}
	// Доля — от вопросов урока (3 из 4), а не от данных ответов (3 из 3).
	s := got.Summary
	if s.Assigned != 2 || s.Passed != 1 || s.Correct != 3 || s.Questions != 4 ||
		s.LastSeenAt == nil || !s.LastSeenAt.Equal(seen) {
		t.Errorf("сводка карточки: %+v", s)
	}
	if p := percentOrNil(s.Correct, s.Questions); p == nil || *p != 75 {
		t.Errorf("75 %% верных, а не 100: %v", p)
	}
}

func TestCardConversation(t *testing.T) {
	if c := cardConversation(ConversationRow{}); c != nil {
		t.Errorf("разговора не было — итога нет: %+v", c)
	}
	if c := cardConversation(ConversationRow{Turns: 2, ReportStatus: ReportPending}); c == nil || c.Ready {
		t.Errorf("разговор был, разбор не готов: %+v", c)
	}
}
