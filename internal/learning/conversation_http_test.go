package learning

// Форма ответов разговора по целям — дословно по договору
// docs/product/avatar-goals/contract.md, раздел 6: по нему параллельно
// пишутся экраны ученика и учителя. Здесь проверяются имена полей, коды и
// тексты отказов, а не арифметика — она в остальных тестах.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/go-chi/chi/v5"
)

func conversationRouter(api *API, actor *iam.Actor) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(iam.WithActor(req.Context(), *actor)))
		})
	})
	r.Get("/my/attempts/{attemptID}/conversation", api.myConversation)
	r.Post("/my/attempts/{attemptID}/conversation/start", api.conversationStart)
	r.Post("/my/attempts/{attemptID}/conversation/answer", api.conversationAnswer)
	r.Post("/my/attempts/{attemptID}/conversation/incident", api.conversationIncident)
	r.Get("/my/attempts/{attemptID}/conversation/summary", api.conversationSummary)
	r.Post("/my/attempts/{attemptID}/conversation/live", api.conversationLive)
	r.Post("/my/attempts/{attemptID}/conversation/live/end", api.conversationLiveEnd)
	r.Post("/my/attempts/{attemptID}/conversation/avatar", api.conversationAvatar)
	r.Get("/my/attempts/{attemptID}/conversation/progress", api.conversationProgress)
	r.Post("/my/attempts/{attemptID}/conversation/check", api.conversationCheck)
	r.Get("/assignments/{assignmentID}/conversation", api.classConversation)
	r.Get("/assignments/{assignmentID}/students/{studentID}/conversation", api.studentConversation)
	return r
}

// exactKeys — у объекта ровно эти поля: лишнее поле в ответе ученика — это
// то, что договор ему не обещал, а может быть и то, чего ему видеть нельзя.
func exactKeys(t *testing.T, what string, obj any, keys ...string) jsonObj {
	t.Helper()
	m, ok := obj.(jsonObj)
	if !ok {
		t.Fatalf("%s: не объект: %#v", what, obj)
	}
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	want := append([]string(nil), keys...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s: поля %v, ожидали %v", what, got, want)
	}
	return m
}

