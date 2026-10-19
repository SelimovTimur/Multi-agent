package learning

import (
	"net/http"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// API — HTTP-поверхность назначений и прохождения.
type API struct {
	svc   *Service
	guard *iam.Guard
}

func NewAPI(svc *Service, guard *iam.Guard) *API {
	return &API{svc: svc, guard: guard}
}

func (a *API) Mount(r chi.Router) {
	g := a.guard

	// Учитель
	g.Handle(r, http.MethodPost, "/assignments", iam.PermLessonsOwn, a.assign)
	g.Handle(r, http.MethodGet, "/classes/{classID}/assignments", iam.PermLessonsOwn, a.classAssignments)
	g.Handle(r, http.MethodPatch, "/assignments/{assignmentID}", iam.PermLessonsOwn, a.reschedule)
	g.Handle(r, http.MethodPost, "/assignments/{assignmentID}/close", iam.PermLessonsOwn, a.close)
	g.Handle(r, http.MethodDelete, "/assignments/{assignmentID}", iam.PermLessonsOwn, a.unassign)
	g.Handle(r, http.MethodGet, "/assignments/{assignmentID}/progress", iam.PermStudentsOwn, a.progress)
	g.Handle(r, http.MethodGet, "/classes/{classID}/students/{studentID}/card", iam.PermStudentsOwn, a.studentCard)
	// Аналитика — про учеников, а не про уроки: право то же, что у карточки
	// ученика, и владение классом сервис проверяет сам.
	g.Handle(r, http.MethodGet, "/classes/{classID}/analytics", iam.PermStudentsOwn, a.classAnalytics)
	g.Handle(r, http.MethodGet, "/classes/{classID}/analytics/words", iam.PermStudentsOwn, a.classHardWords)
	g.Handle(r, http.MethodGet, "/assignments/{assignmentID}/analytics", iam.PermStudentsOwn, a.lessonAnalytics)

	// Ученик. Прав из матрицы у него нет — принадлежность проверяет сервис.
	g.Handle(r, http.MethodGet, "/my/lessons", iam.PermAuthenticated, a.myLessons)
	g.Handle(r, http.MethodPost, "/my/lessons/{assignmentID}/start", iam.PermAuthenticated, a.start)
	g.Handle(r, http.MethodGet, "/my/attempts/{attemptID}", iam.PermAuthenticated, a.attempt)
	g.Handle(r, http.MethodGet, "/my/attempts/{attemptID}/lesson", iam.PermAuthenticated, a.lessonContent)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/step", iam.PermAuthenticated, a.step)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/answer", iam.PermAuthenticated, a.answer)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/finish", iam.PermAuthenticated, a.finish)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/feedback", iam.PermAuthenticated, a.feedback)

	// Урок в классе: выдача заданий и наблюдение за классом.
	a.mountClasswork(r)

	// Разговор по целям урока: ходы, итог ученику, отчёт учителю.
	a.mountConversation(r)
}

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

type assignmentDTO struct {
	ID        string  `json:"id"`
	ClassID   string  `json:"class_id"`
	VersionID string  `json:"lesson_version_id"`
	LessonID  string  `json:"lesson_id"`
	Title     string  `json:"title"`
	Level     string  `json:"level"`
	Version   int     `json:"version"`
	State     string  `json:"state"`
	OpensAt   *string `json:"opens_at"`
	ClosesAt  *string `json:"closes_at"`
	ClosedAt  *string `json:"closed_at"`
	CreatedAt string  `json:"created_at"`

	// Строка урока на вкладке «Уроки»: «выдано N из 8», «идёт урок»,
	// «на дом до 03.10».
	TasksTotal  int      `json:"tasks_total"`
	TasksIssued int      `json:"tasks_issued"`
	ClassOpen   bool     `json:"class_open"`
	Home        *homeDTO `json:"home"`
	Legacy      bool     `json:"legacy"`
}

// homeDTO — домашняя работа в строке урока; null — её нет.
type homeDTO struct {
	DueAt *string `json:"due_at"`
	Open  bool    `json:"open"`
}

func (a *API) assignmentDTO(v AssignmentView) assignmentDTO {
	d := toAssignmentDTO(v)
	d.TasksTotal, d.TasksIssued = v.Summary.TasksTotal, v.Summary.TasksIssued
	d.ClassOpen, d.Legacy = v.Summary.ClassOpen, v.Summary.Legacy
	if h := v.Summary.Home; h != nil {
		d.Home = &homeDTO{DueAt: a.tsp(h.DueAt), Open: h.Open}
	}
	return d
}

