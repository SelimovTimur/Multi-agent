package learning

// «Урок в классе»: задания урока выдаются классу по одному — на уроке или на
// дом, а учитель видит, кто как их проходит. Контракт ручек —
// docs/backend/class-lessons-api.md, требования — docs/product/class-lessons.
//
// Здесь — понятия и чистая арифметика: что выдано, открыто ли, что сделано и
// какой клеткой это показать. Без базы и сети намеренно: правила экрана
// учителя проверяются тестами, а не глазами на уроке.

import (
	"math"
	"slices"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/google/uuid"
)

// Виды выдачи: на уроке и на дом. Они же — разделы у ученика.
const (
	IssueClass = "class"
	IssueHome  = "home"
)

// allTasksKey — «все задания урока». Так записаны назначения, созданные до
// выдачи по заданиям: они стали одной выдачей «на дом» на весь урок, а
// список заданий урока знает только content.
const allTasksKey = "*"

// exerciseKeyPrefix — ключ задания-упражнения: «ex:» и идентификатор
// упражнения. Зеркало content.ExerciseTaskPrefix.
const exerciseKeyPrefix = "ex:"

// Виды заданий. Зеркало констант content.
const (
	TaskObjectives = "objectives"
	TaskText       = "text"
	TaskLexicon    = "lexicon"
	TaskExercise   = "exercise"
	TaskGrammar    = "grammar"
	TaskDialog     = "dialog"
)

// Состояния клетки таблицы «Как проходит класс».
const (
	CellNone   = "none"   // выдача открыта, задание не открывали
	CellDoing  = "doing"  // упражнение открыто или начато
	CellOpened = "opened" // задание без вопросов открыто, «Дальше» не нажато
	CellDone   = "done"   // сделано
	CellMiss   = "miss"   // выдача закрыта, а задание не сделано
)

// Состояния задания в карточке ученика.
const (
	CardDone    = "done"
	CardPending = "pending"
	CardMissed  = "missed"
)

// Причины, по которым у ученика пуст раздел.
const (
	ReasonClassFinished = "class_finished"
	ReasonHomeExpired   = "home_expired"
)

// Отказы ученику, когда задание сейчас нельзя делать. Текст — из контракта:
// экран показывает его как есть, и он должен объяснять, что случилось, а не
// что «запрос отклонён».
const (
	msgClassFinished = "Учитель завершил работу на уроке"
	msgHomeExpired   = "Срок домашней работы прошёл"
	msgNotIssued     = "Это задание ещё не выдано"
)

// onlineWindow — «в сети»: ученик обращался к любой своей ручке за последние
// две минуты. Экран урока опрашивает сервер раз в 5 секунд, так что двух
// минут хватает с запасом на пропавшую сеть и спящий планшет.
const onlineWindow = 2 * time.Minute

// LessonTask — задание урока. Зеркало content.LessonTask: порядок, ключи и
// заголовки задаёт content, learning хранит только ключи выданного.
type LessonTask struct {
	Key       string
	N         int
	Kind      string
	Type      string
	TypeLabel string
	Title     string
	Questions int
	// ExerciseID — только у упражнений.
	ExerciseID uuid.UUID
	// ItemIDs — чем ученик отвечает на вопросы упражнения. Засчитываются
	// только ответы на них: ответ на несуществующий вопрос упражнение не
	// закрывает.
	ItemIDs []string
}

// byAnswers — задание засчитывается ответами, а не кнопкой «Дальше».
func (t LessonTask) byAnswers() bool { return t.Kind == TaskExercise && len(t.ItemIDs) > 0 }

// ExerciseReview — вопросы упражнения с текстами вариантов и верными
// ответами. Зеркало content.ExerciseReview; только для экранов учителя.
type ExerciseReview struct {
	Questions []QuestionReview
}

// QuestionReview — вопрос упражнения глазами учителя.
type QuestionReview struct {
	ItemID   string
	Prompt   string
	Expected string
	// Choices — варианты: идентификатор → текст.
	Choices map[string]string
}

// text переводит ответ ученика в слова: на тест он отвечает идентификатором
// варианта, а учителю нужен сам вариант.
func (q QuestionReview) text(value string) string {
	if t, ok := q.Choices[value]; ok {
		return t
	}
	return value
}

