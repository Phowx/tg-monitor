package serverapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/config"
	"github.com/tg-monitor/tg-monitor/internal/httpapi"
	"github.com/tg-monitor/tg-monitor/internal/monitoring"
	"github.com/tg-monitor/tg-monitor/internal/storage/sqlite"
)

const (
	retentionInterval = 24 * time.Hour
	shutdownTimeout   = 10 * time.Second
)

type App struct {
	store              *sqlite.Store
	monitor            *monitoring.Service
	server             *http.Server
	checkpointInterval time.Duration
	logger             *slog.Logger
	closeOnce          sync.Once
	closeErr           error
}

func New(ctx context.Context, cfg config.ServerRuntimeConfig, logger *slog.Logger) (*App, error) {
	store, err := sqlite.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	logger = appLoggerOrDiscard(logger)
	monitor := monitoring.NewService(store)
	handler := httpapi.NewHandler(httpapi.Dependencies{
		Readiness:     store,
		Authenticator: auth.NewAuthenticator(store),
		Ingestor:      monitor,
		Logger:        logger,
	})
	return &App{
		store:              store,
		monitor:            monitor,
		checkpointInterval: cfg.CheckpointInterval,
		logger:             logger,
		server: &http.Server{
			Addr:              cfg.ListenAddr,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    1 << 20,
		},
	}, nil
}

func (app *App) Serve(ctx context.Context, listener net.Listener) error {
	if _, err := RunRetentionOnce(ctx, app.store, time.Now()); err != nil {
		app.logger.Error("metric retention failed", "error", err)
	}

	workerCtx, stopWorkers := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Add(2)
	go app.runCheckpointWorker(workerCtx, &workers)
	go app.runRetentionWorker(workerCtx, &workers)

	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- app.server.Serve(listener)
	}()

	select {
	case serveErr := <-serveErrors:
		stopWorkers()
		workers.Wait()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		checkpointErr := app.monitor.Checkpoint(shutdownCtx)
		closeErr := app.Close(shutdownCtx)
		return errors.Join(normalizeServeError(serveErr), checkpointErr, closeErr)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownErr := app.server.Shutdown(shutdownCtx)
		stopWorkers()
		workers.Wait()
		serveErr := <-serveErrors
		checkpointErr := app.monitor.Checkpoint(shutdownCtx)
		closeErr := app.Close(shutdownCtx)
		return errors.Join(shutdownErr, normalizeServeError(serveErr), checkpointErr, closeErr)
	}
}

func (app *App) Close(ctx context.Context) error {
	_ = ctx
	app.closeOnce.Do(func() {
		app.closeErr = app.store.Close()
	})
	return app.closeErr
}

func (app *App) runCheckpointWorker(ctx context.Context, workers *sync.WaitGroup) {
	defer workers.Done()
	ticker := time.NewTicker(app.checkpointInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := app.monitor.Checkpoint(ctx); err != nil {
				app.logger.Error("metric checkpoint failed", "error", err)
			}
		}
	}
}

func (app *App) runRetentionWorker(ctx context.Context, workers *sync.WaitGroup) {
	defer workers.Done()
	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			deleted, err := RunRetentionOnce(ctx, app.store, now)
			if err != nil {
				app.logger.Error("metric retention failed", "error", err)
				continue
			}
			app.logger.Info("metric retention completed", "deleted", deleted)
		}
	}
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("serve HTTP: %w", err)
}

func appLoggerOrDiscard(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
