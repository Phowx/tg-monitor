package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var configEnvNames = []string{
	"TG_MONITOR_DATABASE_PATH",
	"TG_MONITOR_PUBLIC_URL",
	"TG_MONITOR_BOT_TOKEN",
	"TG_MONITOR_WEBHOOK_SECRET",
	"TG_MONITOR_ADMIN_TELEGRAM_IDS",
	"TG_MONITOR_LISTEN_ADDR",
	"TG_MONITOR_AGENT_DOWNLOAD_BASE_URL",
	"TG_MONITOR_SESSION_TTL",
	"TG_MONITOR_INIT_DATA_MAX_AGE",
	"TG_MONITOR_CHECKPOINT_INTERVAL",
	"TG_MONITOR_HISTORY_RETENTION",
	"TG_MONITOR_OFFLINE_THRESHOLD",
	"TG_MONITOR_ALERT_THRESHOLD",
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	for _, name := range configEnvNames {
		t.Setenv(name, "")
	}
	t.Setenv("TG_MONITOR_PUBLIC_URL", "https://monitor.example.com")
	t.Setenv("TG_MONITOR_BOT_TOKEN", "123456:super-secret-bot-token")
	t.Setenv("TG_MONITOR_WEBHOOK_SECRET", "super-secret-webhook-value")
	t.Setenv("TG_MONITOR_ADMIN_TELEGRAM_IDS", "101, 202")
	t.Setenv("TG_MONITOR_AGENT_DOWNLOAD_BASE_URL", "https://monitor.example.com/agent")
}

func TestLoadFromEnvUsesDefaults(t *testing.T) {
	setRequiredEnv(t)

	got, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if got.DatabasePath != "/var/lib/tg-monitor/monitor.db" {
		t.Errorf("DatabasePath = %q", got.DatabasePath)
	}
	if got.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr = %q", got.ListenAddr)
	}
	if !reflect.DeepEqual(got.AdminTelegramIDs, []int64{101, 202}) {
		t.Errorf("AdminTelegramIDs = %v", got.AdminTelegramIDs)
	}
	durations := map[string]struct {
		got, want time.Duration
	}{
		"session TTL":       {got.SessionTTL, 12 * time.Hour},
		"init-data max age": {got.InitDataMaxAge, 5 * time.Minute},
		"checkpoint":        {got.CheckpointInterval, 15 * time.Second},
		"retention":         {got.HistoryRetention, 7 * 24 * time.Hour},
		"offline":           {got.OfflineThreshold, 60 * time.Second},
		"alert":             {got.AlertThreshold, 120 * time.Second},
	}
	for name, duration := range durations {
		if duration.got != duration.want {
			t.Errorf("%s = %v, want %v", name, duration.got, duration.want)
		}
	}
}

func TestLoadFromEnvUsesOverrides(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("TG_MONITOR_DATABASE_PATH", "/tmp/monitor-custom.db")
	t.Setenv("TG_MONITOR_LISTEN_ADDR", ":9090")
	t.Setenv("TG_MONITOR_SESSION_TTL", "3h")
	t.Setenv("TG_MONITOR_INIT_DATA_MAX_AGE", "90s")
	t.Setenv("TG_MONITOR_CHECKPOINT_INTERVAL", "20s")
	t.Setenv("TG_MONITOR_HISTORY_RETENTION", "48h")
	t.Setenv("TG_MONITOR_OFFLINE_THRESHOLD", "75s")
	t.Setenv("TG_MONITOR_ALERT_THRESHOLD", "3m")

	got, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if got.DatabasePath != "/tmp/monitor-custom.db" || got.ListenAddr != ":9090" {
		t.Fatalf("path/listen overrides not applied: %#v", got)
	}
	want := []time.Duration{3 * time.Hour, 90 * time.Second, 20 * time.Second, 48 * time.Hour, 75 * time.Second, 3 * time.Minute}
	gotDurations := []time.Duration{got.SessionTTL, got.InitDataMaxAge, got.CheckpointInterval, got.HistoryRetention, got.OfflineThreshold, got.AlertThreshold}
	if !reflect.DeepEqual(gotDurations, want) {
		t.Fatalf("duration overrides = %v, want %v", gotDurations, want)
	}
}

func TestLoadFromEnvRejectsInvalidValuesWithoutLeakingSecrets(t *testing.T) {
	tests := []struct {
		name, variable, value string
	}{
		{"missing public URL", "TG_MONITOR_PUBLIC_URL", ""},
		{"non-http public URL", "TG_MONITOR_PUBLIC_URL", "ftp://example.com"},
		{"public URL credentials", "TG_MONITOR_PUBLIC_URL", "https://user:pass@example.com"},
		{"agent URL fragment", "TG_MONITOR_AGENT_DOWNLOAD_BASE_URL", "https://example.com/agent#fragment"},
		{"missing bot token", "TG_MONITOR_BOT_TOKEN", ""},
		{"missing webhook secret", "TG_MONITOR_WEBHOOK_SECRET", ""},
		{"invalid listen", "TG_MONITOR_LISTEN_ADDR", "127.0.0.1"},
		{"invalid listen port", "TG_MONITOR_LISTEN_ADDR", "127.0.0.1:70000"},
		{"blank admin", "TG_MONITOR_ADMIN_TELEGRAM_IDS", "101,,202"},
		{"duplicate admin", "TG_MONITOR_ADMIN_TELEGRAM_IDS", "101,101"},
		{"non-positive admin", "TG_MONITOR_ADMIN_TELEGRAM_IDS", "0"},
		{"invalid duration", "TG_MONITOR_SESSION_TTL", "forever"},
		{"non-positive duration", "TG_MONITOR_ALERT_THRESHOLD", "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(tt.variable, tt.value)
			_, err := LoadFromEnv()
			if err == nil {
				t.Fatal("LoadFromEnv() error = nil, want non-nil")
			}
			message := err.Error()
			for _, secret := range []string{"123456:super-secret-bot-token", "super-secret-webhook-value"} {
				if strings.Contains(message, secret) {
					t.Fatalf("error leaked secret %q: %s", secret, message)
				}
			}
		})
	}
}
