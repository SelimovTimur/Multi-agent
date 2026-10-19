package mlgw

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/NATIVE/NATIVE/internal/platform/telemetry"
	"github.com/google/uuid"
)

// Record — одна строка учёта вызова модели.
//
// Пишется на КАЖДЫЙ вызов LLM, ASR и TTS по отдельности, без общего счётчика
// «ИИ-операции»: иначе на вопрос «сколько стоил вот этот урок» ответить нечем,
// а задним числом объёмы не восстановить (NR-41).
type Record struct {
	Billing        []Billing
	OrgID          *uuid.UUID
	UserID         *uuid.UUID
	RefID          *uuid.UUID // урок, попытка или задача — к чему относится вызов
	Pipeline       string     // lesson_build, key_phrases, avatar_chat, asr, tts
	Stage          string     // refine, frank, matching, gap_fill...
	Pool           string
	Provider       string
	Model          string
	TokensIn       int
	TokensOut      int
	QueueMS        int
	DurationMS     int
	CostMicro      int64
	OK             bool
	Error          string
	StartedAt      time.Time
	QualityMetrics json.RawMessage
}

// Recorder принимает записи учёта.
type Recorder interface {
	Record(r Record)
	Close(ctx context.Context) error
}

// NopRecorder ничего не пишет. Используется, пока база не подключена:
// приложение должно работать и без нового контура.
type NopRecorder struct{}

func (NopRecorder) Record(Record)               {}
func (NopRecorder) Close(context.Context) error { return nil }

// PGRecorder пишет учёт в ops.pipeline_calls асинхронно.
//
// Запись НЕ должна удлинять вызов модели и НЕ должна ронять разговор при сбое
// базы. Поэтому записи уходят в буфер, а отдельная горутина сливает их пачками.
// Если буфер полон — запись отбрасывается со счётчиком: потерять строку учёта
// допустимо, задержать ученику ответ аватара — нет.
type PGRecorder struct {
	recordMu sync.RWMutex
	metrics  *telemetry.Metrics
	pool     *pg.Pool
	ch       chan Record
	done     chan struct{}
	wg       sync.WaitGroup
	dropped  atomic.Int64
	closed   atomic.Bool

	batchSize int
	flushIval time.Duration
}

// NewPGRecorder запускает фонового писателя.
func NewPGRecorder(pool *pg.Pool, bufferSize int) *PGRecorder {
	if bufferSize < 64 {
		bufferSize = 1024
	}
	r := &PGRecorder{
		pool:      pool,
		ch:        make(chan Record, bufferSize),
		done:      make(chan struct{}),
		batchSize: 64,
		flushIval: 2 * time.Second,
	}
	r.wg.Add(1)
	go r.loop()
	return r
}

// Record кладёт запись в буфер. Никогда не блокирует.
func (r *PGRecorder) Record(rec Record) {
	r.recordMu.RLock()
	defer r.recordMu.RUnlock()
	if r.closed.Load() {
		if r.metrics != nil {
			r.metrics.Failures.WithLabelValues("billing_closed").Inc()
		}
		return
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = time.Now()
	}
	select {
	case r.ch <- rec:
	default:
		r.dropped.Add(1)
		if r.metrics != nil {
			r.metrics.Failures.WithLabelValues("billing_queue_full").Inc()
		}
	}
}

// Dropped — сколько записей потеряно из-за переполнения буфера.
// Ненулевое значение означает, что база не успевает: это метрика, а не норма.
func (r *PGRecorder) Dropped() int64 { return r.dropped.Load() }

func (r *PGRecorder) loop() {
	defer r.wg.Done()

	batch := make([]Record, 0, r.batchSize)
	ticker := time.NewTicker(r.flushIval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		// Отдельный контекст: слив не должен отменяться вместе с запросом,
		// который породил запись.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		r.write(ctx, batch)
		cancel()
		batch = batch[:0]
	}

	for {
		select {
		case rec, ok := <-r.ch:
			if !ok {
				flush()
				return
			}
			batch = append(batch, rec)
			if len(batch) >= r.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-r.done:
			// Дочитываем то, что уже в буфере, и выходим.
			for {
				select {
				case rec := <-r.ch:
					batch = append(batch, rec)
					if len(batch) >= r.batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (r *PGRecorder) write(ctx context.Context, batch []Record) {
	rows := make([][]any, 0, len(batch))
	for _, rec := range batch {
		metrics := rec.QualityMetrics
		if len(metrics) == 0 {
			metrics = json.RawMessage(`{}`)
		}
		billing := json.RawMessage(`[]`)
		if len(rec.Billing) > 0 {
			billing, _ = json.Marshal(rec.Billing)
		}
		rows = append(rows, []any{
			idgen.New(), rec.OrgID, rec.UserID, rec.Pipeline, rec.Stage, rec.Pool,
			rec.Provider, rec.Model, rec.TokensIn, rec.TokensOut, rec.QueueMS,
			rec.DurationMS, rec.CostMicro, rec.OK, truncate(rec.Error, 512),
			rec.RefID, rec.StartedAt, metrics, billing,
		})
	}

	err := r.copy(ctx, rows)
	if err != nil && len(rows) > 1 {
		// COPY отказывает целиком из-за одной строки. Пишем по одной, чтобы
		// плохая запись не унесла учёт и стоимость соседних вызовов.
		lost := 0
		for _, row := range rows {
			if r.copy(ctx, [][]any{row}) != nil {
				lost++
			}
		}
		if lost == 0 {
			return
		}
		batch, err = batch[:lost], fmt.Errorf("%d строк не записаны по одной: %w", lost, err)
	}
	if err != nil {
		if r.metrics != nil {
			r.metrics.Failures.WithLabelValues("billing_write").Add(float64(len(batch)))
		}
		// Сбой учёта не влияет на пользователя: логируем и идём дальше.
		logx.From(ctx).Error("не удалось записать учёт вызовов моделей",
			"count", len(batch), "err", err)
	}
}

func (r *PGRecorder) copy(ctx context.Context, rows [][]any) error {
	_, err := r.pool.CopyFrom(ctx,
		pgxIdentity{"ops", "pipeline_calls"},
		[]string{"id", "org_id", "user_id", "pipeline", "stage", "pool",
			"provider", "model", "tokens_in", "tokens_out", "queue_ms",
			"duration_ms", "cost_micro", "ok", "error", "ref_id", "started_at", "quality_metrics", "billing"},
		copyRows(rows))
	return err
}

// Close останавливает писателя, слив то, что осталось в буфере.
func (r *PGRecorder) Close(ctx context.Context) error {
	r.recordMu.Lock()
	if r.closed.Swap(true) {
		r.recordMu.Unlock()
		return nil
	}
	close(r.done)
	r.recordMu.Unlock()

	finished := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(finished)
	}()

	select {
	case <-finished:
		if d := r.dropped.Load(); d > 0 {
			logx.From(ctx).Warn("часть записей учёта потеряна из-за переполнения буфера",
				"dropped", d)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// truncate укорачивает текст ошибки до max байт по границе символа.
//
// Резать по байтам нельзя: ошибки по-русски — два байта на букву, и
// обрезок посреди буквы PostgreSQL отвергает вместе со всей пачкой COPY.
// Нулевой байт он не примет тоже.
func truncate(s string, max int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "\uFFFD")
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
