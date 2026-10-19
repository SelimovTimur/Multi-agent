package iam

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
)

// Регистрация теперь двухступенчатая: подтверждённая почта ставит заявку в
// очередь, вход открывает человек. Здесь закрепляется именно это — и то, что
// обойти очередь нельзя ни ссылкой подтверждения, ни «забыли пароль».
//
// Базы в тестах пакета нет, поэтому проверяется то, что от базы не зависит:
// правила переходов, тексты и коды ответов, разбор списков и сами письма.
// Что именно осталось непроверенным — написано в docs/backend/AUTH.md.

// ---------------------------------------------------------------------------
// Кого пускаем
// ---------------------------------------------------------------------------

func TestOnlyActiveCanLogIn(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{StatusActive, true},
		{StatusPending, false},
		{StatusAwaitingApproval, false},
		{StatusBlocked, false},
		{StatusDeleted, false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.status, func(t *testing.T) {
			if got := CanLogin(c.status); got != c.want {
				t.Errorf("CanLogin(%q) = %v, ожидали %v", c.status, got, c.want)
			}
			if got := (User{Status: c.status}).Active(); got != c.want {
				t.Errorf("User.Active() при статусе %q = %v", c.status, got)
			}
		})
	}
}

// Текст ошибки входа — это инструкция «что делать дальше». Она показывается
// только после ВЕРНОГО пароля, поэтому статус своего аккаунта человек видеть
// вправе; для чужого адреса этот ответ недостижим.
func TestLoginRefusalPerStatus(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		wantNil    bool
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{
			name:    "активный проходит",
			status:  StatusActive,
			wantNil: true,
		},
		{
			name:       "почта не подтверждена — зовём в почту",
			status:     StatusPending,
			wantStatus: http.StatusForbidden,
			wantCode:   httpx.CodeForbidden,
			wantMsg:    "Подтвердите почту — мы отправили ссылку на неё",
		},
		{
			name:       "ждёт одобрения — обещаем написать",
			status:     StatusAwaitingApproval,
			wantStatus: http.StatusForbidden,
			wantCode:   httpx.CodeForbidden,
			wantMsg:    "Заявка на рассмотрении. Мы напишем, когда аккаунт одобрят",
		},
		{
			name:       "заблокирован — без подробностей",
			status:     StatusBlocked,
			wantStatus: http.StatusForbidden,
			wantCode:   httpx.CodeForbidden,
			wantMsg:    "Доступ закрыт",
		},
		{
			name:       "удалён отвечает как заблокированный",
			status:     StatusDeleted,
			wantStatus: http.StatusForbidden,
			wantCode:   httpx.CodeForbidden,
			wantMsg:    "Доступ закрыт",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := errStatusRefused(c.status)
			if c.wantNil {
				if err != nil {
					t.Fatalf("активному аккаунту отказали: %v", err)
				}
				return
			}
			e := httpx.AsError(err)
			if e.Status != c.wantStatus || e.Code != c.wantCode {
				t.Errorf("получили %d/%s, ожидали %d/%s", e.Status, e.Code, c.wantStatus, c.wantCode)
			}
			if e.Message != c.wantMsg {
				t.Errorf("сообщение = %q, ожидали %q", e.Message, c.wantMsg)
			}
			// Фронт разводит экраны по машиночитаемому полю, а не по тексту.
			if e.Fields["status"] != c.status {
				t.Errorf("в ответе нет статуса %q: fields=%v", c.status, e.Fields)
			}
		})
	}
}

// Отказ по статусу обязан существовать ровно для тех, кому вход закрыт.
// Разъехавшись, эти две проверки дали бы либо молчаливый отказ, либо вход
// вопреки статусу.
func TestRefusalMatchesCanLogin(t *testing.T) {
	for _, status := range []string{StatusActive, StatusPending, StatusAwaitingApproval,
		StatusBlocked, StatusDeleted, "нечто-новое"} {
		if CanLogin(status) != (errStatusRefused(status) == nil) {
			t.Errorf("статус %q: CanLogin и errStatusRefused расходятся", status)
		}
	}
}

// ---------------------------------------------------------------------------
// Заблокированный аккаунт не воскресает письмом
// ---------------------------------------------------------------------------

