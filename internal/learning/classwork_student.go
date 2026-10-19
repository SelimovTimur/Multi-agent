package learning

// «Урок в классе» глазами ученика: что выдано на уроке и что на дом, какие
// задания раздела открыты, и отметки «открыл» и «сделал».

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/NATIVE/NATIVE/internal/platform/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// msgNothingIssued — урок назначен, но учитель ещё ничего не выдал: ученик
// урока не видит и открыть его не может.
const msgNothingIssued = "Учитель ещё не выдал задания по этому уроку"

// StudentLesson — строка «Моих занятий».
type StudentLesson struct {
	Assignment Assignment
	Version    VersionInfo
	Class      ClassInfo
	Attempt    *Attempt
	TasksTotal int
	TasksDone  int
	// ClassSection — открытые выдачи «на уроке» (их бывает несколько); nil —
	// открытых нет.
	ClassSection *LessonSection
	// HomeSection — открытая домашняя работа; nil — её нет или срок прошёл.
	HomeSection *LessonSection
	// Finished — все задания урока выданы и все выдачи закрыты: урок уходит в
	// «Завершённые». Снятый с класса урок тоже завершён — больше по нему
	// ничего не придёт.
	Finished bool
	// Issues — выдачи ученика по уроку со счётом и звёздами.
	Issues []StudentIssue
	// lastIssued — когда по уроку выдавали последний раз: свежие выше.
	lastIssued time.Time
}

// LessonSection — раздел урока у ученика: «на уроке» или «на дом».
type LessonSection struct {
	// Tasks — сколько заданий в разделе, Left — сколько из них не сделано.
	Tasks int
	Left  int
	// Begun — ученик сделал или открыл хоть одно задание раздела: кнопка
	// «Продолжить», а не «Начать».
	Begun bool
	// DueAt — срок домашней работы; nil — «без срока».
	DueAt *time.Time
	// Stars — звёзды выдачи раздела. Экран показывает их у сделанного
	// раздела, рядом с «готово ✓».
	Stars []Star
}

// MyLessons — «Мои занятия»: уроки, по которым выдано хоть одно задание.
//
// Запросов столько же при любом числе уроков: классы, назначения, выдачи,
// версии, задания, попытки и прогресс — по одному пакетному на всё.
func (s *Service) MyLessons(ctx context.Context, actor iam.Actor, classFilter uuid.UUID) ([]StudentLesson, error) {
	classes, err := s.classes.StudentClasses(ctx, actor)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]ClassInfo, len(classes))
	classIDs := make([]uuid.UUID, 0, len(classes))
	for _, c := range classes {
		if classFilter != uuid.Nil && c.ID != classFilter {
			continue
		}
		byID[c.ID] = c
		classIDs = append(classIDs, c.ID)
	}
	if len(classIDs) == 0 {
		return nil, nil
	}

	assignments, err := s.store.ListForClasses(ctx, s.pool, actor.OrgID, classIDs)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать задания").Wrap(err)
	}
	ids := make([]uuid.UUID, 0, len(assignments))
	for _, a := range assignments {
		ids = append(ids, a.ID)
	}
	issues, err := s.store.IssuesOfAssignments(ctx, s.pool, actor.OrgID, ids)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать задания").Wrap(err)
	}
	// Назначенный, но ни разу не выданный урок ученик не видит вовсе.
	assignments = slices.DeleteFunc(assignments, func(a Assignment) bool { return len(issues[a.ID]) == 0 })
	if len(assignments) == 0 {
		return nil, nil
	}

	versionIDs := make([]uuid.UUID, 0, len(assignments))
	for _, a := range assignments {
		versionIDs = append(versionIDs, a.LessonVersionID)
	}
	versions, err := s.lessons.VersionsInfo(ctx, actor.OrgID, versionIDs)
	if err != nil {
		return nil, err
	}
	tasks, err := s.lessons.LessonTasks(ctx, actor.OrgID, versionIDs)
	if err != nil {
		return nil, err
	}

	attempts, err := s.store.AttemptsOfStudent(ctx, s.pool, actor.OrgID, actor.StudentID)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать прогресс").Wrap(err)
	}
	byAssignment := make(map[uuid.UUID]*Attempt, len(attempts))
	attemptIDs := make([]uuid.UUID, 0, len(attempts))
	for i := range attempts {
		byAssignment[attempts[i].AssignmentID] = &attempts[i]
		attemptIDs = append(attemptIDs, attempts[i].ID)
	}
	progress, err := s.store.ProgressOfAttempts(ctx, s.pool, actor.OrgID, attemptIDs)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать прогресс").Wrap(err)
	}
	progressOf := make(map[uuid.UUID][]TaskProgress, len(attempts))
	for _, p := range progress {
		progressOf[p.AttemptID] = append(progressOf[p.AttemptID], p)
	}
	// Третья звезда — по разбору разговора. Читаются только счётчики, а не
	// сам разбор: список опрашивается раз в 5 секунд.
	words, err := s.store.ConversationWords(ctx, s.pool, actor.OrgID, attemptIDs)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать прогресс").Wrap(err)
	}

	now := time.Now()
	out := make([]StudentLesson, 0, len(assignments))
	for _, a := range assignments {
		attempt := byAssignment[a.ID]
		var own []TaskProgress
		var talk ConversationWords
		if attempt != nil {
			own, talk = progressOf[attempt.ID], words[attempt.ID]
		}
		item := studentLesson(a, tasks[a.LessonVersionID], issues[a.ID], attempt, own, talk, s.Zone(), now)
		item.Version, item.Class = versions[a.LessonVersionID], byID[a.ClassID]
		out = append(out, item)
	}
	slices.SortStableFunc(out, func(x, y StudentLesson) int { return y.lastIssued.Compare(x.lastIssued) })
	return out, nil
}

