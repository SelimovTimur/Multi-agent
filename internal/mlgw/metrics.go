package mlgw

import (
	"github.com/NATIVE/NATIVE/internal/platform/telemetry"
	"github.com/prometheus/client_golang/prometheus"
)

func (g *Gateway) WithMetrics(m *telemetry.Metrics) *Gateway {
	g.metrics = m
	g.waits = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "native_ml_queue_duration_seconds", Help: "Ожидание слота сервиса обработки.", Buckets: []float64{.001, .01, .05, .1, .5, 1, 2, 5, 10, 30, 60, 120}}, []string{"pool", "outcome"})
	m.Registry.MustRegister(g.waits)
	for name, pool := range g.pools {
		for metric, fn := range map[string]func() float64{
			"native_ml_pool_capacity": func() float64 { return float64(pool.Stats().Capacity) },
			"native_ml_pool_inflight": func() float64 { return float64(pool.Stats().InFlight) },
			"native_ml_pool_waiting":  func() float64 { return float64(pool.Stats().Waiting) },
		} {
			m.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: metric, Help: "Состояние ограничителя параллельности.", ConstLabels: prometheus.Labels{"pool": name}}, fn))
		}
	}
	return g
}

func (r *PGRecorder) WithMetrics(m *telemetry.Metrics) *PGRecorder { r.metrics = m; return r }
