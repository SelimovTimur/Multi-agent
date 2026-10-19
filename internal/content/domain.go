// Package content — уроки, их версии и содержимое.
//
// Границы: читает и пишет только схему content. Об учениках, классах и
// назначениях не знает — назначение ссылается на версию урока, а не
// наоборот.
//
// Главная идея модуля: урок собирается один раз и живёт годами. Правка не
// меняет уже выданное, а создаёт новую версию. Класс, которому урок выдали
// в сентябре, в мае видит то же, что видел в сентябре.
package content

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Статусы урока в библиотеке.
const (
	LessonDraft     = "draft"     // есть хотя бы одна версия, опубликованных нет
	LessonPublished = "published" // есть опубликованная версия
	LessonArchived  = "archived"  // скрыт из библиотеки
)

// Статусы версии.
//
// draft → ready → published — путь вперёд; archived — тупик с любого места.
// Опубликованная версия неизменяема: её можно только архивировать.
const (
	VersionDraft     = "draft"
	VersionReady     = "ready"
	VersionPublished = "published"
	VersionArchived  = "archived"
)

// versionTransitions — разрешённые переходы статуса версии.
//
// Таблицей, а не россыпью if'ов: так список допустимого можно показать
// пользователю в тексте ошибки, а не заставлять его угадывать.
var versionTransitions = map[string][]string{
	VersionDraft:     {VersionReady, VersionArchived},
	VersionReady:     {VersionPublished, VersionDraft, VersionArchived},
	VersionPublished: {VersionArchived},
	VersionArchived:  {},
}

// AllowedNext возвращает статусы, в которые можно перейти из текущего.
func AllowedNext(status string) []string { return versionTransitions[status] }

