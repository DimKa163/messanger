package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DimKa163/messanger/database/migrations"
	"github.com/DimKa163/messanger/internal/domain"
	"github.com/DimKa163/messanger/internal/usecase"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUsersPostgres(t *testing.T) {
	pool := isolatedUsersDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	repo := NewUsers(pool)
	user, err := repo.Create(ctx, domain.User{
		ID: uuid.New(), Name: "Алиса", Login: "alice", Password: []byte("encoded hash"), Salt: []byte("random salt"),
	})
	if err != nil || user.CreatedAt.IsZero() {
		t.Fatalf("Create error=%v created_at=%v", err, user.CreatedAt)
	}
	loaded, err := repo.ByLogin(ctx, "alice")
	if err != nil || loaded.ID != user.ID || loaded.Name != user.Name || loaded.Email != "" || !bytes.Equal(loaded.Password, user.Password) || !bytes.Equal(loaded.Salt, user.Salt) {
		t.Fatalf("persisted user did not round-trip: %v", err)
	}
	for _, login := range []string{"missing", "alice' OR true --"} {
		if _, err := repo.ByLogin(ctx, login); !errors.Is(err, usecase.ErrUserNotFound) {
			t.Fatalf("ByLogin(%q) error=%v", login, err)
		}
	}
	// Both requests reach the repository together; the database arbitrates the conflict.
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := repo.Create(ctx, domain.User{
				ID: uuid.New(), Name: "Bob", Login: "bob", Email: "bob@example.com",
				Password: []byte("encoded hash"), Salt: []byte("random salt"),
			})
			results <- err
		}()
	}
	close(start)
	success, conflict := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			success++
		case errors.Is(err, usecase.ErrLoginTaken):
			conflict++
		default:
			t.Errorf("concurrent Create: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent registration: successes=%d conflicts=%d", success, conflict)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.users WHERE login = $1", "bob").Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate user persisted: count=%d error=%v", count, err)
	}
}

func isolatedUsersDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	name := "messanger_auth_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	cfg.MinConns, cfg.MaxConns = 0, 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
