package mlgw

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/NATIVE/NATIVE/internal/platform/telemetry"
	"github.com/prometheus/client_golang/prometheus"
	"strconv"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/google/uuid"
)

// Call описывает, что и для кого вызывается.
//
// Заполняется вызывающим кодом и целиком уходит в учёт: по этим полям потом
// отвечают на вопросы «сколько стоил вот этот урок» и «кто съел квоту».
type Call struct {
	Pipeline string // lesson_build, key_phrases, avatar_chat, asr, tts
	Stage    string // стадия внутри операции
	Pool     string // один из Pool* — какой контур ограничивает вызов
	OrgID    *uuid.UUID
	UserID   *uuid.UUID
	RefID    *uuid.UUID
}

// Usage — расход, который вернул провайдер. Возвращается функцией вызова.
type Usage struct {
	Billing   []Billing
	TokensIn  int
	TokensOut int
	// Provider и Model заполняются вызывающим кодом: у ASR и TTS они другие,
	// чем у LLM, а шлюз не должен знать про конкретные клиенты.
	Provider string
	Model    string
	// Только агрегаты качества, без исходного текста и замечаний аудитора.
	QualityMetrics json.RawMessage
}

// Gateway — единая точка вызова моделей.
//
// Через него проходит КАЖДЫЙ вызов LLM, ASR и TTS: это проверяется тестом
// TestNoDirectModelCalls, который ищет обращения мимо шлюза.
type Gateway struct {
	metrics *telemetry.Metrics
	waits   *prometheus.HistogramVec
	pools   map[string]*Pool
	rec     Recorder
	cfg     config.MLConfig
}

// New собирает шлюз с пулами из конфигурации.
func New(cfg config.MLConfig, rec Recorder) *Gateway {
	if rec == nil {
		rec = NopRecorder{}
	}
	t := cfg.AcquireTimeout
	return &Gateway{
		cfg: cfg,
		rec: rec,
		pools: map[string]*Pool{
			PoolLLMChat:  NewPool(PoolLLMChat, cfg.LLMChatConcurrency, t),
			PoolLLMBuild: NewPool(PoolLLMBuild, cfg.LLMBuildConcurrency, t),
			PoolASR:      NewPool(PoolASR, cfg.ASRConcurrency, t),
			PoolTTS:      NewPool(PoolTTS, cfg.TTSConcurrency, t),
			PoolMedia:    NewPool(PoolMedia, cfg.MediaConcurrency, t),
		},
	}
}

// Pool отдаёт пул по имени. Неизвестное имя — ошибка сборки маршрутов,
// поэтому паникуем: тихо работать без ограничителя хуже, чем упасть в тесте.
func (g *Gateway) Pool(name string) *Pool {
	p, ok := g.pools[name]
	if !ok {
		panic("mlgw: неизвестный пул " + name)
	}
	return p
}

