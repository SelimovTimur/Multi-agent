package learning

// «Урок в классе» глазами учителя: выдать задания, завершить работу на
// уроке, продлить и отменить, видеть таблицу класса и ответы ученика.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/telemetry"
	"github.com/google/uuid"
)

// Отказы учителю — из контракта.
const (
	msgLessonRemoved   = "Урок снят с класса"
	msgHomeExists      = "Домашняя работа по уроку уже задана"
	msgTaskTaken       = "Задание уже выдано"
	msgNoSuchTask      = "В уроке нет такого задания"
	msgNoTasks         = "Отметьте хотя бы одно задание"
	msgDueRequired     = "Укажите срок"
	msgDueFormat       = "Срок — дата в формате ГГГГ-ММ-ДД"
	msgDueBeforeToday  = "Срок — не раньше сегодняшнего дня"
	msgDueNotLater     = "Новый срок должен быть позже текущего"
	msgFinishOnlyClass = "Завершить можно только работу на уроке"
	msgClassDone       = "Работа на уроке уже завершена"
	msgExtendOnlyHome  = "Продлить можно только домашнюю работу"
	msgIssueStarted    = "Задания этой выдачи уже начали — отменить нельзя"
)

// IssueInput — что учитель выдаёт.
type IssueInput struct {
	Kind     string
	TaskKeys []string
	// DueDate — срок домашней работы, «YYYY-MM-DD» по времени школы.
	DueDate string
}

// IssueView — выдача на экране учителя.
type IssueView struct {
	Issue Issue
	// TaskKeys — задания выдачи в порядке урока; «все задания» перенесённого
	// назначения раскрыты в список.
	TaskKeys []string
	Open     bool
	// Started — хоть один ученик открыл или начал хоть одно задание выдачи.
	// Пока false, выдачу можно отменить.
	Started bool
	// DoneStudents — сколько учеников нынешнего состава сделали все задания
	// выдачи.
	DoneStudents int
}

// classwork — урок класса целиком: назначение, задания, выдачи и работа
// учеников. Читается разом на экран учителя.
type classwork struct {
	assignment Assignment
	class      ClassInfo
	version    VersionInfo
	tasks      []LessonTask
	issues     []Issue
	members    []ClassMember
	attempts   []Attempt
	progress   []TaskProgress
	presence   map[uuid.UUID]time.Time
}

func (c classwork) board(now time.Time) lessonBoard {
	return newBoard(c.tasks, c.issues, now, c.assignment.State == StateClosed)
}

// works — работа каждого ученика по уроку, по идентификатору ученика.
func (c classwork) works() map[uuid.UUID]studentWork {
	return worksOf(c.attempts, c.progress)
}

func worksOf(attempts []Attempt, progress []TaskProgress) map[uuid.UUID]studentWork {
	byAttempt := make(map[uuid.UUID][]TaskProgress, len(attempts))
	for _, p := range progress {
		byAttempt[p.AttemptID] = append(byAttempt[p.AttemptID], p)
	}
	out := make(map[uuid.UUID]studentWork, len(attempts))
	for i := range attempts {
		at := &attempts[i]
		out[at.StudentID] = newWork(at, byAttempt[at.ID])
	}
	return out
}

// ownedAssignment — назначение учителя вместе с классом. Чужое или
// несуществующее — 404: существование чужого урока учителя не касается.
func (s *Service) ownedAssignment(ctx context.Context, actor iam.Actor, id uuid.UUID) (Assignment, ClassInfo, error) {
	a, err := s.store.AssignmentByID(ctx, s.pool, actor.OrgID, id)
	if errors.Is(err, ErrNotFound) {
		return Assignment{}, ClassInfo{}, httpx.NotFound("Назначение не найдено")
	}
	if err != nil {
		return Assignment{}, ClassInfo{}, httpx.Internal("Не удалось прочитать назначение").Wrap(err)
	}
	class, err := s.classes.OwnedClass(ctx, actor, a.ClassID)
	if err != nil {
		return Assignment{}, ClassInfo{}, httpx.NotFound("Назначение не найдено")
	}
	return a, class, nil
}

