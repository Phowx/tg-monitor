package servercmd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/config"
	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/serverapp"
	"github.com/tg-monitor/tg-monitor/internal/storage/sqlite"
	"github.com/tg-monitor/tg-monitor/internal/telegramapi"
)

type Store interface {
	Close() error
	CreateServer(context.Context, domain.Server) (domain.Server, error)
	ListServers(context.Context) ([]domain.Server, error)
	UpdateServerTokenHash(context.Context, int64, []byte) error
	GetLatestMetrics(context.Context, int64) (domain.LatestMetrics, error)
	QueryMinuteSamples(context.Context, int64, int64, int64) ([]domain.MinuteSample, error)
}

type TelegramWebhookClient interface {
	SetWebhook(context.Context, string, string) error
	GetWebhookInfo(context.Context) (telegramapi.WebhookInfo, error)
	DeleteWebhook(context.Context) error
}

type Dependencies struct {
	Stdout                io.Writer
	Stderr                io.Writer
	Random                io.Reader
	LoadServerConfig      func() (config.ServerRuntimeConfig, error)
	LoadApplicationConfig func() (config.ApplicationRuntimeConfig, error)
	LoadTelegramConfig    func() (config.TelegramRuntimeConfig, error)
	OpenStore             func(context.Context, string) (Store, error)
	Serve                 func(context.Context, config.ApplicationRuntimeConfig, *slog.Logger) error
	NewTelegramClient     func(string, time.Duration) (TelegramWebhookClient, error)
}

