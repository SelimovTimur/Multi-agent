package learning

// Форма ответов живого режима — дословно по договору
// docs/product/avatar-goals/contract.md, раздел 10.2: по нему параллельно
// пишется экран ученика. Здесь — имена полей, коды и тексты отказов.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLiveConversationHTTPContract(t *testing.T) {
	f, engine, a := conversationFixture(t, 2)
	voice := withLive(f)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	actor := aidar
	h := conversationRouter(&API{svc: f.svc}, &actor)
	base := "/my/attempts/" + attempt.ID.String() + "/conversation"

	_, out := call(t, h, http.MethodGet, base, nil)
	if out["live_available"] != true {
		t.Fatalf("live_available: %v", out)
	}

	code, out := call(t, h, http.MethodPost, base+"/live", jsonObj{"sdp": testSDP})
	if code != http.StatusCreated {
		t.Fatalf("живая сессия: %d %v", code, out)
	}
	exactKeys(t, "живая сессия", out, "session", "transport", "steer", "turns_taken", "max_turns", "status")
	exactKeys(t, "session", out["session"], "id")
	transport := exactKeys(t, "transport", out["transport"], "type", "sdp")
	if transport["type"] != "webrtc" || out["steer"] != "Turn guidance: greet the learner and lead to p1" ||
		out["turns_taken"] != float64(0) || out["max_turns"] != float64(2) || out["status"] != "active" {
		t.Errorf("живая сессия: %v", out)
	}
	code, out = call(t, h, http.MethodPost, base+"/live", jsonObj{"sdp": testSDP, "voice": "marin"})
	if code != http.StatusBadRequest {
		t.Errorf("настройки сессии с клиента не принимаются: %d %v", code, out)
	}

	// Реплика аватара: перевод; без номера — 422; номер вперёд — 409.
	code, out = call(t, h, http.MethodPost, base+"/avatar",
		jsonObj{"client_msg_id": "a-0", "turn_no": 0, "content": "Исәнме!", "steer_turn": 0})
	if code != http.StatusOK {
		t.Fatalf("реплика аватара: %d %v", code, out)
	}
	exactKeys(t, "реплика аватара", out, "translation")
	if out["translation"] != "Перевод: Исәнме!" {
		t.Errorf("перевод: %v", out)
	}
	if last := engine.lastObserve(); last.SteerTurn == nil || *last.SteerTurn != 0 {
		t.Errorf("steer_turn не дошёл до движка: %+v", last)
	}
	code, out = call(t, h, http.MethodPost, base+"/avatar", jsonObj{"client_msg_id": "a-x", "content": "Сәлам"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("без номера: %d %v", code, out)
	}
	refusal(t, "без номера", out, "validation_failed", "Нужен номер ответа, на который отвечает реплика", "turn_no")
	code, out = call(t, h, http.MethodPost, base+"/avatar",
		jsonObj{"client_msg_id": "a-x", "turn_no": 0, "content": "Сәлам", "steer_turn": -1})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("отрицательный steer_turn: %d %v", code, out)
	}
	refusal(t, "отрицательный steer_turn", out, "validation_failed", "Номер указания — не меньше 0", "steer_turn")
	code, out = call(t, h, http.MethodPost, base+"/avatar", jsonObj{"client_msg_id": "a-x", "turn_no": 1, "content": "Сәлам"})
	if code != http.StatusConflict {
		t.Fatalf("реплика на незаписанный ответ: %d %v", code, out)
	}
	refusal(t, "реплика на незаписанный ответ", out, "conflict", "Ответ ученика с этим номером ещё не записан", "")

	// Перевод не удался — 503 с кодом договора, реплика сохранена.
	engine.failObs = 1
	code, out = call(t, h, http.MethodPost, base+"/avatar",
		jsonObj{"client_msg_id": "a-0b", "turn_no": 0, "content": "Синең исемең ничек?"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("сбой перевода: %d %v", code, out)
	}
	refusal(t, "сбой перевода", out, CodeAvatarUnavailable, "Не удалось перевести реплику аватара — открой подсказку ещё раз", "")

	// Ответ живого режима — указание вместо реплики.
	answer := jsonObj{"client_msg_id": "s-1", "text": "Минем исемем Айдар", "input": "voice",
		"asr_text": "Минем исемем Айдар", "asr_confidence": 0.93, "edited": false, "mode": "live"}
	code, out = call(t, h, http.MethodPost, base+"/answer", answer)
	if code != http.StatusOK {
		t.Fatalf("ответ живого режима: %d %v", code, out)
	}
	exactKeys(t, "ответ живого режима", out, "reply", "translation", "turn_no", "turns_taken", "max_turns", "status",
		"report_status", "steer")
	if out["reply"] != "" || out["translation"] != "" || out["steer"] != "Turn guidance: lead to g1 after answer 1 (call 1)" ||
		out["turn_no"] != float64(1) || out["turns_taken"] != float64(1) {
		t.Errorf("ответ живого режима: %v", out)
	}
	code, again := call(t, h, http.MethodPost, base+"/answer", answer)
	if code != http.StatusOK || again["steer"] != out["steer"] {
		t.Errorf("повтор ответа: %d %v", code, again)
	}

	_, out = call(t, h, http.MethodGet, base, nil)
	if out["pending_answer"] != nil || len(out["turns"].([]any)) != 3 {
		t.Errorf("разговор после ответа: %v", out)
	}

	// Инструменты делегата.
	code, out = call(t, h, http.MethodGet, base+"/progress", nil)
	if code != http.StatusOK {
		t.Fatalf("ход разговора: %d %v", code, out)
	}
	exactKeys(t, "ход разговора", out, "goals", "target_goal", "avoid_phrases", "corrections", "answers_left", "finished")
	exactKeys(t, "цель", first(t, "goals", out["goals"]), "key", "kind", "title", "note", "status")
	exactKeys(t, "верная форма", first(t, "corrections", out["corrections"]), "quote", "correct_form")
	if out["target_goal"] != "p1" || out["answers_left"] != float64(1) || out["finished"] != false {
		t.Errorf("ход разговора: %v", out)
	}
	code, out = call(t, h, http.MethodPost, base+"/check", jsonObj{"text": "Әйбәт! Ә син кайда яшисең?"})
	if code != http.StatusOK {
		t.Fatalf("проверка реплики: %d %v", code, out)
	}
	exactKeys(t, "проверка реплики", out, "ok", "problems")
	if out["ok"] != true || out["problems"] == nil || len(out["problems"].([]any)) != 0 {
		t.Errorf("проверка реплики: %v", out)
	}
	code, out = call(t, h, http.MethodPost, base+"/check", jsonObj{"text": ""})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("пустая реплика: %d %v", code, out)
	}
	refusal(t, "пустая реплика", out, "validation_failed", "Пустая реплика", "text")

	// Конец сессии — 204 без тела, место на ключе освобождено.
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(jsonObj{"session_id": "live-1"})
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, base+"/live/end", bytes.NewReader(body)))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("конец сессии: %d %q", rec.Code, rec.Body.String())
	}
	voice.mu.Lock()
	released := append([]string(nil), voice.released...)
	voice.mu.Unlock()
	if len(released) != 1 || released[0] != "live-1" {
		t.Errorf("освобождено: %v", released)
	}

	// Учителю ручки ученика отвечают 404, как и остальные.
	actor = f.teacher
	code, _ = call(t, h, http.MethodGet, base+"/progress", nil)
	if code != http.StatusNotFound {
		t.Errorf("учитель на ручке ученика: %d", code)
	}
}