// ownedIssue — выдача урока учителя.
func (s *Service) ownedIssue(ctx context.Context, actor iam.Actor, id uuid.UUID) (Issue, Assignment, ClassInfo, error) {
	is, err := s.store.IssueByID(ctx, s.pool, actor.OrgID, id)
	if errors.Is(err, ErrNotFound) {
		return Issue{}, Assignment{}, ClassInfo{}, httpx.NotFound("Выдача не найдена")
	}
	if err != nil {
		return Issue{}, Assignment{}, ClassInfo{}, httpx.Internal("Не удалось прочитать выдачу").Wrap(err)
	}
	a, class, err := s.ownedAssignment(ctx, actor, is.AssignmentID)
	if err != nil {
		return Issue{}, Assignment{}, ClassInfo{}, httpx.NotFound("Выдача не найдена")
	}
	return is, a, class, nil
}

// loadClasswork дочитывает к назначению всё, что нужно экрану учителя.
//
// Число запросов не зависит ни от числа учеников, ни от числа заданий:
// версия, состав класса, выдачи, попытки, прогресс и «в сети» — по одному
// пакетному запросу. Экран опрашивает сервер раз в 5 секунд, и N+1 здесь
// умножился бы на каждый открытый урок в школе.
func (s *Service) loadClasswork(ctx context.Context, actor iam.Actor, a Assignment, class ClassInfo) (classwork, error) {
	c := classwork{assignment: a, class: class}

	versions, err := s.lessons.VersionsInfo(ctx, actor.OrgID, []uuid.UUID{a.LessonVersionID})
	if err != nil {
		return classwork{}, err
	}
	c.version = versions[a.LessonVersionID]

	if c.tasks, err = s.tasksOf(ctx, actor.OrgID, a.LessonVersionID); err != nil {
		return classwork{}, err
	}
	issues, err := s.store.IssuesOfAssignments(ctx, s.pool, actor.OrgID, []uuid.UUID{a.ID})
	if err != nil {
		return classwork{}, httpx.Internal("Не удалось прочитать выдачи").Wrap(err)
	}
	c.issues = issues[a.ID]

	if c.members, err = s.classes.Roster(ctx, actor, a.ClassID); err != nil {
		return classwork{}, err
	}
	if c.attempts, err = s.store.AttemptsOfAssignment(ctx, s.pool, actor.OrgID, a.ID); err != nil {
		return classwork{}, httpx.Internal("Не удалось прочитать прогресс").Wrap(err)
	}
	if c.progress, err = s.store.ProgressOfAssignment(ctx, s.pool, actor.OrgID, a.ID); err != nil {
		return classwork{}, httpx.Internal("Не удалось прочитать прогресс").Wrap(err)
	}
	ids := make([]uuid.UUID, 0, len(c.members))
	for _, m := range c.members {
		ids = append(ids, m.ID)
	}
	if c.presence, err = s.store.PresenceOf(ctx, s.pool, actor.OrgID, ids); err != nil {
		return classwork{}, httpx.Internal("Не удалось прочитать активность учеников").Wrap(err)
	}
	return c, nil
}

// tasksOf — задания одной версии урока.
func (s *Service) tasksOf(ctx context.Context, orgID, versionID uuid.UUID) ([]LessonTask, error) {
	tasks, err := s.lessons.LessonTasks(ctx, orgID, []uuid.UUID{versionID})
	if err != nil {
		return nil, err
	}
	return tasks[versionID], nil
}

