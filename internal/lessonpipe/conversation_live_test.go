package lessonpipe

// Клиент живого режима движка — JSON договора
// (docs/product/avatar-goals/contract.md, раздел 10.1): reply_mode и steer в
// start и turn, observe, progress, check. Движок пишется параллельно по тому
// же договору; имя поля, разошедшееся здесь, сломает живой разговор без
// единой ошибки компиляции.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/NATIVE/NATIVE/internal/mlgw"
	"github.com/google/uuid"
)

func TestConversationSteerModeContract(t *testing.T) {
	stub := &engineStub{t: t, reply: `{"state":{"v":1,"applied":0},"reply":"","reply_ru":"","target_goal":"p1",
		"steer":"Lead to the goal «Минем исемем …» (phrase)."}`}
	srv := stub.server()
	defer srv.Close()
	runner := newTestRunner(srv.URL)

	out, err := runner.ConversationStart(context.Background(), uuid.New(), uuid.New(),
		ConversationStartRequest{Language: "tatar", Level: "a2", MaxTurns: 8, ReplyMode: ReplyModeSteer})
	if err != nil {
		t.Fatal(err)
	}
	if stub.body["reply_mode"] != "steer" {
		t.Errorf("reply_mode в start: %v", stub.body["reply_mode"])
	}
	if out.Reply != "" || out.Steer != "Lead to the goal «Минем исемем …» (phrase)." || out.TargetGoal != "p1" {
		t.Errorf("ответ start в режиме steer: %+v", out)
	}

	stub.reply = `{"state":{"v":1,"applied":1},"reply":"","reply_ru":"","marks":[],"target_goal":"g1",
		"approach_changed":true,"steer":"Lead to the goal «Местный падеж» (grammar)."}`
	turn, err := runner.ConversationTurn(context.Background(), uuid.New(), uuid.New(), ConversationTurnRequest{
		State: json.RawMessage(`{"v":1,"applied":0}`), Answer: ConversationAnswer{TurnNo: 1, Text: "Әйе", Input: "voice"},
		ReplyMode: ReplyModeSteer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(stub.body); got != "answer,final,history,reply_mode,state" || stub.body["reply_mode"] != "steer" {
		t.Errorf("запрос turn в режиме steer: %s %v", got, stub.body["reply_mode"])
	}
	if turn.Steer != "Lead to the goal «Местный падеж» (grammar)." || !turn.ApproachChanged || turn.Reply != "" {
		t.Errorf("ответ turn в режиме steer: %+v", turn)
	}
}

func TestConversationObserveContract(t *testing.T) {
	stub := &engineStub{t: t, reply: `{"state":{"v":1,"last_reply":"Әйбәт!"},"reply_ru":"Хорошо!"}`}
	srv := stub.server()
	defer srv.Close()
	runner := newTestRunner(srv.URL)

	out, err := runner.ConversationObserve(context.Background(), uuid.New(), uuid.New(), ConversationObserveRequest{
		State: json.RawMessage(`{"v":1}`), TurnNo: 2, Reply: "Әйбәт!", Translate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.method != http.MethodPost || stub.path != "/v1/conversation/observe" {
		t.Fatalf("адрес: %s %s", stub.method, stub.path)
	}
	// Без номера указания поля в запросе нет: движок считает реплику
	// идущей по последнему указанию.
	if got := keysOf(stub.body); got != "reply,state,translate,turn_no" {
		t.Errorf("поля запроса observe: %s", got)
	}
	if stub.body["turn_no"] != float64(2) || stub.body["translate"] != true || stub.body["reply"] != "Әйбәт!" {
		t.Errorf("запрос observe: %v", stub.body)
	}
	if out.ReplyRU != "Хорошо!" || !strings.Contains(string(out.State), `"last_reply"`) || out.Leaked == nil {
		t.Errorf("ответ observe: %+v", out)
	}

	steer := 0
	stub.reply = `{"state":{"v":2},"reply_ru":"","leaked":["p1"]}`
	out, err = runner.ConversationObserve(context.Background(), uuid.New(), uuid.New(), ConversationObserveRequest{
		State: json.RawMessage(`{"v":1}`), TurnNo: 3, Reply: "Минем исемем Алсу.", SteerTurn: &steer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(stub.body); got != "reply,state,steer_turn,translate,turn_no" || stub.body["steer_turn"] != float64(0) {
		t.Errorf("observe с номером указания: %s %v", got, stub.body["steer_turn"])
	}
	if len(out.Leaked) != 1 || out.Leaked[0] != "p1" {
		t.Errorf("произнесённые фокусные фразы: %+v", out.Leaked)
	}

	stub.status, stub.reply = http.StatusConflict, `{"detail":"stale"}`
	if _, err := runner.ConversationObserve(context.Background(), uuid.New(), uuid.New(),
		ConversationObserveRequest{State: json.RawMessage(`{}`), TurnNo: 1, Reply: "Әйе"}); !errors.Is(err, ErrConversationConflict) {
		t.Errorf("409 observe: %v", err)
	}
}

func TestConversationProgressContract(t *testing.T) {
	stub := &engineStub{t: t, reply: `{"goals":[{"key":"p1","kind":"phrase","title":"Минем исемем …","note":"",
		"status":"open"}],"target_goal":"p1","avoid_phrases":["Минем исемем …"],
		"corrections":[{"quote":"Казанга","correct_form":"Казанда"}],"answers_left":5,"finished":false}`}
	srv := stub.server()
	defer srv.Close()
	runner := newTestRunner(srv.URL)

	out, err := runner.ConversationProgress(context.Background(), json.RawMessage(`{"v":1,"last_turn":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if stub.method != http.MethodPost || stub.path != "/v1/conversation/progress" || keysOf(stub.body) != "state" {
		t.Fatalf("запрос progress: %s %s %s", stub.method, stub.path, keysOf(stub.body))
	}
	if st := stub.body["state"].(map[string]any); st["last_turn"] != float64(3) {
		t.Errorf("состояние изменено по дороге: %v", st)
	}
	if out.TargetGoal == nil || *out.TargetGoal != "p1" || len(out.Goals) != 1 || out.Goals[0].Status != "open" ||
		out.AvoidPhrases[0] != "Минем исемем …" || out.Corrections[0].CorrectForm != "Казанда" || out.AnswersLeft != 5 {
		t.Errorf("ответ progress: %+v", out)
	}

	// Все цели закрыты: target_goal — null; пропущенные списки — пустые.
	stub.reply = `{"goals":[],"target_goal":null,"answers_left":0,"finished":true}`
	out, err = runner.ConversationProgress(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.TargetGoal != nil || !out.Finished || out.AvoidPhrases == nil || out.Corrections == nil || out.Goals == nil {
		t.Errorf("ответ progress без цели: %+v", out)
	}
}

func TestConversationCheckContract(t *testing.T) {
	stub := &engineStub{t: t, reply: `{"ok":false,"problems":["фраза «Минем исемем …» ещё не зачтена","два вопроса"]}`}
	srv := stub.server()
	defer srv.Close()
	runner := newTestRunner(srv.URL)

	out, err := runner.ConversationCheck(context.Background(), ConversationCheckRequest{
		State: json.RawMessage(`{"v":1}`), Text: "Минем исемем Алсу. Ә синеке? Кайда яшисең?", Final: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stub.path != "/v1/conversation/check" || keysOf(stub.body) != "final,state,text" || stub.body["final"] != true {
		t.Fatalf("запрос check: %s %v", stub.path, stub.body)
	}
	if out.OK || len(out.Problems) != 2 {
		t.Errorf("ответ check: %+v", out)
	}
	stub.reply = `{"ok":true}`
	out, err = runner.ConversationCheck(context.Background(), ConversationCheckRequest{State: json.RawMessage(`{}`), Text: "Әйбәт!"})
	if err != nil || !out.OK || out.Problems == nil || len(out.Problems) != 0 {
		t.Errorf("годная реплика: %+v %v", out, err)
	}
}

// Вызовы модели — в пуле чата; ручки движка без модели — мимо пулов:
// делегат живой сессии не ждёт слот за чужими вызовами модели.
func TestConversationLiveCallsAndPools(t *testing.T) {
	stub := &engineStub{t: t, reply: `{"state":{"v":1},"ok":true,"goals":[]}`}
	srv := stub.server()
	defer srv.Close()
	runner := newTestRunner(srv.URL)
	chat := func() int64 { return runner.gw.Pool(mlgw.PoolLLMChat).Stats().Total }
	ctx := context.Background()

	if _, err := runner.ConversationProgress(ctx, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ConversationCheck(ctx, ConversationCheckRequest{State: json.RawMessage(`{}`), Text: "Әйе"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ConversationObserve(ctx, uuid.New(), uuid.New(),
		ConversationObserveRequest{State: json.RawMessage(`{}`), TurnNo: 1, Reply: "Әйе"}); err != nil {
		t.Fatal(err)
	}
	if n := chat(); n != 0 {
		t.Errorf("вызовы без модели заняли слоты чата: %d", n)
	}
	if _, err := runner.ConversationObserve(ctx, uuid.New(), uuid.New(),
		ConversationObserveRequest{State: json.RawMessage(`{}`), TurnNo: 1, Reply: "Әйе", Translate: true}); err != nil {
		t.Fatal(err)
	}
	if n := chat(); n != 1 {
		t.Errorf("перевод — вызов модели, он идёт через пул чата: %d", n)
	}
}
