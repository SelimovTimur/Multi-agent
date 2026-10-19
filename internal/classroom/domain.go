// Package classroom — классы и их состав.
//
// Границы модуля: читает и пишет ТОЛЬКО схему classroom. Имена учеников
// живут в схеме iam и дочитываются через интерфейс iam.StudentDirectory,
// а не join'ом между схемами. Это то, что позволит вынести classroom в
// отдельный сервис, не переписывая запросы.
//
// Зависимость направлена в одну сторону: classroom → iam. Обратной нет.
package classroom

import (
	"time"

	"github.com/google/uuid"
)

// Статусы класса и записи ученика в классе.
const (
	ClassActive   = "active"
	ClassArchived = "archived"

	EnrollmentActive  = "active"
	EnrollmentRemoved = "removed"
)

// joinCodeLength — длина кода класса. Самостоятельного входа по коду больше
// нет, но колонка обязательна и уникальна, поэтому код по-прежнему выдаётся.
const joinCodeLength = 6

// Class — класс или группа учителя.
//
// JoinCode и JoinOpen остались от самостоятельного входа в класс по коду.
// Входа больше нет — учеников заводят списком, — но колонки в базе есть, и
// их значения читаются вместе с классом.
type Class struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	TeacherID    uuid.UUID
	Title        string
	Subject      string
	LangCode     string
	DefaultLevel string
	JoinCode     string
	JoinOpen     bool
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Enrollment — запись ученика в классе.
//
// Исключение мягкое: статус removed закрывает доступ, но сохраняет ученика
// и его ответы. Иначе исключение из класса стирало бы историю обучения,
// которая нужна и учителю, и самому ученику при возврате.
type Enrollment struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	ClassID    uuid.UUID
	StudentID  uuid.UUID
	TeacherID  uuid.UUID
	Status     string
	JoinedAt   time.Time
	RemovedAt  *time.Time
	LastSeenAt *time.Time
}

// Member — строка состава класса: запись плюс имя ученика из iam.
type Member struct {
	Enrollment  Enrollment
	DisplayName string
	PublicTag   string
	// PublicCode — личный код ученика. Учитель видит его в составе класса:
	// карточки раздаёт он, и потерянную карточку восстанавливает тоже он.
	PublicCode string
	Level      string
	// LastSeenAt — когда ученик последний раз заходил на платформу, из
	// профиля в iam. Отметка в записи о классе для этого не годится: она
	// ставится при записи в класс и дальше не меняется, а учеников теперь
	// записывает учитель — «был сегодня» у ребёнка, который ни разу не
	// входил, было бы неправдой.
	LastSeenAt *time.Time
}
