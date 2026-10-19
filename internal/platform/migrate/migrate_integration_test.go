package migrate

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationsWithSingleConnection(t *testing.T) {
	dsn := os.Getenv("NATIVE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("NATIVE_TEST_DATABASE_URL не задан")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pg.Connect(ctx, pg.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	name := "native_migrate_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+quoted); err != nil {
			t.Error(err)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	cfg.MaxConns = 1
	rawPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool := &pg.Pool{Pool: rawPool}
	t.Cleanup(pool.Close)
	list, err := Load(fstest.MapFS{
		"0001_test.up.sql":   {Data: []byte("CREATE TABLE test_single_connection (id int PRIMARY KEY)")},
		"0001_test.down.sql": {Data: []byte("DROP TABLE test_single_connection")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := Up(ctx, pool, list); err != nil || n != 1 {
		t.Fatalf("Применение: %d, %v", n, err)
	}
	if n, err := Up(ctx, pool, list); err != nil || n != 0 {
		t.Fatalf("Повторное применение: %d, %v", n, err)
	}
	if n, err := Down(ctx, pool, list, 1); err != nil || n != 1 {
		t.Fatalf("Откат: %d, %v", n, err)
	}
	if n, err := Up(ctx, pool, list); err != nil || n != 1 {
		t.Fatalf("Применение после отката: %d, %v", n, err)
	}
}
