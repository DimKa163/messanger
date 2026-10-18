// Package migrations embeds the SQL migration pairs shipped with the server.
package migrations

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgmigrate "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed *.up.sql *.down.sql
var files embed.FS

// Up applies pending migrations. The caller retains ownership of pool.
// golang-migrate tracks versions and dirty state and serializes runs with a DB lock.
func Up(ctx context.Context, pool *pgxpool.Pool) (err error) {
	source, err := iofs.New(files, ".")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	defer func() { err = errors.Join(err, source.Close()) }()

	// Adapt the existing pgxpool for golang-migrate's database/sql interface.
	// Closing this wrapper does not close the underlying pool.
	db := stdlib.OpenDBFromPool(pool)
	defer func() { err = errors.Join(err, db.Close()) }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()

	// Bound advisory-lock waits, including the lock taken during driver initialization.
	// Restore the previous setting before returning this connection to the pool.
	var lockTimeout string
	if err := conn.QueryRowContext(ctx, "SHOW lock_timeout").Scan(&lockTimeout); err != nil {
		return fmt.Errorf("read migration lock timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET lock_timeout = '30s'"); err != nil {
		return fmt.Errorf("set migration lock timeout: %w", err)
	}
	defer func() {
		resetCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, resetErr := conn.ExecContext(resetCtx, "SELECT set_config('lock_timeout', $1, false)", lockTimeout)
		err = errors.Join(err, resetErr)
	}()

	// WithConnection keeps ownership explicit even if version-table creation fails.
	// The connection comes from pgxpool; no connection is opened through lib/pq.
	driver, err := pgmigrate.WithConnection(ctx, conn, &pgmigrate.Config{
		SchemaName:       "public",
		StatementTimeout: 30 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("create migration driver: %w", err)
	}
	runner, err := migrate.NewWithInstance("iofs", source, "postgres", driver)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	// Let PostgreSQL time out first so the lock goroutine finishes before cleanup.
	runner.LockTimeout = 35 * time.Second
	if err := runner.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