// studentLesson — арифметика строки «Моих занятий» без базы. words — итог
// разбора разговора попытки, zone — время школы.
func studentLesson(a Assignment, tasks []LessonTask, issues []Issue, attempt *Attempt,
	progress []TaskProgress, words ConversationWords, zone *time.Location, now time.Time) StudentLesson {
	removed := a.State == StateClosed
	b := newBoard(tasks, issues, now, removed)
	w := newWork(attempt, progress)

	out := StudentLesson{Assignment: a, Attempt: attempt, TasksTotal: len(tasks)}
	out.Issues = studentIssues(b, w, words, zone)
	for _, t := range tasks {
		if w.status(t).Done {
			out.TasksDone++
		}
	}
	var classIdx []int
	for i, is := range b.issues {
		if is.OpenedAt.After(out.lastIssued) {
			out.lastIssued = is.OpenedAt
		}
		if is.Kind == IssueClass && b.open[i] {
			classIdx = append(classIdx, i)
		}
	}
	if len(classIdx) > 0 {
		out.ClassSection = sectionOf(b, w, classIdx)
		if si, ok := sectionIssue(out.Issues, IssueClass); ok {
			out.ClassSection.Stars = si.Stars
		}
	}
	if i, ok := b.home(); ok && b.open[i] {
		out.HomeSection = sectionOf(b, w, []int{i})
		out.HomeSection.DueAt = b.issues[i].DueAt
		if si, ok := sectionIssue(out.Issues, IssueHome); ok {
			out.HomeSection.Stars = si.Stars
		}
	}
	out.Finished = b.lessonFinished(removed)
	return out
}

// sectionOf — сколько заданий в разделе, сколько осталось и начат ли он.
func sectionOf(b lessonBoard, w studentWork, issues []int) *LessonSection {
	sec := &LessonSection{}
	var tasks []LessonTask
	for _, t := range b.tasks {
		if i, ok := b.issueOf(t.Key); ok && slices.Contains(issues, i) {
			tasks = append(tasks, t)
		}
	}
	// Цели урока — вводный экран, не задание: в «4 задания» не входят, но
	// открытые цели — уже начало раздела.
	intro := onlyIntro(tasks)
	for _, t := range tasks {
		st := w.status(t)
		if st.Done || st.Opened {
			sec.Begun = true
		}
		if !countsAsTask(t, intro) {
			continue
		}
		sec.Tasks++
		if !st.Done {
			sec.Left++
		}
	}
	return sec
}

// ---------------------------------------------------------------------------
// Результаты урока
// ---------------------------------------------------------------------------

