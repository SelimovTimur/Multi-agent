package learning

// Ручки «Урока в классе». Поля, коды и тексты отказов — по контракту
// docs/backend/class-lessons-api.md: по нему же пишутся экраны учителя и
// ученика, и расхождение здесь ломает их молча.

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// mountClasswork вешает ручки выдачи заданий.
func (a *API) mountClasswork(r chi.Router) {
	g := a.guard

	// Учитель. Таблица класса и выдачи — работа с уроком; панель ученика —
	// его ответы, право то же, что у карточки ученика.
	g.Handle(r, http.MethodGet, "/assignments/{assignmentID}/live", iam.PermLessonsOwn, a.live)
	g.Handle(r, http.MethodPost, "/assignments/{assignmentID}/issues", iam.PermLessonsOwn, a.issue)
	g.Handle(r, http.MethodGet, "/assignments/{assignmentID}/students/{studentID}", iam.PermStudentsOwn, a.studentPanel)
	g.Handle(r, http.MethodPost, "/issues/{issueID}/finish", iam.PermLessonsOwn, a.finishIssue)
	g.Handle(r, http.MethodPost, "/issues/{issueID}/extend", iam.PermLessonsOwn, a.extendIssue)
	g.Handle(r, http.MethodDelete, "/issues/{issueID}", iam.PermLessonsOwn, a.cancelIssue)

	// Ученик. Прав из матрицы у него нет — принадлежность попытки проверяет
	// сервис.
	g.Handle(r, http.MethodGet, "/my/attempts/{attemptID}/tasks", iam.PermAuthenticated, a.sectionTasks)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/tasks/{taskKey}/open", iam.PermAuthenticated, a.openTask)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/tasks/{taskKey}/done", iam.PermAuthenticated, a.taskDone)
	g.Handle(r, http.MethodGet, "/my/lessons/{assignmentID}/results", iam.PermAuthenticated, a.lessonResults)
}

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

// ts — время с зоной школы. Зона сервера (в контейнере это UTC) ничего не
// говорит ни учителю, ни тому, кто разбирает ответ глазами: «23:59:59+03:00»
// читается как «конец дня», «20:59:59Z» — нет.
func (a *API) ts(t time.Time) string { return t.In(a.svc.Zone()).Format(time.RFC3339) }

func (a *API) tsp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := a.ts(*t)
	return &s
}

// taskDTO — объект «задание».
type taskDTO struct {
	Key       string `json:"key"`
	N         int    `json:"n"`
	Kind      string `json:"kind"`
	Type      string `json:"type"`
	TypeLabel string `json:"type_label"`
	Title     string `json:"title"`
	Questions int    `json:"questions"`
}

func toTaskDTO(t LessonTask) taskDTO {
	return taskDTO{
		Key: t.Key, N: t.N, Kind: t.Kind, Type: t.Type, TypeLabel: t.TypeLabel,
		Title: t.Title, Questions: t.Questions,
	}
}

// issueDTO — объект «выдача».
type issueDTO struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	TaskKeys     []string `json:"task_keys"`
	OpenedAt     string   `json:"opened_at"`
	DueAt        *string  `json:"due_at"`
	ClosedAt     *string  `json:"closed_at"`
	Open         bool     `json:"open"`
	Started      bool     `json:"started"`
	DoneStudents int      `json:"done_students"`
	Legacy       bool     `json:"legacy"`
}

func (a *API) issueDTO(v IssueView) issueDTO {
	keys := v.TaskKeys
	if keys == nil {
		keys = []string{}
	}
	return issueDTO{
		ID: v.Issue.ID.String(), Kind: v.Issue.Kind, TaskKeys: keys,
		OpenedAt: a.ts(v.Issue.OpenedAt), DueAt: a.tsp(v.Issue.DueAt), ClosedAt: a.tsp(v.Issue.ClosedAt),
		Open: v.Open, Started: v.Started, DoneStudents: v.DoneStudents, Legacy: v.Issue.Legacy,
	}
}

