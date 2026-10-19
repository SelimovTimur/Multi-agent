// Команда migrate — применение и откат миграций схемы.
//
//	go run ./cmd/migrate up          — применить всё неприменённое
//	go run ./cmd/migrate down 1      — откатить последнюю миграцию
//	go run ./cmd/migrate status      — что применено, что ждёт
//
// Подключение берётся из DATABASE_URL.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/platform/migrate"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/NATIVE/NATIVE/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ошибка: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cmd := "up"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	plat := config.LoadPlatform()
	if !plat.Enabled() {
		return fmt.Errorf("не задана переменная DATABASE_URL")
	}

	list, err := migrate.Load(migrations.FS)
	if err != nil {
		return err
	}

	pool, err := pg.Connect(ctx, plat.DB)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch cmd {
	case "up":
		n, err := migrate.Up(ctx, pool, list)
		if err != nil {
			return err
		}
		if n == 0 {
			fmt.Println("миграции: всё уже применено")
		} else {
			fmt.Printf("миграции: применено %d\n", n)
		}
		return nil

	case "down":
		steps := 1
		if len(os.Args) > 2 {
			steps, err = strconv.Atoi(os.Args[2])
			if err != nil {
				return fmt.Errorf("не удалось разобрать число шагов %q: %w", os.Args[2], err)
			}
		}
		n, err := migrate.Down(ctx, pool, list, steps)
		if err != nil {
			return err
		}
		fmt.Printf("миграции: откачено %d\n", n)
		return nil

	case "status":
		applied, err := migrate.Status(ctx, pool)
		if err != nil {
			return err
		}
		fmt.Printf("%-6s %-24s %s\n", "версия", "имя", "состояние")
		for _, m := range list {
			if a, ok := applied[m.Version]; ok {
				mark := "применена"
				if a.Sum != m.Sum {
					mark = "ИЗМЕНЕНА ПОСЛЕ ПРИМЕНЕНИЯ"
				}
				fmt.Printf("%-6d %-24s %s (%s)\n", m.Version, m.Name, mark, a.AppliedAt)
			} else {
				fmt.Printf("%-6d %-24s ожидает\n", m.Version, m.Name)
			}
		}
		return nil

	default:
		return fmt.Errorf("неизвестная команда %q; доступны: up, down, status", cmd)
	}
}
