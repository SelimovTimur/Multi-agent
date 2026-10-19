package lessonpipe

// Разговор по целям урока — движок llm_service /v1/conversation/* и
// справочник уровней /v1/levels/guidance.
//
// Договор — docs/product/avatar-goals/contract.md, разделы 3, 4 и 10.1
// (живой режим: reply_mode и steer, observe, progress, check). Имена полей
// повторяют его один в один: по нему же параллельно пишется сам движок, и
// расхождение здесь ломает разговор молча.
//
// Состоянием движка владеет движок: Go хранит его в базе непрозрачным JSON и
// возвращает без изменений. Разбирать его здесь нельзя — иначе каждая правка
// движка ломает Go.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/mlgw"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/google/uuid"
)

// ErrConversationConflict — движок отказался применить ход: в его состоянии
// уже есть ход с таким же или большим номером (409 по договору). Это не сбой
// сервиса — разговор ушёл дальше, и повтор того же хода ничего не изменит.
var ErrConversationConflict = errors.New("lessonpipe: ход уже применён в состоянии движка")

// Сколько ждём движок. Ход и начало — то, что ученик ждёт глазами: дольше
// полутора минут ответ уже не нужен, ученик нажмёт «Повторить». Разбор идёт в
// фоне, но его аренда в базе — три минуты (learning): разбор обязан
// закончиться раньше, чем его перезапустит следующий запрос итога.
const (
	ConversationTurnBudget    = 90 * time.Second
	ConversationAnalyzeBudget = 150 * time.Second
	// ConversationObserveBudget — реплика аватара живой сессии с переводом:
	// один короткий вызов модели. Перевод ждёт «Подсказка», а не разговор.
	ConversationObserveBudget = 30 * time.Second
	// conversationToolBudget — ручки движка без модели (progress, check,
	// observe без перевода). Их ждёт делегат живой сессии посреди реплики:
	// дольше нескольких секунд — значит, сервис лежит.
	conversationToolBudget = 5 * time.Second
	// levelGuidanceBudget — справочник отвечает из памяти без модели;
	// дольше пары секунд — значит, сервис лежит, и голосовой разговор
	// откроется без указаний уровня, а не будет их ждать.
	levelGuidanceBudget = 3 * time.Second
)

// Как движок отвечает на ход (раздел 10.1 договора).
const (
	// ReplyModeText — реплика аватара текстом: разговор голосовыми
	// сообщениями, как до живого режима.
	ReplyModeText = "text"
	// ReplyModeSteer — указание живой сессии на следующий ход: реплику
	// произносит сама сессия, reply и reply_ru пустые.
	ReplyModeSteer = "steer"
)

// ConversationGoal — цель разговора: фокусная фраза или грамматическая тема.
type ConversationGoal struct {
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

// ConversationLine — реплика в истории разговора.
type ConversationLine struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	TurnNo  int    `json:"turn_no"`
}

// ConversationStartRequest — тело POST /v1/conversation/start.
type ConversationStartRequest struct {
	Language     string             `json:"language"`
	Level        string             `json:"level"`
	RefinedText  string             `json:"refined_text"`
	Collocations string             `json:"collocations"`
	RolePrompt   string             `json:"role_prompt"`
	Goals        []ConversationGoal `json:"goals"`
	MaxTurns     int                `json:"max_turns"`
	// History — реплики уже бывшего разговора (прежние режимы): тогда движок
	// продолжает его, а не здоровается заново.
	History []ConversationLine `json:"history"`
	// ReplyMode — ReplyModeText или ReplyModeSteer; пусто — text.
	ReplyMode string `json:"reply_mode"`
}

// ConversationStartResponse — первое состояние и вступительная реплика или,
// в режиме steer, указание сессии на вступление.
type ConversationStartResponse struct {
	State      json.RawMessage `json:"state"`
	Reply      string          `json:"reply"`
	ReplyRU    string          `json:"reply_ru"`
	TargetGoal string          `json:"target_goal"`
	Steer      string          `json:"steer"`
}