// Issue — выдача: одно действие «выдать» — какие задания, как и когда.
type Issue struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	AssignmentID uuid.UUID
	Kind         string
	OpenedAt     time.Time
	// DueAt — до какого момента открыта. nil — только у перенесённых
	// назначений «без срока».
	DueAt *time.Time
	// ClosedAt — когда закрыли руками: «Завершить работу на уроке» или
	// снятие урока с класса.
	ClosedAt  *time.Time
	Legacy    bool
	CreatedBy uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
	// TaskKeys — ключи как в базе: у перенесённых назначений это «*».
	TaskKeys []string
}

// Closed — выдача закрыта: руками или по сроку. Не то же, что «не открыта»:
// перенесённое назначение с открытием в будущем ещё не открыто, но и не
// закрыто — его задания не «не сделаны», а ещё впереди.
func (i Issue) Closed(now time.Time) bool {
	return i.ClosedAt != nil || (i.DueAt != nil && !now.Before(*i.DueAt))
}

// Open — выдача принимает ответы прямо сейчас.
//
// Считается при чтении, а не фоновой задачей: «до конца дня» и «до срока»
// — это сравнение времени, и ученик не должен удержать задание на минуту
// дольше из-за того, что задача ещё не проснулась. Время открытия
// проверяется ради перенесённых назначений с открытием в будущем: урок,
// обещанный на понедельник, не открывается в воскресенье.
func (i Issue) Open(now time.Time) bool {
	if i.ClosedAt != nil {
		return false
	}
	if now.Before(i.OpenedAt) {
		return false
	}
	if i.DueAt != nil && !now.Before(*i.DueAt) {
		return false
	}
	return true
}

// TaskProgress — что ученик сделал с заданием: открыл и когда сделал.
type TaskProgress struct {
	AttemptID uuid.UUID
	TaskKey   string
	// IssueID — в какой выдаче задание открыто: по нему видно, что сделано на
	// уроке, а что дома.
	IssueID  *uuid.UUID
	OpenedAt time.Time
	DoneAt   *time.Time
}

// ---------------------------------------------------------------------------
// Доска урока: задания и выдачи
// ---------------------------------------------------------------------------

// lessonBoard — задания урока и его выдачи: что выдано, в какой выдаче и
// открыта ли она сейчас.
type lessonBoard struct {
	tasks []LessonTask
	index map[string]int // ключ задания → позиция в tasks
	// issues — по времени выдачи, старые первыми.
	issues []Issue
	open   []bool
	// closed — выдача закрыта (руками, по сроку или вместе со снятым уроком).
	// Ещё не открытая выдача не открыта и не закрыта.
	closed []bool
	holder map[string]int // ключ задания → индекс выдачи
}