// LessonResults — «Результаты урока»: выдачи ученика по уроку со счётом и
// звёздами. Только просмотр: ответить заново отсюда нельзя.
type LessonResults struct {
	Assignment Assignment
	Version    VersionInfo
	Class      ClassInfo
	// Attempt — nil, если ученик урок так и не открыл.
	Attempt  *Attempt
	Issues   []StudentIssue
	Finished bool
}

// LessonResults собирает результаты урока ученика.
//
// Ответы и тексты вопросов сюда не входят: их экран берёт из попытки и
// содержимого урока теми же ручками, что и прохождение. Здесь — то, чего у
// ученика больше нигде нет: закрытые выдачи, их звёзды и счёт.
func (s *Service) LessonResults(ctx context.Context, actor iam.Actor, assignmentID uuid.UUID) (LessonResults, error) {
	a, class, err := s.studentAssignmentClass(ctx, actor, assignmentID)
	if err != nil {
		return LessonResults{}, err
	}
	issues, err := s.store.IssuesOfAssignments(ctx, s.pool, actor.OrgID, []uuid.UUID{a.ID})
	if err != nil {
		return LessonResults{}, httpx.Internal("Не удалось прочитать задания").Wrap(err)
	}
	// Ни разу не выданный урок ученик не видит — и его результатов тоже.
	if len(issues[a.ID]) == 0 {
		return LessonResults{}, httpx.Forbidden(msgNothingIssued)
	}
	tasks, err := s.tasksOf(ctx, actor.OrgID, a.LessonVersionID)
	if err != nil {
		return LessonResults{}, err
	}
	versions, err := s.lessons.VersionsInfo(ctx, actor.OrgID, []uuid.UUID{a.LessonVersionID})
	if err != nil {
		return LessonResults{}, err
	}

	out := LessonResults{Assignment: a, Version: versions[a.LessonVersionID], Class: class}
	var progress []TaskProgress
	words := ConversationWords{State: WordsNone}
	attempt, err := s.store.AttemptOfStudent(ctx, s.pool, actor.OrgID, a.ID, actor.StudentID)
	switch {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return LessonResults{}, httpx.Internal("Не удалось прочитать прогресс").Wrap(err)
	default:
		out.Attempt = &attempt
		if progress, err = s.store.ProgressOfAttempts(ctx, s.pool, actor.OrgID, []uuid.UUID{attempt.ID}); err != nil {
			return LessonResults{}, httpx.Internal("Не удалось прочитать прогресс").Wrap(err)
		}
		talk, err := s.store.ConversationWords(ctx, s.pool, actor.OrgID, []uuid.UUID{attempt.ID})
		if err != nil {
			return LessonResults{}, httpx.Internal("Не удалось прочитать прогресс").Wrap(err)
		}
		if w, ok := talk[attempt.ID]; ok {
			words = w
		}
	}

	removed := a.State == StateClosed
	b := newBoard(tasks, issues[a.ID], time.Now(), removed)
	out.Issues = studentIssues(b, newWork(out.Attempt, progress), words, s.Zone())
	out.Finished = b.lessonFinished(removed)
	return out, nil
}

// ---------------------------------------------------------------------------
// Задания раздела
// ---------------------------------------------------------------------------

// SectionTasks — задания раздела на экране прохождения.
type SectionTasks struct {
	// Section — «class», «home» или пусто (все открытые задания).
	Section string
	Open    bool
	// ClosedReason — почему раздел пуст: ReasonClassFinished,
	// ReasonHomeExpired или пусто — по другой причине.
	ClosedReason string
	DueAt        *time.Time
	Tasks        []StudentTask
}

// StudentTask — задание у ученика: что это и сколько сделано.
type StudentTask struct {
	Task     LessonTask
	Answered int
	Done     bool
}