func Run(ctx context.Context, args []string, dependencies Dependencies) error {
	dependencies = dependencies.withDefaults()
	if len(args) == 0 {
		return errors.New("usage: tg-monitor-server <serve|server|metrics|telegram>")
	}

	switch args[0] {
	case "serve":
		return runServe(ctx, args[1:], dependencies)
	case "server":
		return runServer(ctx, args[1:], dependencies)
	case "metrics":
		return runMetrics(ctx, args[1:], dependencies)
	case "telegram":
		return runTelegram(ctx, args[1:], dependencies)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runServe(ctx context.Context, args []string, dependencies Dependencies) error {
	if len(args) != 0 {
		return errors.New("usage: tg-monitor-server serve")
	}
	cfg, err := dependencies.LoadApplicationConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(dependencies.Stderr, nil))
	return dependencies.Serve(ctx, cfg, logger)
}

func runTelegram(ctx context.Context, args []string, dependencies Dependencies) error {
	if len(args) == 0 {
		return errors.New("usage: tg-monitor-server telegram <set-webhook|get-webhook|delete-webhook>")
	}
	switch args[0] {
	case "set-webhook":
		if len(args) != 1 {
			return errors.New("usage: tg-monitor-server telegram set-webhook")
		}
		return runTelegramSetWebhook(ctx, dependencies)
	case "get-webhook":
		if len(args) != 1 {
			return errors.New("usage: tg-monitor-server telegram get-webhook")
		}
		return runTelegramGetWebhook(ctx, dependencies)
	case "delete-webhook":
		if len(args) != 1 {
			return errors.New("usage: tg-monitor-server telegram delete-webhook")
		}
		return runTelegramDeleteWebhook(ctx, dependencies)
	default:
		return fmt.Errorf("unknown telegram command %q", args[0])
	}
}

func runTelegramSetWebhook(ctx context.Context, dependencies Dependencies) error {
	cfg, err := dependencies.LoadTelegramConfig()
	if err != nil {
		return err
	}
	if err := config.ValidateWebhookPublicURL(cfg.PublicURL); err != nil {
		return err
	}
	client, err := dependencies.NewTelegramClient(cfg.BotToken, cfg.HTTPTimeout)
	if err != nil {
		return err
	}
	if err := client.SetWebhook(ctx, cfg.PublicURL, cfg.WebhookSecret); err != nil {
		return err
	}
	if _, err := io.WriteString(dependencies.Stdout, "webhook=registered\n"); err != nil {
		return fmt.Errorf("write webhook registration result: %w", err)
	}
	return nil
}

func runTelegramGetWebhook(ctx context.Context, dependencies Dependencies) error {
	client, err := loadTelegramClient(dependencies)
	if err != nil {
		return err
	}
	info, err := client.GetWebhookInfo(ctx)
	if err != nil {
		return err
	}
	return encodeIndented(dependencies.Stdout, info)
}

func runTelegramDeleteWebhook(ctx context.Context, dependencies Dependencies) error {
	client, err := loadTelegramClient(dependencies)
	if err != nil {
		return err
	}
	if err := client.DeleteWebhook(ctx); err != nil {
		return err
	}
	if _, err := io.WriteString(dependencies.Stdout, "webhook=deleted\n"); err != nil {
		return fmt.Errorf("write webhook deletion result: %w", err)
	}
	return nil
}

func loadTelegramClient(dependencies Dependencies) (TelegramWebhookClient, error) {
	cfg, err := dependencies.LoadTelegramConfig()
	if err != nil {
		return nil, err
	}
	return dependencies.NewTelegramClient(cfg.BotToken, cfg.HTTPTimeout)
}

func runServer(ctx context.Context, args []string, dependencies Dependencies) error {
	if len(args) == 0 {
		return errors.New("usage: tg-monitor-server server <add|list|rotate-token>")
	}
	switch args[0] {
	case "add":
		return runServerAdd(ctx, args[1:], dependencies)
	case "list":
		return runServerList(ctx, args[1:], dependencies)
	case "rotate-token":
		return runServerRotateToken(ctx, args[1:], dependencies)
	default:
		return fmt.Errorf("unknown server command %q", args[0])
	}
}

func runServerAdd(ctx context.Context, args []string, dependencies Dependencies) error {
	flags := newFlagSet("server add", dependencies.Stderr)
	name := flags.String("name", "", "server name")
	group := flags.String("group", "", "server group")
	sortOrder := flags.Int("sort-order", 0, "server sort order")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*name) == "" {
		return errors.New("usage: tg-monitor-server server add --name <name> [--group <group>] [--sort-order <n>]")
	}

	return withStore(ctx, dependencies, func(store Store) error {
		rawToken, tokenHash, err := auth.GenerateToken(dependencies.Random)
		if err != nil {
			return err
		}
		server, err := store.CreateServer(ctx, domain.Server{
			Name:        strings.TrimSpace(*name),
			Group:       strings.TrimSpace(*group),
			SortOrder:   *sortOrder,
			Enabled:     true,
			TokenSHA256: tokenHash,
		})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(dependencies.Stdout, "server_id=%d\nagent_token=%s\n", server.ID, rawToken); err != nil {
			return fmt.Errorf("write server credentials: %w", err)
		}
		return nil
	})
}

func runServerList(ctx context.Context, args []string, dependencies Dependencies) error {
	if len(args) != 0 {
		return errors.New("usage: tg-monitor-server server list")
	}
	return withStore(ctx, dependencies, func(store Store) error {
		servers, err := store.ListServers(ctx)
		if err != nil {
			return err
		}
		return encodeIndented(dependencies.Stdout, servers)
	})
}

func runServerRotateToken(ctx context.Context, args []string, dependencies Dependencies) error {
	flags := newFlagSet("server rotate-token", dependencies.Stderr)
	id := flags.Int64("id", 0, "server ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *id <= 0 {
		return errors.New("usage: tg-monitor-server server rotate-token --id <id>")
	}
	return withStore(ctx, dependencies, func(store Store) error {
		rawToken, tokenHash, err := auth.GenerateToken(dependencies.Random)
		if err != nil {
			return err
		}
		if err := store.UpdateServerTokenHash(ctx, *id, tokenHash); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(dependencies.Stdout, "server_id=%d\nagent_token=%s\n", *id, rawToken); err != nil {
			return fmt.Errorf("write rotated credentials: %w", err)
		}
		return nil
	})
}