// newBoard собирает доску. removed — урок снят с класса: тогда ни одна
// выдача не открыта, что бы ни было записано в ней самой.
func newBoard(tasks []LessonTask, issues []Issue, now time.Time, removed bool) lessonBoard {
	b := lessonBoard{
		tasks:  tasks,
		index:  make(map[string]int, len(tasks)),
		issues: slices.Clone(issues),
		holder: make(map[string]int, len(tasks)),
	}
	for i, t := range tasks {
		b.index[t.Key] = i
	}
	slices.SortStableFunc(b.issues, func(x, y Issue) int {
		if c := x.OpenedAt.Compare(y.OpenedAt); c != 0 {
			return c
		}
		if c := x.CreatedAt.Compare(y.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(x.ID.String(), y.ID.String())
	})
	b.open = make([]bool, len(b.issues))
	b.closed = make([]bool, len(b.issues))
	for i, is := range b.issues {
		b.open[i] = !removed && is.Open(now)
		b.closed[i] = removed || is.Closed(now)
		for _, k := range is.TaskKeys {
			if k != allTasksKey {
				b.holder[k] = i
			}
		}
	}
	// «Все задания урока» раскрываются вторым проходом: задание, выданное
	// явно, остаётся за своей выдачей.
	for i, is := range b.issues {
		if !slices.Contains(is.TaskKeys, allTasksKey) {
			continue
		}
		for _, t := range tasks {
			if _, taken := b.holder[t.Key]; !taken {
				b.holder[t.Key] = i
			}
		}
	}
	return b
}

func (b lessonBoard) task(key string) (LessonTask, bool) {
	i, ok := b.index[key]
	if !ok {
		return LessonTask{}, false
	}
	return b.tasks[i], true
}

// issueOf — индекс выдачи, в которой задание выдано.
func (b lessonBoard) issueOf(key string) (int, bool) {
	i, ok := b.holder[key]
	return i, ok
}

// issueKeys — задания выдачи в порядке урока. «*» раскрыт; ключи, которых в
// уроке больше нет, не показываются.
func (b lessonBoard) issueKeys(i int) []string {
	out := []string{}
	for _, t := range b.tasks {
		if j, ok := b.holder[t.Key]; ok && j == i {
			out = append(out, t.Key)
		}
	}
	return out
}

// issuedCount — сколько заданий урока выдано.
func (b lessonBoard) issuedCount() int {
	n := 0
	for _, t := range b.tasks {
		if _, ok := b.holder[t.Key]; ok {
			n++
		}
	}
	return n
}

// anyOpen — открыта хоть одна выдача.
func (b lessonBoard) anyOpen() bool { return slices.Contains(b.open, true) }

// allClosed — все выдачи закрыты (и они есть). Ещё не открытая выдача не
// закрыта: урок с ней не «завершён», а впереди.
func (b lessonBoard) allClosed() bool { return len(b.issues) > 0 && !slices.Contains(b.closed, false) }

// lessonFinished — урок ушёл в «Завершённые»: все задания выданы и все
// выдачи закрыты. Снятый с класса урок тоже завершён — больше по нему ничего
// не придёт.
func (b lessonBoard) lessonFinished(removed bool) bool {
	return removed || (len(b.tasks) > 0 && b.issuedCount() == len(b.tasks) && b.allClosed())
}

// home — индекс домашней выдачи.
func (b lessonBoard) home() (int, bool) {
	for i, is := range b.issues {
		if is.Kind == IssueHome {
			return i, true
		}
	}
	return 0, false
}

// gate объясняет, почему задание сейчас нельзя делать; nil — можно.
//
// Закрытая выдача называется по своему виду: ученику важно не «закрыто», а
// «учитель завершил урок» или «срок прошёл» — это разные разговоры с
// учителем.
func (b lessonBoard) gate(key string) error {
	i, ok := b.issueOf(key)
	if !ok {
		return httpx.Conflict(msgNotIssued)
	}
	if b.open[i] {
		return nil
	}
	// Перенесённое назначение с открытием в будущем: задание ещё впереди, а
	// не «срок прошёл».
	if !b.closed[i] {
		return httpx.Conflict(msgNotIssued)
	}
	if b.issues[i].Kind == IssueClass {
		return httpx.Conflict(msgClassFinished)
	}
	return httpx.Conflict(msgHomeExpired)
}

// ---------------------------------------------------------------------------
// Работа ученика
// ---------------------------------------------------------------------------

// studentWork — всё, что ученик сделал по уроку: ответы попытки и прогресс
// по заданиям.
type studentWork struct {
	answers  map[string]GivenAnswer
	progress map[string]TaskProgress
	// legacy — строка «*» попытки, начатой до выдачи по заданиям: «урок
	// открыт тогда-то, пройден тогда-то». Старый урок проходили подряд, и
	// пройденный целиком значит пройденный по всем заданиям.
	legacy *TaskProgress
}

// newWork раскладывает прогресс попытки. attempt может быть nil — ученик
// урок ещё не открывал.
func newWork(attempt *Attempt, progress []TaskProgress) studentWork {
	w := studentWork{progress: make(map[string]TaskProgress, len(progress))}
	if attempt != nil {
		w.answers = attempt.Answers
	}
	for _, p := range progress {
		if p.TaskKey == allTasksKey {
			legacy := p
			w.legacy = &legacy
			continue
		}
		w.progress[p.TaskKey] = p
	}
	return w
}

// taskStatus — состояние задания у ученика.
type taskStatus struct {
	Opened   bool
	Done     bool
	Answered int
	Correct  int
	// OpenedAt и DoneAt — из собственной строки задания. У заданий,
	// пройденных до выдачи по заданиям, их нет: время урока целиком
	// приписывать каждому заданию было бы выдумкой.
	OpenedAt *time.Time
	DoneAt   *time.Time
}

// status — что ученик сделал с заданием.
//
// Ответы на упражнение считаются по попытке, а не по прогрессу: они там уже
// лежат, и второй счётчик того же разошёлся бы с первым при первой гонке.
func (w studentWork) status(t LessonTask) taskStatus {
	var st taskStatus
	if row, ok := w.progress[t.Key]; ok {
		opened := row.OpenedAt
		st.Opened, st.OpenedAt, st.DoneAt = true, &opened, row.DoneAt
		st.Done = row.DoneAt != nil
	}
	if t.byAnswers() {
		prefix := t.ExerciseID.String() + ":"
		for _, id := range t.ItemIDs {
			if g, ok := w.answers[prefix+id]; ok {
				st.Answered++
				if g.Correct {
					st.Correct++
				}
			}
		}
		if st.Answered > 0 {
			st.Opened = true
		}
		if st.Answered >= len(t.ItemIDs) {
			st.Done = true
		}
	}
	if w.legacy != nil {
		st.Opened = true
		if w.legacy.DoneAt != nil {
			st.Done = true
		}
	}
	return st
}

// allDone — сделаны все задания урока. Тогда попытка завершается: по ней
// считается аналитика класса.
func (w studentWork) allDone(tasks []LessonTask) bool {
	if len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		if !w.status(t).Done {
			return false
		}
	}
	return true
}

