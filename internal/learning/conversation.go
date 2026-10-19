package learning

// Разговор с аватаром по целям урока: ходы «голосовыми сообщениями», цели,
// отметки, итог ученику и отчёт учителю. Договор —
// docs/product/avatar-goals/contract.md, разделы 4 и 6; что и почему сделано
// здесь — docs/product/avatar-goals/backend.md.
//
// Логика разговора — в движке llm_service (цели, отметки, выбор следующей
// цели). Здесь то, чего у движка нет и быть не должно: доступ, счёт
// ответов, идемпотентность, закрытие разговора, фоновой разбор и отчёты.
// Состояние движка хранится в базе непрозрачным JSON — разбирать его здесь
// нельзя, им владеет движок.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Conversation — движок разговора по целям (llm_service /v1/conversation/*).
//
// Интерфейс, а не прямой вызов lessonpipe: как и Avatar, реализация — адаптер
// в internal/app, а learning не знает, какой сервис ведёт разговор.
type Conversation interface {
	Start(ctx context.Context, orgID, studentID uuid.UUID, in EngineStart) (EngineReply, error)
	// Turn — ответ ученика и реплика аватара на него (в живом режиме —
	// указание сессии на следующий ход). ErrEngineConflict — в состоянии
	// движка уже есть этот ход или более поздний.
	Turn(ctx context.Context, orgID, studentID uuid.UUID, in EngineTurn) (EngineReply, error)
	// Analyze — разбор закрытого разговора. Ответ ложится в отчёт целиком.
	Analyze(ctx context.Context, orgID, studentID uuid.UUID, in EngineAnalyze) (json.RawMessage, error)

	// Живой режим (раздел 10.1 договора).

	// Observe — настоящая реплика аватара из живой сессии: движок запоминает
	// её (повтор за аватаром не засчитывается) и, если просили, переводит.
	Observe(ctx context.Context, orgID, studentID uuid.UUID, in EngineObserve) (EngineObservation, error)
	// Progress — ход разговора по состоянию, без модели: для инструмента
	// делегата и для указания сессии, открытой посреди разговора.
	Progress(ctx context.Context, orgID, studentID uuid.UUID, state json.RawMessage) (EngineProgress, error)
	// Check — проверка задуманной реплики аватара по правилам, без модели.
	Check(ctx context.Context, orgID, studentID uuid.UUID, in EngineCheck) (EngineCheckResult, error)
}

// ErrEngineConflict — движок отказался применить ход (409 по договору):
// разговор уже ушёл дальше этого хода.
var ErrEngineConflict = errors.New("learning: ход уже применён в состоянии движка")

// EngineLine — реплика в истории и в расшифровке для движка.
type EngineLine struct {
	TurnNo  int
	Role    string
	Content string
	Input   string
	Edited  bool
}

// EngineStart — из чего движок начинает разговор.
type EngineStart struct {
	Language     string
	Level        string
	Text         string
	Collocations string
	RolePrompt   string
	Goals        []AvatarGoal
	MaxTurns     int
	// History — реплики прежних режимов: движок продолжает их, а не
	// здоровается заново.
	History []EngineLine
	// ReplyMode — ReplyText или ReplySteer; пусто — ReplyText.
	ReplyMode string
}

// EngineTurn — ход: ответ ученика к состоянию движка.
type EngineTurn struct {
	State   json.RawMessage
	Answer  EngineLine
	History []EngineLine
	Final   bool
	// ReplyMode — ReplyText или ReplySteer; пусто — ReplyText.
	ReplyMode string
}

// EngineReply — реплика аватара и новое состояние движка. В режиме
// ReplySteer реплики нет (Reply и ReplyRU пустые), есть Steer — указание
// живой сессии на следующий ход.
type EngineReply struct {
	State      json.RawMessage
	Reply      string
	ReplyRU    string
	TargetGoal string
	Steer      string
}

// Как движок отвечает на ход (раздел 10.1 договора).
const (
	// ReplyText — реплика аватара текстом: разговор голосовыми сообщениями.
	ReplyText = "text"
	// ReplySteer — указание живой сессии: реплику произносит сама сессия.
	ReplySteer = "steer"
)

// EngineAnalyze — что разбирать. State == nil — разговор прежних режимов:
// движок строит состояние сам по целям.
type EngineAnalyze struct {
	Language   string
	Level      string
	Goals      []AvatarGoal
	State      json.RawMessage
	Transcript []EngineLine
}

// WithConversation подключает движок разговора по целям. Без него ручки
// разговора отвечают «недоступен», остальной урок работает.
func (s *Service) WithConversation(c Conversation) *Service {
	s.conversation = c
	return s
}

// Способ ввода ответа ученика.
const (
	InputText  = "text"
	InputVoice = "voice"
)

// Статусы разговора у ученика.
const (
	ConversationNotStarted = "not_started"
	ConversationActive     = "active"
	ConversationClosed     = "closed"
)

// Статусы разбора: как в avatar_report_status.
const (
	ReportPending = "pending"
	ReportReady   = "ready"
	ReportFailed  = "failed"
)

// CodeAvatarUnavailable — модель не ответила, ответ ученика сохранён.
// Отдельный код, а не общий unavailable: по нему экран показывает
// «Повторить» с тем же client_msg_id, а не «сервис недоступен».
const CodeAvatarUnavailable = "avatar_unavailable"

// Отказы ученику — из договора.
const (
	msgConversationNotStarted = "Разговор ещё не начат"
	msgConversationFinished   = "Разговор закончен"
	msgAvatarNoAnswer         = "Аватар не смог ответить. Твой ответ сохранён — нажми «Повторить»"
	msgAvatarNoStart          = "Аватар не смог начать разговор — нажми «Повторить»"
	msgConversationOff        = "Разговор с аватаром сейчас недоступен"
)

