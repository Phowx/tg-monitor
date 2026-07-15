package servercmd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/config"
	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/telegramapi"
)

type telegramWebhookClientStub struct {
	setCalls     int
	setPublicURL string
	setSecret    string
	setErr       error
	getCalls     int
	getInfo      telegramapi.WebhookInfo
	getErr       error
	deleteCalls  int
	deleteErr    error
}

func (stub *telegramWebhookClientStub) SetWebhook(_ context.Context, publicURL, secret string) error {
	stub.setCalls++
	stub.setPublicURL = publicURL
	stub.setSecret = secret
	return stub.setErr
}

func (stub *telegramWebhookClientStub) GetWebhookInfo(context.Context) (telegramapi.WebhookInfo, error) {
	stub.getCalls++
	return stub.getInfo, stub.getErr
}

func (stub *telegramWebhookClientStub) DeleteWebhook(context.Context) error {
	stub.deleteCalls++
	return stub.deleteErr
}

type telegramCommandHarness struct {
	stdout bytes.Buffer
	stderr bytes.Buffer
	store  *commandStoreStub
	client *telegramWebhookClientStub

	serverConfig       config.ServerRuntimeConfig
	applicationConfig  config.ApplicationRuntimeConfig
	telegramConfig     config.TelegramRuntimeConfig
	serverLoadErr      error
	applicationLoadErr error
	telegramLoadErr    error
	clientCreateErr    error

	serverLoadCalls      int
	applicationLoadCalls int
	telegramLoadCalls    int
	openCalls            int
	serveCalls           int
	clientCreateCalls    int
	servedConfig         config.ApplicationRuntimeConfig
	clientToken          string
	clientTimeout        time.Duration
}

func newTelegramCommandHarness() *telegramCommandHarness {
	serverConfig := config.ServerRuntimeConfig{
		DatabasePath:       "/tmp/test-monitor.db",
		ListenAddr:         "127.0.0.1:9090",
		CheckpointInterval: 25 * time.Second,
	}
	telegramConfig := config.TelegramRuntimeConfig{
		PublicURL:        "https://monitor.example.com",
		BotToken:         "123456:BOT-TOKEN-CANARY",
		WebhookSecret:    "WEBHOOK_SECRET_CANARY",
		AdminTelegramIDs: []int64{42},
		SessionTTL:       12 * time.Hour,
		InitDataMaxAge:   5 * time.Minute,
		HTTPTimeout:      9 * time.Second,
	}
	return &telegramCommandHarness{
		store:             &commandStoreStub{},
		client:            &telegramWebhookClientStub{},
		serverConfig:      serverConfig,
		applicationConfig: config.ApplicationRuntimeConfig{Server: serverConfig},
		telegramConfig:    telegramConfig,
	}
}

func (harness *telegramCommandHarness) dependencies() Dependencies {
	return Dependencies{
		Stdout: &harness.stdout,
		Stderr: &harness.stderr,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 64)),
		LoadServerConfig: func() (config.ServerRuntimeConfig, error) {
			harness.serverLoadCalls++
			return harness.serverConfig, harness.serverLoadErr
		},
		LoadApplicationConfig: func() (config.ApplicationRuntimeConfig, error) {
			harness.applicationLoadCalls++
			return harness.applicationConfig, harness.applicationLoadErr
		},
		LoadTelegramConfig: func() (config.TelegramRuntimeConfig, error) {
			harness.telegramLoadCalls++
			return harness.telegramConfig, harness.telegramLoadErr
		},
		OpenStore: func(context.Context, string) (Store, error) {
			harness.openCalls++
			return harness.store, nil
		},
		Serve: func(_ context.Context, cfg config.ApplicationRuntimeConfig, _ *slog.Logger) error {
			harness.serveCalls++
			harness.servedConfig = cfg
			return nil
		},
		NewTelegramClient: func(token string, timeout time.Duration) (TelegramWebhookClient, error) {
			harness.clientCreateCalls++
			harness.clientToken = token
			harness.clientTimeout = timeout
			return harness.client, harness.clientCreateErr
		},
	}
}

