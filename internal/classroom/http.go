package classroom

import (
	"net/http"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// API — HTTP-поверхность модуля классов.
type API struct {
	svc   *Service
	guard *iam.Guard
}

func NewAPI(svc *Service, guard *iam.Guard) *API {
	return &API{svc: svc, guard: guard}
}

// Mount объявляет маршруты вместе с требуемыми правами.
//
// Ручки ученика объявлены как PermAuthenticated: матрица прав описывает роли
// сотрудников, у ученика её прав нет. Принадлежность объекта такие ручки
// проверяют сами.
func (a *API) Mount(r chi.Router) {
	g := a.guard

	// Учитель
	g.Handle(r, http.MethodGet, "/classes", iam.PermClassesOwn, a.list)
	g.Handle(r, http.MethodPost, "/classes", iam.PermClassesOwn, a.create)
	g.Handle(r, http.MethodGet, "/classes/{classID}", iam.PermClassesOwn, a.get)
	g.Handle(r, http.MethodPatch, "/classes/{classID}", iam.PermClassesOwn, a.update)
	g.Handle(r, http.MethodPost, "/classes/{classID}/archive", iam.PermClassesOwn, a.archive)

	g.Handle(r, http.MethodGet, "/classes/{classID}/students", iam.PermStudentsOwn, a.roster)
	g.Handle(r, http.MethodPost, "/classes/{classID}/students", iam.PermStudentsOwn, a.addStudents)
	g.Handle(r, http.MethodPost, "/classes/{classID}/students/{studentID}/code", iam.PermStudentsOwn, a.reissueCode)
	g.Handle(r, http.MethodDelete, "/classes/{classID}/students/{studentID}", iam.PermStudentsOwn, a.removeStudent)
	g.Handle(r, http.MethodPost, "/classes/{classID}/students/{studentID}/restore", iam.PermStudentsOwn, a.restoreStudent)

	// Ученик. Входит он по личному коду (iam, /students/restore), а
	// самостоятельного входа в класс по коду класса нет: учеников заводят
	// списком учитель или команда платформы.
	g.Handle(r, http.MethodGet, "/students/me/classes", iam.PermAuthenticated, a.myClasses)
}

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

// classDTO — класс глазами учителя. Имена полей совпадают с экранами:
// name/subject/level/lang.
type classDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Subject   string `json:"subject"`
	Level     string `json:"level"`
	Lang      string `json:"lang"`
	Status    string `json:"status"`
	Students  int    `json:"students"`
	CreatedAt string `json:"created_at"`
}

func toClassDTO(c Class, students int) classDTO {
	return classDTO{
		ID:        c.ID.String(),
		Name:      c.Title,
		Subject:   c.Subject,
		Level:     c.DefaultLevel,
		Lang:      c.LangCode,
		Status:    c.Status,
		Students:  students,
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
	}
}

// memberDTO — строка состава класса.
//
// Code — личный код ученика. Это ручка учителя, и код в ней нужен: учитель
// раздаёт карточки и восстанавливает потерянные. Ученику его код не
// отдаёт ни одна ручка.
type memberDTO struct {
	StudentID  string  `json:"student_id"`
	Name       string  `json:"name"`
	Tag        string  `json:"tag"`
	Code       string  `json:"code"`
	Level      string  `json:"level"`
	Status     string  `json:"status"`
	JoinedAt   string  `json:"joined_at"`
	LastSeenAt *string `json:"last_seen_at"`
}

func toMemberDTO(m Member) memberDTO {
	dto := memberDTO{
		StudentID: m.Enrollment.StudentID.String(),
		Name:      m.DisplayName,
		Tag:       m.PublicTag,
		Code:      m.PublicCode,
		Level:     m.Level,
		Status:    m.Enrollment.Status,
		JoinedAt:  m.Enrollment.JoinedAt.Format(time.RFC3339),
	}
	if m.LastSeenAt != nil {
		v := m.LastSeenAt.Format(time.RFC3339)
		dto.LastSeenAt = &v
	}
	return dto
}

// ---------------------------------------------------------------------------
// Обработчики учителя
// ---------------------------------------------------------------------------