// SectionTasks отдаёт открытые задания раздела в порядке урока.
//
// Экран опрашивает её раз в 5 секунд: так новое задание от учителя
// появляется само, а «Учитель завершил работу на уроке» ученик видит, не
// дожидаясь отказа на ответ.
func (s *Service) SectionTasks(ctx context.Context, actor iam.Actor, attemptID uuid.UUID,
	section string) (SectionTasks, error) {
	if section != "" && section != IssueClass && section != IssueHome {
		return SectionTasks{}, httpx.BadRequest("Раздел — class или home")
	}
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return SectionTasks{}, err
	}
	a, b, err := s.attemptBoard(ctx, actor, attempt)
	if err != nil {
		return SectionTasks{}, err
	}
	progress, err := s.store.ProgressOfAttempts(ctx, s.pool, actor.OrgID, []uuid.UUID{attempt.ID})
	if err != nil {
		return SectionTasks{}, httpx.Internal("Не удалось прочитать прогресс").Wrap(err)
	}
	return buildSection(section, b, newWork(&attempt, progress), a.State == StateClosed, time.Now()), nil
}

func buildSection(section string, b lessonBoard, w studentWork, removed bool, now time.Time) SectionTasks {
	out := SectionTasks{Section: section, Tasks: []StudentTask{}}
	var selected []int
	for i, is := range b.issues {
		if b.open[i] && (section == "" || is.Kind == section) {
			selected = append(selected, i)
		}
	}
	out.Open = len(selected) > 0
	for _, t := range b.tasks {
		i, ok := b.issueOf(t.Key)
		if !ok || !slices.Contains(selected, i) {
			continue
		}
		st := w.status(t)
		out.Tasks = append(out.Tasks, StudentTask{Task: t, Answered: st.Answered, Done: st.Done})
	}

	switch section {
	case IssueClass:
		for _, i := range selected {
			if due := b.issues[i].DueAt; due != nil && (out.DueAt == nil || due.Before(*out.DueAt)) {
				out.DueAt = due
			}
		}
		// Раздел опустел потому, что учитель завершил работу на уроке или
		// кончился день. У снятого урока причина другая — её не называем.
		if !out.Open && !removed && slices.ContainsFunc(b.issues, func(is Issue) bool { return is.Kind == IssueClass }) {
			out.ClosedReason = ReasonClassFinished
		}
	case IssueHome:
		if i, ok := b.home(); ok {
			home := b.issues[i]
			out.DueAt = home.DueAt
			if !out.Open && !removed && home.ClosedAt == nil && home.DueAt != nil && !now.Before(*home.DueAt) {
				out.ClosedReason = ReasonHomeExpired
			}
		}
	}
	return out
}

// attemptBoard — назначение попытки и доска его заданий.
func (s *Service) attemptBoard(ctx context.Context, actor iam.Actor, attempt Attempt) (Assignment, lessonBoard, error) {
	a, err := s.store.AssignmentByID(ctx, s.pool, actor.OrgID, attempt.AssignmentID)
	if err != nil {
		return Assignment{}, lessonBoard{}, httpx.Internal("Не удалось прочитать урок").Wrap(err)
	}
	tasks, err := s.tasksOf(ctx, actor.OrgID, a.LessonVersionID)
	if err != nil {
		return Assignment{}, lessonBoard{}, err
	}
	issues, err := s.store.IssuesOfAssignments(ctx, s.pool, actor.OrgID, []uuid.UUID{a.ID})
	if err != nil {
		return Assignment{}, lessonBoard{}, httpx.Internal("Не удалось прочитать задания").Wrap(err)
	}
	return a, newBoard(tasks, issues[a.ID], time.Now(), a.State == StateClosed), nil
}

// openTaskOf — задание урока, которое ученик может делать прямо сейчас, и
// выдача, в которой оно открыто.
func openTaskOf(b lessonBoard, key string) (LessonTask, uuid.UUID, error) {
	t, ok := b.task(key)
	if !ok {
		return LessonTask{}, uuid.Nil, httpx.NotFound("Задание не найдено")
	}
	if err := b.gate(key); err != nil {
		return LessonTask{}, uuid.Nil, err
	}
	i, _ := b.issueOf(key)
	return t, b.issues[i].ID, nil
}

// OpenTask — ученик открыл задание: у учителя клетка становится «открыл».
// Повторный вызов ничего не меняет.
func (s *Service) OpenTask(ctx context.Context, actor iam.Actor, attemptID uuid.UUID, key string) error {
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return err
	}
	_, b, err := s.attemptBoard(ctx, actor, attempt)
	if err != nil {
		return err
	}
	_, issueID, err := openTaskOf(b, key)
	if err != nil {
		return err
	}
	return s.markOpened(ctx, actor, attempt.ID, key, issueID)
}

