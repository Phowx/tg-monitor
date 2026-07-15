package deploytest

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestTelegramEnvironmentExampleIsDisabledAndComplete(t *testing.T) {
	root := telegramRepositoryRoot(t)
	contents := readTelegramAsset(t, root, "deploy/systemd/server.env.example")
	for _, required := range []string{
		"mode 0600",
		"# Optional Telegram administrator access",
		"TG_MONITOR_TELEGRAM_ENABLED=false",
		"# TG_MONITOR_PUBLIC_URL=https://monitor.example.com",
		"# TG_MONITOR_BOT_TOKEN=replace-with-botfather-token",
		"# TG_MONITOR_WEBHOOK_SECRET=replace-with-random-secret",
		"# TG_MONITOR_ADMIN_TELEGRAM_IDS=replace-with-numeric-user-id",
		"# TG_MONITOR_SESSION_TTL=12h",
		"# TG_MONITOR_INIT_DATA_MAX_AGE=5m",
		"# TG_MONITOR_TELEGRAM_HTTP_TIMEOUT=10s",
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("server.env.example missing %q", required)
		}
	}
	tokenShape := regexp.MustCompile(`\b[0-9]{6,12}:[A-Za-z0-9_-]{20,}\b`)
	if tokenShape.MatchString(contents) {
		t.Fatal("server.env.example contains a real token-shaped value")
	}
}

func TestTelegramCaddyExampleForwardsWholeOriginWithoutSensitiveLogging(t *testing.T) {
	root := telegramRepositoryRoot(t)
	contents := readTelegramAsset(t, root, "deploy/caddy/Caddyfile.example")
	if !strings.Contains(contents, "reverse_proxy 127.0.0.1:8080") {
		t.Fatal("Caddy example does not forward the application origin")
	}
	for _, forbidden := range []string{"handle_path", "request_body", "header_up", "log {", "log_append"} {
		if strings.Contains(contents, forbidden) {
			t.Fatalf("Caddy example contains sensitive or path-stripping directive %q", forbidden)
		}
	}
}

func TestTelegramSmokeAssetExercisesRealSecurityBoundaries(t *testing.T) {
	root := telegramRepositoryRoot(t)
	contents := readTelegramAsset(t, root, "scripts/smoke-telegram.sh")
	for _, required := range []string{
		"set -euo pipefail",
		"mktemp -d",
		"trap cleanup EXIT",
		"choose_port",
		"/dev/tcp/127.0.0.1/",
		"CGO_ENABLED=0",
		"tg-monitor-server",
		"monitor.db",
		"fake-telegram-proxy.go",
		"api.telegram.org",
		"CONNECT",
		"HTTPS_PROXY",
		"SSL_CERT_FILE",
		"crypto/hmac",
		"--cookie-jar",
		"--cookie",
		`"update_id":9001`,
		"kill -TERM \"$server_pid\"",
		"wait \"$server_pid\"",
		"grep -R -F",
		"telegram_webhook=ok duplicate=ok",
		"telegram_session=ok logout=ok",
		"telegram_sigterm=clean secret_log_scan=clean",
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("smoke-telegram.sh missing %q", required)
		}
	}
}

func TestTelegramRunbookCoversDeploymentVerificationAndRollback(t *testing.T) {
	root := telegramRepositoryRoot(t)
	contents := readTelegramAsset(t, root, "README.md")
	for _, required := range []string{
		"@BotFather",
		"sudo chmod 0600 /etc/tg-monitor/server.env",
		"[A-Za-z0-9_-]",
		"telegram set-webhook",
		"telegram get-webhook",
		"send `/status`",
		"scripts/smoke-telegram.sh",
		"Exact canary to scan for",
		"TG_MONITOR_TELEGRAM_ENABLED=false",
		"telegram delete-webhook",
		"Schema version 2 is additive",
		"browser WebApp UI and alert delivery remain subsequent phases",
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("README.md missing Telegram runbook contract %q", required)
		}
	}
	if strings.Contains(contents, "The Telegram Bot/WebApp, alert delivery, and browser UI remain planned") {
		t.Fatal("README.md still describes all Telegram capability as planned")
	}
}

func telegramRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
}

func readTelegramAsset(t *testing.T, root, relativePath string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, relativePath))
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	return string(contents)
}
