package config

import (
	"strings"
	"testing"
	"time"
)

func clearAgentEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"TG_MONITOR_AGENT_SERVER_URL",
		"TG_MONITOR_AGENT_TOKEN",
		"TG_MONITOR_AGENT_INTERVAL",
		"TG_MONITOR_AGENT_HTTP_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadAgentFromEnvDefaultsAndNormalizesEndpoint(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("TG_MONITOR_AGENT_SERVER_URL", " https://monitor.example.com/ ")
	t.Setenv("TG_MONITOR_AGENT_TOKEN", " canary-agent-token ")

	got, err := LoadAgentFromEnv()
	if err != nil {
		t.Fatalf("LoadAgentFromEnv() error = %v", err)
	}
	want := AgentConfig{
		EndpointURL: "https://monitor.example.com/api/v1/metrics",
		Token:       "canary-agent-token",
		Interval:    15 * time.Second,
		HTTPTimeout: 10 * time.Second,
	}
	if got != want {
		t.Fatalf("LoadAgentFromEnv() = %#v, want %#v", got, want)
	}
}

func TestLoadAgentFromEnvUsesDurationOverrides(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("TG_MONITOR_AGENT_SERVER_URL", "HTTPS://monitor.example.com")
	t.Setenv("TG_MONITOR_AGENT_TOKEN", "token")
	t.Setenv("TG_MONITOR_AGENT_INTERVAL", "250ms")
	t.Setenv("TG_MONITOR_AGENT_HTTP_TIMEOUT", "3s")

	got, err := LoadAgentFromEnv()
	if err != nil {
		t.Fatalf("LoadAgentFromEnv() error = %v", err)
	}
	if got.EndpointURL != "https://monitor.example.com/api/v1/metrics" || got.Interval != 250*time.Millisecond || got.HTTPTimeout != 3*time.Second {
		t.Fatalf("LoadAgentFromEnv() = %#v", got)
	}
}

func TestLoadAgentFromEnvAllowsOnlyLoopbackHTTP(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "IPv4", url: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080/api/v1/metrics"},
		{name: "IPv6", url: "http://[::1]:8080/", want: "http://[::1]:8080/api/v1/metrics"},
		{name: "localhost case insensitive", url: "http://LOCALHOST:8080", want: "http://LOCALHOST:8080/api/v1/metrics"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAgentEnv(t)
			t.Setenv("TG_MONITOR_AGENT_SERVER_URL", tt.url)
			t.Setenv("TG_MONITOR_AGENT_TOKEN", "token")
			got, err := LoadAgentFromEnv()
			if err != nil {
				t.Fatalf("LoadAgentFromEnv() error = %v", err)
			}
			if got.EndpointURL != tt.want {
				t.Fatalf("EndpointURL = %q, want %q", got.EndpointURL, tt.want)
			}
		})
	}
}

func TestLoadAgentFromEnvRejectsInvalidValuesWithoutTokenLeak(t *testing.T) {
	const canary = "canary-agent-token-must-not-leak"
	tests := []struct {
		name     string
		server   string
		token    string
		variable string
		value    string
	}{
		{name: "missing URL", token: canary},
		{name: "missing token", server: "https://monitor.example.com"},
		{name: "remote HTTP", server: "http://monitor.example.com", token: canary},
		{name: "relative URL", server: "monitor.example.com", token: canary},
		{name: "opaque URL", server: "mailto:monitor@example.com", token: canary},
		{name: "credentials", server: "https://user:pass@monitor.example.com", token: canary},
		{name: "query", server: "https://monitor.example.com?debug=1", token: canary},
		{name: "fragment", server: "https://monitor.example.com#fragment", token: canary},
		{name: "path prefix", server: "https://monitor.example.com/prefix", token: canary},
		{name: "unsupported scheme", server: "ftp://monitor.example.com", token: canary},
		{name: "invalid interval", server: "https://monitor.example.com", token: canary, variable: "TG_MONITOR_AGENT_INTERVAL", value: "later"},
		{name: "zero interval", server: "https://monitor.example.com", token: canary, variable: "TG_MONITOR_AGENT_INTERVAL", value: "0s"},
		{name: "invalid timeout", server: "https://monitor.example.com", token: canary, variable: "TG_MONITOR_AGENT_HTTP_TIMEOUT", value: "none"},
		{name: "zero timeout", server: "https://monitor.example.com", token: canary, variable: "TG_MONITOR_AGENT_HTTP_TIMEOUT", value: "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearAgentEnv(t)
			t.Setenv("TG_MONITOR_AGENT_SERVER_URL", tt.server)
			t.Setenv("TG_MONITOR_AGENT_TOKEN", tt.token)
			if tt.variable != "" {
				t.Setenv(tt.variable, tt.value)
			}
			_, err := LoadAgentFromEnv()
			if err == nil {
				t.Fatal("LoadAgentFromEnv() error = nil, want non-nil")
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("LoadAgentFromEnv() leaked token: %v", err)
			}
		})
	}
}