// TaskDone — задание без вопросов сделано: ученик нажал «Дальше». Упражнение
// с вопросами так не засчитывается — только ответами.
func (s *Service) TaskDone(ctx context.Context, actor iam.Actor, attemptID uuid.UUID, key string) error {
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return err
	}
	a, b, err := s.attemptBoard(ctx, actor, attempt)
	if err != nil {
		return err
	}
	if t, ok := b.task(key); ok && t.byAnswers() {
		return httpx.Conflict("Задание засчитывается по ответам")
	}
	_, issueID, err := openTaskOf(b, key)
	if err != nil {
		return err
	}
	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.requireOpenIssue(ctx, tx, actor.OrgID, issueID); err != nil {
			return err
		}
		// Блокировка попытки — та же, что берёт запись ответа: последнее
		// «Дальше» и последний ответ, пришедшие разом, иначе не увидели бы
		// друг друга, и урок остался бы незавершённым.
		locked, err := s.store.LockAttempt(ctx, tx, actor.OrgID, attempt.ID)
		if err != nil {
			return err
		}
		if err := s.store.MarkTaskProgress(ctx, tx, actor.OrgID, attempt.ID, key, issueID, true); err != nil {
			return err
		}
		_, err = s.completeIfDone(ctx, tx, actor.OrgID, locked, b.tasks)
		return err
	})
	switch {
	case errors.Is(err, ErrIssueGone):
		return httpx.Conflict(msgNotIssued)
	case err != nil:
		return wrapErr(err, "Не удалось отметить задание")
	}
	telemetry.Emit(ctx, telemetry.Event{Name: "task_completed", EntityID: issueID, Key: key, AssignmentID: a.ID, ClassID: a.ClassID, OwnerID: a.CreatedBy})
	return nil
}

// Проверка под блокировкой повторяет gate из снимка доски. Учитель мог
// завершить работу, пока сервер сверял ответ или ждал соединения с базой.
func (s *Service) requireOpenIssue(ctx context.Context, q pg.Querier, orgID, issueID uuid.UUID) error {
	return issueWriteError(s.store.LockOpenIssue(ctx, q, orgID, issueID), "Не удалось проверить доступ к заданию")
}

func issueWriteError(err error, message string) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrIssueGone):
		return httpx.Conflict(msgNotIssued).Wrap(err)
	case errors.Is(err, ErrClassFinished):
		return httpx.Conflict(msgClassFinished).Wrap(err)
	case errors.Is(err, ErrHomeExpired):
		return httpx.Conflict(msgHomeExpired).Wrap(err)
	case errors.Is(err, errDialogEnded):
		return httpx.Conflict("Разговор закончен — посмотри разбор").Wrap(err)
	default:
		return wrapErr(err, message)
	}
}

// completeIfDone завершает попытку, когда сделаны все задания урока: по
// завершённым попыткам считается аналитика класса. Возвращает, завершена ли
// попытка этим вызовом.
//
// Вызывается внутри транзакции, где попытка уже заблокирована, — иначе два
// последних задания, сделанные одновременно, посчитали бы каждое без
// другого.
func (s *Service) completeIfDone(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, attempt Attempt,
	tasks []LessonTask) (bool, error) {
	if attempt.FinishedAt != nil {
		return false, nil
	}
	progress, err := s.store.ProgressOfAttempts(ctx, tx, orgID, []uuid.UUID{attempt.ID})
	if err != nil {
		return false, err
	}
	if !newWork(&attempt, progress).allDone(tasks) {
		return false, nil
	}
	if err := s.store.CompleteAttempt(ctx, tx, orgID, attempt.ID); err != nil {
		return false, err
	}
	return true, nil
}

// SeenStudent отмечает, что ученик активен, — для «в сети» у учителя.
//
// Ошибка не мешает запросу ученика: «в сети» — подсказка учителю, а не
// условие работы урока.
func (s *Service) SeenStudent(ctx context.Context, actor iam.Actor) {
	if !actor.IsStudent() {
		return
	}
	if err := s.store.TouchPresence(ctx, s.pool, actor.OrgID, actor.StudentID); err != nil {
		logx.From(ctx).Warn("не удалось отметить активность ученика", "err", err)
	}
}