// Пределы разговора.
const (
	answerMaxRunes      = 1000
	clientMsgIDMaxRunes = 64
	asrTextMaxRunes     = 2000
	// incidentLimit — сбоев на попытку. Журнал пополняет браузер, и без
	// потолка он стал бы местом, куда пишут что угодно сколько угодно.
	incidentLimit           = 50
	incidentMessageMaxRunes = 300
	// В движок уходит не больше этого: тот же потолок, что у прежних
	// режимов, — разговор в 20 ответов в него укладывается с запасом.
	historyMaxLines     = 60
	historyLineMaxRunes = 1000
	replyMaxRunes       = 2000
)

// Виды сбоев. avatar пишет сервер, остальные — браузер.
const (
	IncidentAvatar = "avatar"
	IncidentASR    = "asr"
	IncidentTTS    = "tts"
)

var (
	errConversationNotStarted = errors.New("learning: разговор не начат")
	errConversationFinished   = errors.New("learning: разговор закончен")
)

// avatarUnavailable — 503 с кодом договора.
func avatarUnavailable(msg string, err error) *httpx.Error {
	return (&httpx.Error{Status: http.StatusServiceUnavailable, Code: CodeAvatarUnavailable, Message: msg}).Wrap(err)
}

// levelCode — уровень урока строкой договора: «a2». Уровни версий хранятся
// заглавными («A2»), а движок и экраны ждут строчные; неизвестный — пусто.
func levelCode(level string) string {
	l := strings.ToLower(strings.TrimSpace(level))
	switch l {
	case "a1", "a2", "b1", "b2", "c1":
		return l
	}
	return ""
}

// ---------------------------------------------------------------------------
// Разговор как он лежит в базе
// ---------------------------------------------------------------------------

// convTurn — реплика с номером хода.
type convTurn struct {
	DialogTurn
	No int
	// Legacy — реплика прежнего режима: номера в базе нет, он посчитан по
	// порядку при чтении.
	Legacy bool
}

// conversation — разговор попытки: строка попытки и реплики по порядку
// ходов. Чистая арифметика без базы: правила проверяются тестами.
type conversation struct {
	row   ConversationRow
	turns []convTurn
	max   int
}

func newConversation(row ConversationRow, rows []DialogTurn, maxTurns int) conversation {
	return conversation{row: row, turns: numberTurns(rows), max: maxTurns}
}

// numberTurns раскладывает реплики по ходам договора: ответы ученика 1…N,
// реплика аватара — номер ответа, на который она отвечает (0 — вступление).
//
// У реплик прежних режимов номера нет — он считается по порядку тем же
// правилом. Затем реплики упорядочиваются по номеру, ученик раньше аватара:
// две вкладки, ответившие разом, пишут реплики вперемешку по времени, а
// разговор читается по ходам.
func numberTurns(rows []DialogTurn) []convTurn {
	out := make([]convTurn, 0, len(rows))
	last := 0
	for _, r := range rows {
		t := convTurn{DialogTurn: r}
		switch {
		case r.TurnNo != nil:
			t.No = *r.TurnNo
			if r.Role == RoleStudent && t.No > last {
				last = t.No
			}
		case r.Role == RoleStudent:
			last++
			t.No, t.Legacy = last, true
		default:
			t.No, t.Legacy = last, true
		}
		if t.Input == "" {
			t.Input = InputText
		}
		out = append(out, t)
	}
	slices.SortStableFunc(out, func(a, b convTurn) int {
		if a.No != b.No {
			return a.No - b.No
		}
		return roleRank(a.Role) - roleRank(b.Role)
	})
	return out
}

func roleRank(role string) int {
	if role == RoleStudent {
		return 0
	}
	return 1
}

// exhausted — засчитано столько ответов, сколько задал учитель.
func (c conversation) exhausted() bool { return c.max > 0 && c.row.Turns >= c.max }

// closed — разговор закрыт: N-й ответ записан. Исчерпанный разговор прежних
// режимов закрыт так же, даже если момент закрытия ещё не записан.
func (c conversation) closed() bool { return c.row.ClosedAt != nil || c.exhausted() }

// needsClosing — длина исчерпана, а закрытие не записано: так бывает у
// разговора прежних режимов, дошедшего до конца без движка.
func (c conversation) needsClosing() bool { return c.row.ClosedAt == nil && c.exhausted() }

func (c conversation) status() string {
	switch {
	case c.closed():
		return ConversationClosed
	case c.row.HasEngine:
		return ConversationActive
	}
	return ConversationNotStarted
}

// reportStatus — статус разбора для ответа: у закрытого разговора без
// запущенного разбора он «pending» — разбор положен и начнётся с первым же
// запросом итога.
func (c conversation) reportStatus() string {
	if !c.closed() {
		return ""
	}
	if c.row.ReportStatus == "" {
		return ReportPending
	}
	return c.row.ReportStatus
}

// lastStudentNo — номер последнего ответа ученика; 0 — ответов не было.
func (c conversation) lastStudentNo() int {
	n := 0
	for _, t := range c.turns {
		if t.Role == RoleStudent && t.No > n {
			n = t.No
		}
	}
	return n
}

// nextTurnNo — номер следующего ответа.
//
// Обычно это счётчик + 1. Но у разговора прежних режимов записанных ответов
// бывает больше засчитанных (модель не ответила — ответ остался в журнале
// без счёта), и номер по счётчику совпал бы с уже бывшим. Движок по номеру
// решает, применён ли ход, поэтому номер обязан расти.
func (c conversation) nextTurnNo() int { return max(c.lastStudentNo(), c.row.Turns) + 1 }