// Главная защита — условие в UPDATE. Список статусов вынесен в переменную
// именно ради этой проверки: дописав в него blocked, легко открыть вход
// заблокированному, и это должно сломать тест, а не прод.
func TestBlockedUserCannotBeRevivedByEmailLink(t *testing.T) {
	open := map[string]bool{}
	for _, s := range statusesOpenToTransition {
		open[s] = true
	}
	for _, forbidden := range []string{StatusBlocked, StatusDeleted, StatusActive} {
		if open[forbidden] {
			t.Errorf("статус %q попал в statusesOpenToTransition: ссылка из письма "+
				"не должна менять такие учётки", forbidden)
		}
	}
	for _, allowed := range []string{StatusPending, StatusAwaitingApproval} {
		if !open[allowed] {
			t.Errorf("статус %q пропал из statusesOpenToTransition: заявку станет нечем двигать", allowed)
		}
	}
}

// Раньше ResetPassword вызывал ActivateUser: «забыли пароль» возвращал вход
// тому, кому его закрыли, и заодно обходил подтверждение почты. Проверяем по
// исходнику — воспроизвести это без базы иначе нечем.
func TestPasswordResetDoesNotChangeStatus(t *testing.T) {
	calls := callsWithin(t, "service.go", "ResetPassword")
	for _, forbidden := range []string{"ActivateUser", "ConfirmEmail", "SetUserStatus", "RejectUser"} {
		if calls[forbidden] {
			t.Errorf("ResetPassword вызывает %s: смена пароля не должна менять статус аккаунта "+
				"— заблокированный вернул бы себе вход через «забыли пароль»", forbidden)
		}
	}
	// Смысл сброса при этом должен сохраниться.
	if !calls["UpdatePassword"] || !calls["DeleteUserSessions"] {
		t.Error("ResetPassword перестал менять пароль или гасить сессии")
	}
}

// Подтверждение почты не должно открывать сессию: иначе человек оказывается
// в кабинете, минуя одобрение.
func TestVerifyEmailDoesNotOpenSession(t *testing.T) {
	for _, c := range []struct{ file, fn string }{
		{"service.go", "VerifyEmail"},
		{"http.go", "verifyEmail"},
	} {
		if callsWithin(t, c.file, c.fn)["openSession"] {
			t.Errorf("%s: %s открывает сессию — это вход в обход одобрения", c.file, c.fn)
		}
	}
}

// GET по ссылке из письма не имеет права принимать решение: почтовые
// антивирусы и превьюшки мессенджеров ходят по ссылкам сами.
func TestApprovalReadDoesNotConsumeToken(t *testing.T) {
	if callsWithin(t, "service.go", "Approval")["ConsumeEmailToken"] {
		t.Error("Approval гасит токен: тогда заявку «решит» почтовый антивирус")
	}
	// А решение — обязано: одноразовость токена и есть защита от второго
	// клика по той же ссылке.
	if !callsWithin(t, "service.go", "DecideApproval")["ConsumeEmailToken"] {
		t.Error("DecideApproval не гасит токен: по ссылке можно решать сколько угодно раз")
	}
}

// ---------------------------------------------------------------------------
// Список самоодобрения
// ---------------------------------------------------------------------------

func TestAutoApprovedEmails(t *testing.T) {
	svc := &Service{cfg: config.AuthConfig{
		AutoApproveEmails: []string{"team@example.com", " Lead@Example.COM "},
	}}

	cases := []struct {
		email string
		want  bool
	}{
		{"team@example.com", true},
		{"TEAM@example.com", true},
		{"  team@example.com  ", true},
		{"lead@example.com", true},
		{"teacher@school.ru", false},
		{"", false},
		{"team@example.com.evil.ru", false},
	}
	for _, c := range cases {
		t.Run(c.email, func(t *testing.T) {
			if got := svc.autoApproved(c.email); got != c.want {
				t.Errorf("autoApproved(%q) = %v, ожидали %v", c.email, got, c.want)
			}
		})
	}

	// Пустой список — никто не проходит мимо очереди. Это дефолт на проде.
	empty := &Service{}
	if empty.autoApproved("team@example.com") {
		t.Error("с пустым списком самоодобрения аккаунт активировался сам")
	}
}

// ---------------------------------------------------------------------------
// Решение по заявке
// ---------------------------------------------------------------------------

