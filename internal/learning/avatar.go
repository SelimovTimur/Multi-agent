package learning

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/NATIVE/NATIVE/internal/platform/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AvatarBrief — из чего строится разговор: материал урока и настройка
// учителя (NR-29).
//
// learning не знает ни схемы content, ни того, как llm_service собирает
// промпт: сюда приходит уже собранная сводка, дальше она уходит в порт.
type AvatarBrief struct {
	Text         string
	Collocations string
	Language     string
	Level        string

	// Enabled — разговор есть в уроке. Учитель может убрать блок с карты.
	Enabled           bool
	RolePrompt        string
	MaxTurns          int
	PracticePhrases   []string
	PracticeGrammar   []string
	HintEnabled       bool
	DictionaryEnabled bool
	History           []DialogTurn

	// Goals — цели разговора по целям урока: фразы с ключами p1…, затем
	// темы g1… Заморожены версией урока, как и всё в ней: ключ цели в отчёте
	// учителя обязан значить то же, что значил в разговоре ученика.
	Goals []AvatarGoal
	// LevelGuidance — компактные указания уровня из справочника CEFR для
	// промпта голосового разговора. Заполняется только там, где нужен
	// (OpenLiveDialog): движок разговора берёт указания у себя сам.
	LevelGuidance string
}

// Виды целей разговора.
const (
	GoalPhrase  = "phrase"
	GoalGrammar = "grammar"
)

// AvatarGoal — цель разговора: фокусная фраза или грамматическая тема.
//
// Note — пояснение учителя (или перевод и объяснение темы, если учитель
// разговор не настраивал). Уходит модели и в отчёт учителю, ученику — нет.
type AvatarGoal struct {
	Key   string
	Kind  string
	Title string
	Note  string
}

// LevelGuide — указания уровня для промпта голосового разговора.
//
// Справочник уровней живёт в llm_service; learning о нём знает только этот
// интерфейс. Реализация — адаптер в internal/app с небольшим кешем. Ошибок
// не возвращает: без указаний голосовой разговор идёт как раньше, по одной
// строке «уровень A2», и отказывать ученику из-за справочника незачем.
type LevelGuide interface {
	LevelGuidance(ctx context.Context, language, level string) string
}

// LessonMaterial — материал урока и настройка аватара для разговора.
type LessonMaterial interface {
	AvatarBriefFor(ctx context.Context, orgID, versionID uuid.UUID) (AvatarBrief, error)
	// TeacherAvatarBrief — то же для пробного разговора учителя: заодно
	// проверяет, что урок принадлежит учителю. Чужой — «не найден».
	TeacherAvatarBrief(ctx context.Context, actor iam.Actor, versionID uuid.UUID) (AvatarBrief, error)
}

// LiveVoice — непрерывный голосовой разговор (GPT Live).
//
// Звук идёт между браузером и провайдером напрямую; сервер только
// открывает сессию с политикой урока и держит ключ у себя.
type LiveVoice interface {
	// Supports — голосовой разговор на этом языке настроен и доступен.
	Supports(language string) bool
	// OpenConversation — живая сессия разговора по целям (раздел 10
	// договора): политика с целями и правилами разговора по целям и
	// инструменты делегата. brief.History — уже сказанное в разговоре.
	OpenConversation(ctx context.Context, orgID, studentID uuid.UUID, offerSDP string, brief AvatarBrief) (LiveAnswer, error)
	// Release — разговор закончен, место на ключе провайдера свободно.
	// Ошибок не возвращает: сбой освобождения — забота шлюза, а не ученика.
	Release(ctx context.Context, sessionID string)
}

// LiveAnswer — ответ провайдера на предложение соединения.
type LiveAnswer struct {
	SessionID string
	SDP       string
}

// WithMaterial подключает материал разговора.
//
// Отдельным методом, а не параметром конструктора: без аватара модуль
// работает — просто разговор недоступен. Так его можно поднять и без
// llm_service.
func (s *Service) WithMaterial(m LessonMaterial) *Service {
	s.material = m
	return s
}

// WithLiveVoice подключает голосовой разговор. Без него разговор идёт
// текстом и записью голоса через распознавание, как раньше.
func (s *Service) WithLiveVoice(v LiveVoice) *Service {
	s.live = v
	return s
}