// answerOf — ответ ученика с этим client_msg_id.
func (c conversation) answerOf(clientMsgID string) (convTurn, bool) {
	for _, t := range c.turns {
		if t.Role == RoleStudent && t.ClientMsgID == clientMsgID && clientMsgID != "" {
			return t, true
		}
	}
	return convTurn{}, false
}

// replyTo — ответ аватара на ход no, записанный разговором по целям.
func (c conversation) replyTo(no int) (convTurn, bool) {
	for _, t := range c.turns {
		if t.Role == RoleAvatar && !t.Legacy && t.No == no {
			return t, true
		}
	}
	return convTurn{}, false
}

func (c conversation) lastOf(role string) (convTurn, bool) {
	for i := len(c.turns) - 1; i >= 0; i-- {
		if c.turns[i].Role == role {
			return c.turns[i], true
		}
	}
	return convTurn{}, false
}

// isLastAnswer — ответ последний: позже ученик ничего не отправлял.
func (c conversation) isLastAnswer(t convTurn) bool {
	last, ok := c.lastOf(RoleStudent)
	return ok && last.ID == t.ID
}

// PendingAnswer — последний ответ ученика сохранён, а ответа аватара на
// него нет: клиент повторяет отправку с тем же client_msg_id.
type PendingAnswer struct {
	ClientMsgID string
	TurnNo      int
	Text        string
}

// pending — висящий ответ. Ответ живого режима отвечен, когда на него есть
// указание сессии: реплику аватара на него произносит сама сессия, и
// сервер её может так и не увидеть (сессия оборвалась) — повтор ответа
// здесь ничего бы не дал.
func (c conversation) pending() *PendingAnswer {
	last, ok := c.lastOf(RoleStudent)
	if !ok || last.Legacy || last.ClientMsgID == "" || last.Steer != "" {
		return nil
	}
	if _, answered := c.replyTo(last.No); answered {
		return nil
	}
	return &PendingAnswer{ClientMsgID: last.ClientMsgID, TurnNo: last.No, Text: last.Content}
}

// answeredNo — номер последнего засчитанного ответа: больше него номер
// реплики аватара быть не может.
func (c conversation) answeredNo() int { return c.nextTurnNo() - 1 }

// hasGoalTurns — в разговоре по целям уже что-то сказано: вступление,
// ответ ученика или реплика живой сессии. Состояние движка без таких
// реплик — начало живого режима, которое сессия так и не произнесла.
func (c conversation) hasGoalTurns() bool {
	for _, t := range c.turns {
		if !t.Legacy {
			return true
		}
	}
	return false
}

// turnByID — реплика по идентификатору.
func (c conversation) turnByID(id uuid.UUID) (convTurn, bool) {
	for _, t := range c.turns {
		if t.ID == id {
			return t, true
		}
	}
	return convTurn{}, false
}

// avatarLineOf — реплика аватара живой сессии с этим client_msg_id.
// Пространство идентификаторов своё у каждой роли: ответ ученика и реплику
// аватара браузер нумерует независимо.
func (c conversation) avatarLineOf(clientMsgID string) (convTurn, bool) {
	for _, t := range c.turns {
		if t.Role == RoleAvatar && t.ClientMsgID == clientMsgID && clientMsgID != "" {
			return t, true
		}
	}
	return convTurn{}, false
}

// avatarLines — сколько реплик аватара записал разговор по целям.
func (c conversation) avatarLines() int {
	n := 0
	for _, t := range c.turns {
		if t.Role == RoleAvatar && !t.Legacy {
			n++
		}
	}
	return n
}

// dialogTurns — реплики по порядку ходов: история для живой сессии,
// открытой посреди разговора.
func (c conversation) dialogTurns() []DialogTurn {
	out := make([]DialogTurn, 0, len(c.turns))
	for _, t := range c.turns {
		out = append(out, t.DialogTurn)
	}
	return out
}

// lines — реплики для движка, последние historyMaxLines.
func lines(turns []convTurn) []EngineLine {
	if len(turns) > historyMaxLines {
		turns = turns[len(turns)-historyMaxLines:]
	}
	out := make([]EngineLine, 0, len(turns))
	for _, t := range turns {
		out = append(out, EngineLine{
			TurnNo: t.No, Role: t.Role, Content: clip(t.Content, historyLineMaxRunes),
			Input: t.Input, Edited: t.Edited,
		})
	}
	return out
}

// before — история до ответа с номером no: всё, что было сказано раньше.
func (c conversation) before(no int) []EngineLine {
	var out []convTurn
	for _, t := range c.turns {
		if t.No < no {
			out = append(out, t)
		}
	}
	return lines(out)
}

// transcript — весь разговор для разбора. Ответы ученика не обрезаются
// потолком истории: разбор обязан видеть каждый засчитанный ответ.
func (c conversation) transcript() []EngineLine {
	out := make([]EngineLine, 0, len(c.turns))
	for _, t := range c.turns {
		out = append(out, EngineLine{TurnNo: t.No, Role: t.Role, Content: t.Content, Input: t.Input, Edited: t.Edited})
	}
	return out
}

// clip — не длиннее n символов.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ---------------------------------------------------------------------------
// Ученик: разговор целиком
// ---------------------------------------------------------------------------

