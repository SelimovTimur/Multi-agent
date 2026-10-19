package learning

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Аналитика — это арифметика поверх агрегатов базы. Ошибка здесь не падает и
// не пишется в лог: она просто показывает учителю неправильную цифру, по
// которой тот принимает решение о классе.

// Новый класс открывают чаще всего: учитель завёл его и сразу зашёл
// посмотреть. Пустых списков на экране ждут, деления на ноль — нет.
func TestAnalyticsOfEmptyClass(t *testing.T) {
	got := assembleAnalytics(nil, nil, nil, nil, nil, time.Now())

	if got.Assigned != 0 || got.Passed != 0 || got.Expected != 0 {
		t.Errorf("у пустого класса всё по нулям, получили %+v", got)
	}
	// Именно срезы, а не nil: экран рисует таблицу, а не «данные не пришли».
	if got.Lessons == nil || got.Students == nil || got.Inactive == nil {
		t.Error("списки должны быть пустыми, а не nil")
	}
}

// Уроки выданы, ученики есть, отвечать никто ещё не начинал. Это нормальное
// состояние понедельника, и оно не должно выглядеть как ошибка.
func TestAnalyticsWithoutAnswers(t *testing.T) {
	versionID, lessonID := uuid.New(), uuid.New()
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	a := Assignment{ID: uuid.New(), LessonVersionID: versionID, CreatedAt: now.Add(-24 * time.Hour)}
	b := Assignment{ID: uuid.New(), LessonVersionID: versionID, CreatedAt: now.Add(-2 * time.Hour)}
	members := []ClassMember{{ID: uuid.New(), DisplayName: "Айгуль"}, {ID: uuid.New(), DisplayName: "Тимур"}}

	got := assembleAnalytics(members, []Assignment{b, a},
		map[uuid.UUID]VersionInfo{versionID: {LessonID: lessonID, Title: "Мой день", Level: "A2"}},
		nil, nil, now)

	// Два урока на двух учеников — четыре ожидаемых прохождения.
	if got.Expected != 4 || got.Passed != 0 {
		t.Errorf("ожидалось 4, пройдено 0, получили %d и %d", got.Expected, got.Passed)
	}
	if len(got.Lessons) != 2 || got.Lessons[0].AssignmentID != b.ID || got.Lessons[0].LessonID != lessonID {
		t.Fatalf("столбцы — уроки в порядке назначений: %+v", got.Lessons)
	}
	for _, s := range got.Students {
		if len(s.Cells) != 2 || s.Cells[0].Started || s.Tasks.Percent() != nil {
			t.Errorf("урок не открыт — клетка пустая и среднего нет: %+v", s)
		}
	}
	if got.Lessons[0].Tasks.Percent() != nil {
		t.Error("по уроку не отвечали — среднее столбца пустое, а не 0 %")
	}
	// Первый урок выдан вчера: «неделю не заходил» пока сказать не о ком.
	if len(got.Inactive) != 0 {
		t.Errorf("урок выдан вчера, а в сводке уже не заходившие: %+v", got.Inactive)
	}
}

