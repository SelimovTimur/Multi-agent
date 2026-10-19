package iam

import (
	"net/http"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
)

// API — HTTP-слой модуля iam.
//
// Слой тонкий: разбирает запрос, зовёт сервис, сериализует ответ. Никаких
// решений о правах и никаких SQL здесь нет — иначе логика расползается между
// обработчиками и её нельзя протестировать без HTTP.
type API struct {
	svc   *Service
	guard *Guard
	cfg   config.AuthConfig
}

// NewAPI собирает HTTP-слой.
func NewAPI(svc *Service, guard *Guard, cfg config.AuthConfig) *API {
	return &API{svc: svc, guard: guard, cfg: cfg}
}

// Mount регистрирует маршруты модуля.
//
// Каждый маршрут объявляет требуемое право через guard.Handle — маршрут без
// объявления не пройдёт проверку Guard.Verify при старте.
func (a *API) Mount(r chi.Router) {
	// --- Учитель: вход и восстановление -------------------------------------
	a.guard.Handle(r, http.MethodPost, "/auth/register", PermPublic, a.register)
	a.guard.Handle(r, http.MethodPost, "/auth/verify", PermPublic, a.verifyEmail)
	a.guard.Handle(r, http.MethodPost, "/auth/resend", PermPublic, a.resendVerification)
	// Одобрение заявки: обе ручки публичные, потому что решение принимает не
	// вошедший пользователь, а тот, у кого ссылка из письма. Токен и есть
	// авторизация.
	a.guard.Handle(r, http.MethodGet, "/auth/approval", PermPublic, a.approval)
	a.guard.Handle(r, http.MethodPost, "/auth/approve", PermPublic, a.approve)
	a.guard.Handle(r, http.MethodPost, "/auth/login", PermPublic, a.login)
	a.guard.Handle(r, http.MethodGet, "/auth/status", PermPublic, a.status)
	a.guard.Handle(r, http.MethodPost, "/auth/logout", PermPublic, a.logout)
	a.guard.Handle(r, http.MethodPost, "/auth/password/reset-request", PermPublic, a.resetRequest)
	a.guard.Handle(r, http.MethodPost, "/auth/password/reset", PermPublic, a.resetPassword)

	// --- Учитель: профиль ---------------------------------------------------
	a.guard.Handle(r, http.MethodGet, "/me", PermAuthenticated, a.me)
	a.guard.Handle(r, http.MethodPatch, "/me", PermAuthenticated, a.updateMe)
	a.guard.Handle(r, http.MethodPost, "/me/password", PermAuthenticated, a.changePassword)

	// --- Ученик -------------------------------------------------------------
	a.guard.Handle(r, http.MethodPost, "/students/restore", PermPublic, a.studentRestore)
	a.guard.Handle(r, http.MethodGet, "/students/me", PermAuthenticated, a.studentMe)
	a.guard.Handle(r, http.MethodPatch, "/students/me", PermAuthenticated, a.studentUpdate)
	a.guard.Handle(r, http.MethodPost, "/students/logout", PermPublic, a.studentLogout)
}

// ---------------------------------------------------------------------------
// DTO
//
// Отдельные типы, а не доменные структуры: в User лежит PasswordHash, и
// сериализовать домен напрямую — верный способ однажды отдать его наружу.
// ---------------------------------------------------------------------------