// ConversationView — разговор глазами ученика.
type ConversationView struct {
	Enabled      bool
	Language     string
	Level        string
	Status       string
	MaxTurns     int
	TurnsTaken   int
	Turns        []convTurn
	Pending      *PendingAnswer
	ReportStatus string
	// Словарь — выражения урока, если учитель его включил. Цели и их
	// пояснения ученику не отдаются: они подсказали бы, что засчитают.
	DictionaryEnabled bool
	Phrases           []string
	// LiveAvailable — разговор можно вести живой сессией (раздел 10).
	LiveAvailable bool
}

// ConversationStep — реплика аватара и то, сколько разговора осталось.
type ConversationStep struct {
	Reply        string
	Translation  string
	TurnNo       int
	TurnsTaken   int
	MaxTurns     int
	Status       string
	ReportStatus string
	// Steer — в живом режиме вместо реплики: указание сессии на следующий
	// ход. Reply и Translation тогда пустые.
	Steer string
}

func (c conversation) stepOf(t convTurn) ConversationStep {
	return ConversationStep{
		Reply: t.Content, Translation: t.Translation, TurnNo: t.No,
		TurnsTaken: c.row.Turns, MaxTurns: c.max, Status: c.status(), ReportStatus: c.reportStatus(),
	}
}

// latestStep — последняя реплика аватара: её отдаёт повторный start и
// повтор ответа, который разговор уже обогнал.
func (c conversation) latestStep() ConversationStep {
	if t, ok := c.lastOf(RoleAvatar); ok {
		return c.stepOf(t)
	}
	step := c.stepOf(convTurn{})
	step.TurnNo = c.lastStudentNo()
	return step
}

// loadConversation читает разговор попытки целиком.
func (s *Service) loadConversation(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID,
	maxTurns int) (conversation, error) {
	row, err := s.store.ConversationOf(ctx, q, orgID, attemptID)
	if err != nil {
		return conversation{}, err
	}
	rows, err := s.store.ConversationTurns(ctx, q, orgID, attemptID)
	if err != nil {
		return conversation{}, err
	}
	return newConversation(row, rows, maxTurns), nil
}

func (s *Service) readConversation(ctx context.Context, orgID, attemptID uuid.UUID, maxTurns int) (conversation, error) {
	c, err := s.loadConversation(ctx, s.pool, orgID, attemptID, maxTurns)
	if err != nil {
		return conversation{}, httpx.Internal("Не удалось прочитать разговор").Wrap(err)
	}
	return c, nil
}

// Conversation — разговор по целям урока у ученика.
//
// Не проверяет, открыто ли задание: закончивший разговор смотрит его и
// после того, как учитель завершил урок. Начать и продолжить — только в
// открытом (StartConversation, AnswerConversation).
func (s *Service) Conversation(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) (ConversationView, error) {
	if s.material == nil {
		return ConversationView{}, httpx.Unavailable(msgConversationOff)
	}
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return ConversationView{}, err
	}
	brief, err := s.briefFor(ctx, actor, attempt)
	if err != nil {
		return ConversationView{}, err
	}
	conv, err := s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		return ConversationView{}, err
	}
	if brief.Enabled && conv.needsClosing() {
		if conv, err = s.settle(ctx, actor, attempt, brief); err != nil {
			return ConversationView{}, err
		}
	}
	view := ConversationView{
		Enabled: brief.Enabled, Language: brief.Language, Level: levelCode(brief.Level),
		Status: conv.status(), MaxTurns: conv.max, TurnsTaken: conv.row.Turns,
		Turns: conv.turns, Pending: conv.pending(), ReportStatus: conv.reportStatus(),
		DictionaryEnabled: brief.DictionaryEnabled, Phrases: []string{},
		LiveAvailable: s.liveAvailable(brief, conv),
	}
	if brief.DictionaryEnabled {
		view.Phrases = append(view.Phrases, brief.PracticePhrases...)
	}
	return view, nil
}

// conversationGate — задание «Диалог» выдано и открыто: выдача, в которой
// оно открыто, и задания урока (они нужны, чтобы завершить попытку, когда
// закрытый разговор окажется последним несделанным заданием).
func (s *Service) conversationGate(ctx context.Context, actor iam.Actor, attempt Attempt) (uuid.UUID, []LessonTask, error) {
	_, b, err := s.attemptBoard(ctx, actor, attempt)
	if err != nil {
		return uuid.Nil, nil, err
	}
	if _, ok := b.task(TaskDialog); !ok {
		return uuid.Nil, nil, httpx.Conflict(msgNoDialog)
	}
	_, issueID, err := openTaskOf(b, TaskDialog)
	return issueID, b.tasks, err
}

// markConversationDone — закрытый разговор отмечает задание «Диалог»
// сделанным в открытой выдаче и, если это было последнее задание урока,
// завершает попытку. Внутри транзакции, где попытка уже заблокирована.
func (s *Service) markConversationDone(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, locked Attempt,
	issueID uuid.UUID, tasks []LessonTask) error {
	if err := s.store.MarkTaskProgress(ctx, tx, orgID, locked.ID, TaskDialog, issueID, true); err != nil {
		return err
	}
	_, err := s.completeIfDone(ctx, tx, orgID, locked, tasks)
	return err
}

// settle закрывает разговор, длина которого уже исчерпана, — разговор
// прежних режимов, дошедший до конца без движка: по договору он считается
// закрытым и разбирается. Задание отмечается сделанным, только если оно
// сейчас открыто; закрытие и разбор от этого не зависят.
func (s *Service) settle(ctx context.Context, actor iam.Actor, attempt Attempt, brief AvatarBrief) (conversation, error) {
	if err := s.store.CloseConversation(ctx, s.pool, actor.OrgID, attempt.ID); err != nil {
		return conversation{}, httpx.Internal("Не удалось закрыть разговор").Wrap(err)
	}
	if issueID, tasks, err := s.conversationGate(ctx, actor, attempt); err == nil {
		err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			locked, err := s.store.LockAttempt(ctx, tx, actor.OrgID, attempt.ID)
			if err != nil {
				return err
			}
			return s.markConversationDone(ctx, tx, actor.OrgID, locked, issueID, tasks)
		})
		if err != nil {
			// Отметка задания — для учителя, разговор от неё не зависит.
			logx.From(ctx).Warn("не удалось отметить разговор сделанным", "attempt_id", attempt.ID, "err", err)
		}
	}
	s.kickReport(ctx, actor.OrgID, attempt.ID, attempt.StudentID, brief)
	return s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
}

