// Package pg — подключение к PostgreSQL и помощники транзакций.
//
// Здесь нет доменной логики: только пул, health-check и обёртка транзакции.
// Репозитории модулей принимают Querier, поэтому один и тот же код работает
// и на пуле, и внутри транзакции.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config — настройки пула. Все значения читаются из окружения (инвариант И-4:
// ёмкость задаётся конфигом, а не кодом).
type Config struct {
	Tracer          pgx.QueryTracer
	DSN             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
	// StatementTimeout защищает от одного тяжёлого запроса, который держит
	// соединение и выедает пул. Ставится на уровне сессии соединения.
	StatementTimeout time.Duration
}

// Querier — общий знаменатель pgxpool.Pool и pgx.Tx.
//
// Репозиторий, принимающий Querier, одинаково работает и сам по себе, и в
// составе транзакции: не нужно дублировать методы «...Tx».
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Pool — пул соединений с приложенными настройками.
type Pool struct {
	*pgxpool.Pool
}

// Connect поднимает пул и проверяет, что база отвечает.
//
// Падение здесь — это отказ старта: работать без базы приложение не должно,
// иначе первые запросы упадут уже у пользователя.
func Connect(ctx context.Context, cfg Config) (*Pool, error) {
	if cfg.DSN == "" {
		return nil, errors.New("pg: пустой DSN (переменная DATABASE_URL)")
	}

	pcfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("pg: не удалось разобрать DSN: %w", err)
	}

	if cfg.Tracer != nil {
		pcfg.ConnConfig.Tracer = cfg.Tracer
	}
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pcfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	if cfg.ConnectTimeout > 0 {
		pcfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}
	if cfg.StatementTimeout > 0 {
		if pcfg.ConnConfig.RuntimeParams == nil {
			pcfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		ms := int64(cfg.StatementTimeout / time.Millisecond)
		pcfg.ConnConfig.RuntimeParams["statement_timeout"] = fmt.Sprintf("%d", ms)
	}

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("pg: не удалось создать пул: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: база не отвечает: %w", err)
	}

	return &Pool{Pool: pool}, nil
}

// InTx выполняет fn в транзакции: коммит при nil-ошибке, откат при любой другой.
//
// Откат делается и при панике — иначе соединение возвращается в пул с открытой
// транзакцией и отравляет следующие запросы.
func InTx(ctx context.Context, p interface {
	Begin(context.Context) (pgx.Tx, error)
}, fn func(tx pgx.Tx) error) (err error) {
	tx, err := p.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg: не удалось открыть транзакцию: %w", err)
	}

	defer func() {
		if rec := recover(); rec != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(rec)
		}
		if err != nil {
			// Контекст мог быть уже отменён клиентом — откат всё равно нужен.
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg: не удалось зафиксировать транзакцию: %w", err)
	}
	return nil
}

// IsUniqueViolation отличает нарушение уникальности от прочих ошибок базы.
// Нужно, чтобы сервис вернул 409, а не 500, когда email уже занят.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// IsRestrictViolation ловит наши охранные триггеры: попытку изменить
// опубликованную версию урока или запись append-only журнала.
func IsRestrictViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23001"
}

// IsNoRows — удобная обёртка: pgx возвращает свою sentinel-ошибку.
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