// Do выполняет вызов модели под ограничителем и записывает учёт.
//
// Порядок такой:
//  1. занять слот в пуле (или отказать, если ожидание превысило таймаут);
//  2. выполнить fn;
//  3. записать строку в ops.pipeline_calls — асинхронно, не задерживая ответ.
//
// Ошибка fn возвращается вызывающему как есть: шлюз не подменяет доменные
// ошибки, он только считает и ограничивает.
func (g *Gateway) Do(ctx context.Context, call Call, fn func(context.Context) (Usage, error)) error {
	started := time.Now()
	var finalErr error
	if g.metrics != nil {
		defer func() {
			panicked := recover()
			if panicked != nil {
				finalErr = errors.New("Паника во время операции")
			}
			g.metrics.Observe(call.Pipeline+"."+call.Stage, telemetry.Classify(finalErr), time.Since(started))
			if panicked != nil {
				panic(panicked)
			}
		}()
	}
	pool := g.Pool(call.Pool)

	release, waited, err := pool.Acquire(ctx)
	if g.waits != nil {
		g.waits.WithLabelValues(call.Pool, telemetry.Classify(err)).Observe(waited.Seconds())
	}
	finalErr = err
	if err != nil {
		// Отказ в слоте тоже попадает в учёт: по нему видно, что система
		// упирается в квоту, а не в модель.
		g.rec.Record(Record{
			OrgID: call.OrgID, UserID: call.UserID, RefID: call.RefID,
			Pipeline: call.Pipeline, Stage: call.Stage, Pool: call.Pool,
			QueueMS: int(waited.Milliseconds()), OK: false,
			Error: "не удалось занять слот: " + err.Error(), StartedAt: time.Now(),
		})
		return err
	}
	defer release()

	start := time.Now()
	capture := &billingCapture{}
	ctx = context.WithValue(ctx, billingKey{}, capture)
	usage, callErr := fn(ctx)
	finalErr = callErr
	capture.mu.Lock()
	if capture.received {
		usage.Billing = capture.rows
		usage.TokensIn = 0
		usage.TokensOut = 0
		for _, bill := range capture.rows {
			usage.TokensIn += bill.TokensIn
			usage.TokensOut += bill.TokensOut
		}
	}
	capture.mu.Unlock()
	if len(usage.Billing) == 0 && !capture.received {
		bill := Billing{Provider: usage.Provider, Model: usage.Model, Source: "unknown", Calls: 1, TokensIn: usage.TokensIn, TokensOut: usage.TokensOut}
		if usage.TokensIn+usage.TokensOut > 0 && g.cfg.CostInputMicroPerToken > 0 && g.cfg.CostOutputMicroPerToken > 0 {
			value := strconv.FormatFloat(float64(g.cost(usage))/1e6, 'f', 6, 64)
			bill.Cost = &value
			bill.Source = "estimated"
			bill.Currency = "RUB"
		}
		usage.Billing = []Billing{bill}
	}
	duration := time.Since(start)

	rec := Record{
		OrgID: call.OrgID, UserID: call.UserID, RefID: call.RefID,
		Pipeline: call.Pipeline, Stage: call.Stage, Pool: call.Pool,
		Provider: usage.Provider, Model: usage.Model,
		TokensIn: usage.TokensIn, TokensOut: usage.TokensOut,
		QueueMS: int(waited.Milliseconds()), DurationMS: int(duration.Milliseconds()),
		CostMicro: g.cost(usage), OK: callErr == nil, StartedAt: start,
		QualityMetrics: usage.QualityMetrics, Billing: usage.Billing,
	}
	if callErr != nil {
		rec.Error = callErr.Error()
	}
	g.rec.Record(rec)

	logx.From(ctx).Debug("вызов модели",
		"pipeline", call.Pipeline, "stage", call.Stage, "pool", call.Pool,
		"queue_ms", rec.QueueMS, "duration_ms", rec.DurationMS,
		"tokens_in", usage.TokensIn, "tokens_out", usage.TokensOut,
		"ok", callErr == nil)

	return callErr
}

// cost считает стоимость вызова в миллионных долях рубля.
//
// Ставки берутся из конфигурации: прайс провайдера меняется, и это не повод
// пересобирать бинарник. Отрицательной стоимости не бывает — при нулевых
// ставках учёт всё равно ведётся, просто с нулём в деньгах.
func (g *Gateway) cost(u Usage) int64 {
	return int64(u.TokensIn)*g.cfg.CostInputMicroPerToken +
		int64(u.TokensOut)*g.cfg.CostOutputMicroPerToken
}

// Stats — состояние всех пулов. Отдаётся в /health и в метрики.
func (g *Gateway) Stats() []Stats {
	// Порядок фиксированный: иначе вывод health скачет между запросами и его
	// неудобно сравнивать глазами.
	order := []string{PoolLLMChat, PoolLLMBuild, PoolASR, PoolTTS, PoolMedia}
	out := make([]Stats, 0, len(order))
	for _, name := range order {
		out = append(out, g.pools[name].Stats())
	}
	return out
}

// Уровни деградации голосового контура (VOICE-8).
//
// Переключение автоматическое по загрузке пулов. Гистерезис намеренный:
// пороги подъёма и спуска разные, иначе режим дребезжит на границе.
const (
	DegradeNone      = 0 // полный голос
	DegradeTextFirst = 1 // текст сразу, аудио догоняет
	DegradeTextOnly  = 2 // только текст, микрофон доступен
	DegradeNoAvatar  = 3 // упражнения без аватара
)

// DegradationLevel подсказывает, насколько урезать голосовой контур.
//
// Пока голосовой контур не реализован, метод используется health-эндпоинтом
// как индикатор насыщения. Когда появится dialog-gateway, он будет читать
// именно его.
func (g *Gateway) DegradationLevel() int {
	tts := g.Pool(PoolTTS).Saturation()
	asr := g.Pool(PoolASR).Saturation()
	chat := g.Pool(PoolLLMChat).Saturation()

	switch {
	case chat >= 1.0 && tts >= 1.0 && asr >= 1.0:
		return DegradeNoAvatar
	case asr >= 0.95 || chat >= 0.95:
		return DegradeTextOnly
	case tts >= 0.9:
		return DegradeTextFirst
	default:
		return DegradeNone
	}
}

// Close останавливает учёт, сливая буфер.
func (g *Gateway) Close(ctx context.Context) error { return g.rec.Close(ctx) }