// ---------------------------------------------------------------------------
// Ученик: начать
// ---------------------------------------------------------------------------

// StartConversation начинает разговор: первое состояние движка и
// вступительная реплика аватара.
//
// Повторный вызов разговор заново не начинает и модель не зовёт: отдаёт
// последнюю реплику аватара. У ученика могла оборваться связь, и новое
// приветствие поверх сказанного — это потерянный разговор.
func (s *Service) StartConversation(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) (ConversationStep, error) {
	if s.conversation == nil || s.material == nil {
		return ConversationStep{}, httpx.Unavailable(msgConversationOff)
	}
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return ConversationStep{}, err
	}
	issueID, _, err := s.conversationGate(ctx, actor, attempt)
	if err != nil {
		return ConversationStep{}, err
	}
	brief, err := s.enabledBrief(ctx, actor, attempt)
	if err != nil {
		return ConversationStep{}, err
	}
	// Начатый разговор отмечает задание открытым: учитель видит «открыл», а
	// выдачу с идущим разговором уже нельзя отменить.
	if err := s.markOpened(ctx, actor, attempt.ID, TaskDialog, issueID); err != nil {
		return ConversationStep{}, err
	}
	conv, err := s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		return ConversationStep{}, err
	}
	if conv.needsClosing() {
		if conv, err = s.settle(ctx, actor, attempt, brief); err != nil {
			return ConversationStep{}, err
		}
	}
	// Начатый разговор продолжается с последней реплики аватара. Состояние
	// движка без единой реплики — начало живого режима (…/live), которое
	// сессия так и не произнесла: связь не установилась, и ученик перешёл к
	// голосовым сообщениям. Такое начало ничего не применило, и его
	// вступление пишется здесь, текстом, поверх.
	if conv.closed() || conv.hasGoalTurns() {
		return conv.latestStep(), nil
	}

	// Разговор прежних режимов продолжается: движок получает его реплики и
	// не здоровается заново. Вступление — реплика аватара перед следующим
	// ответом ученика, и номер у неё на единицу меньше (у нового разговора 0).
	intro := conv.nextTurnNo() - 1
	// Дальше начало доводится до конца и без ученика: оборвалась связь —
	// повторный start отдаст уже записанное вступление, а не позовёт модель.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), turnBudget)
	defer cancel()
	reply, err := s.conversation.Start(ctx, actor.OrgID, actor.StudentID, EngineStart{
		Language: brief.Language, Level: levelCode(brief.Level), Text: brief.Text,
		Collocations: brief.Collocations, RolePrompt: brief.RolePrompt, Goals: brief.Goals,
		MaxTurns: brief.MaxTurns, History: lines(conv.turns), ReplyMode: ReplyText,
	})
	if err == nil {
		err = checkReply(reply, ReplyText)
	}
	if err != nil {
		s.avatarIncident(ctx, actor.OrgID, attempt.ID, intro, err)
		return ConversationStep{}, avatarUnavailable(msgAvatarNoStart, err)
	}

	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.store.LockAttempt(ctx, tx, actor.OrgID, attempt.ID); err != nil {
			return err
		}
		c, err := s.loadConversation(ctx, tx, actor.OrgID, attempt.ID, brief.MaxTurns)
		if err != nil {
			return err
		}
		if c.hasGoalTurns() {
			// Соседняя вкладка успела раньше: её вступление (или реплика
			// живой сессии) уже записано, и второе ученику не нужно.
			return nil
		}
		// Состояния нет — оно кладётся впервые; есть — это молчаливое
		// начало живого режима, и оно заменяется: ходов в нём нет.
		if err := s.store.SetEngine(ctx, tx, actor.OrgID, attempt.ID, reply.State); err != nil {
			return err
		}
		return s.store.InsertConversationTurn(ctx, tx, DialogTurn{
			ID: idgen.New(), OrgID: actor.OrgID, AttemptID: attempt.ID, StudentID: actor.StudentID,
			Role: RoleAvatar, Content: clip(reply.Reply, replyMaxRunes), TurnNo: &intro,
			Input: InputText, Translation: clip(reply.ReplyRU, replyMaxRunes),
		})
	})
	if err != nil {
		return ConversationStep{}, httpx.Internal("Не удалось сохранить разговор").Wrap(err)
	}
	conv, err = s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		return ConversationStep{}, err
	}
	return conv.latestStep(), nil
}

// checkReply — ответ движка годится в разговор: есть состояние и реплика
// (в режиме ReplySteer — указание сессии). Пустая реплика — это молчащий
// аватар, пустое указание — сессия без указания на ход; записывать их как
// ответ нельзя.
func checkReply(r EngineReply, mode string) error {
	if !engineState(r.State) {
		return errors.New("learning: движок вернул пустое состояние")
	}
	text := r.Reply
	if mode == ReplySteer {
		text = r.Steer
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("learning: движок вернул пустую реплику или указание")
	}
	return nil
}

