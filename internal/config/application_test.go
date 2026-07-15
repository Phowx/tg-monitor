package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var applicationEnvNames = []string{
	"TG_MONITOR_DATABASE_PATH",
	"TG_MONITOR_LISTEN_ADDR",
	"TG_MONITOR_CHECKPOINT_INTERVAL",
	"TG_MONITOR_TELEGRAM_ENABLED",
	"TG_MONITOR_PUBLIC_URL",
	"TG_MONITOR_BOT_TOKEN",
	"TG_MONITOR_WEBHOOK_SECRET",
	"TG_MONITOR_ADMIN_TELEGRAM_IDS",
	"TG_MONITOR_SESSION_TTL",
	"TG_MONITOR_INIT_DATA_MAX_AGE",
	"TG_MONITOR_TELEGRAM_HTTP_TIMEOUT",
}

func clearApplicationEnv(t *testing.T) {
	t.Helper()
	for _, name := range applicationEnvNames {
		t.Setenv(name, "")
	}
}

func setEnabledTelegramEnv(t *testing.T) {
	t.Helper()
	clearApplicationEnv(t)
	t.Setenv("TG_MONITOR_TELEGRAM_ENABLED", "true")
	t.Setenv("TG_MONITOR_PUBLIC_URL", "https://monitor.example.com/")
	t.Setenv("TG_MONITOR_BOT_TOKEN", "123456:canary-bot-token")
	t.Setenv("TG_MONITOR_WEBHOOK_SECRET", "Webhook_Secret-1")
	t.Setenv("TG_MONITOR_ADMIN_TELEGRAM_IDS", "101, 202")
}

func TestLoadApplicationRuntimeFromEnvDefaultsToCoreOnly(t *testing.T) {
	clearApplicationEnv(t)
	t.Setenv("TG_MONITOR_BOT_TOKEN", "ignored-canary-token")
	t.Setenv("TG_MONITOR_WEBHOOK_SECRET", "ignored-canary-secret")

	got, err := LoadApplicationRuntimeFromEnv()
	if err != nil {
		t.Fatalf("LoadApplicationRuntimeFromEnv() error = %v", err)
	}
	if got.Telegram != nil {
		t.Fatalf("Telegram = %#v, want nil", got.Telegram)
	}
	wantServer := ServerRuntimeConfig{
		DatabasePath:       "/var/lib/tg-monitor/monitor.db",
		ListenAddr:         "127.0.0.1:8080",
		CheckpointInterval: 15 * time.Second,
	}
	if got.Server != wantServer {
		t.Fatalf("Server = %#v, want %#v", got.Server, wantServer)
	}
}

func TestLoadApplicationRuntimeFromEnvExplicitFalseRemainsCoreOnly(t *testing.T) {
	clearApplicationEnv(t)
	t.Setenv("TG_MONITOR_TELEGRAM_ENABLED", " false ")
	t.Setenv("TG_MONITOR_PUBLIC_URL", "not a URL")

	got, err := LoadApplicationRuntimeFromEnv()
	if err != nil {
		t.Fatalf("LoadApplicationRuntimeFromEnv() error = %v", err)
	}
	if got.Telegram != nil {
		t.Fatalf("Telegram = %#v, want nil", got.Telegram)
	}
}

