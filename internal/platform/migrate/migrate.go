// Package migrate — применение SQL-миграций, вшитых в бинарник.
//
// Почему не внешний инструмент: миграции нужны и в контейнере приложения
// (проверка «база на нужной версии» при старте), и в CI, и руками. Вшитый
// набор гарантирует, что версия схемы и версия кода едут вместе.
//
// Правила:
//   - у каждой миграции есть обратная (файл .down.sql);
//   - применённую миграцию нельзя редактировать — контрольная сумма
//     сверяется при каждом запуске;
//   - параллельный запуск безопасен: берётся advisory-lock.
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/jackc/pgx/v5"
)

// lockKey — произвольная константа. Важно только, чтобы все инстансы
// приложения использовали одну и ту же.
const lockKey int64 = 8613427

// Migration — пара up/down с номером версии.
type Migration struct {
	Version int
	Name    string
	Up      string
	Down    string
	Sum     string // sha256 от Up
}

// Applied — строка журнала применённых миграций.
type Applied struct {
	Version   int
	Name      string
	Sum       string
	AppliedAt string
}

// Load читает миграции из файловой системы (обычно embed.FS).
//
// Ожидаемые имена: 0001_name.up.sql и 0001_name.down.sql. Миграция без
// обратной — ошибка загрузки, а не предупреждение: откат должен быть возможен
// всегда, и узнавать об его отсутствии на проде поздно.
func Load(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: не удалось прочитать каталог миграций: %w", err)
	}

	ups := map[int]Migration{}
	downs := map[int]string{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := e.Name()
		base := path.Base(name)

		var direction string
		switch {
		case strings.HasSuffix(base, ".up.sql"):
			direction = "up"
			base = strings.TrimSuffix(base, ".up.sql")
		case strings.HasSuffix(base, ".down.sql"):
			direction = "down"
			base = strings.TrimSuffix(base, ".down.sql")
		default:
			return nil, fmt.Errorf("migrate: файл %q не заканчивается на .up.sql или .down.sql", name)
		}

		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("migrate: имя %q не в формате NNNN_name", name)
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("migrate: не удалось разобрать номер версии в %q: %w", name, err)
		}

		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("migrate: не удалось прочитать %q: %w", name, err)
		}

		if direction == "up" {
			sum := sha256.Sum256(body)
			ups[version] = Migration{
				Version: version,
				Name:    parts[1],
				Up:      string(body),
				Sum:     hex.EncodeToString(sum[:]),
			}
		} else {
			downs[version] = string(body)
		}
	}

	out := make([]Migration, 0, len(ups))
	for v, m := range ups {
		down, ok := downs[v]
		if !ok {
			return nil, fmt.Errorf("migrate: у миграции %04d_%s нет обратной (.down.sql)", v, m.Name)
		}
		m.Down = down
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func ensureTable(ctx context.Context, q pg.Querier) error {
	_, err := q.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.schema_migrations (
			version    int PRIMARY KEY,
			name       text NOT NULL,
			checksum   text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("migrate: не удалось создать журнал миграций: %w", err)
	}
	return nil
}

func appliedMap(ctx context.Context, q pg.Querier) (map[int]Applied, error) {
	rows, err := q.Query(ctx, `SELECT version, name, checksum, applied_at::text
	                           FROM public.schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("migrate: не удалось прочитать журнал: %w", err)
	}
	defer rows.Close()

	out := map[int]Applied{}
	for rows.Next() {
		var a Applied
		if err := rows.Scan(&a.Version, &a.Name, &a.Sum, &a.AppliedAt); err != nil {
			return nil, fmt.Errorf("migrate: не удалось разобрать строку журнала: %w", err)
		}
		out[a.Version] = a
	}
	return out, rows.Err()
}

// Up применяет все неприменённые миграции по возрастанию версии.
//
// Каждая миграция идёт в собственной транзакции: упавшая не оставляет схему
// в половинчатом состоянии, а уже применённые остаются применёнными.
// Возвращает число выполненных миграций.
func Up(ctx context.Context, pool *pg.Pool, list []Migration) (int, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("migrate: не удалось получить соединение: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return 0, fmt.Errorf("migrate: не удалось взять advisory-lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockKey)
	}()

	if err := ensureTable(ctx, conn); err != nil {
		return 0, err
	}
	applied, err := appliedMap(ctx, conn)
	if err != nil {
		return 0, err
	}

	done := 0
	for _, m := range list {
		if a, ok := applied[m.Version]; ok {
			if a.Sum != m.Sum {
				return done, fmt.Errorf(
					"migrate: миграция %04d_%s изменена после применения "+
						"(в журнале %s, в коде %s). Применённые миграции править нельзя — "+
						"добавьте новую", m.Version, m.Name, a.Sum[:8], m.Sum[:8])
			}
			continue
		}

		// Транзакция использует соединение с advisory-lock: второе соединение
		// при MaxConns=1 не появится, а несколько миграторов исчерпали бы пул.
		err := pg.InTx(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.Up); err != nil {
				return fmt.Errorf("migrate: миграция %04d_%s упала: %w", m.Version, m.Name, err)
			}
			_, err := tx.Exec(ctx,
				`INSERT INTO public.schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
				m.Version, m.Name, m.Sum)
			return err
		})
		if err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// Down откатывает последние steps миграций.
func Down(ctx context.Context, pool *pg.Pool, list []Migration, steps int) (int, error) {
	if steps < 1 {
		return 0, fmt.Errorf("migrate: steps должно быть >= 1, получено %d", steps)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("migrate: не удалось получить соединение: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return 0, fmt.Errorf("migrate: не удалось взять advisory-lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockKey)
	}()

	if err := ensureTable(ctx, conn); err != nil {
		return 0, err
	}
	applied, err := appliedMap(ctx, conn)
	if err != nil {
		return 0, err
	}

	byVersion := map[int]Migration{}
	for _, m := range list {
		byVersion[m.Version] = m
	}

	versions := make([]int, 0, len(applied))
	for v := range applied {
		versions = append(versions, v)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(versions)))

	done := 0
	for _, v := range versions {
		if done >= steps {
			break
		}
		m, ok := byVersion[v]
		if !ok {
			return done, fmt.Errorf(
				"migrate: версия %d применена в базе, но файла миграции нет — "+
					"откатить нечем", v)
		}

		err := pg.InTx(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.Down); err != nil {
				return fmt.Errorf("migrate: откат %04d_%s упал: %w", m.Version, m.Name, err)
			}
			_, err := tx.Exec(ctx, `DELETE FROM public.schema_migrations WHERE version = $1`, v)
			return err
		})
		if err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// Status возвращает, какие версии применены. Используется командой status и
// проверкой готовности при старте приложения.
func Status(ctx context.Context, pool *pg.Pool) (map[int]Applied, error) {
	if err := ensureTable(ctx, pool); err != nil {
		return nil, err
	}
	return appliedMap(ctx, pool)
}

// Pending возвращает список неприменённых миграций.
func Pending(list []Migration, applied map[int]Applied) []Migration {
	out := []Migration{}
	for _, m := range list {
		if _, ok := applied[m.Version]; !ok {
			out = append(out, m)
		}
	}
	return out
}
