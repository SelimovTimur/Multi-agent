// Package learning — назначения уроков, попытки и ответы учеников.
//
// Границы: читает и пишет только схему learning. Об уроках знает ровно одно —
// идентификатор версии; о классах — идентификатор класса. Всё остальное
// берётся через интерфейсы у content и classroom.
//
// Назначение ссылается на конкретную версию урока, а не на урок. Поэтому
// правка урока в библиотеке не меняет того, что уже выдано классу: класс,
// получивший урок в сентябре, в мае видит сентябрьскую редакцию.
package learning

import (
	"time"

	"github.com/google/uuid"
)

// Состояния назначения.
//
// С выдачей по заданиям («Урок в классе») назначение только прикрепляет урок
// к классу: что ученик видит и когда, решают выдачи (classwork.go). Новые
// назначения создаются активными; «запланированное» бывает только у старых,
// и фоновая задача открывает его по opens_at. Закрытое — урок снят с класса.
const (
	StateScheduled = "scheduled" // время не наступило (старые назначения)
	StateActive    = "active"    // урок прикреплён к классу
	StateClosed    = "closed"    // урок снят с класса, ответы сохранены
)

// Цели назначения. Пока поддержан только класс: подгруппы и отдельные
// ученики есть в схеме заделом, но API их не принимает.
const (
	TargetClass = "class"
)

// Assignment — урок, выданный классу на период.
type Assignment struct {
	ID              uuid.UUID
	OrgID           uuid.UUID
	LessonVersionID uuid.UUID
	ClassID         uuid.UUID
	TargetType      string
	OpensAt         *time.Time
	ClosesAt        *time.Time
	// ClosedAt — когда доступ кончился на самом деле. У закрытого по сроку
	// совпадает с ClosesAt, у закрытого учителем — момент нажатия. Пустой,
	// пока назначение живо: страница класса по нему и различает «закрыт
	// 24 сентября» и «закрыт досрочно».
	ClosedAt  *time.Time
	State     string
	CreatedBy uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Open сообщает, открыто ли окно назначения прямо сейчас.
//
// Это окно старых назначений (opens_at/closes_at), которое ещё правит старая
// ручка переноса сроков (PATCH /assignments/{id}). Доступ ученика к заданиям
// решают выдачи (Issue.Open): при переносе окно стало сроком перенесённой
// выдачи «на дом».
func (a Assignment) Open(now time.Time) bool {
	if a.State == StateClosed {
		return false
	}
	if a.OpensAt != nil && now.Before(*a.OpensAt) {
		return false
	}
	if a.ClosesAt != nil && !now.Before(*a.ClosesAt) {
		return false
	}
	return true
}

// Attempt — прохождение урока учеником.
//
// Одна на пару назначение-ученик: повторный вход продолжает начатое, а не
// начинает заново. Иначе ученик, у которого сел интернет, терял бы прогресс.
type Attempt struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	AssignmentID   uuid.UUID
	StudentID      uuid.UUID
	CurrentStep    int
	Score          int
	CorrectAnswers int
	TotalAnswers   int
	// TotalQuestions — сколько вопросов в уроке. От него считается доля
	// верных: от числа данных ответов она врёт в пользу того, кто отвечал
	// меньше.
	TotalQuestions int
	// Answers — что уже отвечено, ключ «упражнение:вопрос». Повторный ответ
	// на тот же вопрос счёт не меняет, а при возврате в урок видно прежний
	// выбор.
	Answers map[string]GivenAnswer
	// Completed — урок пройден, а не пролистан кнопкой «Дальше».
	Completed  bool
	StartedAt  time.Time
	LastSeenAt time.Time
	FinishedAt *time.Time
}

// GivenAnswer — ответ, который ученик уже дал.
type GivenAnswer struct {
	Answer   string `json:"answer"`
	Correct  bool   `json:"correct"`
	Expected string `json:"expected,omitempty"`
	Step     int    `json:"step,omitempty"`
	// At — когда дан ответ: по нему считается лучшая серия верных подряд.
	// У ответов, данных до появления серии, его нет — их серия идёт по
	// порядку урока.
	At time.Time `json:"at,omitzero"`
}

// Answered — на сколько вопросов ученик ответил.
func (a Attempt) Answered() int { return len(a.Answers) }

// Passed — можно ли считать урок пройденным.
//
// Не «дошёл до конца», а «сделал больше половины»: девять нажатий «Дальше»
// без единого ответа — это пролистанный урок, и показывать его учителю как
// пройденный значит врать в ту сторону, где он перестанет проверять.
func (a Attempt) Passed() bool {
	if a.TotalQuestions == 0 {
		// В уроке нечего отвечать — засчитываем сам факт прохождения.
		return a.FinishedAt != nil
	}
	return a.FinishedAt != nil && a.Answered()*2 >= a.TotalQuestions
}

// AnswerEvent — один проверенный ответ.
//
// Пишется в секционированную по неделям таблицу. Запись асинхронная: её
// сбой не должен ронять проверку ответа — ученик важнее статистики.
type AnswerEvent struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	AttemptID  uuid.UUID
	StudentID  uuid.UUID
	ExerciseID *uuid.UUID
	Step       int
	Answer     string
	IsCorrect  bool
	ScoreDelta int
	// Kind — вид задания: сопоставление, пропуски, логическое продолжение.
	// По нему считается разбивка «как даются задания»; номер шага для этого
	// не годится — у разных уроков под одним номером разные задания.
	Kind       string
	OccurredAt time.Time
}

// DialogTurn — одна реплика в разговоре с аватаром.
//
// Пишется в секционированную по неделям таблицу: у класса в 30 человек
// разговор порождает сотни строк за урок.
type DialogTurn struct {
	ID          uuid.UUID
	OrgID       uuid.UUID
	AttemptID   uuid.UUID
	StudentID   uuid.UUID
	Role        string // student или avatar
	Content     string
	ClientMsgID string
	OccurredAt  time.Time

	// Поля разговора по целям урока (миграция 0021). Прежние режимы их не
	// пишут: у их реплик номер хода пуст, ввод — «text», остальное пусто.
	//
	// TurnNo — номер хода: ответы ученика 1…N, вступление аватара 0, ответ
	// аватара на k-й ответ — k.
	TurnNo *int
	// Input — как получен ответ: InputVoice или InputText.
	Input string
	// ASRText и ASRConfidence — что распознала модель. В разговор и оценку
	// идёт Content — отправленный текст; распознанный нужен учителю.
	ASRText       string
	ASRConfidence *float32
	// Edited — ученик исправил распознанный текст перед отправкой.
	Edited bool
	// Translation — перевод реплики аватара для «Подсказки».
	Translation string
	// Steer — у ответа ученика в живом режиме: указание сессии на следующий
	// ход, которое движок выдал на этот ответ (миграция 0022). Повтор ответа
	// отдаёт его же.
	Steer string
	// SteerTurn — у реплики аватара из живой сессии: номер хода указания,
	// которое уже дошло до сессии к началу реплики. nil — браузер не прислал.
	SteerTurn *int
}

// Роли в диалоге.
const (
	RoleStudent = "student"
	RoleAvatar  = "avatar"
)
