// Package app собирает приложение.
//
// Здесь и только здесь модули знают друг о друге: iam, mlgw, lessonpipe
// связываются в один граф зависимостей. Сами модули друг на друга не
// ссылаются, кроме как через интерфейсы (например, iam.StudentDirectory).
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/NATIVE/NATIVE/internal/classroom"
	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/content"
	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/learning"
	"github.com/NATIVE/NATIVE/internal/lessonpipe"
	"github.com/NATIVE/NATIVE/internal/live"
	"github.com/NATIVE/NATIVE/internal/migrate"
	"github.com/NATIVE/NATIVE/internal/mlgw"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/NATIVE/NATIVE/internal/platform/telemetry"
	"github.com/NATIVE/NATIVE/internal/service"
	"github.com/NATIVE/NATIVE/internal/speech"
	"github.com/go-chi/chi/v5"
)

// App — собранное приложение.
type App struct {
	Metrics *telemetry.Metrics
	Pool    *pg.Pool
	IAM     *iam.Service
	Guard   *iam.Guard
	Gateway *mlgw.Gateway
	Runner  *lessonpipe.Runner
	Classes *classroom.Service
	Content *content.Service
	Learn   *learning.Service
	Speech  *speech.Service

	iamAPI     *iam.API
	classAPI   *classroom.API
	contentAPI *content.API
	learnAPI   *learning.API
	speechAPI  *speech.API
	liveAPI    *live.API
	log        *slog.Logger
	plat       config.Platform
	stopBg     context.CancelFunc
	bgWg       sync.WaitGroup
	closers    []func(context.Context) error
}

// New поднимает приложение: база, миграции, модули.
//
// Возвращает ошибку, если что-то из обязательного не поднялось. Работать
// «наполовину» приложение не должно: первые же запросы упадут у пользователя.
//
// legacy нужен ради клиентов ASR, ffmpeg и синтеза: их настройки (адреса
// сервисов, лимиты) живут в Config.
func New(ctx context.Context, plat config.Platform, legacy *config.Config, log *slog.Logger) (*App, error) {
	// Часовой пояс школы разбирается до всего остального: опечатка в
	// SCHOOL_TZ сдвинула бы все сроки выдач.
	schoolZone, err := plat.SchoolLocation()
	if err != nil {
		return nil, err
	}

	metrics := telemetry.New()
	plat.DB.Tracer = metrics
	pool, err := pg.Connect(ctx, plat.DB)
	if err != nil {
		return nil, err
	}

	if err := migrate.Ensure(ctx, pool, plat.MigrateOnStart, plat.RequireDBVersion, log); err != nil {
		pool.Close()
		return nil, err
	}

	recorder := mlgw.NewPGRecorder(pool, 4096).WithMetrics(metrics)
	gateway := mlgw.New(plat.ML, recorder).WithMetrics(metrics)

	mailer, err := buildMailer(plat, log)
	if err != nil {
		_ = gateway.Close(ctx)
		pool.Close()
		return nil, err
	}

	iamSvc, err := iam.NewService(ctx, pool, plat.Auth, mailer)
	if err != nil {
		_ = gateway.Close(ctx)
		pool.Close()
		return nil, err
	}

	guard := iam.NewGuard(iamSvc, plat.Auth)
	// К моделям приложение ходит через llm_service: движок разговора и
	// указания уровня живут там.
	runner := lessonpipe.NewRunner(lessonpipe.NewClient(plat.LLMServiceURL, plat.LLMTimeout), gateway)
	iamAPI := iam.NewAPI(iamSvc, guard, plat.Auth)

	// classroom знает об iam только через интерфейс StudentDirectory.
	classSvc := classroom.NewService(pool, iamSvc)
	contentSvc := content.NewService(pool)
	// Голосовой разговор: сессия GPT Live создаётся здесь, звук идёт мимо
	// сервера. Заводится до learning — разговор урока идёт через него.
	liveAPI := live.NewAPI(legacy.OpenAIAPIKey, guard).
		WithGateway(gateway).
		WithMonitor(live.NewMonitor(pool))
	// learning знает о классах и уроках только через интерфейсы —
	// адаптеры лежат в adapters.go.
	learnSvc := learning.NewService(pool,
		classAdapter{svc: classSvc}, lessonAdapter{svc: contentSvc}).
		WithMaterial(materialAdapter{svc: contentSvc}).
		WithConversation(conversationAdapter{runner: runner}).
		WithLiveVoice(liveAdapter{api: liveAPI}).
		WithLevelGuide(newLevelGuide(runner)).
		WithSchoolZone(schoolZone)
	// Библиотека показывает, в какие классы выдан урок. Подключается после
	// learning: зависимость взаимная.
	contentSvc.WithAssignments(assignmentsAdapter{svc: learnSvc})

	// Голосовой ввод ученика: распознавание и синтез через пулы шлюза.
	speechSvc := speech.NewService(
		service.NewASRService(legacy), service.NewAudioService(legacy.MaxAudioSec),
		service.NewTTSService(legacy), gateway, "").
		WithVoiceStyle(voiceStyleAdapter{svc: learnSvc})

	a := &App{
		Metrics: metrics,
		Pool:    pool,
		IAM:     iamSvc,
		Guard:   guard,
		Gateway: gateway,
		Runner:  runner,
		Classes: classSvc,
		Content: contentSvc,
		Learn:   learnSvc,
		Speech:  speechSvc,

		iamAPI:     iamAPI,
		classAPI:   classroom.NewAPI(classSvc, guard),
		contentAPI: content.NewAPI(contentSvc, guard),
		learnAPI:   learning.NewAPI(learnSvc, guard),
		speechAPI:  speech.NewAPI(speechSvc, guard),
		liveAPI:    liveAPI,
		log:        log,
		plat:       plat,
	}
	a.closers = append(a.closers, gateway.Close)

	a.startBackground()
	return a, nil
}

