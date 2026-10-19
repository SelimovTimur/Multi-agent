package lessonpipe

// Клиент движка разговора проверяется на JSON договора
// (docs/product/avatar-goals/contract.md, разделы 3 и 4): движок пишется
// параллельно по тому же договору, и имя поля, разошедшееся здесь, сломает
// разговор без единой ошибки компиляции.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/mlgw"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/google/uuid"
)

// engineStub — llm_service, который запоминает тело запроса и отвечает
// заготовкой.
type engineStub struct {
	t      *testing.T
	path   string
	method string
	query  string
	body   map[string]any
	status int
	reply  string
}

func (s *engineStub) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.path, s.method, s.query = r.URL.Path, r.Method, r.URL.RawQuery
		// Тело — только этого запроса: в одну карту разные запросы теста
		// сложили бы свои поля.
		s.body = nil
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &s.body); err != nil {
					s.t.Errorf("тело запроса не JSON: %s", raw)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		status := s.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, s.reply)
	}))
}

func keysOf(m map[string]any) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func newTestRunner(url string) *Runner {
	return NewRunner(NewClient(url, 5*time.Second), mlgw.New(config.MLConfig{LLMChatConcurrency: 2}, nil))
}

func TestConversationStartContract(t *testing.T) {
	stub := &engineStub{t: t, reply: `{"state":{"v":1,"applied":0},"reply":"Исәнме! Синең исемең ничек?",
		"reply_ru":"Привет! Как тебя зовут?","target_goal":"p1"}`}
	srv := stub.server()
	defer srv.Close()

	out, err := newTestRunner(srv.URL).ConversationStart(context.Background(), uuid.New(), uuid.New(),
		ConversationStartRequest{
			Language: "tatar", Level: "a2", RefinedText: "Минем исемем Алсу.",
			Collocations: "исем — имя", RolePrompt: "",
			Goals:    []ConversationGoal{{Key: "p1", Kind: "phrase", Title: "Минем исемем …", Note: "Меня зовут …"}},
			MaxTurns: 8,
		})
	if err != nil {
		t.Fatal(err)
	}
	if stub.method != http.MethodPost || stub.path != "/v1/conversation/start" {
		t.Fatalf("адрес: %s %s", stub.method, stub.path)
	}
	if got := keysOf(stub.body); got != "collocations,goals,history,language,level,max_turns,refined_text,reply_mode,role_prompt" {
		t.Errorf("поля запроса start: %s", got)
	}
	// Режим не задан — текст, как до живого режима.
	if stub.body["reply_mode"] != "text" {
		t.Errorf("reply_mode по умолчанию: %v", stub.body["reply_mode"])
	}
	// Пустая история уходит массивом: null движок прочёл бы как ошибку схемы.
	if h, ok := stub.body["history"].([]any); !ok || len(h) != 0 {
		t.Errorf("history: %#v", stub.body["history"])
	}
	goal := stub.body["goals"].([]any)[0].(map[string]any)
	if keysOf(goal) != "key,kind,note,title" || goal["key"] != "p1" || goal["kind"] != "phrase" {
		t.Errorf("цель: %v", goal)
	}
	if stub.body["level"] != "a2" || stub.body["max_turns"] != float64(8) {
		t.Errorf("уровень и длина: %v %v", stub.body["level"], stub.body["max_turns"])
	}
	if out.Reply == "" || out.ReplyRU != "Привет! Как тебя зовут?" || out.TargetGoal != "p1" ||
		!strings.Contains(string(out.State), `"applied":0`) {
		t.Errorf("ответ start: %+v", out)
	}
}

func TestConversationTurnContract(t *testing.T) {
	stub := &engineStub{t: t, reply: `{"state":{"v":1,"applied":3},"reply":"Казан матур шәһәр!","reply_ru":"Казань — красивый город!",
		"marks":[{"turn_no":3,"goal_key":"g1","verdict":"credited","error_kind":null,"quote":"Казанда","correct_form":"","note_ru":""}],
		"target_goal":"p2","approach_changed":false}`}
	srv := stub.server()
	defer srv.Close()

	out, err := newTestRunner(srv.URL).ConversationTurn(context.Background(), uuid.New(), uuid.New(),
		ConversationTurnRequest{
			State:   json.RawMessage(`{"v":1,"applied":2}`),
			Answer:  ConversationAnswer{TurnNo: 3, Text: "Мин Казанда яшим", Input: "voice"},
			History: []ConversationLine{{Role: "avatar", Content: "Син кайда яшисең?", TurnNo: 2}},
		})
	if err != nil {
		t.Fatal(err)
	}
	if stub.path != "/v1/conversation/turn" {
		t.Fatalf("адрес: %s", stub.path)
	}
	if got := keysOf(stub.body); got != "answer,final,history,reply_mode,state" {
		t.Errorf("поля запроса turn: %s", got)
	}
	if stub.body["reply_mode"] != "text" {
		t.Errorf("reply_mode по умолчанию: %v", stub.body["reply_mode"])
	}
	answer := stub.body["answer"].(map[string]any)
	if keysOf(answer) != "edited,input,text,turn_no" || answer["turn_no"] != float64(3) || answer["input"] != "voice" {
		t.Errorf("ответ ученика: %v", answer)
	}
	line := stub.body["history"].([]any)[0].(map[string]any)
	if keysOf(line) != "content,role,turn_no" {
		t.Errorf("реплика истории: %v", line)
	}
	// Состояние уходит тем же JSON, что пришёл из базы.
	if st := stub.body["state"].(map[string]any); st["applied"] != float64(2) {
		t.Errorf("состояние изменено по дороге: %v", st)
	}
	if out.Reply != "Казан матур шәһәр!" || out.TargetGoal != "p2" || len(out.Marks) != 1 ||
		out.Marks[0].Verdict != "credited" || out.Marks[0].ErrorKind != nil || out.Marks[0].Quote != "Казанда" {
		t.Errorf("ответ turn: %+v", out)
	}
}