// ConversationAnswer — ответ ученика на ходе.
type ConversationAnswer struct {
	TurnNo int    `json:"turn_no"`
	Text   string `json:"text"`
	Input  string `json:"input"`
	Edited bool   `json:"edited"`
}

// ConversationTurnRequest — тело POST /v1/conversation/turn.
type ConversationTurnRequest struct {
	State   json.RawMessage    `json:"state"`
	Answer  ConversationAnswer `json:"answer"`
	History []ConversationLine `json:"history"`
	// Final — ответ последний: аватар прощается, а не задаёт вопрос, на
	// который уже не ответят.
	Final bool `json:"final"`
	// ReplyMode — ReplyModeText или ReplyModeSteer; пусто — text.
	ReplyMode string `json:"reply_mode"`
}

// ConversationMark — отметка по цели на ходе.
type ConversationMark struct {
	TurnNo      int     `json:"turn_no"`
	GoalKey     string  `json:"goal_key"`
	Verdict     string  `json:"verdict"`
	ErrorKind   *string `json:"error_kind"`
	Quote       string  `json:"quote"`
	CorrectForm string  `json:"correct_form"`
	NoteRU      string  `json:"note_ru"`
}

// ConversationTurnResponse — новое состояние и реплика аватара (или, в
// режиме steer, указание живой сессии на следующий ход).
//
// Отметки приходят и здесь, но ученику не показываются: они лежат в
// состоянии движка, а в отчёт попадают через разбор.
type ConversationTurnResponse struct {
	State           json.RawMessage    `json:"state"`
	Reply           string             `json:"reply"`
	ReplyRU         string             `json:"reply_ru"`
	Marks           []ConversationMark `json:"marks"`
	TargetGoal      string             `json:"target_goal"`
	ApproachChanged bool               `json:"approach_changed"`
	Steer           string             `json:"steer"`
}

// ConversationTranscriptLine — реплика в расшифровке для разбора.
type ConversationTranscriptLine struct {
	TurnNo  int    `json:"turn_no"`
	Role    string `json:"role"`
	Content string `json:"content"`
	Input   string `json:"input"`
	Edited  bool   `json:"edited"`
}

// ConversationAnalyzeRequest — тело POST /v1/conversation/analyze.
//
// State == nil уходит как null: разговор прежнего режима, состояние движок
// строит сам по целям.
type ConversationAnalyzeRequest struct {
	Language   string                       `json:"language"`
	Level      string                       `json:"level"`
	Goals      []ConversationGoal           `json:"goals"`
	State      json.RawMessage              `json:"state"`
	Transcript []ConversationTranscriptLine `json:"transcript"`
}

// ConversationObserveRequest — тело POST /v1/conversation/observe: настоящая
// реплика аватара из расшифровки живой сессии.
type ConversationObserveRequest struct {
	State  json.RawMessage `json:"state"`
	TurnNo int             `json:"turn_no"`
	Reply  string          `json:"reply"`
	// Translate — нужен перевод для «Подсказки»: один короткий вызов модели.
	Translate bool `json:"translate"`
	// SteerTurn — номер хода указания, которое уже дошло до сессии к началу
	// реплики (вступление — 0). nil — поля в запросе нет, и движок считает
	// реплику идущей по последнему указанию.
	SteerTurn *int `json:"steer_turn,omitempty"`
}

// ConversationObserveResponse — состояние с запомненной репликой, перевод и
// незачтённые фокусные фразы, которые аватар произнёс сам.
type ConversationObserveResponse struct {
	State   json.RawMessage `json:"state"`
	ReplyRU string          `json:"reply_ru"`
	Leaked  []string        `json:"leaked"`
}

// ConversationStateRequest — тело POST /v1/conversation/progress.
type ConversationStateRequest struct {
	State json.RawMessage `json:"state"`
}

