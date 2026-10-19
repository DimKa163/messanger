// Package app is the composition root: it builds dependencies and owns their lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/DimKa163/messanger/database/migrations"
	"github.com/DimKa163/messanger/internal/config"
	"github.com/DimKa163/messanger/internal/security"
	"github.com/DimKa163/messanger/internal/storage/postgres"
	httptransport "github.com/DimKa163/messanger/internal/transport/http"
	"github.com/DimKa163/messanger/internal/usecase"
	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	pool            *pgxpool.Pool
	server          *http.Server
	logger          *slog.Logger
	shutdownTimeout time.Duration
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	tokens, err := security.NewJWT(cfg.JWTSecret, cfg.JWTTTL)
	if err != nil {
		return nil, err
	}
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pool, err := postgres.NewPool(connectCtx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := migrations.Up(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	logger.Info("database migrations applied")
	auth, err := usecase.NewAuth(ctx, postgres.NewUsers(pool), security.NewArgon2id(), tokens)
	if err != nil {
		pool.Close()
		return nil, err
	}

	return &App{
		pool:            pool,
		logger:          logger,
		shutdownTimeout: cfg.ShutdownTimeout,
		server: &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           httptransport.NewRouter(pool, auth),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}, nil
}

// Run serves until cancellation or a listener error. Call Close after Run returns.
func (a *App) Run(ctx context.Context) error {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", a.server.Addr)
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	return a.serve(ctx, listener)
}

func (a *App) serve(ctx context.Context, listener net.Listener) error {
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	a.server.BaseContext = func(net.Listener) context.Context { return requestCtx }

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.server.Serve(listener) }()
	a.logger.Info("HTTP server started", "address", listener.Addr().String())

	var runErr error
	serveFinished := false
	select {
	case runErr = <-serveErr:
		serveFinished = true
	case <-ctx.Done():
		a.logger.Info("HTTP server stopping")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.shutdownTimeout)
	defer cancel()
	shutdownErr := a.server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		cancelRequests()
		shutdownErr = errors.Join(shutdownErr, a.server.Close())
	}
	// Serve always finishes after Shutdown or Close; no goroutine is left sending.
	if !serveFinished {
		runErr = <-serveErr
	}
	if errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	return errors.Join(runErr, shutdownErr)
}

func (a *App) Close() {
	a.pool.Close()
}
