package live

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/google/uuid"
)

type recordedEvents struct{ events []Event }

func (r *recordedEvents) Record(_ context.Context, event Event) error {
	r.events = append(r.events, event)
	return nil
}

func TestLiveEventRejectsConversationContent(t *testing.T) {
	recorder := &recordedEvents{}
	a := NewAPI("key", nil).WithMonitor(recorder)
	actor := iam.Actor{Kind: "student", OrgID: uuid.New(), StudentID: uuid.New()}
	req := httptest.NewRequest(http.MethodPost, "/live/events", strings.NewReader(`{
"stage":"connect","result":"failed","protocol":"http/1.1","network":"offline","content":"секретная реплика"}`))
	req = req.WithContext(iam.WithActor(req.Context(), actor))
	w := httptest.NewRecorder()

	a.event(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("статус = %d, ответ: %s", w.Code, w.Body.String())
	}
	if len(recorder.events) != 0 {
		t.Fatalf("в журнал попало содержимое разговора: %+v", recorder.events)
	}

	req = httptest.NewRequest(http.MethodPost, "/live/events", strings.NewReader(`
{"stage":"connect","result":"failed","protocol":"http/1.1","network":"offline"}`))
	req = req.WithContext(iam.WithActor(req.Context(), actor))
	w = httptest.NewRecorder()
	a.event(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("техническое событие: статус = %d, ответ: %s", w.Code, w.Body.String())
	}
	if len(recorder.events) != 1 {
		t.Fatalf("событий = %d, ожидалось 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.OrgID != actor.OrgID || event.ActorID != actor.StudentID || event.ActorRole != "student" {
		t.Fatalf("контекст события: %+v", event)
	}
	if event.Stage != "connect" || event.Result != "failed" || event.Protocol != "http/1.1" || event.Network != "offline" {
		t.Fatalf("технические признаки: %+v", event)
	}
	if _, err := liveEvent(actor, "connect", "failed", "h2", "секретная реплика"); err == nil {
		t.Fatal("текст не должен маскироваться под признак сети")
	}
	if _, err := liveEvent(actor, "connect", "failed", "секретная реплика", "online"); err == nil {
		t.Fatal("текст не должен маскироваться под протокол страницы")
	}
	if event, err := liveEvent(actor, "connect", "failed", "", "online"); err != nil || event.Protocol != "" {
		t.Fatalf("браузер без Navigation Timing: %+v, %v", event, err)
	}
}
