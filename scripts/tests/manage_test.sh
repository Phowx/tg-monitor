#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
temporary_root="$(mktemp -d)"
trap 'rm -rf "$temporary_root"' EXIT

export TG_MONITOR_TEST_MODE=1
export TG_MONITOR_ROOT="$temporary_root/root"
source "$repository_root/scripts/manage.sh"
if grep -Fq 'SELECTED_BINARY="$(download_binary' "$repository_root/scripts/manage.sh"; then
  printf 'FAIL: Release download must preserve WORK_DIR in the current shell\n' >&2
  exit 1
fi

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_eq() {
  [[ "$1" == "$2" ]] || fail "got [$1], want [$2]"
}

assert_exists() {
  [[ -e "$1" ]] || fail "missing $1"
}

assert_absent() {
  [[ ! -e "$1" ]] || fail "unexpected $1"
}

assert_eq "$(detect_arch x86_64)" "amd64"
assert_eq "$(detect_arch aarch64)" "arm64"
if detect_arch riscv64 >/dev/null 2>&1; then
  fail "unsupported architecture accepted"
fi

mkdir -p "$(root_path /etc/tg-monitor)" "$(root_path /var/lib/tg-monitor)" "$(root_path /usr/local/bin)" "$(root_path /etc/systemd/system)"
write_server_env_file "$(root_path /etc/tg-monitor/server.env)" \
  "/var/lib/tg-monitor/monitor.db" "127.0.0.1:8080" "true" \
  "https://monitor.example.com:8443" "BOT-CANARY" "WEBHOOK-CANARY" "12345"
write_agent_env_file "$(root_path /etc/tg-monitor/agent.env)" \
  "https://monitor.example.com:8443" "AGENT-CANARY" "15s" "10s"
assert_eq "$(stat -c '%a' "$(root_path /etc/tg-monitor/server.env)")" "600"
assert_eq "$(stat -c '%a' "$(root_path /etc/tg-monitor/agent.env)")" "600"
grep -q '^TG_MONITOR_BOT_TOKEN=BOT-CANARY$' "$(root_path /etc/tg-monitor/server.env)" || fail "server token missing"
grep -q '^TG_MONITOR_AGENT_TOKEN=AGENT-CANARY$' "$(root_path /etc/tg-monitor/agent.env)" || fail "agent token missing"
grep -q '^TG_MONITOR_CLOUDFLARE_ENABLED=false$' "$(root_path /etc/tg-monitor/server.env)" || fail "Cloudflare safe default missing"

zones='{"example.com":"0123456789abcdef0123456789abcdef"}'
write_cloudflare_env_file "$(root_path /etc/tg-monitor/server.env)" "true" "CF-CANARY" "$zones" "9s"
grep -q '^TG_MONITOR_CLOUDFLARE_ENABLED=true$' "$(root_path /etc/tg-monitor/server.env)" || fail "Cloudflare enable flag missing"
grep -q '^TG_MONITOR_CLOUDFLARE_API_TOKEN=CF-CANARY$' "$(root_path /etc/tg-monitor/server.env)" || fail "Cloudflare token missing"
assert_eq "$(read_env_value "$(root_path /etc/tg-monitor/server.env)" TG_MONITOR_CLOUDFLARE_ZONES)" "$zones"
assert_eq "$(stat -c '%a' "$(root_path /etc/tg-monitor/server.env)")" "600"
validate_cloudflare_zone_name "example.com" || fail "valid Cloudflare zone rejected"
validate_cloudflare_zone_id "0123456789abcdef0123456789abcdef" || fail "valid Cloudflare zone ID rejected"
if validate_cloudflare_zone_name "bad..example"; then fail "invalid Cloudflare zone accepted"; fi
if validate_cloudflare_zone_id "not-a-zone-id"; then fail "invalid Cloudflare zone ID accepted"; fi
cloudflare_summary="$(render_cloudflare_summary true "CF-CANARY" "$zones")"
[[ "$cloudflare_summary" == *"API Token：已设置"* && "$cloudflare_summary" != *"CF-CANARY"* ]] || fail "Cloudflare summary leaked token"

summary="$(render_server_summary "127.0.0.1:8080" "true" "https://monitor.example.com:8443" "BOT-CANARY" "WEBHOOK-CANARY")"
[[ "$summary" == *"Bot Token：已设置"* ]] || fail "summary does not describe token"
[[ "$summary" != *"BOT-CANARY"* && "$summary" != *"WEBHOOK-CANARY"* ]] || fail "summary leaked secret"

touch "$(root_path /usr/local/bin/tg-monitor-server)" "$(root_path /usr/local/bin/tg-monitor-agent)"
touch "$(root_path /etc/systemd/system/tg-monitor.service)" "$(root_path /etc/systemd/system/tg-monitor-agent.service)"
touch "$(root_path /var/lib/tg-monitor/monitor.db)"
uninstall_server_files keep
assert_absent "$(root_path /usr/local/bin/tg-monitor-server)"
assert_absent "$(root_path /etc/systemd/system/tg-monitor.service)"
assert_exists "$(root_path /etc/tg-monitor/server.env)"
assert_exists "$(root_path /var/lib/tg-monitor/monitor.db)"
assert_exists "$(root_path /usr/local/bin/tg-monitor-agent)"

uninstall_agent_files keep
assert_absent "$(root_path /usr/local/bin/tg-monitor-agent)"
assert_exists "$(root_path /etc/tg-monitor/agent.env)"

uninstall_server_files purge
uninstall_agent_files purge
assert_absent "$(root_path /etc/tg-monitor/server.env)"
assert_absent "$(root_path /etc/tg-monitor/agent.env)"
assert_absent "$(root_path /var/lib/tg-monitor)"

help_output="$(show_help)"
for label in "安装或升级 Server" "安装或升级 Agent" "同机安装或升级 Server + Agent" "卸载 Server" "卸载 Agent" "卸载全部组件" "配置/验证 Cloudflare DNS"; do
  [[ "$help_output" == *"$label"* ]] || fail "menu missing $label"
done

printf 'manage_tests=ok\n'
