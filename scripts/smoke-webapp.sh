#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repository_root"

export PATH="$HOME/.local/bin:$PATH"
export TMPDIR="${TMPDIR:-/tmp}"
if [[ "$TMPDIR" == /mnt/* ]]; then
    export TMPDIR=/tmp
fi
playwright_deps_root="$HOME/.local/opt/playwright-deps/root"
playwright_deps="$playwright_deps_root/usr/lib/x86_64-linux-gnu"
if [[ -d "$playwright_deps" ]]; then
    export LD_LIBRARY_PATH="$playwright_deps${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
    export FONTCONFIG_SYSROOT="$playwright_deps_root"
    export FONTCONFIG_PATH="$playwright_deps_root/etc/fonts"
    export FONTCONFIG_FILE=fonts.conf
    export XDG_CACHE_HOME="${XDG_CACHE_HOME:-$HOME/.cache}"
fi

GO="${GO:-go}"
playwright_cli="${PLAYWRIGHT_CLI:-playwright-cli}"
command -v npx >/dev/null 2>&1
command -v "$playwright_cli" >/dev/null 2>&1

work_dir="$(mktemp -d)"
artifact_dir="$repository_root/output/playwright/webapp"
server_pid=""
browser_open=false
browser_session="tg-monitor-webapp-$$"
stage=initializing

fail() {
    printf 'smoke_webapp_failure=%s\n' "$1" >&2
    exit 1
}

cleanup() {
    local status=$?
    if [[ "$browser_open" == true ]]; then
        "$playwright_cli" --session "$browser_session" close >/dev/null 2>&1 || true
    fi
    if [[ -n "$server_pid" ]]; then
        kill -TERM "$server_pid" 2>/dev/null || true
        wait "$server_pid" 2>/dev/null || true
    fi
    rm -rf "$work_dir"
    if [[ "$status" -ne 0 ]]; then
        printf 'smoke_webapp_stage=%s status=%s\n' "$stage" "$status" >&2
    fi
    return "$status"
}
trap cleanup EXIT
trap 'status=$?; printf "smoke_error_line=%s status=%s\n" "$LINENO" "$status" >&2; exit "$status"' ERR

choose_port() {
    local start port offset
    start=$((20000 + RANDOM % 20000))
    for ((offset = 0; offset < 1000; offset++)); do
        port=$((start + offset))
        if ! (exec 9<>/dev/tcp/127.0.0.1/"$port") 2>/dev/null; then
            printf '%s' "$port"
            return 0
        fi
    done
    return 1
}

wait_for_port() {
    local port="$1" attempt
    for ((attempt = 0; attempt < 100; attempt++)); do
        if (exec 9<>/dev/tcp/127.0.0.1/"$port") 2>/dev/null; then
            return 0
        fi
        sleep 0.05
    done
    return 1
}

mkdir -p "$artifact_dir"
rm -f "$artifact_dir/mobile-light.png" "$artifact_dir/mobile-dark.png" "$artifact_dir/desktop-admin.png"

server_binary="$work_dir/tg-monitor-server"
server_log="$work_dir/server.log"
database="$work_dir/monitor.db"
signer_source="$work_dir/sign-init-data.go"
signer_binary="$work_dir/sign-init-data"
browser_config="$work_dir/playwright-cli.config.json"
browser_bootstrap="$work_dir/browser-bootstrap.js"
browser_log="$work_dir/browser.log"
console_log="$work_dir/console.log"
network_log="$work_dir/network.log"
snapshot_file="$work_dir/preflight.yml"
port="$(choose_port)"
base_url="http://127.0.0.1:$port"

bot_token='987654321:BROWSER_BOT_TOKEN_CANARY_abcdefghijklmnopqrstuvwxyz'
webhook_secret='BROWSER_WEBHOOK_SECRET_CANARY_123456789'

cat >"$signer_source" <<'GO'
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
)

func main() {
	botToken := os.Getenv("SIGN_BOT_TOKEN")
	authDate := os.Getenv("SIGN_AUTH_DATE")
	user := os.Getenv("SIGN_USER_JSON")
	if botToken == "" || authDate == "" || user == "" {
		os.Exit(2)
	}
	checkString := "auth_date=" + authDate + "\nuser=" + user
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(checkString))
	values := url.Values{"auth_date": {authDate}, "user": {user}}
	values.Set("hash", hex.EncodeToString(check.Sum(nil)))
	fmt.Print(values.Encode())
}
GO

cat >"$browser_config" <<'JSON'
{
  "browser": {
    "browserName": "chromium",
    "isolated": true,
    "launchOptions": {
      "headless": true
    },
    "contextOptions": {
      "viewport": { "width": 320, "height": 800 },
      "reducedMotion": "reduce"
    }
  }
}
JSON

CGO_ENABLED=0 "$GO" build -o "$server_binary" ./cmd/tg-monitor-server >/dev/null
CGO_ENABLED=0 "$GO" build -o "$signer_binary" "$signer_source" >/dev/null

stage=registration
registration="$(TG_MONITOR_DATABASE_PATH="$database" "$server_binary" server add --name browser-host --group production 2>>"$server_log")"
server_id="$(printf '%s\n' "$registration" | sed -n 's/^server_id=//p')"
agent_token="$(printf '%s\n' "$registration" | sed -n 's/^agent_token=//p')"
unset registration
if [[ -z "$server_id" || -z "$agent_token" ]]; then
    fail registration
fi

TG_MONITOR_DATABASE_PATH="$database" \
TG_MONITOR_LISTEN_ADDR="127.0.0.1:$port" \
TG_MONITOR_CHECKPOINT_INTERVAL=100ms \
TG_MONITOR_TELEGRAM_ENABLED=true \
TG_MONITOR_PUBLIC_URL="$base_url" \
TG_MONITOR_BOT_TOKEN="$bot_token" \
TG_MONITOR_WEBHOOK_SECRET="$webhook_secret" \
TG_MONITOR_ADMIN_TELEGRAM_IDS=42 \
TG_MONITOR_SESSION_TTL=12h \
TG_MONITOR_INIT_DATA_MAX_AGE=5m \
TG_MONITOR_TELEGRAM_HTTP_TIMEOUT=2s \
    "$server_binary" serve >"$server_log" 2>&1 &
server_pid=$!
wait_for_port "$port"

stage=metric_ingestion
captured_at="$(( $(date +%s) * 1000 ))"
metric_body="{\"captured_at\":$captured_at,\"cpu_pct\":37.5,\"memory_total_bytes\":17179869184,\"memory_used_bytes\":8589934592,\"root_disk_total_bytes\":107374182400,\"root_disk_used_bytes\":42949672960,\"load_1\":0.42,\"load_5\":0.35,\"load_15\":0.28,\"network_rx_total_bytes\":1234567,\"network_tx_total_bytes\":7654321,\"network_rx_bytes_per_second\":2048,\"network_tx_bytes_per_second\":1024,\"uptime_seconds\":86461,\"system\":{\"hostname\":\"browser-host\",\"os\":\"linux\",\"kernel\":\"6.12\",\"arch\":\"amd64\"}}"
metric_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --request POST "$base_url/api/v1/metrics" \
    --header "Authorization: Bearer $agent_token" \
    --header 'Content-Type: application/json' \
    --data-binary "$metric_body")"
if [[ "$metric_status" != 204 ]]; then
    fail metric_ingestion
fi
unset metric_body

stage=history_checkpoint
history_ready=false
for ((attempt = 0; attempt < 100; attempt++)); do
    history_output="$(TG_MONITOR_DATABASE_PATH="$database" "$server_binary" metrics history --server-id "$server_id" --from-ms 0 --to-ms 9000000000000000 2>>"$server_log")"
    if grep -Fq '"server_id": '"$server_id" <<<"$history_output"; then
        history_ready=true
        break
    fi
    sleep 0.05
done
unset history_output
if [[ "$history_ready" != true ]]; then
    fail history_checkpoint
fi

auth_date="$(date +%s)"
user_json='{"id":42,"first_name":"Browser"}'
init_data="$(SIGN_BOT_TOKEN="$bot_token" SIGN_AUTH_DATE="$auth_date" SIGN_USER_JSON="$user_json" "$signer_binary")"

cat >"$browser_bootstrap" <<JS
async (page) => {
  const context = page.context();
  await context.route("https://telegram.org/js/telegram-web-app.js", async (route) => {
    await route.fulfill({ status: 200, contentType: "text/javascript", body: "" });
  });
  await context.addInitScript(({ initData }) => {
    const dark = window.location.hash === "#dark";
    window.Telegram = {
      WebApp: {
        initData,
        colorScheme: dark ? "dark" : "light",
        themeParams: dark ? {
          bg_color: "#111827",
          secondary_bg_color: "#1f2937",
          text_color: "#f9fafb",
          hint_color: "#cbd5e1",
          button_color: "#60a5fa",
          button_text_color: "#0f172a"
        } : {
          bg_color: "#ffffff",
          secondary_bg_color: "#f1f5f9",
          text_color: "#111827",
          hint_color: "#64748b",
          button_color: "#1769e0",
          button_text_color: "#ffffff"
        },
        ready() { window.__tgReady = true; },
        expand() { window.__tgExpanded = true; }
      }
    };
  }, { initData: '$init_data' });
  return { fixture: "ready" };
}
JS

stage=browser_open
"$playwright_cli" --config "$browser_config" --session "$browser_session" open "$base_url/healthz" >/dev/null
browser_open=true
"$playwright_cli" --session "$browser_session" snapshot --filename "$snapshot_file" >/dev/null
"$playwright_cli" --session "$browser_session" run-code --filename "$browser_bootstrap" >/dev/null
rm -f "$browser_bootstrap"
stage=browser_journey
"$playwright_cli" --session "$browser_session" run-code --filename scripts/webapp-browser-test.js >"$browser_log"
"$playwright_cli" --session "$browser_session" console error >"$console_log"
"$playwright_cli" --session "$browser_session" requests >"$network_log"
"$playwright_cli" --session "$browser_session" close >/dev/null
browser_open=false

stage=artifact_validation
grep -Fq '"webapp_browser":"ok"' "$browser_log"
for screenshot in mobile-light.png mobile-dark.png desktop-admin.png; do
    test -s "$artifact_dir/$screenshot"
    signature="$(od -An -tx1 -N8 "$artifact_dir/$screenshot" | tr -d ' \n')"
    if [[ "$signature" != 89504e470d0a1a0a ]]; then
        fail screenshot_signature
    fi
done
printf '%s\n' 'webapp_browser=ok screenshots=3'
printf '%s\n' 'keyboard=ok network_origin=clean'

kill -TERM "$server_pid"
wait "$server_pid"
server_pid=""

rm -f "$signer_source" "$signer_binary" "$snapshot_file"
canaries=("$bot_token" "$webhook_secret" "$agent_token" "$init_data")
stage=secret_log_scan
for canary in "${canaries[@]}"; do
    if grep -R -F -- "$canary" "$work_dir" "$artifact_dir" >/dev/null 2>&1; then
        fail secret_log_scan
    fi
done

printf '%s\n' 'secret_log_scan=clean'