// Класс, в котором работают: клетки, средние по строке и столбцу, сводка и
// ушедший ученик, который в таблице не показывается.
func TestAnalyticsMatrix(t *testing.T) {
	versionID := uuid.New()
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	older := Assignment{ID: uuid.New(), LessonVersionID: versionID, CreatedAt: now.Add(-20 * 24 * time.Hour)}
	newer := Assignment{ID: uuid.New(), LessonVersionID: versionID, CreatedAt: now.Add(-3 * 24 * time.Hour)}
	aigul, timur, kamila, left := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	members := []ClassMember{{ID: aigul}, {ID: timur}, {ID: kamila}}
	recent, stale := now.Add(-time.Hour), now.Add(-10*24*time.Hour)

	attempts := []ClassAttempt{
		{ID: uuid.New(), AssignmentID: newer.ID, StudentID: aigul, Completed: true, CorrectAnswers: 9, TotalQuestions: 10, LastSeenAt: recent},
		{ID: uuid.New(), AssignmentID: older.ID, StudentID: aigul, Completed: true, CorrectAnswers: 2, TotalQuestions: 5, LastSeenAt: stale},
		{ID: uuid.New(), AssignmentID: older.ID, StudentID: timur, Completed: false, CorrectAnswers: 1, TotalQuestions: 5, LastSeenAt: stale},
		// Ушедший из класса: его работа в таблицу и сводку не попадает.
		{ID: uuid.New(), AssignmentID: newer.ID, StudentID: left, Completed: true, CorrectAnswers: 0, TotalQuestions: 10, LastSeenAt: recent},
	}
	report, _ := json.Marshal(reportSnapshot{Analysis: json.RawMessage(
		`{"goals":[{"key":"a","credited":true},{"key":"b","credited":false},{"key":"c","credited":true},{"key":"d","credited":true}]}`)})
	convs := []ConversationRow{
		{AttemptID: attempts[0].ID, Turns: 6, ReportStatus: ReportReady, Report: report},
		// Разбор не готов: клетка знает, что разговор был, но в среднее он не идёт.
		{AttemptID: attempts[2].ID, Turns: 2, ReportStatus: ReportPending},
	}

	got := assembleAnalytics(members, []Assignment{newer, older},
		map[uuid.UUID]VersionInfo{versionID: {Title: "Семья"}}, attempts, convs, now)

	if got.Expected != 6 || got.Passed != 2 {
		t.Errorf("ожидалось 6, пройдено 2 (без ушедшего), получили %d и %d", got.Expected, got.Passed)
	}
	if len(got.Students) != 3 {
		t.Fatalf("строки — только состав класса: %+v", got.Students)
	}

	row := got.Students[0]
	if c := row.Cells[0]; !c.Started || !c.Completed || percentOrNil(c.Correct, c.Questions) == nil ||
		*percentOrNil(c.Correct, c.Questions) != 90 {
		t.Errorf("клетка нового урока — 90 %%: %+v", c)
	}
	if c := row.Cells[0].Conversation; c == nil || !c.Ready || c.Credited != 3 || c.Goals != 4 {
		t.Errorf("разговор «3 из 4 целей»: %+v", c)
	}
	// Среднее ученика — 11 верных из 15 вопросов, а не среднее 90 и 40.
	if v := mustPercent(t, row.Tasks.Percent()); v != 73 {
		t.Errorf("среднее ученика = %d, из 11 из 15 выходит 73", v)
	}
	if v := mustPercent(t, row.Conversation.Percent()); v != 75 {
		t.Errorf("разговор ученика = %d, ожидали 75", v)
	}
	if row.LastSeenAt == nil || !row.LastSeenAt.Equal(recent) {
		t.Errorf("последняя активность = %v, ожидали %v", row.LastSeenAt, recent)
	}

	// Среднее столбца старого урока: 2 + 1 верных из 5 + 5 вопросов.
	if v := mustPercent(t, got.Lessons[1].Tasks.Percent()); v != 30 {
		t.Errorf("среднее урока = %d, ожидали 30", v)
	}
	// Разбор Тимура не готов — среднего по разговору у урока нет.
	if got.Lessons[1].Conversation.Percent() != nil {
		t.Error("готовых разборов по уроку нет — среднее разговора пустое")
	}
	if c := got.Students[1].Cells[1].Conversation; c == nil || c.Ready {
		t.Errorf("разговор Тимура был, разбор не готов: %+v", c)
	}

	// Тимур работал десять дней назад, Камила не открывала уроки, а первый
	// урок выдан двадцать дней назад — оба в сводке. Айгуль работала сегодня.
	if len(got.Inactive) != 2 || got.Inactive[0].ID != timur || got.Inactive[1].ID != kamila {
		t.Errorf("неделю не заходили Тимур и Камила: %+v", got.Inactive)
	}
}

// Клетка таблицы и строка урока в карточке ученика считаются из одной
// попытки и обязаны показывать одно и то же.
func TestAnalyticsCellMatchesCard(t *testing.T) {
	tasks, _, _ := testLesson()
	versionID := uuid.New()
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	a := Assignment{ID: uuid.New(), LessonVersionID: versionID, State: StateActive, CreatedAt: now.Add(-time.Hour)}
	student := uuid.New()
	attempt := Attempt{ID: uuid.New(), AssignmentID: a.ID, StudentID: student, Completed: true,
		CorrectAnswers: 3, TotalAnswers: 3, TotalQuestions: 7, LastSeenAt: now}
	report, _ := json.Marshal(reportSnapshot{Analysis: json.RawMessage(
		`{"goals":[{"key":"a","credited":true},{"key":"b","credited":false}]}`)})
	convs := []ConversationRow{{AttemptID: attempt.ID, Turns: 4, ReportStatus: ReportReady, Report: report}}

	card := assembleCard([]Assignment{a}, []Attempt{attempt}, nil,
		map[uuid.UUID][]LessonTask{versionID: tasks}, nil, nil, convs, now)
	matrix := assembleAnalytics([]ClassMember{{ID: student}}, []Assignment{a}, nil,
		[]ClassAttempt{{ID: attempt.ID, AssignmentID: a.ID, StudentID: student, Completed: true,
			CorrectAnswers: attempt.CorrectAnswers, TotalQuestions: attempt.TotalQuestions, LastSeenAt: now}},
		convs, now)

	l, c := card.Lessons[0], matrix.Students[0].Cells[0]
	if l.Attempt.CorrectAnswers != c.Correct || l.Attempt.TotalQuestions != c.Questions ||
		l.Attempt.Completed != c.Completed {
		t.Errorf("задания: карточка %+v, таблица %+v", l.Attempt, c)
	}
	if *l.Conversation != *c.Conversation {
		t.Errorf("разговор: карточка %+v, таблица %+v", l.Conversation, c.Conversation)
	}
	cardPct := percentOrNil(card.Summary.Correct, card.Summary.Questions)
	if *cardPct != *matrix.Students[0].Tasks.Percent() {
		t.Errorf("сводка карточки %d %%, среднее строки %d %%", *cardPct, *matrix.Students[0].Tasks.Percent())
	}
}