// respondClass отдаёт класс вместе с актуальным числом учеников.
// Счётчик читается отдельно: экран класса показывает его рядом с названием,
// и после любой правки он должен совпадать со списком состава.
func (a *API) respondClass(w http.ResponseWriter, r *http.Request, status int, c Class) {
	count, err := a.svc.StudentCount(r.Context(), c.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, status, map[string]any{"class": toClassDTO(c, count)})
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	actor := iam.MustActor(r.Context())
	items, err := a.svc.ListClasses(r.Context(), actor, r.URL.Query().Get("archived") == "1")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]classDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toClassDTO(it.Class, it.Students))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"classes": out})
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string `json:"name"`
		Subject string `json:"subject"`
		Level   string `json:"level"`
		Lang    string `json:"lang"`
	}
	if err := httpx.DecodeJSON(r, &in, 8*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	actor := iam.MustActor(r.Context())
	c, err := a.svc.CreateClass(r.Context(), actor, ClassInput{
		Title: in.Name, Subject: in.Subject, Level: in.Level, LangCode: in.Lang,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"class": toClassDTO(c, 0)})
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "classID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	actor := iam.MustActor(r.Context())
	c, err := a.svc.Class(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.respondClass(w, r, http.StatusOK, c)
}

// update правит поля класса.
func (a *API) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "classID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in struct {
		Name    *string `json:"name"`
		Subject *string `json:"subject"`
		Level   *string `json:"level"`
	}
	if err := httpx.DecodeJSON(r, &in, 8*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	ctx := r.Context()
	actor := iam.MustActor(ctx)
	c, err := a.svc.Class(ctx, actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	if in.Name != nil || in.Subject != nil || in.Level != nil {
		next := ClassInput{Title: c.Title, Subject: c.Subject, Level: c.DefaultLevel}
		if in.Name != nil {
			next.Title = *in.Name
		}
		if in.Subject != nil {
			next.Subject = *in.Subject
		}
		if in.Level != nil {
			next.Level = *in.Level
		}
		if c, err = a.svc.UpdateClass(ctx, actor, id, next); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	a.respondClass(w, r, http.StatusOK, c)
}

func (a *API) archive(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "classID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in struct {
		Archived bool `json:"archived"`
	}
	if err := httpx.DecodeJSON(r, &in, 1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	c, err := a.svc.SetArchived(r.Context(), iam.MustActor(r.Context()), id, in.Archived)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.respondClass(w, r, http.StatusOK, c)
}

func (a *API) roster(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "classID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	members, err := a.svc.Roster(r.Context(), iam.MustActor(r.Context()), id,
		r.URL.Query().Get("removed") == "1")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]memberDTO, 0, len(members))
	for _, m := range members {
		out = append(out, toMemberDTO(m))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"students": out})
}

func (a *API) removeStudent(w http.ResponseWriter, r *http.Request) {
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
	if err := a.svc.RemoveStudent(r.Context(), iam.MustActor(r.Context()), classID, studentID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) restoreStudent(w http.ResponseWriter, r *http.Request) {
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
	if err := a.svc.RestoreStudent(r.Context(), iam.MustActor(r.Context()), classID, studentID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// addStudents заводит учеников списком имён и отдаёт заведённых с кодами.
func (a *API) addStudents(w http.ResponseWriter, r *http.Request) {
	classID, err := pathUUID(r, "classID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var in struct {
		Names []string `json:"names"`
	}
	if err := httpx.DecodeJSON(r, &in, 32*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	res, err := a.svc.AddStudents(r.Context(), iam.MustActor(r.Context()), classID, in.Names)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	added := make([]memberDTO, 0, len(res.Added))
	for _, m := range res.Added {
		added = append(added, toMemberDTO(m))
	}
	skipped := res.Skipped
	if skipped == nil {
		skipped = []string{}
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"added": added, "skipped": skipped})
}

// reissueCode выдаёт ученику новый личный код; старый перестаёт работать.
func (a *API) reissueCode(w http.ResponseWriter, r *http.Request) {
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
	m, err := a.svc.ReissueCode(r.Context(), iam.MustActor(r.Context()), classID, studentID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"student": toMemberDTO(m)})
}

// ---------------------------------------------------------------------------
// Обработчики ученика
// ---------------------------------------------------------------------------

func (a *API) myClasses(w http.ResponseWriter, r *http.Request) {
	actor := iam.MustActor(r.Context())
	if !actor.IsStudent() {
		httpx.Fail(w, r, httpx.NotFound("Не найдено"))
		return
	}
	classes, err := a.svc.MyClasses(r.Context(), actor)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(classes))
	for _, c := range classes {
		out = append(out, map[string]any{
			"id":      c.ID.String(),
			"name":    c.Title,
			"subject": c.Subject,
			"lang":    c.LangCode,
			"level":   c.DefaultLevel,
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"classes": out})
}

// pathUUID разбирает идентификатор из пути.
// Мусор в пути — это 404, а не 400: клиент не должен по коду ответа отличать
// «неправильный формат» от «чужой объект».
func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, httpx.NotFound("Не найдено")
	}
	return id, nil
}
