package app

// Живой режим через порты: адаптер движка переносит reply_mode, steer,
// observe (с номером указания), progress и check без потерь, а адаптер
// голоса — цели с пояснениями и уже сказанное.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/learning"
	"github.com/NATIVE/NATIVE/internal/lessonpipe"
	"github.com/NATIVE/NATIVE/internal/mlgw"
	"github.com/google/uuid"
)

// fakeLLM — llm_service: отвечает заготовкой по пути и запоминает тела.
func fakeLLM(t *testing.T, replies map[string]string) (*httptest.Server, map[string]map[string]any) {
	t.Helper()
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("%s: тело не JSON: %s", r.URL.Path, raw)
		}
		bodies[r.URL.Path] = body
		reply, ok := replies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, bodies
}

func TestConversationAdapterCarriesLiveFields(t *testing.T) {
	srv, bodies := fakeLLM(t, map[string]string{
		"/v1/conversation/start":   `{"state":{"v":1},"reply":"","reply_ru":"","target_goal":"p1","steer":"greet and lead to p1"}`,
		"/v1/conversation/turn":    `{"state":{"v":2},"reply":"","reply_ru":"","marks":[],"target_goal":"g1","steer":"lead to g1"}`,
		"/v1/conversation/observe": `{"state":{"v":3},"reply_ru":"Хорошо!","leaked":["p1"]}`,
		"/v1/conversation/progress": `{"goals":[{"key":"p1","kind":"phrase","title":"Минем исемем …","note":"",
			"status":"closed"}],"target_goal":null,"avoid_phrases":[],"corrections":[{"quote":"Казанга",
			"correct_form":"Казанда"}],"answers_left":2,"finished":false}`,
		"/v1/conversation/check": `{"ok":false,"problems":["два вопроса"]}`,
	})
	runner := lessonpipe.NewRunner(lessonpipe.NewClient(srv.URL, 5*time.Second),
		mlgw.New(config.MLConfig{LLMChatConcurrency: 2}, nil))
	a := conversationAdapter{runner: runner}
	ctx := context.Background()
	org, student := uuid.New(), uuid.New()

	start, err := a.Start(ctx, org, student, learning.EngineStart{Language: "tatar", ReplyMode: learning.ReplySteer})
	if err != nil || start.Steer != "greet and lead to p1" || bodies["/v1/conversation/start"]["reply_mode"] != "steer" {
		t.Fatalf("start в режиме steer: %+v %v %v", start, err, bodies["/v1/conversation/start"])
	}
	// Без режима — текст, как до живого режима.
	if _, err := a.Start(ctx, org, student, learning.EngineStart{Language: "tatar"}); err != nil ||
		bodies["/v1/conversation/start"]["reply_mode"] != "text" {
		t.Fatalf("start без режима: %v %v", err, bodies["/v1/conversation/start"]["reply_mode"])
	}
	turn, err := a.Turn(ctx, org, student, learning.EngineTurn{State: json.RawMessage(`{"v":1}`),
		Answer: learning.EngineLine{TurnNo: 1, Content: "Әйе"}, ReplyMode: learning.ReplySteer})
	if err != nil || turn.Steer != "lead to g1" || bodies["/v1/conversation/turn"]["reply_mode"] != "steer" {
		t.Fatalf("turn в режиме steer: %+v %v", turn, err)
	}

	steer := 1
	obs, err := a.Observe(ctx, org, student, learning.EngineObserve{State: json.RawMessage(`{"v":2}`), TurnNo: 2,
		Reply: "Әйбәт!", Translate: true, SteerTurn: &steer})
	if err != nil || obs.ReplyRU != "Хорошо!" || len(obs.Leaked) != 1 || !strings.Contains(string(obs.State), `"v":3`) {
		t.Fatalf("observe: %+v %v", obs, err)
	}
	if body := bodies["/v1/conversation/observe"]; body["steer_turn"] != float64(1) || body["turn_no"] != float64(2) ||
		body["translate"] != true {
		t.Errorf("что ушло в observe: %v", body)
	}
	if _, err := a.Observe(ctx, org, student, learning.EngineObserve{State: json.RawMessage(`{}`), TurnNo: 2,
		Reply: "Әйбәт!"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := bodies["/v1/conversation/observe"]["steer_turn"]; ok {
		t.Error("без номера указания поле ушло в движок")
	}

	p, err := a.Progress(ctx, org, student, json.RawMessage(`{"v":3}`))
	if err != nil || p.TargetGoal != "" || len(p.Goals) != 1 || p.Goals[0].Status != "closed" ||
		len(p.Corrections) != 1 || p.Corrections[0].CorrectForm != "Казанда" || p.AnswersLeft != 2 {
		t.Fatalf("progress: %+v %v", p, err)
	}
	r, err := a.Check(ctx, org, student, learning.EngineCheck{State: json.RawMessage(`{}`), Text: "Әйбәт?", Final: true})
	if err != nil || r.OK || len(r.Problems) != 1 || bodies["/v1/conversation/check"]["final"] != true {
		t.Fatalf("check: %+v %v", r, err)
	}
}

// Голосу уходят цели с пояснениями и уже сказанное — только роли и текст.
func TestLiveConversationBrief(t *testing.T) {
	no := 1
	brief := liveConversationBrief(learning.AvatarBrief{
		Language: "tatar", Level: "A2", Text: "Текст", RolePrompt: "Роль", MaxTurns: 8, LevelGuidance: "указания",
		Goals: []learning.AvatarGoal{{Key: "p1", Kind: learning.GoalPhrase, Title: "Минем исемем …", Note: "Меня зовут"}},
		History: []learning.DialogTurn{{Role: learning.RoleStudent, Content: "Минем исемем Айдар", TurnNo: &no,
			ClientMsgID: "s-1", Steer: "Turn guidance: lead to g1"}},
	})
	if len(brief.Goals) != 1 || brief.Goals[0].Note != "Меня зовут" || brief.Goals[0].Kind != "phrase" ||
		brief.MaxTurns != 8 || brief.LevelGuidance != "указания" || brief.Level != "A2" {
		t.Errorf("урок для голоса: %+v", brief)
	}
	if brief.History != `[{"content":"Минем исемем Айдар","role":"student"}]` {
		t.Errorf("история для голоса: %s", brief.History)
	}
}
