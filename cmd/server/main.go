package main

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NATIVE/NATIVE/internal/app"
	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/go-chi/chi/v5"
)

func main() {
	// Адреса внутренних сервисов (ASR, TTS) и ключ голосовой сессии.
	cfg := config.Load()

	// База, аутентификация и пулы ML-шлюза.
	plat := config.LoadPlatform()
	logger := logx.New(plat.LogLevel, plat.LogFormat)
	slog.SetDefault(logger)

	if !plat.Enabled() {
		log.Fatal("ОШИБКА: не задан DATABASE_URL — без базы сервер не запускается")
	}

	startCtx, cancelStart := context.WithTimeout(context.Background(), 60*time.Second)
	application, err := app.New(startCtx, plat, cfg, logger)
	cancelStart()
	if err != nil {
		log.Fatalf("ОШИБКА запуска: %v", err)
	}

	r := chi.NewRouter()
	r.Use(application.Metrics.HTTP)

	// /health — жив ли процесс: всегда 200, пока сервер отвечает.
	// /health/ready — можно ли слать трафик: 503, если база недоступна.
	r.Get("/health", func(w http.ResponseWriter, req *http.Request) {
		writeHealth(w, http.StatusOK, healthBody(req.Context(), application))
	})
	r.Get("/health/ready", func(w http.ResponseWriter, req *http.Request) {
		body := healthBody(req.Context(), application)
		status := http.StatusOK
		if body["database"] == "unavailable" {
			status = http.StatusServiceUnavailable
			body["status"] = "degraded"
		}
		writeHealth(w, status, body)
	})

	// Verify внутри Mount падает, если какая-то ручка не объявила требуемое
	// право: лучше не подняться, чем открыть ручку, о доступе к которой никто
	// не подумал.
	if err := application.Mount(r); err != nil {
		log.Fatalf("ОШИБКА маршрутов: %v", err)
	}

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  120 * time.Second,
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Сервер запущен на порту %s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Ошибка сервера: %v", err)
		}
	}()

	<-done
	log.Println("Завершаем работу...")

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Ошибка при завершении HTTP-сервера: %v", err)
	}

	// Контур закрывается после HTTP-сервера: запросы уже дообработаны, и
	// разборы разговоров можно дописать в базу без потерь.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	if err := application.Close(closeCtx); err != nil {
		log.Printf("Ошибка при завершении: %v", err)
	}
	log.Println("Сервер остановлен")
}

func healthBody(ctx context.Context, application *app.App) map[string]any {
	body := map[string]any{"status": "ok"}
	for k, v := range application.Health(ctx) {
		body[k] = v
	}
	return body
}

func writeHealth(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
