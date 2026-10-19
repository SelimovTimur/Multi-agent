// Package telemetry задаёт общий контракт наблюдаемости без зависимостей от доменов.
package telemetry

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Event struct {
	Name         string
	Recipients   []uuid.UUID
	TaskKeys     []string
	EntityID     uuid.UUID
	LessonID     uuid.UUID
	ClassID      uuid.UUID
	AssignmentID uuid.UUID
	OwnerID      uuid.UUID
	Key          string
}

type requestState struct {
	mu      sync.Mutex
	outcome string
	emit    func(Event)
}
type stateKey struct{}
type metricsKey struct{}

type Metrics struct {
	Registry              *prometheus.Registry
	QueryDuration         *prometheus.HistogramVec
	QueryResults          *prometheus.CounterVec
	Requests              *prometheus.CounterVec
	Duration              *prometheus.HistogramVec
	Inflight              prometheus.Gauge
	Operations            *prometheus.CounterVec
	OperationDuration     *prometheus.HistogramVec
	Failures              *prometheus.CounterVec
	BackgroundLastSuccess *prometheus.GaugeVec
	Frontend              *prometheus.CounterVec
	FrontendDuration      *prometheus.HistogramVec
}

func New() *Metrics {
	m := &Metrics{Registry: prometheus.NewRegistry()}
	m.QueryDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "native_db_query_duration_seconds", Help: "Время SQL-запросов без текста и параметров.", Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30}}, []string{"command", "outcome"})
	m.QueryResults = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "native_db_queries_total", Help: "Результаты SQL-запросов по классу SQLSTATE."}, []string{"command", "sqlstate_class"})
	m.Requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "native_http_requests_total", Help: "Завершённые HTTP-запросы."}, []string{"method", "route", "status", "outcome"})
	m.Duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "native_http_request_duration_seconds", Help: "Время HTTP-запроса, включая передачу потока.", Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30, 60, 180, 600, 1200}}, []string{"method", "route"})
	m.Inflight = prometheus.NewGauge(prometheus.GaugeOpts{Name: "native_http_requests_inflight", Help: "Одновременно выполняющиеся API-запросы без heartbeat и health."})
	m.Operations = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "native_operations_total", Help: "Результаты логических операций и этапов."}, []string{"operation", "outcome"})
	m.OperationDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "native_operation_duration_seconds", Help: "Длительность логических операций и этапов.", Buckets: []float64{.01, .05, .1, .25, .5, 1, 2, 5, 10, 30, 60, 120, 300, 600, 1200}}, []string{"operation", "outcome"})
	m.Failures = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "native_telemetry_lost_total", Help: "Потерянные записи наблюдаемости по причине."}, []string{"reason"})
	m.BackgroundLastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "native_background_last_success_timestamp_seconds", Help: "Время последнего успешного выполнения фоновой задачи."}, []string{"task"})
	m.Frontend = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "native_frontend_events_total", Help: "События браузера без текста ошибки и персональных данных."}, []string{"role", "kind"})
	m.FrontendDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "native_frontend_request_duration_seconds", Help: "Время API-запроса глазами браузера.", Buckets: []float64{.1, .25, .5, 1, 2, 5, 10, 30, 60, 180, 600, 1200}}, []string{"role", "outcome"})
	m.Registry.MustRegister(m.QueryDuration, m.QueryResults, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}), m.Requests, m.Duration, m.Inflight, m.Operations, m.OperationDuration, m.Failures, m.BackgroundLastSuccess, m.Frontend, m.FrontendDuration)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Timeout: 10 * time.Second})
}

func (m *Metrics) Context(ctx context.Context) context.Context {
	return context.WithValue(ctx, metricsKey{}, m)
}

func WithEvents(ctx context.Context, emit func(Event)) context.Context {
	if state, ok := ctx.Value(stateKey{}).(*requestState); ok {
		state.mu.Lock()
		state.emit = emit
		state.mu.Unlock()
		return ctx
	}
	return context.WithValue(ctx, stateKey{}, &requestState{emit: emit})
}

func Emit(ctx context.Context, event Event) {
	if state, ok := ctx.Value(stateKey{}).(*requestState); ok {
		state.mu.Lock()
		emit := state.emit
		state.mu.Unlock()
		if emit != nil {
			emit(event)
		}
	}
}

// Outcome позволяет сообщить ошибку после HTTP 200, когда заголовки SSE уже ушли.
func Outcome(ctx context.Context, outcome string) {
	if state, ok := ctx.Value(stateKey{}).(*requestState); ok {
		state.mu.Lock()
		state.outcome = outcome
		state.mu.Unlock()
	}
}

func Classify(err error) string {
	if err == nil {
		return "success"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "error"
}

func Start(ctx context.Context, operation string) func(error) {
	m, _ := ctx.Value(metricsKey{}).(*Metrics)
	if m == nil {
		return func(error) {}
	}
	started := time.Now()
	return func(err error) { m.Observe(operation, Classify(err), time.Since(started)) }
}

func (m *Metrics) Observe(operation, outcome string, duration time.Duration) {
	m.Operations.WithLabelValues(operation, outcome).Inc()
	m.OperationDuration.WithLabelValues(operation, outcome).Observe(duration.Seconds())
}

func (m *Metrics) HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/telemetry/presence" || r.URL.Path == "/api/v1/telemetry/browser" || r.URL.Path == "/health" || r.URL.Path == "/health/ready" {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		method := r.Method
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE":
		default:
			method = "OTHER"
		}
		state := &requestState{}
		r = r.WithContext(context.WithValue(m.Context(r.Context()), stateKey{}, state))
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		m.Inflight.Inc()
		defer func() {
			panicked := recover()
			m.Inflight.Dec()
			route := "unmatched"
			if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
				route = rc.RoutePattern()
			}
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			outcome := "success"
			if status >= 500 {
				outcome = "error"
			} else if status >= 400 {
				outcome = "rejected"
			}
			if r.Context().Err() != nil {
				outcome = Classify(r.Context().Err())
			}
			state.mu.Lock()
			if state.outcome != "" {
				outcome = state.outcome
			}
			state.mu.Unlock()
			if panicked != nil {
				status = 500
				outcome = "error"
			}
			m.Requests.WithLabelValues(method, route, strconv.Itoa(status), outcome).Inc()
			m.Duration.WithLabelValues(method, route).Observe(time.Since(started).Seconds())
			m.Observe(method+" "+route, outcome, time.Since(started))
			if panicked != nil {
				panic(panicked)
			}
		}()
		next.ServeHTTP(ww, r)
	})
}