// WithLevelGuide подключает указания уровня для голосового разговора.
func (s *Service) WithLevelGuide(g LevelGuide) *Service {
	s.guide = g
	return s
}

// LiveSession — открытое голосовое соединение и рамки разговора.
type LiveSession struct {
	Answer     LiveAnswer
	TurnsTaken int
	MaxTurns   int
}

// returnLiveSession — сессия не открылась: место в лимите урока возвращаем.
// Запрос ученика к этому моменту мог оборваться, поэтому контекст отвязан.
func (s *Service) returnLiveSession(ctx context.Context, orgID, attemptID uuid.UUID) {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.ReturnLiveSession(bg, s.pool, orgID, attemptID); err != nil {
		logx.From(bg).Warn("не удалось вернуть место голосовой сессии", "err", err)
	}
}

// maxLiveSessions — сколько раз ученик может открыть голосовой разговор.
//
// Считаем от длины разговора: обрыв связи, перезагрузка страницы и
// «начну-ка заново» — обычное дело, и одной сессии на попытку мало. Но
// бесконечно их открывать нельзя: каждая стоит денег у провайдера.
func maxLiveSessions(maxTurns int) int {
	if maxTurns < 1 {
		maxTurns = defaultAvatarTurns
	}
	return maxTurns + 4
}

// emitDialogueTurn — ответ ученика аватару для продуктовой аналитики.
func (s *Service) emitDialogueTurn(ctx context.Context, attempt Attempt, clientMsgID string) {
	// Ключ приходит от клиента: хеш сохраняет дедупликацию, но не переносит
	// произвольную строку (в том числе текст ученика) в продуктовую аналитику.
	if clientMsgID == "" {
		clientMsgID = idgen.New().String()
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(clientMsgID)))
	telemetry.Emit(ctx, s.attributed(ctx, attempt.OrgID, attempt.AssignmentID,
		telemetry.Event{Name: "dialogue_turn", EntityID: attempt.ID, Key: key}))
}

// attributed дописывает к событию ученика класс и учителя назначения. Без
// них реплики аватару и открытые задания попадали в сводках в «школа без
// учителя», а витрины не могли исключить нагрузочный класс по учителю.
// Назначение не прочиталось — событие уходит как есть: аналитика не повод
// ронять ответ ученика.
func (s *Service) attributed(ctx context.Context, orgID, assignmentID uuid.UUID, ev telemetry.Event) telemetry.Event {
	ev.AssignmentID = assignmentID
	if assignmentID == uuid.Nil {
		return ev
	}
	a, err := s.store.AssignmentByID(ctx, s.pool, orgID, assignmentID)
	if err != nil {
		return ev
	}
	ev.ClassID, ev.OwnerID = a.ClassID, a.CreatedBy
	return ev
}

// liveTurnsCap — жёсткий потолок реплик голосового разговора.
//
// Длина разговора (MaxTurns) — это про урок: дойдя до неё, аватар
// прощается. Потолок — про деньги и журнал: реплики приходят от браузера, и
// сверх этого числа их не примут, сколько бы их ни слали.
func liveTurnsCap(maxTurns int) int {
	if maxTurns < 1 {
		maxTurns = defaultAvatarTurns
	}
	return maxTurns * 3
}

// AvatarVoiceStyle — тон разговора для синтеза речи (NR-29).
//
// Роль и тон учитель пишет один раз, а звучать они должны и в тексте, и в
// голосе: иначе рыночный торговец читает реплики как диктор новостей.
// Локальный Silero стилем не управляет — там строка просто не используется.
func (s *Service) AvatarVoiceStyle(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) (string, error) {
	if s.material == nil {
		return "", nil
	}
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return "", err
	}
	brief, err := s.briefFor(ctx, actor, attempt)
	if err != nil {
		return "", err
	}
	return voiceStyle(brief), nil
}

// voiceStyle — подсказка тона для синтеза из настройки учителя.
//
// В синтез уходит короткая подсказка, а не весь промпт: длинный текст
// модель голоса не читает, а стоит он денег в каждой реплике.
func voiceStyle(brief AvatarBrief) string {
	style := strings.TrimSpace(brief.RolePrompt)
	if len([]rune(style)) > 400 {
		style = string([]rune(style)[:400])
	}
	return style
}