// canTransition проверяет допустимость перехода.
func canTransition(from, to string) bool {
	for _, s := range versionTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Типы упражнений.
const (
	ExerciseMatching = "matching"
	ExerciseGapFill  = "gap_fill"
	ExerciseLogical  = "logical"
	ExerciseGrammar  = "grammar"
	ExerciseCustom   = "custom"
)

// Откуда взялся элемент: предложен моделью или добавлен учителем.
const (
	OriginModel   = "model"
	OriginTeacher = "teacher"
)

// Lesson — урок в библиотеке учителя.
//
// Класса здесь нет намеренно: урок не принадлежит классу, он выдаётся в
// классы назначениями. Отсутствие колонки class_id закреплено тестом.
type Lesson struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	AuthorID   uuid.UUID
	Title      string
	LangCode   string
	Level      string
	Status     string
	SourceKind string
	// SourceMaterialID — запись каталога, из которой собран урок. nil,
	// если материала не было. Даёт пересобрать урок из того же источника
	// и ответить, что уже сделано из этой записи.
	SourceMaterialID *uuid.UUID
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// LessonVersion — конкретная редакция урока.
type LessonVersion struct {
	ID              uuid.UUID
	OrgID           uuid.UUID
	LessonID        uuid.UUID
	Version         int
	Status          string
	Title           string
	Level           string
	RefinedText     string
	FrankText       string
	GrammarRaw      string
	QualityWarnings json.RawMessage
	Note            string
	PublishedAt     *time.Time
	CreatedBy       uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// VersionMark — номер и статус последней неархивной версии урока.
//
// Библиотеке нужен только номер («версия v3») и статус, а не версия целиком:
// строка списка не показывает ни текстов, ни упражнений, и вычитывать их на
// каждый урок значило бы платить за то, чего никто не увидит.
type VersionMark struct {
	Version int
	Status  string
}

// LexicalUnit — ключевое выражение версии.
type LexicalUnit struct {
	ID          uuid.UUID
	Position    int
	Phrase      string
	Translation string
	Origin      string
}

// GrammarTopic — грамматическая тема версии.
//
// Example остаётся строкой примеров через точку с запятой: на нём держатся
// уроки, собранные до появления разбора, и он же короткая подпись темы.
// Разбор живёт рядом: объяснение, примеры парами и задания на конструкцию —
// ровно то, что показывает экран «Как устроен этот текст» в прототипе.
type GrammarTopic struct {
	ID          uuid.UUID
	Position    int
	Title       string
	Example     string
	Origin      string
	Explanation string
	Examples    []GrammarExample
	Practice    []GrammarTask
}

// GrammarExample — пример конструкции и его перевод.
type GrammarExample struct {
	Phrase      string `json:"phrase"`
	Translation string `json:"translation"`
}

// GrammarTask — задание на конструкцию и ответ к нему.
//
// Ответ виден учителю и не уходит ученику: разбор грамматики — это
// объяснение, а не упражнение с проверкой. Проверяемые задания живут
// отдельно, в content.exercises.
type GrammarTask struct {
	Prompt string `json:"prompt"`
	Answer string `json:"answer"`
}

// MaxLexicon — сколько ключевых выражений помещается в урок (NR-27).
const MaxLexicon = 10

// Пределы числа элементов разбора грамматики по NR-27.
// Длину цитат не ограничиваем: размер запроса ограничен на HTTP-границе.
const (
	GrammarMaxTopics   = 3
	GrammarMaxExamples = 10
	GrammarMaxPractice = 10
)

// Exercise — упражнение версии.
//
// AnswerKey отделён от Payload не для красоты: ключ ответов НИКОГДА не
// уходит на ручки ученика, и разделение делает эту ошибку заметной при
// чтении кода.
type Exercise struct {
	ID          uuid.UUID
	Type        string
	Position    int
	Title       string
	Instruction string
	Payload     []byte
	AnswerKey   []byte
	RawSource   string
}

// AvatarConfig — настройка разговора с аватаром в версии урока (NR-29).
//
// Поля — ровно экран «Разговор с аватаром» из прототипа: «Роль и тон» одним
// текстом, длина разговора в репликах и два списка «для отработки в
// разговоре». Подсказка и словарь (NEW-25) заведены здесь же, чтобы не
// открывать экран настройки дважды; оба выключены по умолчанию.
type AvatarConfig struct {
	// Enabled — блок есть в уроке. Учитель может убрать его с карты.
	Enabled         bool
	RolePrompt      string
	MaxTurns        int
	PracticePhrases []string
	PracticeGrammar []string
	// PracticePhraseNotes и PracticeGrammarNotes — пояснение к каждой цели
	// разговора, той же длины, что и списки; пустая строка — пояснения нет.
	// Это текст учителя для модели и для отчёта: ученику не отдаётся, как и
	// роль.
	//
	// nil во входящей правке значит «не прислали» (старый клиент, карта
	// урока): тогда пояснения берутся у прежней настройки по совпадающим
	// фразам, а не стираются. Прочитанная из базы настройка всегда выровнена
	// по длине списков.
	PracticePhraseNotes  []string
	PracticeGrammarNotes []string
	HintEnabled          bool
	DictionaryEnabled    bool
	// Saved — учитель сохранял настройку. Пока не сохранял, списки пусты, и
	// разговор отрабатывает всю лексику и грамматику урока.
	Saved bool
}

// Пределы настройки аватара. Длина разговора — из NR-29 и прототипа
// (ползунок 3–20, по умолчанию 8); остальные держат промпт разумным.
// Пояснение к цели — из договора avatar-goals: оно уходит в каждый ход
// разговора, и абзац текста на цель раздул бы промпт без пользы.
const (
	AvatarDefaultTurns = 8
	AvatarMinTurns     = 3
	AvatarMaxTurns     = 20
	AvatarRoleMaxRunes = 1000
	AvatarMaxPhrases   = 30
	AvatarMaxGrammar   = 10
	AvatarItemMaxRunes = 200
	AvatarNoteMaxRunes = 300
)

// DefaultAvatarConfig — разговор урока, который учитель не настраивал.
// Аватар работает базово, как до NR-29: пустая настройка допустима.
func DefaultAvatarConfig() AvatarConfig {
	return AvatarConfig{Enabled: true, MaxTurns: AvatarDefaultTurns}
}

// Objective — цель урока: чему ученик научится.
//
// Блок декоративный: на генерацию упражнений и аналитику цели не влияют
// (решение по NEW-7). Нужны учителю и отчётности, поэтому живут в версии,
// а не собираются заново при каждом открытии.
type Objective struct {
	ID       uuid.UUID
	Position int
	Text     string
}

// ObjectivesMax — сколько целей помещается в урок. Больше — это уже не
// цели, а пересказ содержания.
const (
	ObjectivesMax     = 8
	ObjectiveMaxRunes = 300
)

// VersionContent — версия вместе со всем содержимым.
type VersionContent struct {
	Version    LessonVersion
	Lexicon    []LexicalUnit
	Grammar    []GrammarTopic
	Exercises  []Exercise
	Avatar     AvatarConfig
	Objectives []Objective
}
