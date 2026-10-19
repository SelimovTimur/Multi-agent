// Package logx — структурное логирование поверх log/slog.
//
// Одно правило: в логе всегда есть request_id. По нему поддержка находит
// инцидент, а тот же идентификатор уходит клиенту в теле ошибки (см. httpx).
package logx

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyLogger
)

// New собирает логгер. format: "json" для прода, "text" для локальной работы.
func New(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if strings.EqualFold(format, "text") {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// WithRequestID кладёт идентификатор запроса в контекст.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// RequestID достаёт идентификатор запроса; пустая строка, если его нет.
func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestID).(string)
	return v
}

// Into кладёт логгер в контекст, чтобы слои ниже не таскали его параметром.
func Into(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKeyLogger, l)
}

// From достаёт логгер из контекста и дописывает request_id.
// Если логгера нет, возвращает slog.Default() — вызывающий код не обязан
// проверять nil.
func From(ctx context.Context) *slog.Logger {
	l, _ := ctx.Value(ctxKeyLogger).(*slog.Logger)
	if l == nil {
		l = slog.Default()
	}
	if id := RequestID(ctx); id != "" {
		l = l.With("request_id", id)
	}
	return l
}
