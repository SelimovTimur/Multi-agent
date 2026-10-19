// Package migrate — проверка и применение миграций при старте приложения.
//
// Отдельно от internal/platform/migrate: тот пакет умеет применять миграции,
// этот решает, НУЖНО ли их применять на старте. Решение зависит от окружения:
// в dev удобно применять автоматически, на проде миграции гоняет отдельный шаг
// выката — иначе две реплики стартуют одновременно и дерутся за схему.
package migrate

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/NATIVE/NATIVE/internal/platform/migrate"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/NATIVE/NATIVE/migrations"
)

// Ensure приводит схему в нужное состояние или отказывает в старте.
//
// applyOnStart — применять неприменённые миграции.
// requireVersion — не стартовать, если остались неприменённые. Защищает от
// выката кода на старую схему: лучше не подняться, чем принимать запросы и
// падать на первом же SQL.
func Ensure(ctx context.Context, pool *pg.Pool, applyOnStart, requireVersion bool, log *slog.Logger) error {
	list, err := migrate.Load(migrations.FS)
	if err != nil {
		return err
	}

	if applyOnStart {
		n, err := migrate.Up(ctx, pool, list)
		if err != nil {
			return err
		}
		if n > 0 {
			log.Info("применены миграции схемы", "count", n)
		}
	}

	applied, err := migrate.Status(ctx, pool)
	if err != nil {
		return err
	}
	pending := migrate.Pending(list, applied)
	if len(pending) == 0 {
		return nil
	}

	names := make([]string, 0, len(pending))
	for _, m := range pending {
		names = append(names, fmt.Sprintf("%04d_%s", m.Version, m.Name))
	}
	if requireVersion {
		return fmt.Errorf(
			"схема базы отстаёт от кода: не применены миграции %v. "+
				"Выполните `go run ./cmd/migrate up` или включите DB_MIGRATE_ON_START", names)
	}
	log.Warn("есть неприменённые миграции", "pending", names)
	return nil
}