func TestLoadApplicationRuntimeFromEnvLoadsEnabledTelegram(t *testing.T) {
	setEnabledTelegramEnv(t)
	t.Setenv("TG_MONITOR_DATABASE_PATH", " /tmp/telegram-monitor.db ")
	t.Setenv("TG_MONITOR_LISTEN_ADDR", " :9090 ")
	t.Setenv("TG_MONITOR_CHECKPOINT_INTERVAL", "20s")
	t.Setenv("TG_MONITOR_SESSION_TTL", "3h")
	t.Setenv("TG_MONITOR_INIT_DATA_MAX_AGE", "90s")
	t.Setenv("TG_MONITOR_TELEGRAM_HTTP_TIMEOUT", "4s")

	got, err := LoadApplicationRuntimeFromEnv()
	if err != nil {
		t.Fatalf("LoadApplicationRuntimeFromEnv() error = %v", err)
	}
	want := ApplicationRuntimeConfig{
		Server: ServerRuntimeConfig{
			DatabasePath:       "/tmp/telegram-monitor.db",
			ListenAddr:         ":9090",
			CheckpointInterval: 20 * time.Second,
		},
		Telegram: &TelegramRuntimeConfig{
			PublicURL:        "https://monitor.example.com",
			BotToken:         "123456:canary-bot-token",
			WebhookSecret:    "Webhook_Secret-1",
			AdminTelegramIDs: []int64{101, 202},
			SessionTTL:       3 * time.Hour,
			InitDataMaxAge:   90 * time.Second,
			HTTPTimeout:      4 * time.Second,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadApplicationRuntimeFromEnv() = %#v, want %#v", got, want)
	}
}

func TestLoadTelegramRuntimeFromEnvUsesDefaultsAndLoopbackHTTP(t *testing.T) {
	tests := []string{
		"http://localhost:8080",
		"http://LOCALHOST:8080/",
		"http://127.0.0.1:8080",
		"http://[::1]:8080/",
	}
	for _, publicURL := range tests {
		t.Run(publicURL, func(t *testing.T) {
			setEnabledTelegramEnv(t)
			t.Setenv("TG_MONITOR_PUBLIC_URL", publicURL)

			got, err := LoadTelegramRuntimeFromEnv()
			if err != nil {
				t.Fatalf("LoadTelegramRuntimeFromEnv() error = %v", err)
			}
			if got.SessionTTL != 12*time.Hour || got.InitDataMaxAge != 5*time.Minute || got.HTTPTimeout != 10*time.Second {
				t.Fatalf("duration defaults = %v/%v/%v", got.SessionTTL, got.InitDataMaxAge, got.HTTPTimeout)
			}
			if strings.HasSuffix(got.PublicURL, "/") {
				t.Fatalf("PublicURL = %q, want origin without trailing slash", got.PublicURL)
			}
		})
	}
}

func TestLoadApplicationRuntimeFromEnvRejectsInvalidEnableFlag(t *testing.T) {
	for _, value := range []string{"1", "yes", "TRUE", "enabled"} {
		t.Run(value, func(t *testing.T) {
			clearApplicationEnv(t)
			t.Setenv("TG_MONITOR_TELEGRAM_ENABLED", value)
			if _, err := LoadApplicationRuntimeFromEnv(); err == nil {
				t.Fatalf("LoadApplicationRuntimeFromEnv(%q) error = nil", value)
			}
		})
	}
}

func TestLoadTelegramRuntimeFromEnvRejectsInvalidValuesWithoutLeakingSecrets(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
	}{
		{name: "missing public URL", variable: "TG_MONITOR_PUBLIC_URL", value: ""},
		{name: "remote HTTP", variable: "TG_MONITOR_PUBLIC_URL", value: "http://monitor.example.com"},
		{name: "credentials", variable: "TG_MONITOR_PUBLIC_URL", value: "https://user:pass@monitor.example.com"},
		{name: "query", variable: "TG_MONITOR_PUBLIC_URL", value: "https://monitor.example.com/?debug=1"},
		{name: "fragment", variable: "TG_MONITOR_PUBLIC_URL", value: "https://monitor.example.com/#fragment"},
		{name: "non-root path", variable: "TG_MONITOR_PUBLIC_URL", value: "https://monitor.example.com/app"},
		{name: "relative URL", variable: "TG_MONITOR_PUBLIC_URL", value: "/monitor"},
		{name: "missing bot token", variable: "TG_MONITOR_BOT_TOKEN", value: ""},
		{name: "missing webhook secret", variable: "TG_MONITOR_WEBHOOK_SECRET", value: ""},
		{name: "secret spaces", variable: "TG_MONITOR_WEBHOOK_SECRET", value: "bad secret"},
		{name: "secret punctuation", variable: "TG_MONITOR_WEBHOOK_SECRET", value: "bad.secret"},
		{name: "secret too long", variable: "TG_MONITOR_WEBHOOK_SECRET", value: strings.Repeat("a", 257)},
		{name: "blank admin", variable: "TG_MONITOR_ADMIN_TELEGRAM_IDS", value: ""},
		{name: "empty admin", variable: "TG_MONITOR_ADMIN_TELEGRAM_IDS", value: "101,,202"},
		{name: "duplicate admin", variable: "TG_MONITOR_ADMIN_TELEGRAM_IDS", value: "101,101"},
		{name: "non-positive admin", variable: "TG_MONITOR_ADMIN_TELEGRAM_IDS", value: "0"},
		{name: "session duration syntax", variable: "TG_MONITOR_SESSION_TTL", value: "forever"},
		{name: "session duration positive", variable: "TG_MONITOR_SESSION_TTL", value: "0s"},
		{name: "init age positive", variable: "TG_MONITOR_INIT_DATA_MAX_AGE", value: "-1s"},
		{name: "HTTP timeout positive", variable: "TG_MONITOR_TELEGRAM_HTTP_TIMEOUT", value: "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnabledTelegramEnv(t)
			t.Setenv(tt.variable, tt.value)
			_, err := LoadTelegramRuntimeFromEnv()
			if err == nil {
				t.Fatal("LoadTelegramRuntimeFromEnv() error = nil")
			}
			for _, secret := range []string{"123456:canary-bot-token", "Webhook_Secret-1"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestValidateWebhookPublicURLRequiresHTTPS(t *testing.T) {
	if err := ValidateWebhookPublicURL("https://monitor.example.com"); err != nil {
		t.Fatalf("ValidateWebhookPublicURL(HTTPS) error = %v", err)
	}
	for _, raw := range []string{
		"http://localhost:8080",
		"http://127.0.0.1:8080",
		"https://monitor.example.com/path",
	} {
		if err := ValidateWebhookPublicURL(raw); err == nil {
			t.Fatalf("ValidateWebhookPublicURL(%q) error = nil", raw)
		}
	}
}
