package deploytest

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDeploymentAssetsContainRequiredContracts(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	required := map[string][]string{
		"deploy/systemd/tg-monitor.service": {
			"User=tg-monitor", "Group=tg-monitor", "StateDirectory=tg-monitor", "EnvironmentFile=/etc/tg-monitor/server.env",
			"ExecStart=/usr/local/bin/tg-monitor-server serve", "Restart=on-failure", "NoNewPrivileges=true",
			"PrivateTmp=true", "ProtectSystem=strict", "ProtectHome=true",
		},
		"deploy/systemd/server.env.example": {
			"TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db",
			"TG_MONITOR_LISTEN_ADDR=127.0.0.1:8080",
		},
		"deploy/caddy/Caddyfile.example": {"reverse_proxy 127.0.0.1:8080"},
		"scripts/build-server.sh":        {"set -euo pipefail", "CGO_ENABLED=0", "GOARCH=amd64", "GOARCH=arm64"},
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