// buildMailer выбирает почтальона по настройкам.
//
// Настроен SMTP — отправляем письма. Не настроен — в dev печатаем ссылку в
// лог, а на проде отказываемся стартовать.
//
// Отказ жёсткий намеренно: без писем регистрация превращается в тупик —
// аккаунт создаётся неактивным, а ссылку активации получить неоткуда. Такое
// лучше увидеть при выкате, чем в поддержке через неделю.
func buildMailer(plat config.Platform, log *slog.Logger) (iam.Mailer, error) {
	if plat.Mail.Configured() {
		log.Info("почта настроена", "smtp", plat.Mail.Host, "from", plat.Mail.From)
		return iam.NewSMTPMailer(plat.Mail, plat.PublicURL), nil
	}

	if plat.Env == "prod" || plat.Env == "production" {
		return nil, fmt.Errorf(
			"почта не настроена: задайте SMTP_HOST и SMTP_FROM. " +
				"Без них учитель не получит ссылку подтверждения и не сможет войти")
	}

	log.Warn("почта не настроена: ссылки подтверждения будут только в логе. " +
		"Для настоящей отправки задайте SMTP_HOST, SMTP_FROM, SMTP_USERNAME, SMTP_PASSWORD")
	return iam.LogMailer{PublicURL: plat.PublicURL}, nil
}

// Mount вешает маршруты нового контура и проверяет, что каждый объявил право.
//
// Проверка обязательна: маршрут без объявленного права — это ручка, о доступе
// к которой никто не подумал, и узнавать об этом на проде поздно (NR-10).
func (a *App) Mount(r chi.Router) error {
	var sub chi.Router

	r.Route("/api/v1", func(v1 chi.Router) {
		sub = v1
		// Порядок важен: сначала определяем адрес клиента, потом всё
		// остальное — счётчики попыток опираются на него.
		v1.Use(httpx.RealIP(a.plat.TrustedProxies))
		v1.Use(httpx.RequestContext(a.log))
		v1.Use(a.Guard.Authenticate)
		v1.Use(a.Guard.AuthenticateStudent)
		v1.Use(a.studentPresence)
		v1.Use(iam.RequireWritable)

		a.iamAPI.Mount(v1)
		a.classAPI.Mount(v1)
		a.contentAPI.Mount(v1)
		a.learnAPI.Mount(v1)
		a.speechAPI.Mount(v1)
		a.liveAPI.Mount(v1)
	})

	if err := a.Guard.Verify(sub); err != nil {
		return err
	}
	return nil
}

// studentPresence отмечает, что ученик активен, — на любой его ручке.
//
// «В сети» у учителя — это «был активен за последние 2 минуты», и
// активность — не только урок: класс, разговор, распознавание речи. Поэтому
// отметка стоит здесь, на входе в /api/v1, а не в обработчиках learning.
// В базу пишется не чаще раза в 20 секунд на ученика (см.
// learning.Store.TouchPresence), так что опрос раз в 5 секунд её не нагружает.
func (a *App) studentPresence(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actor, ok := iam.ActorFrom(r.Context()); ok && actor.IsStudent() {
			a.Learn.SeenStudent(r.Context(), actor)
		}
		next.ServeHTTP(w, r)
	})
}

