package deploytest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentDeploymentAssets(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	required := map[string][]string{
		"deploy/systemd/tg-monitor-agent.service": {
			"User=tg-monitor-agent", "Group=tg-monitor-agent",
			"EnvironmentFile=/etc/tg-monitor/agent.env",
			"ExecStart=/usr/local/bin/tg-monitor-agent run",
			"Restart=on-failure", "RestartSec=30s", "NoNewPrivileges=true",
			"PrivateTmp=true", "PrivateDevices=true", "ProtectSystem=strict",
			"ProtectHome=true", "ProtectProc=invisible", "CapabilityBoundingSet=",
		},
		"deploy/systemd/agent.env.example": {
			"TG_MONITOR_AGENT_SERVER_URL=https://monitor.example.com",
			"TG_MONITOR_AGENT_TOKEN=replace-with-one-time-token",
			"TG_MONITOR_AGENT_INTERVAL=15s", "TG_MONITOR_AGENT_HTTP_TIMEOUT=10s",
		},
		"scripts/build-agent.sh": {"set -euo pipefail", "CGO_ENABLED=0", "GOARCH=amd64", "GOARCH=arm64"},
		"scripts/smoke-agent.sh": {
			"tg-monitor-server", "tg-monitor-agent", "registration=ok",
			"ingestion=ok", "agent_sigterm=clean", "token_log_scan=clean",
		},
	}

	for relativePath, substrings := range required {
		contents, err := os.ReadFile(filepath.Join(repositoryRoot, relativePath))
		if err != nil {
			t.Errorf("read %s: %v", relativePath, err)
			continue
		}
		for _, substring := range substrings {
			if !strings.Contains(string(contents), substring) {
				t.Errorf("%s missing %q", relativePath, substring)
			}
		}
	}
}
