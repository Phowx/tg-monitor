package config

import (
	"testing"
	"time"
)

func clearServerRuntimeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TG_MONITOR_DATABASE_PATH", "")
	t.Setenv("TG_MONITOR_LISTEN_ADDR", "")
	t.Setenv("TG_MONITOR_CHECKPOINT_INTERVAL", "")
}

func TestLoadServerRuntimeFromEnvDefaults(t *testing.T) {
	clearServerRuntimeEnv(t)

	got, err := LoadServerRuntimeFromEnv()
	if err != nil {
		t.Fatalf("LoadServerRuntimeFromEnv() error = %v", err)
	}
	want := ServerRuntimeConfig{
		DatabasePath:       "/var/lib/tg-monitor/monitor.db",
		ListenAddr:         "127.0.0.1:8080",
		CheckpointInterval: 15 * time.Second,
	}
	if got != want {
		t.Fatalf("LoadServerRuntimeFromEnv() = %#v, want %#v", got, want)
	}
}

func TestLoadServerRuntimeFromEnvOverrides(t *testing.T) {
	clearServerRuntimeEnv(t)
	t.Setenv("TG_MONITOR_DATABASE_PATH", " /tmp/tg-monitor.db ")
	t.Setenv("TG_MONITOR_LISTEN_ADDR", " :9090 ")
	t.Setenv("TG_MONITOR_CHECKPOINT_INTERVAL", "250ms")

	got, err := LoadServerRuntimeFromEnv()
	if err != nil {
		t.Fatalf("LoadServerRuntimeFromEnv() error = %v", err)
	}
	want := ServerRuntimeConfig{
		DatabasePath:       "/tmp/tg-monitor.db",
		ListenAddr:         ":9090",
		CheckpointInterval: 250 * time.Millisecond,
	}
	if got != want {
		t.Fatalf("LoadServerRuntimeFromEnv() = %#v, want %#v", got, want)
	}
}

func TestLoadServerRuntimeFromEnvRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
	}{
		{name: "listen address", variable: "TG_MONITOR_LISTEN_ADDR", value: "localhost"},
		{name: "listen port", variable: "TG_MONITOR_LISTEN_ADDR", value: "localhost:70000"},
		{name: "duration syntax", variable: "TG_MONITOR_CHECKPOINT_INTERVAL", value: "later"},
		{name: "positive duration", variable: "TG_MONITOR_CHECKPOINT_INTERVAL", value: "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearServerRuntimeEnv(t)
			t.Setenv(tt.variable, tt.value)
			if _, err := LoadServerRuntimeFromEnv(); err == nil {
				t.Fatal("LoadServerRuntimeFromEnv() error = nil, want non-nil")
			}
		})
	}
}