// issueViews — выдачи урока для экрана учителя.
//
// «Начата» — по прогрессу всех попыток, включая учеников, которых в классе
// уже нет: их работа была, и отменой её не сотрёшь. «Сделали все» — по
// нынешнему составу: рядом стоит «из 8», и считать надо тех же восьмерых.
func issueViews(b lessonBoard, works map[uuid.UUID]studentWork, members []ClassMember,
	progress []TaskProgress) []IssueView {
	started := make(map[uuid.UUID]bool, len(b.issues))
	for _, p := range progress {
		if p.IssueID != nil {
			started[*p.IssueID] = true
		}
	}
	out := make([]IssueView, 0, len(b.issues))
	for i, is := range b.issues {
		v := IssueView{Issue: is, TaskKeys: b.issueKeys(i), Open: b.open[i], Started: started[is.ID]}
		for _, m := range members {
			if doneAll(b, works[m.ID], v.TaskKeys) {
				v.DoneStudents++
			}
		}
		out = append(out, v)
	}
	return out
}

// doneAll — ученик сделал все перечисленные задания.
func doneAll(b lessonBoard, w studentWork, keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	for _, k := range keys {
		t, ok := b.task(k)
		if !ok || !w.status(t).Done {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Выдать, завершить, продлить, отменить
// ---------------------------------------------------------------------------

// IssueTasks выдаёт классу отмеченные задания: на уроке — сразу и до конца
// дня, на дом — до конца выбранного дня.
func (s *Service) IssueTasks(ctx context.Context, actor iam.Actor, assignmentID uuid.UUID,
	in IssueInput) (IssueView, error) {
	a, class, err := s.ownedAssignment(ctx, actor, assignmentID)
	if err != nil {
		return IssueView{}, err
	}
	if a.State == StateClosed {
		return IssueView{}, httpx.Conflict(msgLessonRemoved)
	}
	kind := strings.TrimSpace(in.Kind)
	if kind != IssueClass && kind != IssueHome {
		return IssueView{}, httpx.FieldError("kind", "Выберите, как выдать: на уроке или на дом")
	}

	c, err := s.loadClasswork(ctx, actor, a, class)
	if err != nil {
		return IssueView{}, err
	}
	now := time.Now()
	b := c.board(now)

	fields := map[string]string{}
	keys := uniqueKeys(in.TaskKeys)
	if msg := checkTaskKeys(b, keys); msg != "" {
		fields["task_keys"] = msg
	}
	var due time.Time
	if kind == IssueClass {
		due = classDueAt(now, s.zone)
	} else if day, msg := s.dueDay(in.DueDate, now); msg != "" {
		fields["due_date"] = msg
	} else {
		due = endOfDay(day, s.zone)
	}
	if len(fields) > 0 {
		return IssueView{}, httpx.Validation("Проверьте выдачу", fields)
	}
	if _, ok := b.home(); ok && kind == IssueHome {
		return IssueView{}, httpx.Conflict(msgHomeExists)
	}

	// В базу ключи ложатся в порядке урока — так их проще читать глазами.
	slices.SortFunc(keys, func(x, y string) int { return b.index[x] - b.index[y] })
	is := Issue{
		ID: idgen.New(), OrgID: actor.OrgID, AssignmentID: a.ID, Kind: kind,
		OpenedAt: now, DueAt: &due, CreatedBy: actor.UserID,
	}
	switch err := s.store.CreateIssue(ctx, s.pool, is, keys); {
	case errors.Is(err, ErrHomeTaken):
		return IssueView{}, httpx.Conflict(msgHomeExists)
	case errors.Is(err, ErrTaskTaken):
		// Те же задания только что выдали из соседней вкладки.
		return IssueView{}, httpx.FieldError("task_keys", msgTaskTaken)
	case err != nil:
		return IssueView{}, httpx.Internal("Не удалось выдать задания").Wrap(err)
	}
	recipients := make([]uuid.UUID, 0, len(c.members))
	for _, member := range c.members {
		recipients = append(recipients, member.ID)
	}
	telemetry.Emit(ctx, telemetry.Event{Name: "tasks_issued", EntityID: is.ID, LessonID: c.version.LessonID, ClassID: a.ClassID, AssignmentID: a.ID, Recipients: recipients, TaskKeys: keys})
	return s.freshIssueView(ctx, actor, c, is.ID)
}

// uniqueKeys убирает пустые ключи и повторы, сохраняя порядок.
func uniqueKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// checkTaskKeys — что не так с отмеченными заданиями; "" — всё в порядке.
func checkTaskKeys(b lessonBoard, keys []string) string {
	if len(keys) == 0 {
		return msgNoTasks
	}
	for _, k := range keys {
		if _, ok := b.task(k); !ok {
			return msgNoSuchTask
		}
	}
	for _, k := range keys {
		if _, taken := b.issueOf(k); taken {
			return msgTaskTaken
		}
	}
	return ""
}

// dueDay разбирает срок домашней работы. Второе значение — отказ словами
// контракта; пусто — срок годится.
func (s *Service) dueDay(raw string, now time.Time) (time.Time, string) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, msgDueRequired
	}
	day, ok := parseDueDate(raw, s.zone)
	if !ok {
		return time.Time{}, msgDueFormat
	}
	if beforeToday(day, now, s.zone) {
		return time.Time{}, msgDueBeforeToday
	}
	return day, ""
}

// freshIssueView перечитывает выдачи урока и отдаёт одну из них: после
// правки у выдачи новые сроки, а у урока — новый расклад заданий.
func (s *Service) freshIssueView(ctx context.Context, actor iam.Actor, c classwork, issueID uuid.UUID) (IssueView, error) {
	issues, err := s.store.IssuesOfAssignments(ctx, s.pool, actor.OrgID, []uuid.UUID{c.assignment.ID})
	if err != nil {
		return IssueView{}, httpx.Internal("Не удалось прочитать выдачи").Wrap(err)
	}
	c.issues = issues[c.assignment.ID]
	for _, v := range issueViews(c.board(time.Now()), c.works(), c.members, c.progress) {
		if v.Issue.ID == issueID {
			return v, nil
		}
	}
	return IssueView{}, httpx.NotFound("Выдача не найдена")
}

// FinishIssue — «Завершить работу на уроке»: задания выдачи закрываются у
// всех учеников сразу. Всё, что они успели ответить, остаётся.
func (s *Service) FinishIssue(ctx context.Context, actor iam.Actor, issueID uuid.UUID) (IssueView, error) {
	is, a, class, err := s.ownedIssue(ctx, actor, issueID)
	if err != nil {
		return IssueView{}, err
	}
	if is.Kind != IssueClass {
		return IssueView{}, httpx.Conflict(msgFinishOnlyClass)
	}
	now := time.Now()
	if a.State == StateClosed || !is.Open(now) {
		return IssueView{}, httpx.Conflict(msgClassDone)
	}
	finished, err := s.store.FinishIssue(ctx, s.pool, actor.OrgID, is.ID, now)
	if err != nil {
		return IssueView{}, httpx.Internal("Не удалось завершить работу на уроке").Wrap(err)
	}
	if !finished {
		return IssueView{}, httpx.Conflict(msgClassDone)
	}
	c, err := s.loadClasswork(ctx, actor, a, class)
	if err != nil {
		return IssueView{}, err
	}
	return s.freshIssueView(ctx, actor, c, is.ID)
}

// ExtendIssue переносит срок домашней работы. Если срок уже прошёл, задания
// снова открываются ученикам — открытость считается по сроку при чтении.
func (s *Service) ExtendIssue(ctx context.Context, actor iam.Actor, issueID uuid.UUID,
	dueDate string) (IssueView, error) {
	is, a, class, err := s.ownedIssue(ctx, actor, issueID)
	if err != nil {
		return IssueView{}, err
	}
	if is.Kind != IssueHome {
		return IssueView{}, httpx.Conflict(msgExtendOnlyHome)
	}
	// Снятый урок не оживает продлением: ученики его уже не видят, а
	// «открытая домашка» в уроке, которого у класса нет, — путаница.
	if a.State == StateClosed {
		return IssueView{}, httpx.Conflict(msgLessonRemoved)
	}
	now := time.Now()
	if strings.TrimSpace(dueDate) == "" {
		return IssueView{}, httpx.FieldError("due_date", msgDueRequired)
	}
	day, ok := parseDueDate(dueDate, s.zone)
	if !ok {
		return IssueView{}, httpx.FieldError("due_date", msgDueFormat)
	}
	due := endOfDay(day, s.zone)
	// «Без срока» у перенесённого назначения продлевать некуда: любая дата
	// была бы сокращением, а не продлением.
	if is.DueAt == nil || !due.After(*is.DueAt) {
		return IssueView{}, httpx.FieldError("due_date", msgDueNotLater)
	}
	// Продление в прошлое ничего не открыло бы: срок прошёл бы снова.
	if beforeToday(day, now, s.zone) {
		return IssueView{}, httpx.FieldError("due_date", msgDueBeforeToday)
	}
	extended, err := s.store.ExtendIssue(ctx, s.pool, actor.OrgID, is.ID, due)
	if err != nil {
		return IssueView{}, httpx.Internal("Не удалось продлить срок").Wrap(err)
	}
	if !extended {
		return IssueView{}, httpx.FieldError("due_date", msgDueNotLater)
	}
	c, err := s.loadClasswork(ctx, actor, a, class)
	if err != nil {
		return IssueView{}, err
	}
	return s.freshIssueView(ctx, actor, c, is.ID)
}

// CancelIssue отменяет выдачу, пока её никто не начал: задания снова «не
// выданы» и пропадают у учеников.
//
// Начатую — нельзя: за ней ответы учеников, а отмена означала бы, что их
// работа повисла в задании, которого у класса нет.
func (s *Service) CancelIssue(ctx context.Context, actor iam.Actor, issueID uuid.UUID) error {
	is, a, _, err := s.ownedIssue(ctx, actor, issueID)
	if err != nil {
		return err
	}
	if a.State == StateClosed {
		return httpx.Conflict(msgLessonRemoved)
	}
	switch err := s.store.CancelIssue(ctx, s.pool, actor.OrgID, is.ID); {
	case errors.Is(err, ErrIssueStarted):
		return httpx.Conflict(msgIssueStarted)
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound("Выдача не найдена")
	case err != nil:
		return httpx.Internal("Не удалось отменить выдачу").Wrap(err)
	}
	telemetry.Emit(ctx, telemetry.Event{Name: "issue_cancelled", EntityID: issueID, AssignmentID: a.ID, ClassID: a.ClassID})
	return nil
}

// ---------------------------------------------------------------------------
// Экран «Урок в классе»
// ---------------------------------------------------------------------------

// LiveView — экран «Урок в классе» целиком.
type LiveView struct {
	Assignment Assignment
	Version    VersionInfo
	Class      ClassInfo
	Now        time.Time
	Tasks      []LiveTask
	Issues     []IssueView
	HasHome    bool
	Students   []LiveStudent
}

// LiveTask — задание урока в списке слева.
type LiveTask struct {
	LessonTask
	// IssueID — выдача, в которой задание выдано; nil — не выдано.
	IssueID *uuid.UUID
	// DoneStudents — «сделали X из 8».
	DoneStudents int
}

// LiveStudent — строка таблицы «Как проходит класс».
type LiveStudent struct {
	Member     ClassMember
	Online     bool
	LastSeenAt *time.Time
	// Cells — только по выданным заданиям, по ключу задания.
	Cells map[string]Cell
}

// Live собирает экран «Урок в классе».
func (s *Service) Live(ctx context.Context, actor iam.Actor, assignmentID uuid.UUID) (LiveView, error) {
	a, class, err := s.ownedAssignment(ctx, actor, assignmentID)
	if err != nil {
		return LiveView{}, err
	}
	c, err := s.loadClasswork(ctx, actor, a, class)
	if err != nil {
		return LiveView{}, err
	}
	return buildLive(c, time.Now()), nil
}

// buildLive — арифметика экрана без базы: что выдано, кто что сделал, кто в
// сети.
func buildLive(c classwork, now time.Time) LiveView {
	b := c.board(now)
	works := c.works()
	members := sortedMembers(c.members)

	out := LiveView{
		Assignment: c.assignment, Version: c.version, Class: c.class, Now: now,
		Issues: issueViews(b, works, members, c.progress),
	}
	_, out.HasHome = b.home()

	out.Tasks = make([]LiveTask, 0, len(c.tasks))
	for _, t := range c.tasks {
		lt := LiveTask{LessonTask: t}
		if i, ok := b.issueOf(t.Key); ok {
			id := b.issues[i].ID
			lt.IssueID = &id
		}
		for _, m := range members {
			if works[m.ID].status(t).Done {
				lt.DoneStudents++
			}
		}
		out.Tasks = append(out.Tasks, lt)
	}

	out.Students = make([]LiveStudent, 0, len(members))
	for _, m := range members {
		st := LiveStudent{Member: m, Cells: map[string]Cell{}}
		st.LastSeenAt, st.Online = seen(c.presence, m.ID, now)
		w := works[m.ID]
		for _, t := range c.tasks {
			i, issued := b.issueOf(t.Key)
			if !issued {
				continue
			}
			st.Cells[t.Key] = cellOf(t, w.status(t), b.closed[i])
		}
		out.Students = append(out.Students, st)
	}
	return out
}

// seen — когда ученик был активен и в сети ли он сейчас.
func seen(presence map[uuid.UUID]time.Time, id uuid.UUID, now time.Time) (*time.Time, bool) {
	at, ok := presence[id]
	if !ok {
		return nil, false
	}
	return &at, now.Sub(at) < onlineWindow
}

// sortedMembers — состав класса по имени: учитель ищет ученика глазами, и
// строки не должны прыгать при каждом обновлении таблицы.
func sortedMembers(members []ClassMember) []ClassMember {
	out := slices.Clone(members)
	slices.SortStableFunc(out, func(x, y ClassMember) int {
		if c := strings.Compare(nameKey(x.DisplayName), nameKey(y.DisplayName)); c != 0 {
			return c
		}
		return strings.Compare(x.PublicTag, y.PublicTag)
	})
	return out
}

// nameKey — имя для сортировки: без регистра и с «ё» на месте «е», как в
// классном журнале.
func nameKey(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "ё", "е")
}