// mustPercent разыменовывает долю там, где тест ждёт посчитанного числа.
// Пустое значение здесь — отдельная новость («не из чего считать»), и его
// проверяют другие тесты; молча получить ноль вместо доли нельзя.
func mustPercent(t *testing.T, v *int) int {
	t.Helper()
	if v == nil {
		t.Fatal("доля не посчитана, хотя ответы были")
	}
	return *v
}

// Разница между «ноль верных» и «ответов не было» — это разница между
// обвинением и его отсутствием. Проверяем её отдельно от арифметики.
func TestPercentOrNil(t *testing.T) {
	if got := percentOrNil(0, 0); got != nil {
		t.Errorf("без ответов ожидали пусто, получили %d", *got)
	}
	if got := percentOrNil(5, -1); got != nil {
		t.Errorf("отрицательный знаменатель — это не результат, получили %d", *got)
	}
	got := percentOrNil(0, 12)
	if got == nil {
		t.Fatal("двенадцать ответов, все неверные, — это результат, а не пустота")
	}
	if *got != 0 {
		t.Errorf("доля = %d, все двенадцать ответов неверны — ожидали 0", *got)
	}
}

func TestPercent(t *testing.T) {
	cases := []struct {
		name        string
		part, total int
		want        int
	}{
		{"нет ответов — ноль, а не деление на ноль", 0, 0, 0},
		{"всё верно", 10, 10, 100},
		{"ничего не верно", 0, 12, 0},
		{"округление вверх", 2, 3, 67},
		{"округление вниз", 1, 3, 33},
		{"отрицательный знаменатель не ломает счёт", 5, -1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := percent(c.part, c.total); got != c.want {
				t.Errorf("percent(%d, %d) = %d, ожидали %d", c.part, c.total, got, c.want)
			}
		})
	}
}

// Библиотека спрашивает про свои уроки и ждёт ровно свои классы: один урок,
// выданный в класс дважды разными версиями, — это один бейдж, а не два.
func TestLessonClassIDs(t *testing.T) {
	lesson, other := uuid.New(), uuid.New()
	v1, v2, foreign := uuid.New(), uuid.New(), uuid.New()
	classA, classB := uuid.New(), uuid.New()

	versions := map[uuid.UUID]VersionInfo{
		v1:      {LessonID: lesson},
		v2:      {LessonID: lesson},
		foreign: {LessonID: other},
	}
	pairs := []AssignedClass{
		{VersionID: v1, ClassID: classA},
		{VersionID: v2, ClassID: classA}, // вторая версия того же урока в тот же класс
		{VersionID: v2, ClassID: classB},
		{VersionID: foreign, ClassID: classB},    // чужой урок — не спрашивали
		{VersionID: uuid.New(), ClassID: classB}, // версии нет в ответе порта
	}

	byLesson, classIDs := lessonClassIDs(pairs, versions, map[uuid.UUID]bool{lesson: true})

	if len(byLesson) != 1 {
		t.Fatalf("уроков в ответе %d, спрашивали про один", len(byLesson))
	}
	if got := len(byLesson[lesson]); got != 2 {
		t.Errorf("классов у урока %d, ожидали 2 — повтор версии класс не задваивает", got)
	}
	if len(classIDs) != 2 {
		t.Errorf("имена дочитываются для %d классов, ожидали 2 без повторов", len(classIDs))
	}
}

func TestLessonClassIDsWithoutMatches(t *testing.T) {
	byLesson, classIDs := lessonClassIDs(
		[]AssignedClass{{VersionID: uuid.New(), ClassID: uuid.New()}},
		map[uuid.UUID]VersionInfo{},
		map[uuid.UUID]bool{uuid.New(): true},
	)
	if len(byLesson) != 0 || len(classIDs) != 0 {
		t.Errorf("урок нигде не назначен — ответ пустой, получили %v и %v", byLesson, classIDs)
	}
}