// cellDTO — объект «клетка».
type cellDTO struct {
	State    string `json:"state"`
	Answered int    `json:"answered"`
	Correct  int    `json:"correct"`
	Total    int    `json:"total"`
}

func toCellDTO(c Cell) cellDTO {
	return cellDTO{State: c.State, Answered: c.Answered, Correct: c.Correct, Total: c.Total}
}

// shortTag — тег ученика без решётки: «KZ4F», как в контракте. Решётку
// рисует экран («Айдар #KZ4F»), а в базе тег хранится вместе с ней.
func shortTag(tag string) string { return strings.TrimPrefix(tag, "#") }

func uuidPtr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// ---------------------------------------------------------------------------
// Учитель
// ---------------------------------------------------------------------------

// live — экран «Урок в классе». Опрашивается раз в 5 секунд.
func (a *API) live(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "assignmentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	v, err := a.svc.Live(r.Context(), iam.MustActor(r.Context()), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	type liveTask struct {
		taskDTO
		IssueID      *string `json:"issue_id"`
		DoneStudents int     `json:"done_students"`
	}
	tasks := make([]liveTask, 0, len(v.Tasks))
	for _, t := range v.Tasks {
		tasks = append(tasks, liveTask{
			taskDTO: toTaskDTO(t.LessonTask), IssueID: uuidPtr(t.IssueID), DoneStudents: t.DoneStudents,
		})
	}
	issues := make([]issueDTO, 0, len(v.Issues))
	for _, is := range v.Issues {
		issues = append(issues, a.issueDTO(is))
	}
	students := make([]map[string]any, 0, len(v.Students))
	for _, st := range v.Students {
		cells := make(map[string]cellDTO, len(st.Cells))
		for k, c := range st.Cells {
			cells[k] = toCellDTO(c)
		}
		students = append(students, map[string]any{
			"id": st.Member.ID.String(), "name": st.Member.DisplayName, "tag": shortTag(st.Member.PublicTag),
			"online": st.Online, "last_seen_at": a.tsp(st.LastSeenAt),
			"cells": cells,
		})
	}

	assignment := map[string]any{
		"id":                v.Assignment.ID.String(),
		"class_id":          v.Assignment.ClassID.String(),
		"lesson_id":         "",
		"lesson_version_id": v.Assignment.LessonVersionID.String(),
		"title":             v.Version.Name(),
		"level":             v.Version.Level,
		"version":           v.Version.Version,
		// Снят ли урок с класса. Без этого экран узнавал об этом только по
		// отказу 409 на очередное действие — а учитель мог смотреть на
		// таблицу снятого урока и не понимать, почему ничего не выдаётся.
		"state": v.Assignment.State,
	}
	if v.Version.LessonID != uuid.Nil {
		assignment["lesson_id"] = v.Version.LessonID.String()
	}
	// Ответ не кешируется: экран опрашивает его ради свежих данных.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"assignment":  assignment,
		"class":       map[string]any{"id": v.Class.ID.String(), "name": v.Class.Title},
		"server_time": a.ts(v.Now),
		"tasks":       tasks,
		"issues":      issues,
		"has_home":    v.HasHome,
		"students":    students,
	})
}

// issue — выдать отмеченные задания на уроке или на дом.
func (a *API) issue(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "assignmentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in struct {
		Kind     string   `json:"kind"`
		TaskKeys []string `json:"task_keys"`
		DueDate  string   `json:"due_date"`
	}
	if err := httpx.DecodeJSON(r, &in, 16*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	v, err := a.svc.IssueTasks(r.Context(), iam.MustActor(r.Context()), id,
		IssueInput{Kind: in.Kind, TaskKeys: in.TaskKeys, DueDate: in.DueDate})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"issue": a.issueDTO(v)})
}

func (a *API) finishIssue(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "issueID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	v, err := a.svc.FinishIssue(r.Context(), iam.MustActor(r.Context()), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"issue": a.issueDTO(v)})
}