func runMetrics(ctx context.Context, args []string, dependencies Dependencies) error {
	if len(args) == 0 {
		return errors.New("usage: tg-monitor-server metrics <latest|history>")
	}
	switch args[0] {
	case "latest":
		return runMetricsLatest(ctx, args[1:], dependencies)
	case "history":
		return runMetricsHistory(ctx, args[1:], dependencies)
	default:
		return fmt.Errorf("unknown metrics command %q", args[0])
	}
}

func runMetricsLatest(ctx context.Context, args []string, dependencies Dependencies) error {
	flags := newFlagSet("metrics latest", dependencies.Stderr)
	serverID := flags.Int64("server-id", 0, "server ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *serverID <= 0 {
		return errors.New("usage: tg-monitor-server metrics latest --server-id <id>")
	}
	return withStore(ctx, dependencies, func(store Store) error {
		latest, err := store.GetLatestMetrics(ctx, *serverID)
		if err != nil {
			return err
		}
		return encodeIndented(dependencies.Stdout, latest)
	})
}

func runMetricsHistory(ctx context.Context, args []string, dependencies Dependencies) error {
	flags := newFlagSet("metrics history", dependencies.Stderr)
	serverID := flags.Int64("server-id", 0, "server ID")
	fromMS := flags.Int64("from-ms", 0, "inclusive start in Unix milliseconds")
	toMS := flags.Int64("to-ms", 0, "exclusive end in Unix milliseconds")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *serverID <= 0 || *fromMS < 0 || *toMS <= *fromMS {
		return errors.New("usage: tg-monitor-server metrics history --server-id <id> --from-ms <inclusive> --to-ms <exclusive>")
	}
	return withStore(ctx, dependencies, func(store Store) error {
		history, err := store.QueryMinuteSamples(ctx, *serverID, *fromMS, *toMS)
		if err != nil {
			return err
		}
		return encodeIndented(dependencies.Stdout, history)
	})
}

func withStore(ctx context.Context, dependencies Dependencies, operation func(Store) error) error {
	cfg, err := dependencies.LoadServerConfig()
	if err != nil {
		return err
	}
	store, err := dependencies.OpenStore(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	return errors.Join(operation(store), store.Close())
}

func newFlagSet(name string, output io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	return flags
}

func encodeIndented(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode command output: %w", err)
	}
	return nil
}

func (dependencies Dependencies) withDefaults() Dependencies {
	if dependencies.Stdout == nil {
		dependencies.Stdout = os.Stdout
	}
	if dependencies.Stderr == nil {
		dependencies.Stderr = os.Stderr
	}
	if dependencies.Random == nil {
		dependencies.Random = rand.Reader
	}
	if dependencies.LoadServerConfig == nil {
		dependencies.LoadServerConfig = config.LoadServerRuntimeFromEnv
	}
	if dependencies.LoadApplicationConfig == nil {
		dependencies.LoadApplicationConfig = config.LoadApplicationRuntimeFromEnv
	}
	if dependencies.LoadTelegramConfig == nil {
		dependencies.LoadTelegramConfig = config.LoadTelegramRuntimeFromEnv
	}
	if dependencies.OpenStore == nil {
		dependencies.OpenStore = func(ctx context.Context, path string) (Store, error) {
			return sqlite.Open(ctx, path)
		}
	}
	if dependencies.Serve == nil {
		dependencies.Serve = serve
	}
	if dependencies.NewTelegramClient == nil {
		dependencies.NewTelegramClient = func(token string, timeout time.Duration) (TelegramWebhookClient, error) {
			return telegramapi.New(token, timeout)
		}
	}
	return dependencies
}

func serve(ctx context.Context, cfg config.ApplicationRuntimeConfig, logger *slog.Logger) error {
	app, err := serverapp.New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Server.ListenAddr)
	if err != nil {
		return errors.Join(fmt.Errorf("listen on %s: %w", cfg.Server.ListenAddr, err), app.Close(ctx))
	}
	return app.Serve(ctx, listener)
}
