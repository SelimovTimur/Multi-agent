package iam

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
)

type actorCtxKey struct{}

// WithActor кладёт актора в контекст.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorCtxKey{}, a)
}

// ActorFrom достаёт актора. ok=false, если запрос не аутентифицирован.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorCtxKey{}).(Actor)
	return a, ok
}

// MustActor возвращает актора или паникует.
//
// Паника здесь — это ошибка сборки маршрутов, а не пользовательский ввод:
// обработчик стоит за middleware аутентификации, значит актор обязан быть.
// Recover-middleware превратит её в 500 и запись в логе.
func MustActor(ctx context.Context) Actor {
	a, ok := ActorFrom(ctx)
	if !ok {
		panic("iam: обработчик требует актора, но не защищён middleware аутентификации")
	}
	return a
}

// PermissionDenied — единый отказ по правам.
//
// Отдаёт 403 только там, где факт существования объекта уже известен клиенту.
// Чужой объект должен отдавать 404 — иначе перебором вычисляются чужие
// идентификаторы (NR-10).
func PermissionDenied() error {
	return httpx.Forbidden("Недостаточно прав для этого действия")
}

// Guard — аутентификация, права и реестр маршрутов.
//
// Каждый маршрут ОБЯЗАН объявить требуемое право через Handle. Маршрут без
// объявления не поднимается: Verify возвращает список нарушителей, и сервис
// падает при старте (NR-10). Так право не забудут при добавлении ручки.
type Guard struct {
	svc *Service
	cfg config.AuthConfig

	// declared: "METHOD /pattern" → требуемое право ("" — доступ без прав,
	// например публичные ручки входа).
	declared map[string]string
}

// NewGuard создаёт охрану маршрутов.
func NewGuard(svc *Service, cfg config.AuthConfig) *Guard {
	return &Guard{svc: svc, cfg: cfg, declared: map[string]string{}}
}

// PermPublic — маркер маршрута, доступного без аутентификации.
const PermPublic = ""

// PermAuthenticated — маркер маршрута, требующего входа, но не конкретного права.
const PermAuthenticated = "*"

// Handle объявляет маршрут вместе с требуемым правом и монтирует обработчик.
//
// perm:
//   - PermPublic — вход не нужен;
//   - PermAuthenticated — нужен вход, конкретное право не проверяется;
//   - любое другое значение — право из матрицы iam.role_permissions.
func (g *Guard) Handle(r chi.Router, method, pattern, perm string, h http.HandlerFunc) {
	key := routeKey(method, pattern)
	if _, dup := g.declared[key]; dup {
		panic(fmt.Sprintf("iam: маршрут %s объявлен дважды", key))
	}
	g.declared[key] = perm

	handler := h
	if perm != PermPublic {
		required := perm
		next := h
		handler = func(w http.ResponseWriter, req *http.Request) {
			actor, ok := ActorFrom(req.Context())
			if !ok {
				httpx.Fail(w, req, httpx.Unauthorized("Нужно войти"))
				return
			}
			if required != PermAuthenticated {
				// У ученика прав из матрицы нет: матрица описывает роли
				// сотрудников. Маршруты ученика объявляются как
				// PermAuthenticated и проверяют принадлежность объекта сами.
				if !actor.IsUser() || !g.svc.Can(actor.Role, required) {
					httpx.Fail(w, req, PermissionDenied())
					return
				}
			}
			next(w, req)
		}
	}

	r.Method(method, pattern, http.HandlerFunc(handler))
}

