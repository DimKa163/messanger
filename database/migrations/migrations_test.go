package migrations

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The provided connection is only used to create and drop a unique test database.
func TestUpPostgres(t *testing.T) {
	databaseURL := os.Getenv("MESSANGER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set MESSANGER_TEST_DATABASE_URL to a PostgreSQL instance with CREATEDB permission")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(context.Background()) })
	name := "messanger_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP DATABASE "+identifier); err != nil {
			t.Errorf("drop isolated test database: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	// Exercise migration setup with only one available pooled connection.
	cfg.MaxConns = 1
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if err := Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO public.users (login, name, password, salt) VALUES ('test', 'Test', 'password', 'salt')"); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, pool); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.users").Scan(&count); err != nil || count != 1 {
		t.Fatalf("repeat migration did not preserve data: count=%d, err=%v", count, err)
	}
	var version int
	var dirty bool
	if err := pool.QueryRow(ctx, "SELECT version, dirty FROM public.schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 1 || dirty {
		t.Fatalf("version=%d dirty=%v", version, dirty)
	}
	// Check the existing destructive down script and then rebuild from zero.
	down, err := files.ReadFile("000001_init.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down migration: %v", err)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE public.schema_migrations"); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, pool); err != nil {
		t.Fatalf("up after down: %v", err)
	}
}