// 409 движка — «ход уже применён»: не сбой сервиса, а отдельный ответ,
// по которому learning отдаёт ученику уже записанное.
func TestConversationTurnConflictIsDistinct(t *testing.T) {
	stub := &engineStub{t: t, status: http.StatusConflict, reply: `{"detail":"turn already applied"}`}
	srv := stub.server()
	defer srv.Close()
	_, err := newTestRunner(srv.URL).ConversationTurn(context.Background(), uuid.New(), uuid.New(),
		ConversationTurnRequest{State: json.RawMessage(`{}`), Answer: ConversationAnswer{TurnNo: 1, Text: "Әйе"}})
	if !errors.Is(err, ErrConversationConflict) {
		t.Fatalf("ожидали ErrConversationConflict, получили %v", err)
	}

	// Сбой провайдера — обычная недоступность: ученик увидит «Повторить».
	stub.status, stub.reply = http.StatusBadGateway, `{"code":"provider_timeout"}`
	_, err = newTestRunner(srv.URL).ConversationTurn(context.Background(), uuid.New(), uuid.New(),
		ConversationTurnRequest{State: json.RawMessage(`{}`), Answer: ConversationAnswer{TurnNo: 1, Text: "Әйе"}})
	if he := httpx.AsError(err); he == nil || he.Status != http.StatusServiceUnavailable || errors.Is(err, ErrConversationConflict) {
		t.Fatalf("сбой провайдера: %v", err)
	}
}

func TestConversationAnalyzeContract(t *testing.T) {
	analysis := `{"goals":[{"key":"p1","kind":"phrase","title":"Минем исемем …","note":"","credited":true,
		"credited_turn":1,"correct_uses":1,"errors":0,"misses":0}],"marks":[],"general_errors":[],
		"summary":{"credited_phrases":[{"key":"p1","title":"Минем исемем …"}],"applied_themes":[],
		"phrases_total":1,"themes_total":0,"next_time":null},
		"speech_level":{"estimated":"a1","shares":{"a1":1},"unknown_share":0},"future_field":42}`
	stub := &engineStub{t: t, reply: analysis}
	srv := stub.server()
	defer srv.Close()

	out, err := newTestRunner(srv.URL).ConversationAnalyze(context.Background(), uuid.New(), uuid.New(),
		ConversationAnalyzeRequest{
			Language: "tatar", Level: "a2",
			Goals: []ConversationGoal{{Key: "p1", Kind: "phrase", Title: "Минем исемем …"}},
			Transcript: []ConversationTranscriptLine{
				{TurnNo: 0, Role: "avatar", Content: "Исәнме!", Input: "text"},
				{TurnNo: 1, Role: "student", Content: "Минем исемем Алсу", Input: "voice", Edited: true},
			},
		})
	if err != nil {
		t.Fatal(err)
	}
	if stub.path != "/v1/conversation/analyze" {
		t.Fatalf("адрес: %s", stub.path)
	}
	if got := keysOf(stub.body); got != "goals,language,level,state,transcript" {
		t.Errorf("поля запроса analyze: %s", got)
	}
	// Разговор прежнего режима: состояния нет, уходит null.
	if v, ok := stub.body["state"]; !ok || v != nil {
		t.Errorf("state должен быть null: %#v", v)
	}
	line := stub.body["transcript"].([]any)[1].(map[string]any)
	if keysOf(line) != "content,edited,input,role,turn_no" || line["edited"] != true {
		t.Errorf("строка расшифровки: %v", line)
	}
	// Разбор отдаётся целиком, включая поля, которых Go ещё не знает.
	if !strings.Contains(string(out), `"future_field":42`) {
		t.Errorf("разбор обрезан: %s", out)
	}

	// Пустой разбор в неизменяемый отчёт не ложится.
	stub.reply = `null`
	if _, err := newTestRunner(srv.URL).ConversationAnalyze(context.Background(), uuid.New(), uuid.New(),
		ConversationAnalyzeRequest{Language: "tatar"}); err == nil {
		t.Error("null вместо разбора принят")
	}
}

func TestLevelGuidanceContract(t *testing.T) {
	stub := &engineStub{t: t, reply: `{"level":"a2","text":"Грамматика A1–A2: …"}`}
	srv := stub.server()
	defer srv.Close()

	out, err := newTestRunner(srv.URL).LevelGuidance(context.Background(), "tatar", "a2")
	if err != nil {
		t.Fatal(err)
	}
	if stub.method != http.MethodGet || stub.path != "/v1/levels/guidance" ||
		stub.query != "language=tatar&level=a2" {
		t.Fatalf("запрос: %s %s?%s", stub.method, stub.path, stub.query)
	}
	if out.Level != "a2" || out.Text != "Грамматика A1–A2: …" {
		t.Errorf("ответ: %+v", out)
	}
}
