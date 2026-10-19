package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestStreamingOutcomeAndRouteArePreserved(t *testing.T) {
	m := New()
	r := chi.NewRouter()
	r.Use(m.HTTP)
	r.Get("/lessons/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Fatal("потерян Flusher")
		}
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		Outcome(r.Context(), "error")
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/lessons/private-id", nil))
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range families {
		if f.GetName() != "native_http_requests_total" {
			continue
		}
		for _, metric := range f.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["route"] != "/lessons/{id}" || labels["outcome"] != "error" || labels["status"] != "200" {
				t.Fatalf("неверные метки: %v", labels)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("запрос не учтён")
	}
}

func TestEventsAndCancellationSurviveDetachedContext(t *testing.T) {
	var got Event
	ctx := WithEvents(context.Background(), func(e Event) { got = e })
	Emit(context.WithoutCancel(ctx), Event{Name: "lesson_created"})
	if got.Name != "lesson_created" {
		t.Fatal("потеряно событие после отсоединения контекста")
	}
	if Classify(context.Canceled) != "cancelled" || Classify(context.DeadlineExceeded) != "timeout" {
		t.Fatal("причины отмены смешались")
	}
}
