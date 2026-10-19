package telemetry

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type queryKey struct{}
type queryTiming struct {
	started time.Time
	command string
}

func (m *Metrics) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	command := "other"
	fields := strings.Fields(data.SQL)
	if len(fields) > 0 {
		switch strings.ToUpper(fields[0]) {
		case "SELECT", "INSERT", "UPDATE", "DELETE", "BEGIN", "COMMIT", "ROLLBACK":
			command = strings.ToUpper(fields[0])
		}
	}
	return context.WithValue(ctx, queryKey{}, queryTiming{time.Now(), command})
}

func (m *Metrics) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	timing, ok := ctx.Value(queryKey{}).(queryTiming)
	if !ok {
		return
	}
	state := "00"
	if data.Err != nil {
		state = "other"
		var pgErr *pgconn.PgError
		if errors.As(data.Err, &pgErr) && len(pgErr.Code) == 5 {
			state = pgErr.Code[:2]
		}
	}
	m.QueryDuration.WithLabelValues(timing.command, Classify(data.Err)).Observe(time.Since(timing.started).Seconds())
	m.QueryResults.WithLabelValues(timing.command, state).Inc()
}
