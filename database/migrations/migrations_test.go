package migrations

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The provided connection is only used to create and drop a unique test database.
func isolatedMigrationDatabase(t *testing.T) *pgxpool.Pool {
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
	return pool
}

func TestUpPostgres(t *testing.T) {
	pool := isolatedMigrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
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
	if version != 3 || dirty {
		t.Fatalf("version=%d dirty=%v", version, dirty)
	}
	// Check the existing destructive down script and then rebuild from zero.
	for _, name := range []string{"000003_message_outbox.down.sql", "000002_message_receipts.down.sql", "000001_init.down.sql"} {
		down, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(down)); err != nil {
			t.Fatalf("down migration %s: %v", name, err)
		}
	}
	if _, err := pool.Exec(ctx, "TRUNCATE public.schema_migrations"); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, pool); err != nil {
		t.Fatalf("up after down: %v", err)
	}
}

func TestMessageReceiptsPostgres(t *testing.T) {
	pool := isolatedMigrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Start from version 1 with a message written by the old application.
	initial, err := files.ReadFile("000001_init.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(initial)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.schema_migrations (version BIGINT NOT NULL PRIMARY KEY, dirty BOOLEAN NOT NULL);
		INSERT INTO public.schema_migrations VALUES (1, false);
	`); err != nil {
		t.Fatal(err)
	}
	userID, conversationID, messageID := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO public.users (id, login, name, password, salt)
		VALUES ($1, 'sender', 'Sender', 'password', 'salt')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.conversations (id, type) VALUES ($1, 1)`, conversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.messages
		(id, conversation_id, sender_id, client_message_id, sequence, content)
		VALUES ($1, $2, $3, $4, 1, $5)`, messageID, conversationID, userID, uuid.New(), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, pool); err != nil {
		t.Fatalf("upgrade with existing message: %v", err)
	}
	var messageType string
	if err := pool.QueryRow(ctx, `SELECT message_type FROM public.messages WHERE id=$1`, messageID).Scan(&messageType); err != nil || messageType != "text" {
		t.Fatalf("existing message type=%q err=%v", messageType, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.messages SET message_type='image' WHERE id=$1`, messageID); err == nil {
		t.Fatal("unsupported message type accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.outbox (message_id,event_type,payload) VALUES ($1,'message.send','{}')`, messageID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.outbox (message_id,event_type,payload) VALUES ($1,'message.send','{}')`, messageID); err == nil {
		t.Fatal("duplicate outbox event accepted")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.outbox (message_id,event_type,payload) VALUES ($1,'unknown','{}')`, messageID); err == nil {
		t.Fatal("unknown outbox event accepted")
	}
	var deliveredAt, readAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT delivered_at, read_at FROM public.messages WHERE id = $1`, messageID).
		Scan(&deliveredAt, &readAt); err != nil || deliveredAt != nil || readAt != nil {
		t.Fatalf("existing message receipts: delivered=%v read=%v err=%v", deliveredAt, readAt, err)
	}
	delivered := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	read := delivered.Add(time.Minute)
	for _, tc := range []struct {
		name      string
		delivered *time.Time
		read      *time.Time
		invalid   bool
	}{
		{name: "pending"},
		{name: "delivered", delivered: &delivered},
		{name: "read", delivered: &delivered, read: &read},
		{name: "simultaneous receipt", delivered: &read, read: &read},
		{name: "read without delivery", read: &read, invalid: true},
		{name: "read before delivery", delivered: &read, read: &delivered, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `UPDATE public.messages SET delivered_at = $2, read_at = $3 WHERE id = $1`,
				messageID, tc.delivered, tc.read)
			if tc.invalid {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "ck_messages_read_after_delivery" {
					t.Fatalf("expected receipt constraint violation, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	outboxDown, err := files.ReadFile("000003_message_outbox.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(outboxDown)); err != nil {
		t.Fatal(err)
	}
	down, err := files.ReadFile("000002_message_receipts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("receipt down migration: %v", err)
	}
	var content []byte
	if err := pool.QueryRow(ctx, `SELECT content FROM public.messages WHERE id = $1`, messageID).
		Scan(&content); err != nil || string(content) != "hello" {
		t.Fatalf("down did not preserve message: content=%q err=%v", content, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.schema_migrations SET version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, pool); err != nil {
		t.Fatalf("receipt up after down: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT delivered_at, read_at FROM public.messages WHERE id = $1`, messageID).
		Scan(&deliveredAt, &readAt); err != nil || deliveredAt != nil || readAt != nil {
		t.Fatalf("receipts after re-up: delivered=%v read=%v err=%v", deliveredAt, readAt, err)
	}
}
