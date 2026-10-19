// Package mlgw — единая точка вызова моделей.
//
// Зачем нужен: один общий семафор на все вызовы моделей позволял тяжёлым
// фоновым вызовам выедать слоты у разговоров учеников в соседних классах.
//
// Здесь пулы независимы и приоритизированы:
//
//	живой урок > llm_chat > llm_build > asr > tts
//
// Приоритет — не рекомендация: при заполненном пуле задача сборки ждёт, а
// реплика чата проходит, потому что у них разные пулы и разные размеры.
//
// Ёмкость каждого пула берётся из окружения (инвариант И-4): переход с
// пилотной нагрузки на целевую — это переменные и число реплик, а не правка кода.
package mlgw

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
)

// Имена пулов. Совпадают со значением колонки ops.pipeline_calls.pool.
const (
	PoolLLMChat  = "llm_chat"
	PoolLLMBuild = "llm_build"
	PoolASR      = "asr"
	PoolTTS      = "tts"
	PoolMedia    = "media"
)

// Pool — ограничитель параллельности одного контура.
type Pool struct {
	name    string
	sem     chan struct{}
	timeout time.Duration

	// Счётчики для метрик и health. Читаются атомарно, поэтому безопасны
	// из любого числа горутин.
	inFlight atomic.Int64
	waiting  atomic.Int64
	rejected atomic.Int64
	total    atomic.Int64
}

// NewPool создаёт пул на size слотов.
func NewPool(name string, size int, timeout time.Duration) *Pool {
	if size < 1 {
		size = 1
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Pool{
		name:    name,
		sem:     make(chan struct{}, size),
		timeout: timeout,
	}
}

// Acquire занимает слот.
//
// Возвращает функцию освобождения и время ожидания в очереди. Ожидание
// возвращается отдельно от времени вызова: по нему видно, упирается ли
// система в квоту провайдера или в саму модель.
//
// Если слот не освободился за timeout, задача отклоняется с честным
// сообщением, а не висит молча до таймаута HTTP-клиента.
func (p *Pool) Acquire(ctx context.Context) (release func(), waited time.Duration, err error) {
	start := time.Now()
	p.waiting.Add(1)
	defer p.waiting.Add(-1)
	if err := ctx.Err(); err != nil {
		p.rejected.Add(1)
		return nil, 0, err
	}

	// Быстрый путь: слот свободен прямо сейчас, таймер не нужен.
	select {
	case p.sem <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-p.sem
			p.rejected.Add(1)
			return nil, 0, err
		}
		p.inFlight.Add(1)
		p.total.Add(1)
		return p.releaseFn(), 0, nil
	default:
	}

	timer := time.NewTimer(p.timeout)
	defer timer.Stop()

	select {
	case p.sem <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-p.sem
			p.rejected.Add(1)
			return nil, time.Since(start), err
		}
		p.inFlight.Add(1)
		p.total.Add(1)
		return p.releaseFn(), time.Since(start), nil

	case <-ctx.Done():
		// Клиент ушёл — не занимаем слот ради никому не нужного ответа.
		p.rejected.Add(1)
		return nil, time.Since(start), ctx.Err()

	case <-timer.C:
		p.rejected.Add(1)
		return nil, time.Since(start), httpx.Unavailable(
			"Сейчас много запросов к модели. Попробуйте через пару минут")
	}
}

func (p *Pool) releaseFn() func() {
	var once atomic.Bool
	return func() {
		// Двойной release освободил бы чужой слот и тихо развалил лимит.
		if once.Swap(true) {
			return
		}
		p.inFlight.Add(-1)
		<-p.sem
	}
}

// Stats — снимок состояния пула для /health и метрик.
type Stats struct {
	Name     string `json:"name"`
	Capacity int    `json:"capacity"`
	InFlight int64  `json:"in_flight"`
	Waiting  int64  `json:"waiting"`
	Rejected int64  `json:"rejected"`
	Total    int64  `json:"total"`
}

// Stats возвращает снимок счётчиков.
func (p *Pool) Stats() Stats {
	return Stats{
		Name:     p.name,
		Capacity: cap(p.sem),
		InFlight: p.inFlight.Load(),
		Waiting:  p.waiting.Load(),
		Rejected: p.rejected.Load(),
		Total:    p.total.Load(),
	}
}

// Saturation — доля занятых слотов от 0 до 1.
// По ней ML-шлюз решает, пора ли включать деградацию (VOICE-8).
func (p *Pool) Saturation() float64 {
	c := float64(cap(p.sem))
	if c == 0 {
		return 1
	}
	return float64(p.inFlight.Load()) / c
}