func toAssignmentDTO(v AssignmentView) assignmentDTO {
	d := assignmentDTO{
		ID: v.Assignment.ID.String(), ClassID: v.Assignment.ClassID.String(),
		VersionID: v.Assignment.LessonVersionID.String(),
		// Название урока, а не версии: «v1 · QA» в списке у ученика — это
		// служебная пометка учителя, попавшая не туда.
		Title: v.Version.Name(), Level: v.Version.Level, Version: v.Version.Version,
		State:     v.Assignment.State,
		CreatedAt: v.Assignment.CreatedAt.Format(time.RFC3339),
	}
	if v.Version.LessonID != uuid.Nil {
		d.LessonID = v.Version.LessonID.String()
	}
	d.OpensAt = formatTime(v.Assignment.OpensAt)
	d.ClosesAt = formatTime(v.Assignment.ClosesAt)
	d.ClosedAt = formatTime(v.Assignment.ClosedAt)
	return d
}

// formatTime отдаёт время с зоной. Показывать в местном времени — забота фронта.
func formatTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

type attemptDTO struct {
	ID             string `json:"id"`
	AssignmentID   string `json:"assignment_id"`
	CurrentStep    int    `json:"current_step"`
	Score          int    `json:"score"`
	CorrectAnswers int    `json:"correct_answers"`
	TotalAnswers   int    `json:"total_answers"`
	// TotalQuestions и Answered — «отвечено N из M». Итог урока говорит это
	// всегда, в том числе когда N равно нулю: иначе пролистанный урок
	// выглядит так же, как пройденный.
	TotalQuestions int `json:"total_questions"`
	Answered       int `json:"answered"`
	// Completed — урок пройден, а не пролистан.
	Completed  bool                      `json:"completed"`
	Answers    map[string]givenAnswerDTO `json:"answers"`
	StartedAt  string                    `json:"started_at"`
	FinishedAt *string                   `json:"finished_at"`
}

func toAttemptDTO(a Attempt) attemptDTO {
	answers := make(map[string]givenAnswerDTO, len(a.Answers))
	for k, v := range a.Answers {
		answers[k] = givenAnswerDTO{Answer: v.Answer, Correct: v.Correct, Expected: v.Expected, Step: v.Step}
	}
	return attemptDTO{
		ID: a.ID.String(), AssignmentID: a.AssignmentID.String(),
		CurrentStep: a.CurrentStep, Score: a.Score,
		CorrectAnswers: a.CorrectAnswers, TotalAnswers: a.TotalAnswers,
		TotalQuestions: a.TotalQuestions, Answered: a.Answered(),
		Completed:  a.Completed,
		Answers:    answers,
		StartedAt:  a.StartedAt.Format(time.RFC3339),
		FinishedAt: formatTime(a.FinishedAt),
	}
}

// givenAnswerDTO — ответ, который ученик уже дал.
//
// Уходит ученику вместе с попыткой: открыв урок заново, он видит прежний
// выбор и верный ответ, а не пустые варианты, на которые можно ответить
// второй раз.
type givenAnswerDTO struct {
	Answer   string `json:"answer"`
	Correct  bool   `json:"correct"`
	Expected string `json:"expected,omitempty"`
	Step     int    `json:"step,omitempty"`
}

// ---------------------------------------------------------------------------
// Учитель
// ---------------------------------------------------------------------------

func (a *API) assign(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LessonVersionID string `json:"lesson_version_id"`
		ClassID         string `json:"class_id"`
		// Сроков у назначения больше нет: урок назначается закрытым, срок
		// бывает только у домашней работы. Поля принимаются и не читаются —
		// старый клиент, приславший их, не должен получать отказ.
		OpensAt  any `json:"opens_at"`
		ClosesAt any `json:"closes_at"`
	}
	if err := httpx.DecodeJSON(r, &in, 8*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	versionID, err := parseUUID(in.LessonVersionID, "lesson_version_id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	classID, err := parseUUID(in.ClassID, "class_id")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	actor := iam.MustActor(r.Context())
	out, err := a.svc.Assign(r.Context(), actor, AssignInput{LessonVersionID: versionID, ClassID: classID})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"assignment": a.assignmentDTO(a.svc.View(r.Context(), actor, out)),
	})
}

