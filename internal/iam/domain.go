// Package iam — организации, учителя, роли и права, ученики.
//
// Границы модуля: iam владеет схемой iam и НЕ читает чужие схемы. Другие
// модули узнают о пользователях и учениках через интерфейсы этого пакета
// (Directory, StudentDirectory), а не через SQL-join. См.
// docs/backend/ARCHITECTURE.md.
//
// 152-ФЗ: у ученика нет ФИО, email, телефона и возраста — только отображаемое
// имя (имя или псевдоним), служебный тег и персональный код возврата.
// Проверяется тестом TestStudentTableHasNoPersonalData.
package iam

import (
	"time"

	"github.com/google/uuid"
)

// Роли. Матрица прав лежит в таблице iam.role_permissions и загружается
// при старте — россыпи проверок по обработчикам быть не должно.
const (
	RoleTeacher    = "teacher"
	RoleSuperadmin = "superadmin"
	RoleSupport    = "support"
	RoleModerator  = "moderator"
)

// Права. Строки совпадают с iam.permissions.code; расхождение ловится
// тестом TestPermissionConstantsMatchDatabase.
const (
	PermClassesOwn      = "classes.own"
	PermLessonsOwn      = "lessons.own"
	PermStudentsOwn     = "students.own"
	PermUsersView       = "users.view"
	PermUsersCreate     = "users.create"
	PermUsersBlock      = "users.block"
	PermUsersDelete     = "users.delete"
	PermUsersImpersonat = "users.impersonate"
	PermOrgsView        = "orgs.view"
	PermOrgsManage      = "orgs.manage"
	PermPlansView       = "plans.view"
	PermPlansAssign     = "plans.assign"
	PermModerationQueue = "moderation.queue"
	PermModerationAct   = "moderation.act"
	PermCostView        = "cost.view"
	PermAuditViewOwn    = "audit.view_own"
	PermAuditViewAll    = "audit.view_all"
)

// Статусы пользователя.
//
// Статус — единственный источник правды о том, может ли человек войти.
// Второго независимого признака («почта подтверждена», «одобрен») быть не
// должно: два признака рано или поздно расходятся, и вход открывается тому,
// кому не собирались.
const (
	StatusPending = "pending" // почта не подтверждена — войти нельзя
	// StatusAwaitingApproval — почта подтверждена, ждём решения человека.
	// Подтверждённый адрес доказывает владение почтой, но не то, что человек
	// действительно учитель, — поэтому между ним и входом стоит одобрение.
	StatusAwaitingApproval = "awaiting_approval"
	StatusActive           = "active"
	StatusBlocked          = "blocked"
	StatusDeleted          = "deleted"
)

// Виды одноразовых токенов из писем. Совпадают с CHECK на iam.email_tokens.kind.
const (
	tokenKindVerify  = "verify"
	tokenKindReset   = "reset"
	tokenKindApprove = "approve"
)

// Решения по заявке. Строки приходят от фронта в /auth/approve.
const (
	DecisionApprove = "approve"
	DecisionReject  = "reject"
)

// Происхождение учётки. demo — перенесённые старые демо-логины: они работают
// до даты отключения, записанной в docs/backend/AUTH.md.
const (
	OriginSelf  = "self"
	OriginDemo  = "demo"
	OriginAdmin = "admin"
)

// Виды организаций. Одиночный репетитор и школа — одна модель.
const (
	OrgKindB2C     = "b2c"
	OrgKindSchool  = "school"
	OrgKindPartner = "partner"
)

// Organization — единица тенанта.
type Organization struct {
	ID        uuid.UUID
	Kind      string
	Title     string
	Status    string
	CreatedAt time.Time
}

// User — учитель или администратор платформы.
//
// PasswordHash наружу не выходит: у структуры нет json-тегов намеренно,
// сериализация ответов идёт через отдельные DTO в http.go.
type User struct {
	ID              uuid.UUID
	OrgID           uuid.UUID
	Email           string
	DisplayName     string
	Role            string
	Status          string
	Origin          string
	PasswordHash    string
	EmailVerifiedAt *time.Time
	LastLoginAt     *time.Time
	CreatedAt       time.Time
}

// CanLogin — единственное место, где статус превращается в «пускать или нет».
//
// Пускает только active. Подтверждённая почта (awaiting_approval) входа не
// даёт: между ней и кабинетом стоит решение человека. Проверка одна на весь
// модуль намеренно — второе такое условие где-нибудь в обработчике однажды
// разойдётся с этим.
func CanLogin(status string) bool { return status == StatusActive }

// Active сообщает, может ли пользователь войти.
func (u User) Active() bool { return CanLogin(u.Status) }

// ApprovalRequest — заявка учителя глазами того, кто её рассматривает.
//
// Отдельный тип, а не User: рассматривающий не вошёл в систему (его авторизует
// токен из письма), и отдавать ему доменного пользователя с хешем пароля
// незачем.
type ApprovalRequest struct {
	UserID      uuid.UUID
	Name        string
	Email       string
	Org         string
	RequestedAt time.Time
	Status      string
	// Decided — по ссылке уже кликнули. Нужен, чтобы страница одобрения
	// показала «решение принято», а не пустую форму.
	Decided bool
}

// Session — сессия учителя. Токен хранится только хешем.
type Session struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	UserID         uuid.UUID
	ImpersonatedBy *uuid.UUID
	ReadOnly       bool
	ExpiresAt      time.Time
	LastSeenAt     time.Time
}

// Impersonated сообщает, открыта ли сессия администратором через
// «войти как пользователь». Такая сессия по умолчанию только на чтение.
func (s Session) Impersonated() bool { return s.ImpersonatedBy != nil }

// Student — ученик.
//
// Полей с персональными данными здесь нет и не должно появиться.
// DisplayName — это «как тебя показывать», а не ФИО.
type Student struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	DisplayName string
	// PublicTag — короткий тег вида #A7F2: различает тёзок в одном классе.
	PublicTag string
	// PublicCode — личный код ученика. Заменяет пароль: ученик вводит его с
	// карточки, учитель видит его в составе класса.
	PublicCode string
	Level      string
	CreatedAt  time.Time
	LastSeenAt *time.Time
}

// Actor — кто выполняет запрос. Заполняется middleware и живёт в контексте.
//
// Один тип и для учителя, и для ученика: обработчику важно, кто пришёл и что
// ему разрешено, а не каким контуром он аутентифицирован.
type Actor struct {
	Kind      string // "user" или "student"
	UserID    uuid.UUID
	StudentID uuid.UUID
	OrgID     uuid.UUID
	Role      string
	SessionID uuid.UUID
	ReadOnly  bool
	// Impersonator непустой, когда сессию открыл администратор.
	Impersonator *uuid.UUID
}

// IsUser и IsStudent — короткие проверки для обработчиков.
func (a Actor) IsUser() bool    { return a.Kind == "user" }
func (a Actor) IsStudent() bool { return a.Kind == "student" }