func (a *API) extendIssue(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "issueID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in struct {
		DueDate string `json:"due_date"`
	}
	if err := httpx.DecodeJSON(r, &in, 1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	v, err := a.svc.ExtendIssue(r.Context(), iam.MustActor(r.Context()), id, in.DueDate)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"issue": a.issueDTO(v)})
}

func (a *API) cancelIssue(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "issueID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.svc.CancelIssue(r.Context(), iam.MustActor(r.Context()), id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// studentPanel — панель ученика на экране «Урок в классе».
func (a *API) studentPanel(w http.ResponseWriter, r *http.Request) {
	assignmentID, err := pathUUID(r, "assignmentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	studentID, err := pathUUID(r, "studentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, err := a.svc.StudentPanel(r.Context(), iam.MustActor(r.Context()), assignmentID, studentID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	type questionDTO struct {
		Prompt   string `json:"prompt"`
		Answer   string `json:"answer"`
		Correct  bool   `json:"correct"`
		Expected string `json:"expected"`
	}
	type panelTask struct {
		Key       string        `json:"key"`
		N         int           `json:"n"`
		Kind      string        `json:"kind"`
		Type      string        `json:"type"`
		Title     string        `json:"title"`
		IssueID   string        `json:"issue_id"`
		State     string        `json:"state"`
		Answered  int           `json:"answered"`
		Correct   int           `json:"correct"`
		Total     int           `json:"total"`
		Minutes   *int          `json:"minutes"`
		Questions []questionDTO `json:"questions"`
	}
	tasks := make([]panelTask, 0, len(p.Tasks))
	for _, t := range p.Tasks {
		qs := make([]questionDTO, 0, len(t.Questions))
		for _, q := range t.Questions {
			qs = append(qs, questionDTO{Prompt: q.Prompt, Answer: q.Answer, Correct: q.Correct, Expected: q.Expected})
		}
		tasks = append(tasks, panelTask{
			Key: t.Task.Key, N: t.Task.N, Kind: t.Task.Kind, Type: t.Task.Type, Title: t.Task.Title,
			IssueID: t.IssueID.String(), State: t.Cell.State,
			Answered: t.Cell.Answered, Correct: t.Cell.Correct, Total: t.Cell.Total,
			Minutes: t.Minutes, Questions: qs,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"student": map[string]any{
			"id": p.Member.ID.String(), "name": p.Member.DisplayName, "tag": shortTag(p.Member.PublicTag),
			"online": p.Online, "last_seen_at": a.tsp(p.LastSeenAt),
		},
		"tasks": tasks,
	})
}

// ---------------------------------------------------------------------------
// Ученик
// ---------------------------------------------------------------------------

// sectionTasks — открытые задания раздела. Опрашивается раз в 5 секунд.
func (a *API) sectionTasks(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	section := r.URL.Query().Get("section")
	v, err := a.svc.SectionTasks(r.Context(), actor, id, section)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	type studentTask struct {
		Key       string `json:"key"`
		N         int    `json:"n"`
		Kind      string `json:"kind"`
		Type      string `json:"type"`
		Title     string `json:"title"`
		Questions int    `json:"questions"`
		Answered  int    `json:"answered"`
		Done      bool   `json:"done"`
	}
	tasks := make([]studentTask, 0, len(v.Tasks))
	for _, t := range v.Tasks {
		tasks = append(tasks, studentTask{
			Key: t.Task.Key, N: t.Task.N, Kind: t.Task.Kind, Type: t.Task.Type, Title: t.Task.Title,
			Questions: t.Task.Questions, Answered: t.Answered, Done: t.Done,
		})
	}
	var sectionOut, reason *string
	if v.Section != "" {
		sectionOut = &v.Section
	}
	if v.ClosedReason != "" {
		reason = &v.ClosedReason
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"section":       sectionOut,
		"open":          v.Open,
		"closed_reason": reason,
		"due_at":        a.tsp(v.DueAt),
		"tasks":         tasks,
	})
}

func (a *API) openTask(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	if err := a.svc.OpenTask(r.Context(), actor, id, taskKeyParam(r)); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) taskDone(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	if err := a.svc.TaskDone(r.Context(), actor, id, taskKeyParam(r)); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// taskKeyParam — ключ задания из пути.
//
// Ключ упражнения — «ex:<uuid>», и клиент кодирует двоеточие (%3A). chi в
// таком случае сопоставляет маршрут по сырому пути и отдаёт параметр
// закодированным, поэтому раскодируем сами; некодированный ключ проходит
// как есть.
func taskKeyParam(r *http.Request) string {
	raw := chi.URLParam(r, "taskKey")
	if key, err := url.PathUnescape(raw); err == nil {
		return key
	}
	return raw
}

// ---------------------------------------------------------------------------
// «Мои занятия»
// ---------------------------------------------------------------------------

type sectionDTO struct {
	Tasks int       `json:"tasks"`
	Left  int       `json:"left"`
	Begun bool      `json:"begun"`
	Stars []starDTO `json:"stars"`
}

type homeSectionDTO struct {
	sectionDTO
	DueAt *string `json:"due_at"`
}

func toSectionDTO(s *LessonSection) sectionDTO {
	return sectionDTO{Tasks: s.Tasks, Left: s.Left, Begun: s.Begun, Stars: toStarsDTO(s.Stars)}
}

func (a *API) myLessonDTO(it StudentLesson) map[string]any {
	earned, total := lessonStars(it.Issues)
	row := map[string]any{
		"assignment_id": it.Assignment.ID.String(),
		"lesson_id":     "",
		// Название урока, а не версии: служебная пометка учителя ученику
		// ни о чём не говорит.
		"title":         it.Version.Name(),
		"level":         it.Version.Level,
		"class":         map[string]any{"id": it.Class.ID.String(), "name": it.Class.Title},
		"attempt_id":    nil,
		"tasks_total":   it.TasksTotal,
		"tasks_done":    it.TasksDone,
		"class_section": nil,
		"home_section":  nil,
		"finished":      it.Finished,
		"issued_at":     nil,
		"stars":         starsTotalDTO{Earned: earned, Max: total},
		"correct_pct":   lessonPct(it.Issues),
	}
	if len(it.Issues) > 0 {
		row["issued_at"] = a.ts(issuedAt(it.Issues))
	}
	if it.Version.LessonID != uuid.Nil {
		row["lesson_id"] = it.Version.LessonID.String()
	}
	if it.Attempt != nil {
		row["attempt_id"] = it.Attempt.ID.String()
	}
	if s := it.ClassSection; s != nil {
		row["class_section"] = toSectionDTO(s)
	}
	if s := it.HomeSection; s != nil {
		row["home_section"] = homeSectionDTO{sectionDTO: toSectionDTO(s), DueAt: a.tsp(s.DueAt)}
	}
	return row
}

// ---------------------------------------------------------------------------
// Выдачи и звёзды ученика
// ---------------------------------------------------------------------------

// starDTO — звезда выдачи.
type starDTO struct {
	Kind string `json:"kind"`
	On   bool   `json:"on"`
}

func toStarsDTO(stars []Star) []starDTO {
	out := make([]starDTO, 0, len(stars))
	for _, s := range stars {
		out = append(out, starDTO{Kind: s.Kind, On: s.On})
	}
	return out
}

// starsTotalDTO — звёзды урока: сколько заработано из скольких.
type starsTotalDTO struct {
	Earned int `json:"earned"`
	Max    int `json:"max"`
}

// wordsDTO — выражения урока в разговоре с аватаром.
type wordsDTO struct {
	State string `json:"state"`
	Used  int    `json:"used"`
	Total int    `json:"total"`
	Need  int    `json:"need"`
}

// issueTaskDTO — задание выдачи и что ученик с ним сделал.
type issueTaskDTO struct {
	taskDTO
	Answered int  `json:"answered"`
	Correct  int  `json:"correct"`
	Done     bool `json:"done"`
}

// studentIssueDTO — выдача глазами ученика.
type studentIssueDTO struct {
	Kind       string         `json:"kind"`
	OpenedAt   string         `json:"opened_at"`
	DueAt      *string        `json:"due_at"`
	Open       bool           `json:"open"`
	Closed     bool           `json:"closed"`
	Settled    bool           `json:"settled"`
	TasksTotal int            `json:"tasks_total"`
	TasksDone  int            `json:"tasks_done"`
	Correct    int            `json:"correct"`
	Items      int            `json:"items"`
	Pct        int            `json:"pct"`
	BestStreak int            `json:"best_streak"`
	Words      *wordsDTO      `json:"words"`
	Stars      []starDTO      `json:"stars"`
	Tasks      []issueTaskDTO `json:"tasks"`
}

func (a *API) studentIssueDTO(si StudentIssue) studentIssueDTO {
	out := studentIssueDTO{
		Kind: si.Kind, OpenedAt: a.ts(si.OpenedAt), DueAt: a.tsp(si.DueAt),
		Open: si.Open, Closed: si.Closed, Settled: si.Settled(),
		TasksTotal: si.Total, TasksDone: si.Done,
		Correct: si.Correct, Items: si.Items, Pct: si.Pct(), BestStreak: si.BestStreak,
		Stars: toStarsDTO(si.Stars), Tasks: make([]issueTaskDTO, 0, len(si.Tasks)),
	}
	if w := si.Words; w != nil {
		out.Words = &wordsDTO{State: w.State, Used: w.Used, Total: w.Total, Need: w.Need()}
	}
	for _, it := range si.Tasks {
		out.Tasks = append(out.Tasks, issueTaskDTO{
			taskDTO: toTaskDTO(it.Task), Answered: it.Answered, Correct: it.Correct, Done: it.Done,
		})
	}
	return out
}

// lessonResults — «Результаты урока» ученика.
func (a *API) lessonResults(w http.ResponseWriter, r *http.Request) {
	actor := iam.MustActor(r.Context())
	if !actor.IsStudent() {
		httpx.Fail(w, r, httpx.NotFound("Не найдено"))
		return
	}
	id, err := pathUUID(r, "assignmentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	res, err := a.svc.LessonResults(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	lesson := map[string]any{
		"assignment_id": res.Assignment.ID.String(),
		"title":         res.Version.Name(),
		"class":         map[string]any{"id": res.Class.ID.String(), "name": res.Class.Title},
		"attempt_id":    nil,
		"issued_at":     a.ts(issuedAt(res.Issues)),
		"finished":      res.Finished,
	}
	if res.Attempt != nil {
		lesson["attempt_id"] = res.Attempt.ID.String()
	}
	issues := make([]studentIssueDTO, 0, len(res.Issues))
	for _, si := range res.Issues {
		issues = append(issues, a.studentIssueDTO(si))
	}
	earned, total := lessonStars(res.Issues)
	// Звезда за разговор загорается, когда готов его разбор: ответ из кэша
	// показал бы её погасшей и после этого.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"lesson":      lesson,
		"stars":       starsTotalDTO{Earned: earned, Max: total},
		"correct_pct": lessonPct(res.Issues),
		"issues":      issues,
	})
}

// ---------------------------------------------------------------------------
// Карточка ученика: выдачи в истории
// ---------------------------------------------------------------------------

func (a *API) cardIssuesDTO(issues []CardIssue) []map[string]any {
	out := make([]map[string]any, 0, len(issues))
	for _, ci := range issues {
		tasks := make([]map[string]any, 0, len(ci.Tasks))
		for _, t := range ci.Tasks {
			tasks = append(tasks, map[string]any{
				"key": t.Key, "n": t.N, "state": t.State, "correct": t.Correct, "total": t.Total,
			})
		}
		out = append(out, map[string]any{
			"id":        ci.Issue.ID.String(),
			"kind":      ci.Issue.Kind,
			"opened_at": a.ts(ci.Issue.OpenedAt),
			"due_at":    a.tsp(ci.Issue.DueAt),
			"closed_at": a.tsp(ci.Issue.ClosedAt),
			"open":      ci.Open,
			"legacy":    ci.Issue.Legacy,
			"tasks":     tasks,
		})
	}
	return out
}