// ---------------------------------------------------------------------------
// Панель ученика
// ---------------------------------------------------------------------------

// StudentPanel — ответы одного ученика по выданным заданиям.
type StudentPanel struct {
	Member     ClassMember
	Online     bool
	LastSeenAt *time.Time
	Tasks      []PanelTask
}

// PanelTask — задание в панели ученика.
type PanelTask struct {
	Task    LessonTask
	IssueID uuid.UUID
	Cell    Cell
	// Minutes — от открытия до «сделано», с округлением вверх; nil — не
	// сделано.
	Minutes *int
	// Questions — отвеченные вопросы упражнения, в порядке упражнения.
	Questions []AnsweredQuestion
}

// AnsweredQuestion — ответ ученика словами: текст вопроса, его ответ и
// верный ответ, а не идентификаторы вариантов.
type AnsweredQuestion struct {
	Prompt   string
	Answer   string
	Correct  bool
	Expected string
}

// StudentPanel собирает панель ученика на экране «Урок в классе».
//
// Ученик ищется в нынешнем составе класса: панель открывается кликом по
// строке таблицы, а таблица ведётся по составу. Чужой ученик — 404.
func (s *Service) StudentPanel(ctx context.Context, actor iam.Actor, assignmentID,
	studentID uuid.UUID) (StudentPanel, error) {
	a, class, err := s.ownedAssignment(ctx, actor, assignmentID)
	if err != nil {
		return StudentPanel{}, err
	}
	c, err := s.loadClasswork(ctx, actor, a, class)
	if err != nil {
		return StudentPanel{}, err
	}
	idx := slices.IndexFunc(c.members, func(m ClassMember) bool { return m.ID == studentID })
	if idx < 0 {
		return StudentPanel{}, httpx.NotFound("Ученик не найден")
	}
	// Тексты вопросов и верные ответы — у content, отдельным методом для
	// учителя: ключ ответов не ходит через ручки ученика ни в каком виде.
	reviews, err := s.lessons.ExerciseReviews(ctx, actor.OrgID, a.LessonVersionID)
	if err != nil {
		return StudentPanel{}, err
	}
	return buildPanel(c, c.members[idx], reviews, time.Now()), nil
}