// briefFor — материал и настройка разговора по уроку попытки.
func (s *Service) briefFor(ctx context.Context, actor iam.Actor, attempt Attempt) (AvatarBrief, error) {
	a, err := s.store.AssignmentByID(ctx, s.pool, actor.OrgID, attempt.AssignmentID)
	if err != nil {
		return AvatarBrief{}, httpx.Internal("Не удалось открыть разговор").Wrap(err)
	}
	return s.briefOfVersion(ctx, actor.OrgID, a.LessonVersionID)
}

// briefOfVersion — материал и настройка разговора версии урока. Отдельно от
// briefFor ради отчёта учителю: у него есть назначение, а попытки нет.
func (s *Service) briefOfVersion(ctx context.Context, orgID, versionID uuid.UUID) (AvatarBrief, error) {
	brief, err := s.material.AvatarBriefFor(ctx, orgID, versionID)
	if err != nil {
		return AvatarBrief{}, err
	}
	if brief.MaxTurns <= 0 {
		brief.MaxTurns = defaultAvatarTurns
	}
	return brief, nil
}

// defaultAvatarTurns — длина разговора, если урок её не задал (NR-29).
const defaultAvatarTurns = 8

// msgNoDialog — учитель убрал разговор из урока.
const msgNoDialog = "В этом уроке нет разговора с аватаром"

// enabledBrief — материал разговора, если разговор в уроке есть.
func (s *Service) enabledBrief(ctx context.Context, actor iam.Actor, attempt Attempt) (AvatarBrief, error) {
	brief, err := s.briefFor(ctx, actor, attempt)
	if err != nil {
		return AvatarBrief{}, err
	}
	if !brief.Enabled {
		return AvatarBrief{}, httpx.Conflict(msgNoDialog)
	}
	return brief, nil
}

// markOpened отмечает задание открытым в выдаче issueID.
func (s *Service) markOpened(ctx context.Context, actor iam.Actor, attemptID uuid.UUID, key string,
	issueID uuid.UUID) error {
	err := pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.requireOpenIssue(ctx, tx, actor.OrgID, issueID); err != nil {
			return err
		}
		return s.store.OpenTaskProgress(ctx, tx, actor.OrgID, attemptID, key, issueID)
	})
	switch {
	case errors.Is(err, ErrIssueGone):
		// Учитель отменил выдачу в ту же секунду.
		return httpx.Conflict(msgNotIssued)
	case err != nil:
		return wrapErr(err, "Не удалось открыть задание")
	}
	ev := telemetry.Event{Name: "task_started", EntityID: issueID, Key: key}
	if is, err := s.store.IssueByID(ctx, s.pool, actor.OrgID, issueID); err == nil {
		ev = s.attributed(ctx, actor.OrgID, is.AssignmentID, ev)
	}
	telemetry.Emit(ctx, ev)
	return nil
}

// ownAttempt проверяет, что попытка принадлежит этому ученику.
func (s *Service) ownAttempt(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) (Attempt, error) {
	attempt, err := s.store.AttemptByID(ctx, s.pool, actor.OrgID, attemptID)
	if errors.Is(err, ErrNotFound) {
		return Attempt{}, httpx.NotFound("Урок не найден")
	}
	if err != nil {
		return Attempt{}, httpx.Internal("Не удалось прочитать урок").Wrap(err)
	}
	if attempt.StudentID != actor.StudentID {
		return Attempt{}, httpx.NotFound("Урок не найден")
	}
	return attempt, nil
}

// EndLiveDialog — ученик закончил голосовой разговор (или ушёл со страницы).
//
// Ключи OpenAI держат ограниченное число одновременных голосовых сессий, и
// шлюз раздаёт сессии по ключам. Конец разговора он сам не видит — звук идёт
// мимо сервера, — поэтому о нём сообщает браузер ученика через нас. Попытка
// проверяется: чужой ученик не может сообщить о конце не своей сессии.
func (s *Service) EndLiveDialog(ctx context.Context, actor iam.Actor, attemptID uuid.UUID, sessionID string) error {
	if s.live == nil {
		return nil
	}
	if _, err := s.ownAttempt(ctx, actor, attemptID); err != nil {
		return err
	}
	s.live.Release(context.WithoutCancel(ctx), sessionID)
	return nil
}