// engineState — непустое состояние движка: JSON, а не пустота или null.
func engineState(state json.RawMessage) bool {
	state = bytes.TrimSpace(state)
	return len(state) > 0 && !bytes.Equal(state, []byte("null"))
}

// ---------------------------------------------------------------------------
// Ученик: ответ
// ---------------------------------------------------------------------------

// ConversationInput — ответ ученика.
type ConversationInput struct {
	ClientMsgID   string
	Text          string
	Input         string
	ASRText       string
	ASRConfidence *float64
	Edited        bool
	// Mode — ModeLive: ответ в живой сессии, в ответ — указание сессии, а не
	// реплика аватара. Пусто — голосовые сообщения.
	Mode string
}

// ModeLive — ответ ученика в живой сессии (раздел 10.2 договора).
const ModeLive = "live"

// live — ответ живого режима.
func (in ConversationInput) live() bool { return in.Mode == ModeLive }

func (in ConversationInput) normalize() (ConversationInput, error) {
	in.ClientMsgID = strings.TrimSpace(in.ClientMsgID)
	in.Text = strings.TrimSpace(in.Text)
	in.Input = strings.TrimSpace(in.Input)
	in.ASRText = strings.TrimSpace(in.ASRText)
	in.Mode = strings.TrimSpace(in.Mode)
	fields := map[string]string{}
	if in.Mode != "" && in.Mode != ModeLive {
		fields["mode"] = "Режим ответа — live или без поля"
	}
	switch {
	case in.ClientMsgID == "":
		fields["client_msg_id"] = "Нужен идентификатор ответа"
	case len([]rune(in.ClientMsgID)) > clientMsgIDMaxRunes:
		fields["client_msg_id"] = "Идентификатор ответа — не длиннее 64 символов"
	}
	switch n := len([]rune(in.Text)); {
	case n == 0:
		fields["text"] = "Напиши или скажи что-нибудь"
	case n > answerMaxRunes:
		fields["text"] = "Слишком длинный ответ — не больше 1000 символов"
	}
	if in.Input == "" {
		in.Input = InputText
	}
	if in.Input != InputText && in.Input != InputVoice {
		fields["input"] = "Ответ голосом (voice) или текстом (text)"
	}
	if len([]rune(in.ASRText)) > asrTextMaxRunes {
		fields["asr_text"] = "Слишком длинный распознанный текст"
	}
	if c := in.ASRConfidence; c != nil && (math.IsNaN(*c) || *c < 0 || *c > 1) {
		fields["asr_confidence"] = "Уверенность распознавания — от 0 до 1"
	}
	if len(fields) > 0 {
		return in, httpx.Validation("Проверьте ответ", fields)
	}
	// Ответ текстом — запасной ввод: распознавания у него не было, и
	// «исправил распознанное» о нём не скажешь.
	if in.Input == InputText {
		in.ASRText, in.ASRConfidence, in.Edited = "", nil, false
	}
	return in, nil
}

func (in ConversationInput) confidence() *float32 {
	if in.ASRConfidence == nil {
		return nil
	}
	v := float32(*in.ASRConfidence)
	return &v
}

// AnswerConversation записывает ответ ученика и возвращает реплику аватара,
// а в живом режиме (Mode = ModeLive) — указание сессии на следующий ход.
//
// Порядок — из договора и ради одного: ответ ученика не пропадает и не
// засчитывается дважды, что бы ни случилось с моделью и связью.
//
//  1. Ответ с уже известным client_msg_id не засчитывается второй раз: есть
//     ответ аватара (указание) — отдаётся он, нет — генерируется заново для
//     того же хода.
//  2. Новый ответ записывается и засчитывается до вызова модели. N-й ответ
//     закрывает разговор сразу, не дожидаясь прощания аватара.
//  3. Модель не ответила — 503 avatar_unavailable, сбой в журнал попытки;
//     ученик повторяет с тем же client_msg_id.
//  4. Состояние движка пишется, только если за время хода его не продвинула
//     соседняя вкладка: ход, посчитанный от старого состояния, новое не
//     перетирает. Уточнение живого режима — у saveSteer.
func (s *Service) AnswerConversation(ctx context.Context, actor iam.Actor, attemptID uuid.UUID,
	in ConversationInput) (ConversationStep, error) {
	if s.conversation == nil || s.material == nil {
		return ConversationStep{}, httpx.Unavailable(msgConversationOff)
	}
	in, err := in.normalize()
	if err != nil {
		return ConversationStep{}, err
	}
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return ConversationStep{}, err
	}
	brief, err := s.briefFor(ctx, actor, attempt)
	if err != nil {
		return ConversationStep{}, err
	}
	conv, err := s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		return ConversationStep{}, err
	}
	// Повтор ответа, на который аватар уже ответил: потерян был только
	// HTTP-ответ. Отдаём записанное — без ворот и без модели: это не новая
	// реплика и не новые деньги.
	if t, ok := conv.answerOf(in.ClientMsgID); ok {
		if step, ok := conv.answered(t, in.live()); ok {
			return step, nil
		}
	}

	issueID, tasks, err := s.conversationGate(ctx, actor, attempt)
	if err != nil {
		return ConversationStep{}, err
	}
	if !brief.Enabled {
		return ConversationStep{}, httpx.Conflict(msgNoDialog)
	}

	answer, err := s.recordAnswer(ctx, actor, attempt.ID, in, brief.MaxTurns, issueID, tasks)
	switch {
	case errors.Is(err, errConversationNotStarted):
		return ConversationStep{}, httpx.Conflict(msgConversationNotStarted)
	case errors.Is(err, errConversationFinished):
		return ConversationStep{}, httpx.Conflict(msgConversationFinished)
	case errors.Is(err, ErrIssueGone):
		return ConversationStep{}, httpx.Conflict(msgNotIssued)
	case err != nil:
		return ConversationStep{}, httpx.Internal("Не удалось сохранить ответ").Wrap(err)
	}
	// Повтор с тем же client_msg_id даёт тот же ключ события, и аналитика
	// засчитает ответ один раз. Живой режим приходит сюда же (mode 'live').
	s.emitDialogueTurn(ctx, attempt, in.ClientMsgID)

	// Ответ записан и засчитан — дальше ход доводится до конца, даже если
	// ученик закрыл вкладку или пропала связь: реплика аватара сохранится, и
	// повтор с тем же client_msg_id получит её без второго вызова модели.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), turnBudget)
	defer cancel()

	conv, err = s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		return ConversationStep{}, err
	}
	if t, ok := conv.turnByID(answer.ID); ok {
		answer = t
	}
	if step, ok := conv.answered(answer, in.live()); ok {
		// Пока ответ записывался, соседняя вкладка с тем же client_msg_id
		// уже получила реплику аватара (указание сессии).
		return step, nil
	}
	if answer.Legacy || !conv.isLastAnswer(answer) {
		// Повтор старого ответа, который разговор уже обогнал (или реплики
		// прежнего режима с тем же идентификатором): модель ответила бы на
		// вопрос, ушедший в прошлое. Отдаём последнюю реплику аватара.
		return conv.latest(in.live()), nil
	}
	return s.replyToAnswer(ctx, actor, attempt, brief, conv, answer, in.live())
}