func buildPanel(c classwork, m ClassMember, reviews map[uuid.UUID]ExerciseReview, now time.Time) StudentPanel {
	b := c.board(now)
	w := c.works()[m.ID]
	out := StudentPanel{Member: m, Tasks: []PanelTask{}}
	out.LastSeenAt, out.Online = seen(c.presence, m.ID, now)

	for _, t := range c.tasks {
		i, issued := b.issueOf(t.Key)
		if !issued {
			continue
		}
		st := w.status(t)
		pt := PanelTask{
			Task: t, IssueID: b.issues[i].ID, Cell: cellOf(t, st, b.closed[i]),
			Minutes: st.minutes(), Questions: []AnsweredQuestion{},
		}
		if t.Kind == TaskExercise {
			pt.Questions = answeredQuestions(t, reviews[t.ExerciseID], w.answers)
		}
		out.Tasks = append(out.Tasks, pt)
	}
	return out
}

// answeredQuestions — отвеченные вопросы упражнения словами.
//
// Верный ответ берётся из ключа (через content), а если ключа на вопрос
// нет — из того, что ученику показали после ответа.
func answeredQuestions(t LessonTask, review ExerciseReview, answers map[string]GivenAnswer) []AnsweredQuestion {
	out := []AnsweredQuestion{}
	prefix := t.ExerciseID.String() + ":"
	for _, q := range review.Questions {
		given, ok := answers[prefix+q.ItemID]
		if !ok {
			continue
		}
		expected := q.Expected
		if expected == "" {
			expected = q.text(given.Expected)
		}
		out = append(out, AnsweredQuestion{
			Prompt: q.Prompt, Answer: q.text(given.Answer), Correct: given.Correct, Expected: expected,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Строка урока на вкладке «Уроки»
// ---------------------------------------------------------------------------

// AssignmentSummary — «выдано N из 8», «идёт урок», «на дом до 03.10».
type AssignmentSummary struct {
	TasksTotal  int
	TasksIssued int
	ClassOpen   bool
	// Home — домашняя работа; nil — её нет.
	Home   *HomeSummary
	Legacy bool
}

// HomeSummary — домашняя работа в строке урока.
type HomeSummary struct {
	DueAt *time.Time
	Open  bool
}

// summaries считает строки уроков пачкой: выдачи всех назначений — одним
// запросом, задания всех версий — одним вызовом content.
func (s *Service) summaries(ctx context.Context, orgID uuid.UUID,
	items []Assignment) (map[uuid.UUID]AssignmentSummary, error) {
	out := make(map[uuid.UUID]AssignmentSummary, len(items))
	if len(items) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(items))
	versionIDs := make([]uuid.UUID, 0, len(items))
	for _, a := range items {
		ids = append(ids, a.ID)
		versionIDs = append(versionIDs, a.LessonVersionID)
	}
	issues, err := s.store.IssuesOfAssignments(ctx, s.pool, orgID, ids)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать выдачи").Wrap(err)
	}
	tasks, err := s.lessons.LessonTasks(ctx, orgID, versionIDs)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, a := range items {
		out[a.ID] = summarize(newBoard(tasks[a.LessonVersionID], issues[a.ID], now, a.State == StateClosed))
	}
	return out, nil
}

func summarize(b lessonBoard) AssignmentSummary {
	out := AssignmentSummary{TasksTotal: len(b.tasks), TasksIssued: b.issuedCount()}
	for i, is := range b.issues {
		if is.Kind == IssueClass && b.open[i] {
			out.ClassOpen = true
		}
		if is.Legacy {
			out.Legacy = true
		}
	}
	if i, ok := b.home(); ok {
		out.Home = &HomeSummary{DueAt: b.issues[i].DueAt, Open: b.open[i]}
	}
	return out
}