// Verify обходит дерево маршрутов и требует, чтобы каждый был объявлен.
//
// Вызывается при старте: незадекларированный маршрут — это ручка, о правах
// которой никто не подумал, и узнавать об этом на проде поздно.
func (g *Guard) Verify(routes chi.Routes, skipPrefixes ...string) error {
	var undeclared []string

	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		for _, p := range skipPrefixes {
			if strings.HasPrefix(route, p) {
				return nil
			}
		}
		if _, ok := g.declared[routeKey(method, route)]; !ok {
			undeclared = append(undeclared, method+" "+route)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("iam: не удалось обойти маршруты: %w", err)
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		return fmt.Errorf(
			"iam: маршруты не объявили требуемое право (используйте Guard.Handle):\n  %s",
			strings.Join(undeclared, "\n  "))
	}
	return nil
}

func routeKey(method, pattern string) string {
	// chi.Walk отдаёт паттерн с завершающим слешем для подмаршрутов —
	// приводим к одному виду, иначе объявление не совпадёт.
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		pattern = "/"
	}
	return strings.ToUpper(method) + " " + pattern
}

// ---------------------------------------------------------------------------
// Middleware аутентификации
// ---------------------------------------------------------------------------

// Authenticate распознаёт учителя по cookie сессии.
//
// Не отказывает: просто кладёт актора в контекст, если он есть. Требование
// входа проверяет Guard.Handle — так публичные и защищённые ручки живут в
// одном дереве маршрутов без дублирования middleware.
func (g *Guard) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := readCookie(r, g.cfg.SessionCookie)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		actor, err := g.svc.Authenticate(r.Context(), token)
		if err != nil {
			if !authRejected(err) {
				failUnavailable(w, r, err)
				return
			}
			// Протухшая cookie не должна ронять публичные ручки: чистим её
			// и пропускаем запрос дальше как неаутентифицированный.
			clearCookie(w, g.cfg, g.cfg.SessionCookie)
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
	})
}

// AuthenticateStudent распознаёт ученика по его cookie.
//
// Ставится после Authenticate: если в запросе есть обе cookie, учитель
// приоритетнее — это его браузер, открывший урок на предпросмотр.
func (g *Guard) AuthenticateStudent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := ActorFrom(r.Context()); ok {
			next.ServeHTTP(w, r)
			return
		}
		token := readCookie(r, g.cfg.StudentCookie)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		actor, err := g.svc.AuthenticateStudent(r.Context(), token)
		if err != nil {
			if !authRejected(err) {
				failUnavailable(w, r, err)
				return
			}
			clearCookie(w, g.cfg, g.cfg.StudentCookie)
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
	})
}

// authRejected — токен действительно не принят: не найден, истёк, учётка
// закрыта. Только тогда cookie можно стирать.
func authRejected(err error) bool {
	return httpx.AsError(err).Status == http.StatusUnauthorized
}

// failUnavailable — проверить токен не вышло (база не ответила, пул
// переполнен). Cookie не трогаем и анонимом запрос не пропускаем: иначе
// под нагрузкой браузер навсегда терял вход, ученик заново входил по коду
// класса и получал второй профиль, а учителя выкидывало посреди урока.
func failUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	httpx.Fail(w, r, httpx.Unavailable("Сервер ненадолго недоступен, попробуйте ещё раз").Wrap(err))
}

// RequireWritable блокирует изменяющие запросы в сессии «войти как
// пользователь», открытой на чтение (NEW-18).
func RequireWritable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := ActorFrom(r.Context())
		if ok && actor.ReadOnly && r.Method != http.MethodGet && r.Method != http.MethodHead {
			httpx.Fail(w, r, httpx.Forbidden("Сессия просмотра открыта только на чтение"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Cookie
// ---------------------------------------------------------------------------

func readCookie(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// setCookie ставит cookie сессии.
//
// HttpOnly: JavaScript не должен иметь доступа к токену — иначе любая XSS
// на странице превращается в угон сессии. SameSite=Lax отсекает CSRF на
// изменяющих запросах, но не ломает переход по ссылке-приглашению.
func setCookie(w http.ResponseWriter, cfg config.AuthConfig, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		Domain:   cfg.CookieDomain,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, cfg config.AuthConfig, name string) {
	setCookie(w, cfg, name, "", -1)
}
