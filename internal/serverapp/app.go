package serverapp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/adminapi"
	"github.com/tg-monitor/tg-monitor/internal/alerting"
	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/cloudflare"
	"github.com/tg-monitor/tg-monitor/internal/config"
	"github.com/tg-monitor/tg-monitor/internal/httpapi"
	"github.com/tg-monitor/tg-monitor/internal/monitoring"
	"github.com/tg-monitor/tg-monitor/internal/sessionapi"
	"github.com/tg-monitor/tg-monitor/internal/storage/sqlite"
	"github.com/tg-monitor/tg-monitor/internal/telegramapi"
	"github.com/tg-monitor/tg-monitor/internal/telegrambot"
	"github.com/tg-monitor/tg-monitor/internal/webapp"
)

const (
	retentionInterval = 24 * time.Hour
	shutdownTimeout   = 10 * time.Second
)

type telegramSenderFactory func(string, time.Duration) (telegrambot.Sender, error)

type appDependencies struct {
	newTelegramSender telegramSenderFactory
	random            io.Reader
	now               func() time.Time
}

type App struct {
	store              *sqlite.Store
	monitor            *monitoring.Service
	alertWorker        *alerting.Worker
	server             *http.Server
	checkpointInterval time.Duration
	now                func() time.Time
	logger             *slog.Logger
	closeOnce          sync.Once
	closeErr           error
}

func New(ctx context.Context, cfg config.ApplicationRuntimeConfig, logger *slog.Logger) (*App, error) {
	return newWithDependencies(ctx, cfg, logger, appDependencies{
		newTelegramSender: func(token string, timeout time.Duration) (telegrambot.Sender, error) {
			return telegramapi.New(token, timeout)
		},
		random: rand.Reader,
		now:    time.Now,
	})
}

func newWithDependencies(ctx context.Context, cfg config.ApplicationRuntimeConfig, logger *slog.Logger, dependencies appDependencies) (*App, error) {
	if dependencies.now == nil {
		dependencies.now = time.Now
	}
	if dependencies.random == nil {
		dependencies.random = rand.Reader
	}

	store, err := sqlite.Open(ctx, cfg.Server.DatabasePath)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*App, error) {
		return nil, errors.Join(err, store.Close())
	}

	logger = appLoggerOrDiscard(logger)
	monitor := monitoring.NewService(store)
	core := httpapi.NewHandler(httpapi.Dependencies{
		Readiness:     store,
		Authenticator: auth.NewAuthenticator(store),
		Ingestor:      monitor,
		Now:           dependencies.now,
		Logger:        logger,
	})
	var handler http.Handler = core
	var alertWorker *alerting.Worker

	if telegram := cfg.Telegram; telegram != nil {
		if dependencies.newTelegramSender == nil {
			return fail(errors.New("create Telegram routes: sender factory is required"))
		}
		sender, err := dependencies.newTelegramSender(telegram.BotToken, telegram.HTTPTimeout)
		if err != nil {
			return fail(fmt.Errorf("create Telegram routes: sender: %w", err))
		}
		commander, err := telegrambot.NewCommander(store, telegram.PublicURL, dependencies.now)
		if err != nil {
			return fail(fmt.Errorf("create Telegram routes: commander: %w", err))
		}
		webhook, err := telegrambot.NewWebhookHandler(telegrambot.WebhookDependencies{
			Updates: store, Sender: sender, Replier: commander,
			Secret: telegram.WebhookSecret, AdminIDs: telegram.AdminTelegramIDs,
			Now: dependencies.now, Logger: logger,
		})
		if err != nil {
			return fail(fmt.Errorf("create Telegram routes: webhook: %w", err))
		}
		alertWorker, err = alerting.New(alerting.Config{
			AdminTelegramIDs: telegram.AdminTelegramIDs,
		}, store, sender, dependencies.now, logger)
		if err != nil {
			return fail(fmt.Errorf("create Telegram routes: alert worker: %w", err))
		}
		sessions, err := sessionapi.NewHandler(sessionapi.Config{
			PublicURL: telegram.PublicURL, BotToken: telegram.BotToken,
			AdminTelegramIDs: telegram.AdminTelegramIDs,
			SessionTTL:       telegram.SessionTTL, InitDataMaxAge: telegram.InitDataMaxAge,
		}, sessionapi.Dependencies{
			Repository: store, Random: dependencies.random, Now: dependencies.now, Logger: logger,
		})
		if err != nil {
			return fail(fmt.Errorf("create Telegram routes: sessions: %w", err))
		}
		var dns adminapi.DNSService
		if cfg.Cloudflare != nil {
			dns, err = cloudflare.New(*cfg.Cloudflare)
			if err != nil {
				return fail(fmt.Errorf("create Cloudflare DNS client: %w", err))
			}
		}
		admin, err := adminapi.NewHandler(adminapi.Config{
			PublicURL: telegram.PublicURL,
		}, adminapi.Dependencies{
			Repository: store,
			DNS:        dns,
			Random:     dependencies.random,
			Now:        dependencies.now,
			Logger:     logger,
		})
		if err != nil {
			return fail(fmt.Errorf("create Telegram routes: admin API: %w", err))
		}
		miniApp := webapp.NewHandler()

		mux := http.NewServeMux()
		mux.Handle("/telegram/webhook", webhook)
		mux.Handle("/api/v1/auth/telegram", sessions)
		mux.Handle("/api/v1/auth/session", sessions)
		mux.Handle("/api/v1/auth/logout", sessions)
		mux.Handle("/api/v1/admin/", admin)
		mux.Handle("/app", miniApp)
		mux.Handle("/app/", miniApp)
		mux.Handle("/", core)
		handler = mux
	}

	return &App{
		store:              store,
		monitor:            monitor,
		checkpointInterval: cfg.Server.CheckpointInterval,
		now:                dependencies.now,
		alertWorker:        alertWorker,
		logger:             logger,
		server: &http.Server{
			Addr:              cfg.Server.ListenAddr,
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
	now := app.now()
	if _, err := RunRetentionOnce(ctx, app.store, now); err != nil {
		app.logger.Error("metric retention failed", "error", err)
	}
	if _, err := RunAccessCleanupOnce(ctx, app.store, now); err != nil {
		app.logger.Error("access cleanup failed", "error", err)
	}

	workerCtx, stopWorkers := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Add(2)
	go app.runCheckpointWorker(workerCtx, &workers)
	go app.runRetentionWorker(workerCtx, &workers)

	serveErrors := make(chan error, 1)
	if app.alertWorker != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			app.alertWorker.Run(workerCtx)
		}()
	}
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
			} else {
				app.logger.Info("metric retention completed", "deleted", deleted)
			}
			cleaned, err := RunAccessCleanupOnce(ctx, app.store, now)
			if err != nil {
				app.logger.Error("access cleanup failed", "error", err)
			} else {
				app.logger.Info("access cleanup completed", "sessions", cleaned.Sessions, "updates", cleaned.Updates, "outbox", cleaned.Outbox)
			}
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