func (a *API) classAssignments(w http.ResponseWriter, r *http.Request) {
	classID, err := pathUUID(r, "classID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items, err := a.svc.ClassAssignments(r.Context(), iam.MustActor(r.Context()),
		classID, r.URL.Query().Get("state"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]assignmentDTO, 0, len(items))
	for _, it := range items {
		out = append(out, a.assignmentDTO(it))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"assignments": out})
}

func (a *API) reschedule(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "assignmentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in struct {
		OpensAt  *string `json:"opens_at"`
		ClosesAt *string `json:"closes_at"`
	}
	if err := httpx.DecodeJSON(r, &in, 4*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	opensAt, err := parseTime(in.OpensAt, "opens_at")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	closesAt, err := parseTime(in.ClosesAt, "closes_at")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	actor := iam.MustActor(r.Context())
	out, err := a.svc.Reschedule(r.Context(), actor, id, opensAt, closesAt)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"assignment": a.assignmentDTO(a.svc.View(r.Context(), actor, out)),
	})
}

func (a *API) close(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "assignmentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	actor := iam.MustActor(r.Context())
	out, err := a.svc.Close(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"assignment": a.assignmentDTO(a.svc.View(r.Context(), actor, out)),
	})
}

// unassign снимает назначение. В ответе видно, что именно произошло:
// запись удалена или только закрыта, потому что были попытки.
func (a *API) unassign(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "assignmentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	result, err := a.svc.Unassign(r.Context(), iam.MustActor(r.Context()), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"result": result})
}

func (a *API) progress(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "assignmentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items, err := a.svc.Progress(r.Context(), iam.MustActor(r.Context()), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, at := range items {
		out = append(out, map[string]any{
			"student_id": at.StudentID.String(),
			"attempt":    toAttemptDTO(at),
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"progress": out})
}

// classAnalytics — вкладка «Аналитика» класса: сводка и таблица результатов.
//
// Всё одним ответом: средние строки и столбца считаются из тех же клеток, и
// собирать их несколькими запросами значило бы показать цифры, не
// сходящиеся между собой.
func (a *API) classAnalytics(w http.ResponseWriter, r *http.Request) {
	classID, err := pathUUID(r, "classID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	an, err := a.svc.ClassAnalyticsFor(r.Context(), iam.MustActor(r.Context()), classID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	member := func(m ClassMember) map[string]any {
		return map[string]any{
			"student_id":   m.ID.String(),
			"display_name": m.DisplayName,
			"public_tag":   m.PublicTag,
		}
	}

	inactive := make([]map[string]any, 0, len(an.Inactive))
	for _, m := range an.Inactive {
		inactive = append(inactive, member(m))
	}

	lessons := make([]map[string]any, 0, len(an.Lessons))
	for _, l := range an.Lessons {
		row := map[string]any{
			"assignment_id":        l.AssignmentID.String(),
			"lesson_id":            "",
			"title":                l.Title,
			"level":                l.Level,
			"assigned_at":          a.ts(l.AssignedAt),
			"correct_percent":      l.Tasks.Percent(),
			"conversation_percent": l.Conversation.Percent(),
		}
		if l.LessonID != uuid.Nil {
			row["lesson_id"] = l.LessonID.String()
		}
		lessons = append(lessons, row)
	}

	students := make([]map[string]any, 0, len(an.Students))
	for _, s := range an.Students {
		cells := make([]map[string]any, 0, len(s.Cells))
		for _, c := range s.Cells {
			// Поля клетки — те же, что у строки урока в карточке ученика:
			// экран считает и красит их одинаково.
			cell := map[string]any{
				"started":         c.Started,
				"completed":       c.Completed,
				"correct_answers": c.Correct,
				"total_questions": c.Questions,
				"correct_percent": percentOrNil(c.Correct, c.Questions),
				"conversation":    nil,
			}
			if conv := c.Conversation; conv != nil {
				cell["conversation"] = map[string]any{"ready": conv.Ready, "credited": conv.Credited, "goals": conv.Goals}
			}
			cells = append(cells, cell)
		}
		row := member(s.Member)
		row["last_seen_at"] = a.tsp(s.LastSeenAt)
		row["correct_percent"] = s.Tasks.Percent()
		row["conversation_percent"] = s.Conversation.Percent()
		row["cells"] = cells
		students = append(students, row)
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"assigned_count": an.Assigned,
		"expected_count": an.Expected,
		"passed_count":   an.Passed,
		"total_students": len(an.Students),
		"inactive":       inactive,
		"lessons":        lessons,
		"students":       students,
	})
}