func TestServeUsesOnlyApplicationConfig(t *testing.T) {
	harness := newTelegramCommandHarness()
	if err := Run(context.Background(), []string{"serve"}, harness.dependencies()); err != nil {
		t.Fatalf("Run(serve) error = %v", err)
	}
	if harness.applicationLoadCalls != 1 || harness.serveCalls != 1 || harness.servedConfig != harness.applicationConfig {
		t.Fatalf("application loads/serve/config = %d/%d/%#v", harness.applicationLoadCalls, harness.serveCalls, harness.servedConfig)
	}
	assertOnlyExpectedCommandDependencies(t, harness, "application")
}

func TestServerAndMetricsUseOnlyServerConfig(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "server", args: []string{"server", "list"}},
		{name: "metrics", args: []string{"metrics", "latest", "--server-id", "7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newTelegramCommandHarness()
			harness.store.latest = domain.LatestMetrics{ServerID: 7}
			if err := Run(context.Background(), tt.args, harness.dependencies()); err != nil {
				t.Fatalf("Run(%v) error = %v", tt.args, err)
			}
			if harness.serverLoadCalls != 1 || harness.openCalls != 1 {
				t.Fatalf("server loads/open = %d/%d", harness.serverLoadCalls, harness.openCalls)
			}
			assertOnlyExpectedCommandDependencies(t, harness, "server")
		})
	}
}

func TestTelegramSetWebhookValidatesHTTPSAndRegisters(t *testing.T) {
	t.Run("invalid public URL before client", func(t *testing.T) {
		harness := newTelegramCommandHarness()
		harness.telegramConfig.PublicURL = "http://127.0.0.1"
		err := Run(context.Background(), []string{"telegram", "set-webhook"}, harness.dependencies())
		if err == nil {
			t.Fatal("Run(telegram set-webhook) error = nil")
		}
		if harness.telegramLoadCalls != 1 || harness.clientCreateCalls != 0 || harness.openCalls != 0 {
			t.Fatalf("loads/client/open = %d/%d/%d", harness.telegramLoadCalls, harness.clientCreateCalls, harness.openCalls)
		}
	})

	t.Run("register", func(t *testing.T) {
		harness := newTelegramCommandHarness()
		if err := Run(context.Background(), []string{"telegram", "set-webhook"}, harness.dependencies()); err != nil {
			t.Fatalf("Run(telegram set-webhook) error = %v", err)
		}
		if harness.client.setCalls != 1 || harness.client.setPublicURL != harness.telegramConfig.PublicURL || harness.client.setSecret != harness.telegramConfig.WebhookSecret {
			t.Fatalf("SetWebhook calls/url/secret = %d/%q/%q", harness.client.setCalls, harness.client.setPublicURL, harness.client.setSecret)
		}
		if harness.clientToken != harness.telegramConfig.BotToken || harness.clientTimeout != harness.telegramConfig.HTTPTimeout {
			t.Fatalf("client token/timeout = %q/%v", harness.clientToken, harness.clientTimeout)
		}
		if got := harness.stdout.String(); got != "webhook=registered\n" {
			t.Fatalf("stdout = %q", got)
		}
		assertOnlyExpectedCommandDependencies(t, harness, "telegram")
	})
}