func TestConversationHTTPContract(t *testing.T) {
	f, engine, a := conversationFixture(t, 2)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	actor := aidar
	h := conversationRouter(&API{svc: f.svc}, &actor)
	base := "/my/attempts/" + attempt.ID.String() + "/conversation"

	code, out := call(t, h, http.MethodGet, base, nil)
	if code != http.StatusOK {
		t.Fatalf("GET разговора: %d %v", code, out)
	}
	exactKeys(t, "разговор", out, "enabled", "language", "level", "status", "max_turns", "turns_taken",
		"turns", "pending_answer", "report_status", "dictionary_enabled", "phrases", "live_available")
	if out["status"] != "not_started" || out["pending_answer"] != nil || out["report_status"] != "" || out["level"] != "a2" {
		t.Errorf("разговор до начала: %v", out)
	}
	// Голос GPT Live в этом сервисе не подключён — живого режима нет.
	if out["live_available"] != false {
		t.Errorf("live_available без голосового контура: %v", out["live_available"])
	}

	code, out = call(t, h, http.MethodPost, base+"/start", nil)
	if code != http.StatusOK {
		t.Fatalf("start: %d %v", code, out)
	}
	exactKeys(t, "start", out, "reply", "translation", "turn_no", "turns_taken", "max_turns", "status")
	if out["turn_no"] != float64(0) || out["status"] != "active" {
		t.Errorf("start: %v", out)
	}

	// Сбой модели — 503 с кодом договора и понятным сообщением.
	engine.failTurns[1] = 1
	answer := jsonObj{"client_msg_id": "c-1", "text": "Минем исемем Алсу", "input": "voice",
		"asr_text": "Минем исемем Алсу", "asr_confidence": 0.71, "edited": false}
	code, out = call(t, h, http.MethodPost, base+"/answer", answer)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("сбой модели: %d %v", code, out)
	}
	refusal(t, "сбой модели", out, CodeAvatarUnavailable,
		"Аватар не смог ответить. Твой ответ сохранён — нажми «Повторить»", "")

	_, out = call(t, h, http.MethodGet, base, nil)
	exactKeys(t, "pending_answer", out["pending_answer"], "client_msg_id", "turn_no", "text")
	turn := first(t, "реплики", out["turns"])
	exactKeys(t, "реплика", turn, "turn_no", "role", "content", "translation", "input", "edited")

	code, out = call(t, h, http.MethodPost, base+"/answer", answer)
	if code != http.StatusOK {
		t.Fatalf("повтор ответа: %d %v", code, out)
	}
	exactKeys(t, "answer", out, "reply", "translation", "turn_no", "turns_taken", "max_turns", "status", "report_status")
	if out["turn_no"] != float64(1) || out["turns_taken"] != float64(1) {
		t.Errorf("повтор ответа: %v", out)
	}

	// Неизвестное поле — 400: опечатка в имени поля видна сразу.
	code, _ = call(t, h, http.MethodPost, base+"/answer", jsonObj{"client_msg_id": "c-2", "text": "Әйе", "speaker": "voice"})
	if code != http.StatusBadRequest {
		t.Errorf("неизвестное поле: %d", code)
	}
	// Режим ответа — только live или без поля.
	code, out = call(t, h, http.MethodPost, base+"/answer", jsonObj{"client_msg_id": "c-2", "text": "Әйе", "mode": "voice"})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("неизвестный режим: %d %v", code, out)
	}
	refusal(t, "неизвестный режим", out, "validation_failed", "Режим ответа — live или без поля", "mode")

	rec := httptest.NewRecorder()
	body, _ := json.Marshal(jsonObj{"kind": "tts", "turn_no": 1, "message": "NotAllowedError"})
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, base+"/incident", bytes.NewReader(body)))
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("сбой озвучки: %d %q", rec.Code, rec.Body.String())
	}

	code, out = call(t, h, http.MethodGet, base+"/summary", nil)
	if code != http.StatusOK {
		t.Fatalf("итог: %d %v", code, out)
	}
	exactKeys(t, "итог до закрытия", out, "status", "summary")
	if out["status"] != "not_closed" || out["summary"] != nil {
		t.Errorf("итог до закрытия: %v", out)
	}

	code, out = call(t, h, http.MethodPost, base+"/answer",
		jsonObj{"client_msg_id": "c-2", "text": "Мин Казанга яшим", "input": "text"})
	if code != http.StatusOK || out["status"] != "closed" {
		t.Fatalf("последний ответ: %d %v", code, out)
	}
	code, out = call(t, h, http.MethodPost, base+"/answer", jsonObj{"client_msg_id": "c-3", "text": "Тагын"})
	if code != http.StatusConflict {
		t.Fatalf("после закрытия: %d %v", code, out)
	}
	refusal(t, "после закрытия", out, "conflict", "Разговор закончен", "")

	f.waitReports(t)
	_, out = call(t, h, http.MethodGet, base+"/summary", nil)
	if out["status"] != "ready" {
		t.Fatalf("итог: %v", out)
	}
	summary := exactKeys(t, "итог", out["summary"], "credited_phrases", "applied_themes", "phrases_total",
		"themes_total", "next_time", "incidents")
	exactKeys(t, "next_time", summary["next_time"], "text_ru", "example")
	exactKeys(t, "зачтённая фраза", first(t, "credited_phrases", summary["credited_phrases"]), "key", "title")
	exactKeys(t, "сбои", first(t, "incidents", summary["incidents"]), "kind", "count")
	raw, _ := json.Marshal(out)
	for _, leak := range []string{"СЕКРЕТ", "general_errors", "marks", "above_level", "Казанга"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("в итоге ученика %q: %s", leak, raw)
		}
	}

	// Учитель.
	actor = f.teacher
	code, out = call(t, h, http.MethodGet, "/assignments/"+a.ID.String()+"/conversation", nil)
	if code != http.StatusOK {
		t.Fatalf("отчёт класса: %d %v", code, out)
	}
	exactKeys(t, "отчёт класса", out, "enabled", "level", "max_turns", "goals", "counts", "students", "themes", "common_errors")
	exactKeys(t, "цель", first(t, "goals", out["goals"]), "key", "kind", "title", "note")
	exactKeys(t, "счётчики", out["counts"], "not_started", "in_progress", "completed")
	st := exactKeys(t, "ученик", first(t, "students", out["students"]), "student", "status", "turns_taken",
		"report_status", "credited", "goals_total", "errors", "misses")
	ref := exactKeys(t, "ученик в отчёте", st["student"], "id", "name", "tag")
	if ref["tag"] != "KZ4F" || st["status"] != "completed" {
		t.Errorf("ученик: %v", st)
	}
	th := exactKeys(t, "тема", first(t, "themes", out["themes"]), "key", "kind", "title", "correct_uses", "errors",
		"credited_students", "students")
	exactKeys(t, "ученик в теме", first(t, "students", th["students"]), "student", "correct_uses", "errors", "misses")
	ce := exactKeys(t, "общая ошибка", first(t, "common_errors", out["common_errors"]), "group_key", "label", "type",
		"level", "count", "students")
	es := exactKeys(t, "ученик в ошибке", first(t, "students", ce["students"]), "student", "quotes")
	exactKeys(t, "цитата", first(t, "quotes", es["quotes"]), "turn_no", "quote", "correct_form")

	code, out = call(t, h, http.MethodGet,
		"/assignments/"+a.ID.String()+"/students/"+aidar.StudentID.String()+"/conversation", nil)
	if code != http.StatusOK {
		t.Fatalf("карточка ученика: %d %v", code, out)
	}
	exactKeys(t, "карточка", out, "student", "status", "turns_taken", "max_turns", "report_status", "report")
	report := exactKeys(t, "отчёт", out["report"], "closed_at", "level", "goals", "turns", "marks", "general_errors",
		"above_level_count", "speech_level", "incidents")
	exactKeys(t, "цель отчёта", first(t, "goals", report["goals"]), "key", "kind", "title", "note", "credited",
		"credited_turn", "correct_uses", "errors", "misses")
	exactKeys(t, "реплика отчёта", first(t, "turns", report["turns"]), "turn_no", "role", "content", "translation",
		"input", "edited", "asr_text")
	exactKeys(t, "отметка", first(t, "marks", report["marks"]), "turn_no", "goal_key", "verdict", "error_kind",
		"quote", "correct_form", "note_ru")
	ge := exactKeys(t, "общая ошибка ученика", first(t, "general_errors", report["general_errors"]), "turn_no", "quote",
		"type", "correct_form", "rule_ru", "level", "level_source", "visibility", "group_key", "group_label")
	if ge["visibility"] == "above_level" || report["above_level_count"] != float64(1) {
		t.Errorf("ошибки выше уровня: %v / %v", ge, report["above_level_count"])
	}

	code, out = call(t, h, http.MethodGet,
		"/assignments/"+a.ID.String()+"/students/"+f.students[1].StudentID.String()+"/conversation", nil)
	if code != http.StatusOK || out["report"] != nil || out["status"] != "not_started" {
		t.Errorf("не приступавший: %d %v", code, out)
	}
}