// answered — то, что уже записано в ответ на ответ ученика t: реплика
// аватара у голосовых сообщений, указание сессии в живом режиме.
func (c conversation) answered(t convTurn, live bool) (ConversationStep, bool) {
	if live {
		if t.Steer != "" {
			return c.liveStepOf(t), true
		}
		return ConversationStep{}, false
	}
	if r, ok := c.replyTo(t.No); ok {
		return c.stepOf(r), true
	}
	return ConversationStep{}, false
}

// latest — последняя реплика аватара или, в живом режиме, последнее
// указание сессии: их отдаёт повтор ответа, который разговор уже обогнал,
// и отказ движка «ход уже применён».
func (c conversation) latest(live bool) ConversationStep {
	if live {
		return c.latestLiveStep()
	}
	return c.latestStep()
}

// turnBudget — сколько ход может идти после записи ответа: вызов модели со
// своим таймаутом (lessonpipe, 90 с) и запись реплики. Потолок нужен
// потому, что ход отвязан от запроса и сам по себе не оборвётся.
const turnBudget = 2 * time.Minute

// recordAnswer — шаг 1–2: найти повтор или записать и засчитать новый ответ.
// Под блокировкой попытки: два ответа из двух вкладок получают разные
// номера, и N-й закрывает разговор ровно один раз.
func (s *Service) recordAnswer(ctx context.Context, actor iam.Actor, attemptID uuid.UUID, in ConversationInput,
	maxTurns int, issueID uuid.UUID, tasks []LessonTask) (convTurn, error) {
	var answer convTurn
	err := pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		locked, err := s.store.LockAttempt(ctx, tx, actor.OrgID, attemptID)
		if err != nil {
			return err
		}
		c, err := s.loadConversation(ctx, tx, actor.OrgID, attemptID, maxTurns)
		if err != nil {
			return err
		}
		if !c.row.HasEngine {
			return errConversationNotStarted
		}
		if t, ok := c.answerOf(in.ClientMsgID); ok {
			answer = t
			return nil
		}
		if c.closed() {
			return errConversationFinished
		}
		no := c.nextTurnNo()
		t := DialogTurn{
			ID: idgen.New(), OrgID: actor.OrgID, AttemptID: attemptID, StudentID: actor.StudentID,
			Role: RoleStudent, Content: in.Text, ClientMsgID: in.ClientMsgID, TurnNo: &no,
			Input: in.Input, ASRText: in.ASRText, ASRConfidence: in.confidence(), Edited: in.Edited,
		}
		if err := s.store.InsertConversationTurn(ctx, tx, t); err != nil {
			return err
		}
		answer = convTurn{DialogTurn: t, No: no}
		_, closedAt, err := s.store.CountAnswer(ctx, tx, actor.OrgID, attemptID, maxTurns)
		if err != nil {
			return err
		}
		if closedAt != nil && c.row.ClosedAt == nil {
			return s.markConversationDone(ctx, tx, actor.OrgID, locked, issueID, tasks)
		}
		return nil
	})
	return answer, err
}