// classHardWords — блок «Что повторить на следующем уроке»: трудные слова
// класса по всем урокам. Фильтр по уроку на главной аналитики — на экране:
// слов немного, а переключение урока не должно ждать сервера. Страница урока
// (#68) просит только свой урок — ?assignment_id=, чтобы не считать чужие.
func (a *API) classHardWords(w http.ResponseWriter, r *http.Request) {
	classID, err := pathUUID(r, "classID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var lesson uuid.UUID
	if v := r.URL.Query().Get("assignment_id"); v != "" {
		if lesson, err = parseUUID(v, "assignment_id"); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	words, err := a.svc.ClassHardWords(r.Context(), iam.MustActor(r.Context()), classID, lesson)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	member := func(m ClassMember) map[string]any {
		return map[string]any{"student_id": m.ID.String(), "display_name": m.DisplayName, "public_tag": m.PublicTag}
	}
	members := func(ms []ClassMember) []map[string]any {
		out := make([]map[string]any, 0, len(ms))
		for _, m := range ms {
			out = append(out, member(m))
		}
		return out
	}
	// struggled — «кому повторить» с ошибками каждого: карточка ученика
	// показывает его число рядом с числом класса.
	struggled := func(hw HardWord) []map[string]any {
		out := members(hw.Struggled)
		for i, m := range hw.Struggled {
			out[i]["errors"] = hw.StudentErrors[m.ID]
		}
		return out
	}
	tally := func(t WordTally) map[string]any {
		return map[string]any{
			"answers": t.Answers, "errors": t.Errors, "students": t.Students, "erred": t.Erred,
			"correct_percent": percentOrNil(t.Answers-t.Errors, t.Answers),
		}
	}

	out := make([]map[string]any, 0, len(words))
	for _, hw := range words {
		written := make([]map[string]any, 0, len(hw.Written))
		for _, m := range hw.Written {
			written = append(written, map[string]any{
				"task_title": m.TaskTitle, "task_type": m.TaskType, "prompt": m.Prompt,
				"answer": m.Answer, "expected": m.Expected, "count": m.Count, "students": members(m.Students),
			})
		}
		spoken := make([]map[string]any, 0, len(hw.Spoken))
		for _, q := range hw.Spoken {
			spoken = append(spoken, map[string]any{
				"student": member(q.Member), "turn_no": q.TurnNo, "quote": q.Quote, "correct_form": q.CorrectForm,
			})
		}
		out = append(out, map[string]any{
			"assignment_id": hw.AssignmentID.String(),
			"lesson_title":  hw.LessonTitle,
			"phrase":        hw.Word.Phrase,
			"translation":   hw.Word.Translation,
			"errors":        hw.Errors,
			"answers":       hw.Answers,
			"students_met":  hw.Met,
			"struggled":     struggled(hw),
			"tasks":         tally(hw.Tasks),
			"conversation":  tally(hw.Conversation),
			"written":       written,
			"spoken":        spoken,
			"confusions":    hw.Confusions,
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"words": out})
}

// lessonAnalytics — страница урока в аналитике: сводка по классу и разбор
// письменных заданий. Разговор с аватаром страница берёт из отчёта
// /assignments/{id}/conversation.
func (a *API) lessonAnalytics(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "assignmentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	an, err := a.svc.LessonAnalyticsFor(r.Context(), iam.MustActor(r.Context()), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	tasks := make([]map[string]any, 0, len(an.Exercises))
	for _, ex := range an.Exercises {
		mistakes := make([]map[string]any, 0, len(ex.Mistakes))
		for _, m := range ex.Mistakes {
			names := make([]string, 0, len(m.Students))
			for _, st := range m.Students {
				names = append(names, st.DisplayName)
			}
			mistakes = append(mistakes, map[string]any{
				"prompt": m.Prompt, "answer": m.Answer, "expected": m.Expected,
				"count": m.Count, "students": names,
			})
		}
		tasks = append(tasks, map[string]any{
			"key": ex.Task.Key, "n": ex.Task.N, "title": ex.Task.Title, "type_label": ex.Task.TypeLabel,
			"questions": ex.Task.Questions, "students": ex.Students,
			"answered": ex.Answered, "correct": ex.Correct,
			"correct_percent": percentOrNil(ex.Correct, ex.Answered),
			"mistakes":        mistakes,
		})
	}

	assignment := map[string]any{
		"id":          an.Assignment.ID.String(),
		"title":       an.Version.Name(),
		"level":       an.Version.Level,
		"assigned_at": a.ts(an.Assignment.CreatedAt),
		"state":       an.Assignment.State,
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"assignment":           assignment,
		"class":                map[string]any{"id": an.Class.ID.String(), "name": an.Class.Title},
		"total_students":       an.Students,
		"passed_count":         an.Passed,
		"correct_percent":      an.Tasks.Percent(),
		"conversation_percent": an.Conversation.Percent(),
		"talked_count":         an.Talked,
		"has_dialog":           an.HasDialog,
		"dialog_issued":        an.DialogIssued,
		"tasks":                tasks,
	})
}

// ---------------------------------------------------------------------------
// Ученик
// ---------------------------------------------------------------------------

func (a *API) myLessons(w http.ResponseWriter, r *http.Request) {
	actor := iam.MustActor(r.Context())
	if !actor.IsStudent() {
		httpx.Fail(w, r, httpx.NotFound("Не найдено"))
		return
	}
	var classFilter uuid.UUID
	if v := r.URL.Query().Get("class_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			classFilter = id
		}
	}

	items, err := a.svc.MyLessons(r.Context(), actor, classFilter)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, a.myLessonDTO(it))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"lessons": out})
}