type userDTO struct {
	ID          string   `json:"id"`
	OrgID       string   `json:"org_id"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Role        string   `json:"role"`
	Status      string   `json:"status"`
	Permissions []string `json:"permissions"`
}

func (a *API) userDTO(u User) userDTO {
	return userDTO{
		ID:          u.ID.String(),
		OrgID:       u.OrgID.String(),
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		Status:      u.Status,
		Permissions: a.svc.Permissions(u.Role),
	}
}

// approvalDTO — заявка глазами рассматривающего.
//
// Идентификатора пользователя здесь нет: страницу открывают по ссылке из
// письма, и всё, что на ней нужно, — понять, кого впускают.
type approvalDTO struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	Org         string `json:"org"`
	RequestedAt string `json:"requested_at"`
	Status      string `json:"status"`
	Decided     bool   `json:"decided"`
}

func toApprovalDTO(r ApprovalRequest) approvalDTO {
	return approvalDTO{
		Name:        r.Name,
		Email:       r.Email,
		Org:         r.Org,
		RequestedAt: r.RequestedAt.Format(time.RFC3339),
		Status:      r.Status,
		Decided:     r.Decided,
	}
}

// studentDTO — ученик глазами самого ученика.
//
// Личного кода здесь нет намеренно: он заменяет пароль, и ученик знает его
// с карточки. Код видит только учитель — в составе своего класса.
type studentDTO struct {
	ID          string `json:"id"`
	OrgID       string `json:"org_id"`
	DisplayName string `json:"display_name"`
	PublicTag   string `json:"public_tag"`
	Level       string `json:"level,omitempty"`
}

func toStudentDTO(s Student) studentDTO {
	return studentDTO{
		ID:          s.ID.String(),
		OrgID:       s.OrgID.String(),
		DisplayName: s.DisplayName,
		PublicTag:   s.PublicTag,
		Level:       s.Level,
	}
}

// ---------------------------------------------------------------------------
// Обработчики: учитель
// ---------------------------------------------------------------------------

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := httpx.DecodeJSON(r, &in, 8*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}

	res, err := a.svc.Register(r.Context(), in.Email, in.Password, in.Name)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	// Сессия НЕ открывается: аккаунт неактивен до подтверждения почты.
	// Фронт показывает экран «Проверьте почту».
	out := map[string]any{
		"user_id": res.User.ID.String(),
		"org_id":  res.Org.ID.String(),
		"status":  res.User.Status,
	}
	// Поле появляется, только когда письма никуда не уходят. Имя выбрано
	// кричащим намеренно: увидев его в ответе прода, надо чинить прод.
	if res.DevVerifyURL != "" {
		out["dev_verify_url"] = res.DevVerifyURL
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := httpx.DecodeJSON(r, &in, 4*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	res, err := a.svc.VerifyEmail(r.Context(), in.Token)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	// Сессия НЕ открывается. Подтверждённая почта больше не даёт права входа
	// — его даёт одобрение заявки, и открытая здесь сессия была бы обходом
	// этого решения. Фронт по status показывает «Заявка на рассмотрении»
	// либо, для самоодобренных адресов, зовёт войти.
	out := map[string]any{
		"user":   a.userDTO(res.User),
		"status": res.User.Status,
	}
	if res.DevApproveURL != "" {
		out["dev_approve_url"] = res.DevApproveURL
	}
	httpx.JSON(w, http.StatusOK, out)
}

// approval показывает заявку, НЕ принимая решения: по ссылке из письма первым
// ходит почтовый антивирус, а не человек.
func (a *API) approval(w http.ResponseWriter, r *http.Request) {
	req, err := a.svc.Approval(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toApprovalDTO(req))
}

func (a *API) approve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Decision string `json:"decision"`
	}
	if err := httpx.DecodeJSON(r, &in, 4*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req, err := a.svc.DecideApproval(r.Context(), in.Token, in.Decision)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"decision":  in.Decision,
		"applicant": toApprovalDTO(req),
	})
}

func (a *API) resendVerification(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	if err := httpx.DecodeJSON(r, &in, 4*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.svc.ResendVerification(r.Context(), in.Email); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// Ответ одинаков независимо от существования аккаунта.
	httpx.JSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(r, &in, 8*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	res, err := a.svc.Login(r.Context(), in.Email, in.Password, r.UserAgent(), httpx.ClientIP(r.Context()))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// Одно устройство — одна роль. Оставшаяся cookie ученика ничего бы не
	// сломала (приоритет всё равно у учителя), но путала бы состояние.
	clearCookie(w, a.cfg, a.cfg.StudentCookie)
	setCookie(w, a.cfg, a.cfg.SessionCookie, res.Token, int(a.cfg.SessionTTL/time.Second))
	httpx.JSON(w, http.StatusOK, map[string]any{"user": a.userDTO(res.User)})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.Logout(r.Context(), readCookie(r, a.cfg.SessionCookie)); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	clearCookie(w, a.cfg, a.cfg.SessionCookie)
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// status отвечает и анонимному запросу: фронт по нему решает, показывать
// кабинет или форму входа, и не должен ловить 401 при первой загрузке.
func (a *API) status(w http.ResponseWriter, r *http.Request) {
	actor, ok := ActorFrom(r.Context())
	if !ok {
		httpx.JSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}

	switch {
	case actor.IsUser():
		user, err := a.svc.UserByID(r.Context(), actor.UserID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		out := map[string]any{
			"authenticated": true,
			"kind":          "user",
			"user":          a.userDTO(user),
		}
		if actor.Impersonator != nil {
			// Фронт обязан показать баннер на каждом экране, пока идёт
			// сессия просмотра (NEW-18).
			out["impersonated"] = true
			out["read_only"] = actor.ReadOnly
		}
		httpx.JSON(w, http.StatusOK, out)

	case actor.IsStudent():
		st, err := a.svc.StudentByID(r.Context(), actor.StudentID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"kind":          "student",
			"student":       toStudentDTO(st),
		})

	default:
		httpx.JSON(w, http.StatusOK, map[string]any{"authenticated": false})
	}
}

func (a *API) resetRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	if err := httpx.DecodeJSON(r, &in, 4*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.svc.RequestPasswordReset(r.Context(), in.Email); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (a *API) resetPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(r, &in, 8*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.svc.ResetPassword(r.Context(), in.Token, in.Password); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// Все сессии завершены — текущая cookie тоже больше не действует.
	clearCookie(w, a.cfg, a.cfg.SessionCookie)
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	actor := MustActor(r.Context())
	if !actor.IsUser() {
		httpx.Fail(w, r, PermissionDenied())
		return
	}
	user, err := a.svc.UserByID(r.Context(), actor.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a.userDTO(user))
}

func (a *API) updateMe(w http.ResponseWriter, r *http.Request) {
	actor := MustActor(r.Context())
	if !actor.IsUser() {
		httpx.Fail(w, r, PermissionDenied())
		return
	}
	var in struct {
		Name *string `json:"name"`
	}
	if err := httpx.DecodeJSON(r, &in, 4*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if in.Name == nil {
		httpx.Fail(w, r, httpx.FieldError("profile", "Укажите, что изменить"))
		return
	}
	if err := a.svc.UpdateProfile(r.Context(), actor.UserID, *in.Name); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	user, err := a.svc.UserByID(r.Context(), actor.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, a.userDTO(user))
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	actor := MustActor(r.Context())
	if !actor.IsUser() {
		httpx.Fail(w, r, PermissionDenied())
		return
	}
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := httpx.DecodeJSON(r, &in, 8*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.svc.ChangePassword(r.Context(), actor, in.Current, in.New); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// Обработчики: ученик
// ---------------------------------------------------------------------------

func (a *API) studentRestore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code   string `json:"code"`
		Device string `json:"device"`
	}
	if err := httpx.DecodeJSON(r, &in, 4*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	st, token, err := a.svc.RestoreStudent(r.Context(), in.Code, in.Device, httpx.ClientIP(r.Context()))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// Роль на устройстве становится «ученик»: cookie учителя, если она была,
	// убираем. Иначе сервер продолжил бы считать этот браузер учительским, и
	// ручки ученика отвечали бы 404 сразу после успешного входа. Саму сессию
	// учителя не закрываем: у него могут быть открыты другие устройства.
	clearCookie(w, a.cfg, a.cfg.SessionCookie)
	setCookie(w, a.cfg, a.cfg.StudentCookie, token, int(a.cfg.StudentTTL/time.Second))
	httpx.JSON(w, http.StatusOK, map[string]any{"student": toStudentDTO(st)})
}

func (a *API) studentMe(w http.ResponseWriter, r *http.Request) {
	actor := MustActor(r.Context())
	if !actor.IsStudent() {
		httpx.Fail(w, r, PermissionDenied())
		return
	}
	st, err := a.svc.StudentByID(r.Context(), actor.StudentID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toStudentDTO(st))
}

func (a *API) studentUpdate(w http.ResponseWriter, r *http.Request) {
	actor := MustActor(r.Context())
	if !actor.IsStudent() {
		httpx.Fail(w, r, PermissionDenied())
		return
	}
	var in struct {
		DisplayName string `json:"display_name"`
	}
	if err := httpx.DecodeJSON(r, &in, 4*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.svc.UpdateStudentName(r.Context(), actor.StudentID, in.DisplayName); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	st, err := a.svc.StudentByID(r.Context(), actor.StudentID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toStudentDTO(st))
}

func (a *API) studentLogout(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.StudentLogout(r.Context(), readCookie(r, a.cfg.StudentCookie)); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	clearCookie(w, a.cfg, a.cfg.StudentCookie)
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}