// replyToAnswer — шаг 3–4: реплика аватара (в живом режиме — указание
// сессии) на записанный ответ.
func (s *Service) replyToAnswer(ctx context.Context, actor iam.Actor, attempt Attempt, brief AvatarBrief,
	conv conversation, answer convTurn, live bool) (ConversationStep, error) {
	state := conv.row.Engine
	// Последний ответ закрытого разговора — прощание: аватар благодарит и не
	// задаёт вопрос, на который уже не ответят.
	final := conv.closed()
	mode := ReplyText
	if live {
		mode = ReplySteer
	}
	reply, err := s.conversation.Turn(ctx, actor.OrgID, actor.StudentID, EngineTurn{
		State: state,
		Answer: EngineLine{TurnNo: answer.No, Role: RoleStudent, Content: answer.Content,
			Input: answer.Input, Edited: answer.Edited},
		History:   conv.before(answer.No),
		Final:     final,
		ReplyMode: mode,
	})
	if errors.Is(err, ErrEngineConflict) {
		// Движок уже применил ход с этим номером или позже: разговор ушёл
		// вперёд без этой реплики. Отдаём последнюю — ученик продолжает.
		return conv.latest(live), nil
	}
	if err == nil {
		err = checkReply(reply, mode)
	}
	if err != nil {
		s.avatarIncident(ctx, actor.OrgID, attempt.ID, answer.No, err)
		if final && !live {
			// Разговор закрыт и без прощания: разбор не ждёт повтора. В
			// живом режиме прощание произносит сессия, и разбор запускает
			// его запись (…/avatar) или первый запрос итога.
			s.kickReport(ctx, actor.OrgID, attempt.ID, attempt.StudentID, brief)
		}
		return ConversationStep{}, avatarUnavailable(msgAvatarNoAnswer, err)
	}
	if live {
		return s.saveSteer(ctx, actor, attempt, brief, answer, state, reply.State, reply.Steer)
	}

	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.store.LockAttempt(ctx, tx, actor.OrgID, attempt.ID); err != nil {
			return err
		}
		c, err := s.loadConversation(ctx, tx, actor.OrgID, attempt.ID, brief.MaxTurns)
		if err != nil {
			return err
		}
		if _, ok := c.replyTo(answer.No); ok {
			// Повтор из соседней вкладки ответил первым: его реплика уже у
			// ученика, вторая была бы другой — оставляем первую.
			return nil
		}
		no := answer.No
		if err := s.store.InsertConversationTurn(ctx, tx, DialogTurn{
			ID: idgen.New(), OrgID: actor.OrgID, AttemptID: attempt.ID, StudentID: actor.StudentID,
			Role: RoleAvatar, Content: clip(reply.Reply, replyMaxRunes), TurnNo: &no,
			Input: InputText, Translation: clip(reply.ReplyRU, replyMaxRunes),
		}); err != nil {
			return err
		}
		// Состояние — только от того же состояния, что ушло в ход. Если
		// соседняя вкладка за это время продвинула разговор, наш ход старше,
		// и его состояние без её отметок затёрло бы их. Отметки этого хода
		// не пропадут: ход без отметок движок оценит при разборе.
		if bytes.Equal(c.row.Engine, state) {
			return s.store.SetEngine(ctx, tx, actor.OrgID, attempt.ID, reply.State)
		}
		return nil
	})
	if err != nil {
		return ConversationStep{}, httpx.Internal("Не удалось сохранить ответ аватара").Wrap(err)
	}
	if final {
		s.kickReport(ctx, actor.OrgID, attempt.ID, attempt.StudentID, brief)
	}
	conv, err = s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		return ConversationStep{}, err
	}
	if r, ok := conv.replyTo(answer.No); ok {
		return conv.stepOf(r), nil
	}
	return conv.latestStep(), nil
}

// ---------------------------------------------------------------------------
// Сбои
// ---------------------------------------------------------------------------

// incident — запись журнала сбоев попытки.
type incident struct {
	Kind    string    `json:"kind"`
	TurnNo  *int      `json:"turn_no"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

func (s *Service) addIncident(ctx context.Context, orgID, attemptID uuid.UUID, in incident) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = s.store.AddIncident(ctx, s.pool, orgID, attemptID, raw, incidentLimit)
	return err
}

// avatarIncident — модель не ответила. Это сбой, а не ошибка ученика: он
// идёт в журнал попытки, итог показывает его отдельно от качества речи.
// Сбой записи журнала ответ ученику не меняет.
func (s *Service) avatarIncident(ctx context.Context, orgID, attemptID uuid.UUID, turnNo int, cause error) {
	msg := "Аватар не ответил"
	var he *httpx.Error
	if errors.As(cause, &he) {
		// Сообщение httpx-ошибки написано для человека и без внутренностей.
		msg += ": " + he.Message
	}
	no := turnNo
	logx.From(ctx).Warn("аватар не ответил", "attempt_id", attemptID, "turn_no", turnNo, "err", cause)
	// Своим таймаутом: ход мог упасть как раз по истечении своего.
	ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.addIncident(ictx, orgID, attemptID,
		incident{Kind: IncidentAvatar, TurnNo: &no, Message: clip(msg, incidentMessageMaxRunes), At: time.Now()}); err != nil {
		logx.From(ctx).Error("не удалось записать сбой разговора", "attempt_id", attemptID, "err", err)
	}
}

// IncidentInput — сбой, о котором сообщает браузер: распознавание или озвучка.
type IncidentInput struct {
	Kind    string
	TurnNo  *int
	Message string
}

// ConversationIncident записывает сбой распознавания или озвучки.
//
// Ворот задания нет: озвучка прощания ломается и после закрытия разговора,
// а журнал — про честный итог, а не про новые реплики. Потолок записей
// держит запрос в базе; сверх него сбои молча не пишутся.
func (s *Service) ConversationIncident(ctx context.Context, actor iam.Actor, attemptID uuid.UUID, in IncidentInput) error {
	kind := strings.TrimSpace(in.Kind)
	if kind != IncidentASR && kind != IncidentTTS {
		return httpx.FieldError("kind", "Сбой распознавания (asr) или озвучки (tts)")
	}
	if in.TurnNo != nil && (*in.TurnNo < 0 || *in.TurnNo > 1000) {
		return httpx.FieldError("turn_no", "Неизвестный номер хода")
	}
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return err
	}
	err = s.addIncident(ctx, actor.OrgID, attempt.ID, incident{
		Kind: kind, TurnNo: in.TurnNo, At: time.Now(),
		Message: clip(strings.TrimSpace(in.Message), incidentMessageMaxRunes),
	})
	if err != nil {
		return httpx.Internal("Не удалось записать сбой").Wrap(err)
	}
	return nil
}