func (a *API) start(w http.ResponseWriter, r *http.Request) {
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
	attempt, version, err := a.svc.StartLesson(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"attempt":           toAttemptDTO(attempt),
		"lesson_version_id": version.ID.String(),
		"title":             version.Name(),
	})
}

// lessonContent отдаёт ученику урок: тексты, выражения, упражнения.
// Ключа ответов здесь нет и быть не может — content не выбирает его из базы.
func (a *API) lessonContent(w http.ResponseWriter, r *http.Request) {
	actor := iam.MustActor(r.Context())
	if !actor.IsStudent() {
		httpx.Fail(w, r, httpx.NotFound("Не найдено"))
		return
	}
	id, err := pathUUID(r, "attemptID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	content, err := a.svc.LessonContent(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, content)
}

func (a *API) answer(w http.ResponseWriter, r *http.Request) {
	actor := iam.MustActor(r.Context())
	if !actor.IsStudent() {
		httpx.Fail(w, r, httpx.NotFound("Не найдено"))
		return
	}
	attemptID, err := pathUUID(r, "attemptID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in struct {
		ExerciseID string `json:"exercise_id"`
		Step       int    `json:"step"`
		// ItemID — идентификатор вопроса в тесте или левая часть пары в
		// сопоставлении: без него непонятно, что именно отвечено.
		ItemID string `json:"item_id"`
		Answer string `json:"answer"`
	}
	if err := httpx.DecodeJSON(r, &in, 64*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	input := AnswerInput{
		AttemptID: attemptID, Step: in.Step, ItemID: in.ItemID, Answer: in.Answer,
	}
	if in.ExerciseID != "" {
		if id, err := uuid.Parse(in.ExerciseID); err == nil {
			input.ExerciseID = &id
		}
	}

	res, err := a.svc.RecordAnswer(r.Context(), actor, input)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// Правильный ответ отдаём после попытки: не увидев его, ученик
	// запомнит только то, что ошибся. repeated — на вопрос уже отвечали
	// (например, из второй вкладки): счёт не изменился, и серия верных
	// подряд от такого ответа не растёт.
	httpx.JSON(w, http.StatusOK, map[string]any{
		"attempt":  toAttemptDTO(res.Attempt),
		"correct":  res.Correct,
		"expected": res.Expected,
		"repeated": res.Repeated,
	})
}

func (a *API) finish(w http.ResponseWriter, r *http.Request) {
	actor := iam.MustActor(r.Context())
	if !actor.IsStudent() {
		httpx.Fail(w, r, httpx.NotFound("Не найдено"))
		return
	}
	id, err := pathUUID(r, "attemptID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	attempt, err := a.svc.FinishLesson(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"attempt": toAttemptDTO(attempt)})
}

// ---------------------------------------------------------------------------
// Разбор параметров
// ---------------------------------------------------------------------------

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, httpx.NotFound("Не найдено")
	}
	return id, nil
}

func parseUUID(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, httpx.FieldError(field, "Неверный идентификатор")
	}
	return id, nil
}

// parseTime разбирает время в RFC 3339 с обязательной зоной.
//
// Без зоны сервер и учитель понимают «18:00» по-разному, и урок закрывается
// не тогда, когда обещали.
func parseTime(s *string, field string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil, httpx.FieldError(field, "Дата в формате 2026-09-20T18:00:00+03:00")
	}
	return &t, nil
}