func TestTelegramGetAndDeleteWebhook(t *testing.T) {
	t.Run("get", func(t *testing.T) {
		harness := newTelegramCommandHarness()
		harness.client.getInfo = telegramapi.WebhookInfo{
			URL: "https://monitor.example.com/telegram/webhook", PendingUpdateCount: 3, LastErrorDate: 1_700_000_000,
		}
		if err := Run(context.Background(), []string{"telegram", "get-webhook"}, harness.dependencies()); err != nil {
			t.Fatalf("Run(telegram get-webhook) error = %v", err)
		}
		want := "{\n  \"url\": \"https://monitor.example.com/telegram/webhook\",\n  \"pending_update_count\": 3,\n  \"last_error_date\": 1700000000\n}\n"
		if harness.client.getCalls != 1 || harness.stdout.String() != want {
			t.Fatalf("get calls/output = %d/%q", harness.client.getCalls, harness.stdout.String())
		}
		assertOnlyExpectedCommandDependencies(t, harness, "telegram")
	})

	t.Run("delete", func(t *testing.T) {
		harness := newTelegramCommandHarness()
		if err := Run(context.Background(), []string{"telegram", "delete-webhook"}, harness.dependencies()); err != nil {
			t.Fatalf("Run(telegram delete-webhook) error = %v", err)
		}
		if harness.client.deleteCalls != 1 || harness.stdout.String() != "webhook=deleted\n" {
			t.Fatalf("delete calls/output = %d/%q", harness.client.deleteCalls, harness.stdout.String())
		}
		assertOnlyExpectedCommandDependencies(t, harness, "telegram")
	})
}

func TestTelegramErrorsDoNotExposeSecrets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*telegramCommandHarness)
		args   []string
	}{
		{
			name: "configuration", args: []string{"telegram", "get-webhook"},
			mutate: func(harness *telegramCommandHarness) { harness.telegramLoadErr = errors.New("CONFIG-ERROR-CANARY") },
		},
		{
			name: "client creation", args: []string{"telegram", "get-webhook"},
			mutate: func(harness *telegramCommandHarness) { harness.clientCreateErr = errors.New("CLIENT-ERROR-CANARY") },
		},
		{
			name: "operation", args: []string{"telegram", "set-webhook"},
			mutate: func(harness *telegramCommandHarness) { harness.client.setErr = errors.New("OPERATION-ERROR-CANARY") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newTelegramCommandHarness()
			tt.mutate(harness)
			err := Run(context.Background(), tt.args, harness.dependencies())
			if err == nil {
				t.Fatalf("Run(%v) error = nil", tt.args)
			}
			combined := err.Error() + harness.stdout.String() + harness.stderr.String()
			for _, secret := range []string{harness.telegramConfig.BotToken, harness.telegramConfig.WebhookSecret} {
				if strings.Contains(combined, secret) {
					t.Fatalf("error/output leaked %q: %q", secret, combined)
				}
			}
		})
	}
}

func TestInvalidTelegramCommandsFailBeforeDependencies(t *testing.T) {
	for _, args := range [][]string{
		{"telegram"},
		{"telegram", "unknown"},
		{"telegram", "set-webhook", "extra"},
		{"telegram", "get-webhook", "extra"},
		{"telegram", "delete-webhook", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			harness := newTelegramCommandHarness()
			if err := Run(context.Background(), args, harness.dependencies()); err == nil {
				t.Fatalf("Run(%v) error = nil", args)
			}
			assertOnlyExpectedCommandDependencies(t, harness, "none")
		})
	}
}

func assertOnlyExpectedCommandDependencies(t *testing.T, harness *telegramCommandHarness, expected string) {
	t.Helper()
	if expected != "server" && (harness.serverLoadCalls != 0 || harness.openCalls != 0) {
		t.Fatalf("unexpected server dependencies: load=%d open=%d", harness.serverLoadCalls, harness.openCalls)
	}
	if expected != "application" && (harness.applicationLoadCalls != 0 || harness.serveCalls != 0) {
		t.Fatalf("unexpected application dependencies: load=%d serve=%d", harness.applicationLoadCalls, harness.serveCalls)
	}
	if expected != "telegram" && (harness.telegramLoadCalls != 0 || harness.clientCreateCalls != 0) {
		t.Fatalf("unexpected Telegram dependencies: load=%d client=%d", harness.telegramLoadCalls, harness.clientCreateCalls)
	}
}