// Health — состояние контура для /health и мониторинга.
func (a *App) Health(ctx context.Context) map[string]any {
	out := map[string]any{
		"database":    "ok",
		"ml_pools":    a.Gateway.Stats(),
		"degradation": a.Gateway.DegradationLevel(),
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := a.Pool.Ping(pingCtx); err != nil {
		out["database"] = "unavailable"
	}
	return out
}

// startBackground запускает периодические задачи.
//
// Обе задачи обязательны для инварианта И-5 «ничего не растёт неограниченно»:
// без чистки растут таблицы сессий и попыток входа, без создания секций
// вперёд однажды не окажется секции на завтра.
func (a *App) startBackground() {
	ctx, cancel := context.WithCancel(context.Background())
	a.stopBg = cancel

	a.bgWg.Add(1)
	go func() {
		defer a.bgWg.Done()
		a.loop(ctx, "чистка iam", time.Hour, func(ctx context.Context) error {
			return a.IAM.Housekeeping(ctx)
		})
	}()

	a.bgWg.Add(1)
	go func() {
		defer a.bgWg.Done()
		// Раз в минуту: приёмка NR-34 требует точности до минуты, а урок,
		// открывшийся на минуту позже обещанного, — это сорванное занятие.
		a.loop(ctx, "состояния назначений", time.Minute, a.Learn.SyncStates)
	}()

	a.bgWg.Add(1)
	go func() {
		defer a.bgWg.Done()
		// Секции создаются на четыре недели вперёд; проверка раз в сутки
		// оставляет огромный запас на случай простоя задачи.
		a.loop(ctx, "секции таблиц событий", 24*time.Hour, func(ctx context.Context) error {
			return EnsurePartitions(ctx, a.Pool)
		})
	}()

}

// loop выполняет задачу сразу и далее по интервалу.
func (a *App) loop(ctx context.Context, name string, every time.Duration, fn func(context.Context) error) {
	run := func() {
		taskCtx, cancel := context.WithTimeout(logx.Into(ctx, a.log), 5*time.Minute)
		defer cancel()
		started := time.Now()
		err := fn(taskCtx)
		a.Metrics.Observe("background."+name, telemetry.Classify(err), time.Since(started))
		if err == nil {
			a.Metrics.BackgroundLastSuccess.WithLabelValues(name).SetToCurrentTime()
		}
		if err != nil {
			a.log.Error("фоновая задача завершилась ошибкой", "task", name, "err", err)
		}
	}

	run()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// Close останавливает фоновые задачи, сливает учёт и закрывает пул.
func (a *App) Close(ctx context.Context) error {
	if a.stopBg != nil {
		a.stopBg()
	}
	// Ждём фоновые задачи, но не дольше отпущенного на остановку времени:
	// зависшая задача не должна мешать завершению процесса.
	done := make(chan struct{})
	go func() {
		a.bgWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		a.log.Warn("фоновые задачи не успели завершиться")
	}
	// Разборы разговоров пишут отчёт в базу — ждём их до закрытия пула.
	// Не дождались — аренда в базе истечёт, и разбор перезапустится.
	if err := a.Learn.WaitReports(ctx); err != nil {
		a.log.Warn("разборы разговоров не успели завершиться — перезапустятся по аренде")
	}

	var firstErr error
	for _, c := range a.closers {
		if err := c(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	a.Pool.Close()
	return firstErr
}

// EnsurePartitions создаёт недельные секции вперёд для таблиц событий.
//
// Идемпотентно: повторный вызов не трогает уже созданные секции.
func EnsurePartitions(ctx context.Context, pool *pg.Pool) error {
	targets := []struct{ schema, table string }{
		{"learning", "answer_events"},
		{"learning", "dialog_turns"},
		{"ops", "pipeline_calls"},
	}
	for _, t := range targets {
		var created int
		err := pool.QueryRow(ctx,
			`SELECT public.ensure_weekly_partitions($1, $2, current_date, 6)`,
			t.schema, t.table).Scan(&created)
		if err != nil {
			return fmt.Errorf("app: не удалось создать секции для %s.%s: %w", t.schema, t.table, err)
		}
		if created > 0 {
			logx.From(ctx).Info("созданы секции таблицы",
				"table", t.schema+"."+t.table, "created", created)
		}
	}
	return nil
}