/* ---------------------------------------------------------------------------
 * Разговор с аватаром
 * ------------------------------------------------------------------------- */

// attempt отдаёт состояние попытки: счёт, прежние ответы, «отвечено N из M».
//
// Нужен при открытии урока: без прежних ответов ученик видит пустые
// варианты на вопросах, где уже отвечал, и отвечает второй раз.
func (a *API) attempt(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	attempt, err := a.svc.AttemptState(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"attempt": toAttemptDTO(attempt)})
}

// step запоминает, до какого шага дошёл ученик.
func (a *API) step(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	var in struct {
		Step int `json:"step"`
	}
	if err := httpx.DecodeJSON(r, &in, 1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.svc.SetStep(r.Context(), actor, id, in.Step); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// feedback принимает ответы ученика на опрос после урока.
func (a *API) feedback(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	var in struct {
		Lesson    string `json:"lesson"`
		Avatar    string `json:"avatar"`
		LessonWhy string `json:"lesson_why"`
		AvatarWhy string `json:"avatar_why"`
		Unclear   string `json:"unclear"`
	}
	if err := httpx.DecodeJSON(r, &in, 16*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	err := a.svc.SaveFeedback(r.Context(), actor, id, AttemptFeedback{
		Lesson: in.Lesson, Avatar: in.Avatar, LessonWhy: in.LessonWhy, AvatarWhy: in.AvatarWhy, Unclear: in.Unclear,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// studentAttempt — общая часть ручек ученика: проверка роли и разбор пути.
func (a *API) studentAttempt(w http.ResponseWriter, r *http.Request) (iam.Actor, uuid.UUID, bool) {
	actor := iam.MustActor(r.Context())
	if !actor.IsStudent() {
		httpx.Fail(w, r, httpx.NotFound("Не найдено"))
		return actor, uuid.Nil, false
	}
	id, err := pathUUID(r, "attemptID")
	if err != nil {
		httpx.Fail(w, r, err)
		return actor, uuid.Nil, false
	}
	return actor, id, true
}

// studentCard — статистика одного ученика для учителя.
func (a *API) studentCard(w http.ResponseWriter, r *http.Request) {
	classID, err := pathUUID(r, "classID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	studentID, err := pathUUID(r, "studentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	card, err := a.svc.StudentCardFor(r.Context(), iam.MustActor(r.Context()), classID, studentID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	lessons := make([]map[string]any, 0, len(card.Lessons))
	for _, l := range card.Lessons {
		row := map[string]any{
			"assignment_id": l.Assignment.ID.String(),
			"title":         l.Version.Name(),
			"level":         l.Version.Level,
			"assigned_at":   a.ts(l.Assignment.CreatedAt),
			"state":         l.Assignment.State,
			// Урок в классе: «X из 8 заданий» и группы по выдачам — что
			// сделано на уроке, а что дома.
			"tasks_total":     l.TasksTotal,
			"tasks_done":      l.TasksDone,
			"issues":          a.cardIssuesDTO(l.Issues),
			"started":         l.Attempt != nil,
			"completed":       false,
			"correct_answers": 0,
			"total_questions": 0,
			// null — урок не открывали; 0 — открывали и всё мимо. Экран
			// показывает их по-разному, как и вкладка «Аналитика».
			"correct_percent": nil,
			"last_seen_at":    nil,
			"conversation":    nil,
		}
		if at := l.Attempt; at != nil {
			row["completed"] = at.Completed
			row["correct_answers"] = at.CorrectAnswers
			row["total_questions"] = at.TotalQuestions
			row["correct_percent"] = percentOrNil(at.CorrectAnswers, at.TotalQuestions)
			row["last_seen_at"] = a.ts(at.LastSeenAt)
		}
		if c := l.Conversation; c != nil {
			row["conversation"] = map[string]any{"ready": c.Ready, "credited": c.Credited, "goals": c.Goals}
		}
		lessons = append(lessons, row)
	}

	sum := card.Summary
	httpx.JSON(w, http.StatusOK, map[string]any{
		"summary": map[string]any{
			"assigned":        sum.Assigned,
			"passed":          sum.Passed,
			"correct_answers": sum.Correct,
			"total_questions": sum.Questions,
			"correct_percent": percentOrNil(sum.Correct, sum.Questions),
			"last_seen_at":    a.tsp(sum.LastSeenAt),
		},
		"lessons": lessons,
	})
}