// Проверки, которые отрабатывают до похода в базу: сервис без пула их
// переживает, а значит до базы дело действительно не доходит.
func TestDecideApprovalRejectsBadInput(t *testing.T) {
	svc := &Service{}
	cases := []struct {
		name       string
		token      string
		decision   string
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "неизвестное решение",
			token:      "какой-то-токен",
			decision:   "maybe",
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "пустое решение",
			token:      "какой-то-токен",
			decision:   "",
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "пустой токен отвечает как несуществующий",
			token:      "   ",
			decision:   DecisionApprove,
			wantStatus: http.StatusNotFound,
			wantMsg:    msgDeadLink,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := svc.DecideApproval(context.Background(), c.token, c.decision)
			e := httpx.AsError(err)
			if e == nil {
				t.Fatal("ошибки нет")
			}
			if e.Status != c.wantStatus {
				t.Errorf("код %d, ожидали %d (%s)", e.Status, c.wantStatus, e.Message)
			}
			if c.wantMsg != "" && e.Message != c.wantMsg {
				t.Errorf("сообщение = %q, ожидали %q", e.Message, c.wantMsg)
			}
		})
	}
}

// Пустая ссылка и просроченная отвечают одинаково: по разнице ответов иначе
// перебирались бы когда-то существовавшие токены.
func TestApprovalHidesUnknownToken(t *testing.T) {
	_, err := (&Service{}).Approval(context.Background(), "")
	e := httpx.AsError(err)
	if e == nil || e.Status != http.StatusNotFound || e.Message != msgDeadLink {
		t.Fatalf("ожидали 404 с текстом %q, получили %v", msgDeadLink, err)
	}
}

// ---------------------------------------------------------------------------
// Лимит писем (NR-7)
// ---------------------------------------------------------------------------

func TestMailInterval(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		last time.Time
		sent bool
		want bool
	}{
		{name: "писем ещё не было", sent: false, want: false},
		{name: "письмо только что ушло", last: now.Add(-time.Second), sent: true, want: true},
		{name: "ровно минута назад — уже можно", last: now.Add(-time.Minute), sent: true, want: false},
		{name: "давно", last: now.Add(-time.Hour), sent: true, want: false},
		{name: "время съехало вперёд", last: now.Add(time.Minute), sent: true, want: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := tooSoon(c.last, c.sent, now); got != c.want {
				t.Errorf("tooSoon = %v, ожидали %v", got, c.want)
			}
		})
	}

	if mailInterval < time.Minute {
		t.Errorf("лимит писем ослаб до %s: NR-7 требует не чаще письма в минуту", mailInterval)
	}
}

// Лимит обязан жить в базе: реплик API две, и счётчик в памяти процесса
// пропускал бы по письму на каждую, а после перезапуска — сколько угодно.
func TestMailLimitIsStoredInDatabase(t *testing.T) {
	calls := callsWithin(t, "service.go", "mayMail")
	if !calls["LockUser"] {
		t.Error("mayMail не берёт блокировку строки: две реплики отправят по письму одновременно")
	}
	if !calls["LastEmailTokenAt"] {
		t.Error("mayMail считает лимит не по базе — он не переживёт перезапуск")
	}
}

// ---------------------------------------------------------------------------
// Схема
// ---------------------------------------------------------------------------

// Статусы и виды токенов из кода обязаны быть разрешены CHECK'ом в базе:
// иначе первый же переход упирается в ошибку ограничения уже на проде.
func TestMigrationAllowsNewStatusAndTokenKind(t *testing.T) {
	sql := readFile(t, filepath.Join("..", "..", "migrations", "0001_schema.up.sql"))
	for _, want := range []string{StatusAwaitingApproval, tokenKindApprove, "approved_at", "approved_by"} {
		if !strings.Contains(sql, want) {
			t.Errorf("в схеме нет %q", want)
		}
	}
	for _, want := range []string{StatusPending, StatusActive, StatusBlocked, StatusDeleted,
		tokenKindVerify, tokenKindReset} {
		if !strings.Contains(sql, want) {
			t.Errorf("схема потеряла значение %q — CHECK его не пропустит", want)
		}
	}
}

// ---------------------------------------------------------------------------
// Вспомогательное
// ---------------------------------------------------------------------------

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("не удалось прочитать %s: %v", path, err)
	}
	return string(data)
}

// callsWithin собирает имена методов, вызываемых внутри функции.
//
// Проверка по исходнику, а не по поведению, — вынужденная: интересующие нас
// переходы живут в SQL, а базы в тестах пакета нет. Зато она ловит ровно тот
// класс ошибок, который уже случался: «заодно активируем аккаунт здесь».
func callsWithin(t *testing.T, file, fnName string) map[string]bool {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("не удалось разобрать %s: %v", file, err)
	}

	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Name.Name == fnName {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatalf("в %s нет функции %s", file, fnName)
	}

	calls := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.SelectorExpr:
			calls[f.Sel.Name] = true
		case *ast.Ident:
			calls[f.Name] = true
		}
		return true
	})
	return calls
}
