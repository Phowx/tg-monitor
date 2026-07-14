#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repository_root"

GO="${GO:-go}"
work_dir="$(mktemp -d)"
server_pid=""
agent_pid=""

cleanup() {
    if [[ -n "$agent_pid" ]]; then
        kill -TERM "$agent_pid" 2>/dev/null || true
        wait "$agent_pid" 2>/dev/null || true
    fi
    if [[ -n "$server_pid" ]]; then
        kill -TERM "$server_pid" 2>/dev/null || true
        wait "$server_pid" 2>/dev/null || true
    fi
    rm -rf "$work_dir"
}
trap cleanup EXIT

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

server_binary="$work_dir/tg-monitor-server"
agent_binary="$work_dir/tg-monitor-agent"
server_log="$work_dir/server.log"
agent_log="$work_dir/agent.log"
database="$work_dir/monitor.db"
port="$(choose_port)"

CGO_ENABLED=0 "$GO" build -o "$server_binary" ./cmd/tg-monitor-server >/dev/null
CGO_ENABLED=0 "$GO" build -o "$agent_binary" ./cmd/tg-monitor-agent >/dev/null

registration="$(TG_MONITOR_DATABASE_PATH="$database" "$server_binary" server add --name smoke-host --group smoke 2>>"$server_log")"
server_id="$(printf '%s\n' "$registration" | sed -n 's/^server_id=//p')"
agent_token="$(printf '%s\n' "$registration" | sed -n 's/^agent_token=//p')"
unset registration
if [[ -z "$server_id" || -z "$agent_token" ]]; then
    echo "registration=failed" >&2
    exit 1
fi

TG_MONITOR_DATABASE_PATH="$database" \
TG_MONITOR_LISTEN_ADDR="127.0.0.1:$port" \
TG_MONITOR_CHECKPOINT_INTERVAL=100ms \
    "$server_binary" serve >/dev/null 2>>"$server_log" &
server_pid=$!
wait_for_port "$port"
printf '%s\n' 'central_ready=ok'

TG_MONITOR_AGENT_SERVER_URL="http://127.0.0.1:$port" \
TG_MONITOR_AGENT_TOKEN="$agent_token" \
TG_MONITOR_AGENT_INTERVAL=100ms \
TG_MONITOR_AGENT_HTTP_TIMEOUT=2s \
    "$agent_binary" once >/dev/null 2>>"$agent_log"
printf '%s\n' 'agent_once=ok'

latest_output="$(TG_MONITOR_DATABASE_PATH="$database" "$server_binary" metrics latest --server-id "$server_id" 2>>"$server_log")"
if ! grep -Fq '"server_id": '"$server_id" <<<"$latest_output"; then
    echo "latest_check=failed" >&2
    exit 1
fi
unset latest_output
printf '%s\n' 'latest_check=ok'

history_output=""
for ((attempt = 0; attempt < 100; attempt++)); do
    history_output="$(TG_MONITOR_DATABASE_PATH="$database" "$server_binary" metrics history --server-id "$server_id" --from-ms 0 --to-ms 9000000000000000 2>>"$server_log")"
    if grep -Fq '"server_id": '"$server_id" <<<"$history_output"; then
        break
    fi
    sleep 0.05
done
if ! grep -Fq '"server_id": '"$server_id" <<<"$history_output"; then
    echo "history_check=failed" >&2
    exit 1
fi
unset history_output
printf '%s\n' 'history_check=ok'

TG_MONITOR_AGENT_SERVER_URL="http://127.0.0.1:$port" \
TG_MONITOR_AGENT_TOKEN="$agent_token" \
TG_MONITOR_AGENT_INTERVAL=100ms \
TG_MONITOR_AGENT_HTTP_TIMEOUT=2s \
    "$agent_binary" run >/dev/null 2>>"$agent_log" &
agent_pid=$!
sleep 0.35
kill -TERM "$agent_pid"
wait "$agent_pid"
agent_pid=""
printf '%s\n' 'agent_sigterm=ok'

if grep -Fq -- "$agent_token" "$server_log" "$agent_log"; then
    echo "token_log_scan=leak" >&2
    exit 1
fi
unset agent_token
printf '%s\n' 'token_log_scan=clean'