// minutes — сколько минут ушло на задание, с округлением вверх; nil — не
// сделано или время неизвестно.
func (st taskStatus) minutes() *int {
	if !st.Done || st.OpenedAt == nil || st.DoneAt == nil {
		return nil
	}
	d := st.DoneAt.Sub(*st.OpenedAt)
	if d < 0 {
		d = 0
	}
	m := int(math.Ceil(d.Minutes()))
	return &m
}

// Cell — клетка таблицы «Как проходит класс».
type Cell struct {
	State    string
	Answered int
	Correct  int
	Total    int
}

// cellOf — клетка по контракту: сделано — «done»; не сделано в закрытой
// выдаче — «miss»; открыто — «doing» у упражнения и «opened» у задания без
// вопросов; иначе — «none».
func cellOf(t LessonTask, st taskStatus, closed bool) Cell {
	c := Cell{Answered: st.Answered, Correct: st.Correct, Total: t.Questions}
	switch {
	case st.Done:
		c.State = CellDone
	case closed:
		c.State = CellMiss
	case st.Opened && t.Kind == TaskExercise && t.Questions > 0:
		c.State = CellDoing
	case st.Opened:
		c.State = CellOpened
	default:
		c.State = CellNone
	}
	return c
}

// cardState — задание в карточке ученика: сделано, пока не сделано (выдача
// открыта), не сделано (выдача закрыта).
func cardState(st taskStatus, closed bool) string {
	switch {
	case st.Done:
		return CardDone
	case closed:
		return CardMissed
	}
	return CardPending
}

// ---------------------------------------------------------------------------
// Сроки по времени школы
// ---------------------------------------------------------------------------

// dueDateLayout — срок домашней работы: дата без времени, день по времени
// школы.
const dueDateLayout = "2006-01-02"

// endOfDay — 23:59:59 того же календарного дня по времени школы.
func endOfDay(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 23, 59, 59, 0, loc)
}

// classDueAt — выдача на уроке закрывается сама в конце дня, если учитель
// не нажал «Завершить».
func classDueAt(now time.Time, loc *time.Location) time.Time { return endOfDay(now, loc) }

// parseDueDate разбирает срок «YYYY-MM-DD» как день по времени школы.
func parseDueDate(s string, loc *time.Location) (time.Time, bool) {
	d, err := time.ParseInLocation(dueDateLayout, strings.TrimSpace(s), loc)
	if err != nil {
		return time.Time{}, false
	}
	return d, true
}

// beforeToday — день раньше сегодняшнего по времени школы.
func beforeToday(day, now time.Time, loc *time.Location) bool {
	return endOfDay(day, loc).Before(endOfDay(now, loc))
}

// ExerciseTaskKey — ключ задания упражнения. По нему ответ на упражнение
// находит своё задание и выдачу. Обязан совпадать с content.ExerciseTaskKey —
// это проверяет тест адаптеров в internal/app.
func ExerciseTaskKey(exerciseID uuid.UUID) string { return exerciseKeyPrefix + exerciseID.String() }