// ConversationProgressGoal — цель и открыта ли она ещё (open, closed).
type ConversationProgressGoal struct {
	Key    string `json:"key"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Note   string `json:"note"`
	Status string `json:"status"`
}

// ConversationCorrection — верная форма, которую аватару стоит естественно
// употребить после ошибки ученика.
type ConversationCorrection struct {
	Quote       string `json:"quote"`
	CorrectForm string `json:"correct_form"`
}

// ConversationProgressResponse — ход разговора для инструмента делегата.
type ConversationProgressResponse struct {
	Goals []ConversationProgressGoal `json:"goals"`
	// TargetGoal — null, когда все цели закрыты.
	TargetGoal   *string                  `json:"target_goal"`
	AvoidPhrases []string                 `json:"avoid_phrases"`
	Corrections  []ConversationCorrection `json:"corrections"`
	AnswersLeft  int                      `json:"answers_left"`
	Finished     bool                     `json:"finished"`
}

// ConversationCheckRequest — тело POST /v1/conversation/check.
type ConversationCheckRequest struct {
	State json.RawMessage `json:"state"`
	Text  string          `json:"text"`
	Final bool            `json:"final"`
}

// ConversationCheckResponse — годится ли реплика и что в ней не так.
type ConversationCheckResponse struct {
	OK       bool     `json:"ok"`
	Problems []string `json:"problems"`
}

// LevelGuidance — компактные указания уровня для промпта.
type LevelGuidance struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// ConversationStart начинает разговор.
func (c *Client) ConversationStart(ctx context.Context, in ConversationStartRequest) (ConversationStartResponse, error) {
	in.Goals = nonNilGoals(in.Goals)
	if in.History == nil {
		in.History = []ConversationLine{}
	}
	in.ReplyMode = replyMode(in.ReplyMode)
	var out ConversationStartResponse
	err := c.conversationCall(ctx, "/v1/conversation/start", in, &out)
	return out, err
}

// ConversationTurn — ответ ученика и реплика аватара (или указание живой
// сессии) на него.
func (c *Client) ConversationTurn(ctx context.Context, in ConversationTurnRequest) (ConversationTurnResponse, error) {
	if in.History == nil {
		in.History = []ConversationLine{}
	}
	in.ReplyMode = replyMode(in.ReplyMode)
	var out ConversationTurnResponse
	err := c.conversationCall(ctx, "/v1/conversation/turn", in, &out)
	return out, err
}

// ConversationAnalyze — разбор закрытого разговора. Ответ отдаётся сырым
// JSON: он целиком ложится в снимок отчёта, и поле, которого Go ещё не
// знает, не должно потеряться по дороге.
func (c *Client) ConversationAnalyze(ctx context.Context, in ConversationAnalyzeRequest) (json.RawMessage, error) {
	in.Goals = nonNilGoals(in.Goals)
	if in.Transcript == nil {
		in.Transcript = []ConversationTranscriptLine{}
	}
	var out json.RawMessage
	if err := c.conversationCall(ctx, "/v1/conversation/analyze", in, &out); err != nil {
		return nil, err
	}
	// Разбор — объект. null или массив здесь значит сломанный ответ, и
	// класть его в неизменяемый отчёт нельзя.
	if trimmed := strings.TrimSpace(string(out)); !strings.HasPrefix(trimmed, "{") {
		return nil, httpx.Internal("ML-сервис вернул пустой разбор разговора")
	}
	return out, nil
}

// ConversationObserve — настоящая реплика аватара живой сессии: движок
// запоминает её и, если просили, переводит.
func (c *Client) ConversationObserve(ctx context.Context, in ConversationObserveRequest) (ConversationObserveResponse, error) {
	var out ConversationObserveResponse
	if err := c.conversationCall(ctx, "/v1/conversation/observe", in, &out); err != nil {
		return ConversationObserveResponse{}, err
	}
	if out.Leaked == nil {
		out.Leaked = []string{}
	}
	return out, nil
}

// ConversationProgress — ход разговора по состоянию, без модели.
func (c *Client) ConversationProgress(ctx context.Context, state json.RawMessage) (ConversationProgressResponse, error) {
	var out ConversationProgressResponse
	if err := c.conversationCall(ctx, "/v1/conversation/progress", ConversationStateRequest{State: state}, &out); err != nil {
		return ConversationProgressResponse{}, err
	}
	// Пустые списки — массивами: делегат читает ответ как есть, и null на
	// месте списка он понял бы как «данных нет», а не «ничего не нужно».
	if out.Goals == nil {
		out.Goals = []ConversationProgressGoal{}
	}
	if out.AvoidPhrases == nil {
		out.AvoidPhrases = []string{}
	}
	if out.Corrections == nil {
		out.Corrections = []ConversationCorrection{}
	}
	return out, nil
}

// ConversationCheck — проверка задуманной реплики аватара, без модели.
func (c *Client) ConversationCheck(ctx context.Context, in ConversationCheckRequest) (ConversationCheckResponse, error) {
	var out ConversationCheckResponse
	if err := c.conversationCall(ctx, "/v1/conversation/check", in, &out); err != nil {
		return ConversationCheckResponse{}, err
	}
	if out.Problems == nil {
		out.Problems = []string{}
	}
	return out, nil
}

// replyMode — режим ответа движка; пустой — текст, как до живого режима.
// Уходит всегда: неизвестное поле движок старой версии пропустит.
func replyMode(mode string) string {
	if mode == "" {
		return ReplyModeText
	}
	return mode
}

// conversationCall — POST движку с отдельной обработкой 409.
func (c *Client) conversationCall(ctx context.Context, path string, in, out any) error {
	status, raw, err := c.exchange(ctx, http.MethodPost, path, in)
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		return ErrConversationConflict
	}
	if status < 200 || status >= 300 {
		return upstreamError(status, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return httpx.Internal("Не удалось разобрать ответ ML-сервиса").Wrap(err)
	}
	return nil
}

// LevelGuidance — указания уровня (профиль CEFR) для промпта.
func (c *Client) LevelGuidance(ctx context.Context, language, level string) (LevelGuidance, error) {
	q := url.Values{"language": {language}, "level": {level}}
	status, raw, err := c.exchange(ctx, http.MethodGet, "/v1/levels/guidance?"+q.Encode(), nil)
	if err != nil {
		return LevelGuidance{}, err
	}
	if status < 200 || status >= 300 {
		return LevelGuidance{}, upstreamError(status, raw)
	}
	var out LevelGuidance
	if err := json.Unmarshal(raw, &out); err != nil {
		return LevelGuidance{}, httpx.Internal("Не удалось разобрать ответ ML-сервиса").Wrap(err)
	}
	return out, nil
}

func nonNilGoals(goals []ConversationGoal) []ConversationGoal {
	if goals == nil {
		return []ConversationGoal{}
	}
	return goals
}

// ---------------------------------------------------------------------------
// Через пулы
// ---------------------------------------------------------------------------
//
// Вызовы модели — в пуле чата: ученик ждёт и реплику, и итог разговора, а
// очередь за фоновыми сборками уроков здесь недопустима (INFRA-8). Ручки
// движка без модели (progress, check, observe без перевода) идут мимо
// пулов, как справочник уровней: учитывать в них нечего, а ждать слот за
// чужими вызовами модели живой сессии нельзя.

// ConversationStart — начало разговора через пул чата.
func (r *Runner) ConversationStart(ctx context.Context, orgID, userID uuid.UUID,
	in ConversationStartRequest) (ConversationStartResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, ConversationTurnBudget)
	defer cancel()
	var out ConversationStartResponse
	err := r.gw.Do(ctx, r.conversationCall(orgID, userID, "conversation_start"),
		func(ctx context.Context) (mlgw.Usage, error) {
			var callErr error
			out, callErr = r.client.ConversationStart(ctx, in)
			return r.usage(), callErr
		})
	return out, err
}

// ConversationTurn — ход разговора через пул чата.
func (r *Runner) ConversationTurn(ctx context.Context, orgID, userID uuid.UUID,
	in ConversationTurnRequest) (ConversationTurnResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, ConversationTurnBudget)
	defer cancel()
	var out ConversationTurnResponse
	err := r.gw.Do(ctx, r.conversationCall(orgID, userID, "conversation_turn"),
		func(ctx context.Context) (mlgw.Usage, error) {
			var callErr error
			out, callErr = r.client.ConversationTurn(ctx, in)
			return r.usage(), callErr
		})
	return out, err
}

// ConversationAnalyze — разбор разговора через пул чата.
func (r *Runner) ConversationAnalyze(ctx context.Context, orgID, userID uuid.UUID,
	in ConversationAnalyzeRequest) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, ConversationAnalyzeBudget)
	defer cancel()
	var out json.RawMessage
	err := r.gw.Do(ctx, r.conversationCall(orgID, userID, "conversation_analyze"),
		func(ctx context.Context) (mlgw.Usage, error) {
			var callErr error
			out, callErr = r.client.ConversationAnalyze(ctx, in)
			return r.usage(), callErr
		})
	return out, err
}

// ConversationObserve — реплика аватара живой сессии. С переводом это вызов
// модели — через пул чата, как ход. Без перевода движок отвечает без
// модели — мимо пулов, как progress и check.
func (r *Runner) ConversationObserve(ctx context.Context, orgID, userID uuid.UUID,
	in ConversationObserveRequest) (ConversationObserveResponse, error) {
	if !in.Translate {
		ctx, cancel := context.WithTimeout(ctx, conversationToolBudget)
		defer cancel()
		return r.client.ConversationObserve(ctx, in)
	}
	ctx, cancel := context.WithTimeout(ctx, ConversationObserveBudget)
	defer cancel()
	var out ConversationObserveResponse
	err := r.gw.Do(ctx, r.conversationCall(orgID, userID, "conversation_observe"),
		func(ctx context.Context) (mlgw.Usage, error) {
			var callErr error
			out, callErr = r.client.ConversationObserve(ctx, in)
			return r.usage(), callErr
		})
	return out, err
}

// ConversationProgress — ход разговора для инструмента делегата живой
// сессии. Мимо пулов намеренно: движок отвечает из состояния без модели, а
// делегат ждёт ответ посреди реплики. В пуле чата он стоял бы в очереди за
// вызовами модели всего класса — секунды тишины в живом разговоре.
func (r *Runner) ConversationProgress(ctx context.Context, state json.RawMessage) (ConversationProgressResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, conversationToolBudget)
	defer cancel()
	return r.client.ConversationProgress(ctx, state)
}

// ConversationCheck — проверка реплики для инструмента делегата. Мимо пулов
// по той же причине, что и ConversationProgress: модели в ней нет.
func (r *Runner) ConversationCheck(ctx context.Context, in ConversationCheckRequest) (ConversationCheckResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, conversationToolBudget)
	defer cancel()
	return r.client.ConversationCheck(ctx, in)
}

// LevelGuidance — указания уровня. Мимо пулов намеренно: это справочник в
// памяти llm_service, а не вызов модели.
func (r *Runner) LevelGuidance(ctx context.Context, language, level string) (LevelGuidance, error) {
	ctx, cancel := context.WithTimeout(ctx, levelGuidanceBudget)
	defer cancel()
	return r.client.LevelGuidance(ctx, language, level)
}

// conversationCall — разговор идёт в пуле чата: ученик ждёт ответ на уроке.
func (r *Runner) conversationCall(orgID, userID uuid.UUID, stage string) mlgw.Call {
	return mlgw.Call{
		Pipeline: "avatar",
		Stage:    stage,
		Pool:     mlgw.PoolLLMChat,
		OrgID:    nilable(orgID),
		UserID:   nilable(userID),
	}
}
