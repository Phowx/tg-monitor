# tg-monitor

`tg-monitor` is a self-hosted server monitoring project. The current release includes a deployable central server and a non-root Linux Agent. Both are CGO-free Go binaries for amd64 and arm64.

The Telegram Bot/WebApp, alert delivery, and browser UI remain planned follow-on phases.

## Current capabilities

- Local server registration and token rotation. Each Agent token is shown once; only its SHA-256 hash is stored.
- A Linux Agent that collects CPU, memory, root filesystem, load, network, uptime, and host identity directly from the kernel.
- `POST /api/v1/metrics` with Bearer authentication, a strict 64 KiB JSON limit, validation, and out-of-order protection.
- `GET /healthz` and SQLite-backed `GET /readyz` probes.
- Latest metrics plus checkpointed UTC-minute history in pure-Go SQLite.
- Startup/daily retention based on the persisted `settings.history_retention_days` value.
- Non-root hardened systemd services, graceful signals, bounded inputs, TLS 1.2 minimum, and secret-safe transition logs.
- Defensive HTTP timeouts, graceful `SIGINT`/`SIGTERM` shutdown, systemd hardening, and a Caddy TLS example.

The server listens on `127.0.0.1:8080` by default. Do not expose its plain HTTP listener publicly: remote Agents must use TLS through Caddy, nginx, or another trusted reverse proxy.

## Build

Go 1.26 or newer is required. Build both supported Linux targets for the central server and Agent:

```bash
GO=/path/to/go bash scripts/build-server.sh
GO=/path/to/go bash scripts/build-agent.sh
ls -lh dist/tg-monitor-*-linux-*
```

For local development:

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./...
PATH=/tmp/go-toolchain/bin:$PATH go vet ./...
CGO_ENABLED=0 /tmp/go-toolchain/bin/go build -o /tmp/tg-monitor-server ./cmd/tg-monitor-server
CGO_ENABLED=0 /tmp/go-toolchain/bin/go build -o /tmp/tg-monitor-agent ./cmd/tg-monitor-agent
```

## Central server deployment

The examples below assume a systemd-based Linux host and an amd64 binary. Use the arm64 artifact on ARM servers.

### 1. Install the binary and service files

Run from a checked-out release directory:

```bash
sudo useradd --system --home-dir /var/lib/tg-monitor --shell /usr/sbin/nologin tg-monitor 2>/dev/null || true
sudo install -o root -g root -m 0755 dist/tg-monitor-server-linux-amd64 /usr/local/bin/tg-monitor-server
sudo install -d -o root -g root -m 0755 /etc/tg-monitor
sudo install -o root -g root -m 0600 deploy/systemd/server.env.example /etc/tg-monitor/server.env
sudo install -o root -g root -m 0644 deploy/systemd/tg-monitor.service /etc/systemd/system/tg-monitor.service
```

Review `/etc/tg-monitor/server.env`. Its production-safe defaults are:

```dotenv
TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db
TG_MONITOR_LISTEN_ADDR=127.0.0.1:8080
TG_MONITOR_CHECKPOINT_INTERVAL=15s
```

systemd creates `/var/lib/tg-monitor` with the configured service ownership through `StateDirectory=tg-monitor`.

### 2. Start and inspect the central server

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now tg-monitor
sudo systemctl status --no-pager tg-monitor
curl --fail --silent http://127.0.0.1:8080/healthz
curl --fail --silent http://127.0.0.1:8080/readyz
```

Both probes should return `{"status":"ok"}`.

### 3. Register the first monitored server

Run local administration commands as the service user so the SQLite file remains correctly owned:

```bash
sudo -u tg-monitor env TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db \
  /usr/local/bin/tg-monitor-server server add --name first-host --group production --sort-order 1
```

The command prints:

```text
server_id=1
agent_token=<one-time-token>
```

Save the token in a password manager or the future Agent configuration. It cannot be recovered from the database or `server list`.

### 4. Send the deterministic curl smoke report

Paste the one-time value without adding it to the command history:

```bash
read -rsp 'Agent token: ' AGENT_TOKEN; echo
captured_at_ms=$(date +%s%3N)
curl --fail-with-body --request POST http://127.0.0.1:8080/api/v1/metrics \
  --header "Authorization: Bearer ${AGENT_TOKEN}" \
  --header 'Content-Type: application/json' \
  --data @- <<JSON
{
  "captured_at": ${captured_at_ms},
  "cpu_pct": 12.5,
  "memory_total_bytes": 17179869184,
  "memory_used_bytes": 4294967296,
  "root_disk_total_bytes": 107374182400,
  "root_disk_used_bytes": 32212254720,
  "load_1": 0.12,
  "load_5": 0.20,
  "load_15": 0.18,
  "network_rx_total_bytes": 123456789,
  "network_tx_total_bytes": 98765432,
  "network_rx_bytes_per_second": 1024,
  "network_tx_bytes_per_second": 2048,
  "uptime_seconds": 86400,
  "system": {
    "hostname": "first-host",
    "os": "linux",
    "kernel": "6.12.0",
    "arch": "amd64"
  }
}
JSON
unset AGENT_TOKEN
```

A successful report returns HTTP 204 with an empty body.

### 5. Verify latest and minute history

```bash
sudo -u tg-monitor env TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db \
  /usr/local/bin/tg-monitor-server metrics latest --server-id 1

sleep 16
from_ms=$(( (captured_at_ms / 60000) * 60000 ))
to_ms=$(( from_ms + 60000 ))
sudo -u tg-monitor env TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db \
  /usr/local/bin/tg-monitor-server metrics history --server-id 1 --from-ms "${from_ms}" --to-ms "${to_ms}"
```

The latest result should contain `first-host` and the submitted gauges. After one checkpoint interval, history should contain the matching minute bucket.

Inspect structured service logs without exposing Agent tokens:

```bash
sudo journalctl -u tg-monitor --since '10 minutes ago' --no-pager
```

## Linux Agent deployment

Register each monitored machine on the central host with `tg-monitor-server server add` as shown above. Save both the returned `server_id` and the one-time Agent token before leaving the terminal.

On the monitored host, choose `amd64` for Intel/AMD x86-64 or `arm64` for 64-bit ARM, then create the dedicated non-login account and install the matching artifact:

```bash
ARCH=amd64 # change to arm64 on 64-bit ARM
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin tg-monitor-agent 2>/dev/null || true
sudo install -o root -g root -m 0755 "dist/tg-monitor-agent-linux-${ARCH}" /usr/local/bin/tg-monitor-agent
sudo install -d -o root -g root -m 0755 /etc/tg-monitor
sudo install -o root -g root -m 0600 deploy/systemd/agent.env.example /etc/tg-monitor/agent.env
sudo install -o root -g root -m 0644 deploy/systemd/tg-monitor-agent.service /etc/systemd/system/tg-monitor-agent.service
sudoedit /etc/tg-monitor/agent.env
```

Set the public HTTPS origin and the saved one-time token in `agent.env`. The URL must not include `/api/v1/metrics`; the Agent appends it. Plain HTTP is accepted only for literal loopback addresses or `localhost`, never for traffic between hosts.

Test two real samples and one authenticated report before enabling the daemon. This keeps the token out of shell arguments and history:

```bash
sudo sh -c 'set -a; . /etc/tg-monitor/agent.env; set +a; exec runuser -u tg-monitor-agent -- /usr/local/bin/tg-monitor-agent once'
sudo systemctl daemon-reload
sudo systemctl enable --now tg-monitor-agent
sudo systemctl status --no-pager tg-monitor-agent
```

Verify ingestion from the central host, replacing `1` with the saved server ID:

```bash
sudo -u tg-monitor env TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db \
  /usr/local/bin/tg-monitor-server metrics latest --server-id 1
now_ms=$(date +%s%3N)
from_ms=$((now_ms - 3600000))
to_ms=$((now_ms + 60000))
sudo -u tg-monitor env TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db \
  /usr/local/bin/tg-monitor-server metrics history --server-id 1 --from-ms "${from_ms}" --to-ms "${to_ms}"
```

Inspect structured logs on both machines. Agent logs contain only transition classes, never tokens or report bodies:

```bash
sudo journalctl -u tg-monitor-agent --since '10 minutes ago' --no-pager
sudo journalctl -u tg-monitor --since '10 minutes ago' --no-pager
```

For a repository-level real-process check, run the central server and Agent together on loopback. The last line must be `token_log_scan=clean`:

```bash
GO=/path/to/go bash scripts/smoke-agent.sh
```

## TLS with Caddy

Copy `deploy/caddy/Caddyfile.example` into the active Caddy configuration, replace `monitor.example.com` with a DNS name pointing at the host, and reload Caddy. The relevant route is:

```caddyfile
monitor.example.com {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8080
}
```

Verify the public TLS endpoint with `curl --fail https://monitor.example.com/readyz`. Keep port 8080 firewalled from remote networks.

## Administration

List registered servers:

```bash
sudo -u tg-monitor env TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db \
  /usr/local/bin/tg-monitor-server server list
```

Rotate on the central host first and capture the replacement immediately; the old token stops authenticating as soon as this command succeeds:

```bash
sudo -u tg-monitor env TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db \
  /usr/local/bin/tg-monitor-server server rotate-token --id 1
```

The replacement is displayed exactly once. Next, update the root-owned `/etc/tg-monitor/agent.env` on the monitored host, restart the Agent, and verify that fresh data reaches `metrics latest`:

```bash
sudoedit /etc/tg-monitor/agent.env
sudo systemctl restart tg-monitor-agent
sudo systemctl status --no-pager tg-monitor-agent
```

## Upgrade and rollback

Back up SQLite and preserve the current binary before an upgrade:

```bash
sudo systemctl stop tg-monitor
sudo cp --preserve=mode,ownership /var/lib/tg-monitor/monitor.db /var/lib/tg-monitor/monitor.db.backup
sudo cp --preserve=mode,ownership /usr/local/bin/tg-monitor-server /usr/local/bin/tg-monitor-server.previous
sudo install -o root -g root -m 0755 dist/tg-monitor-server-linux-amd64 /usr/local/bin/tg-monitor-server
sudo systemctl start tg-monitor
curl --fail http://127.0.0.1:8080/readyz
```

If verification fails, stop the service, restore `/usr/local/bin/tg-monitor-server.previous`, and start it again. Restore the database backup only when release notes explicitly describe an incompatible migration; current migrations are forward-only and idempotent.

Upgrade the Agent independently on each monitored host, preserving the previous executable:

```bash
ARCH=amd64 # change to arm64 when required
sudo systemctl stop tg-monitor-agent
sudo cp --preserve=mode,ownership /usr/local/bin/tg-monitor-agent /usr/local/bin/tg-monitor-agent.previous
sudo install -o root -g root -m 0755 "dist/tg-monitor-agent-linux-${ARCH}" /usr/local/bin/tg-monitor-agent
sudo systemctl start tg-monitor-agent
sudo systemctl status --no-pager tg-monitor-agent
```

Confirm a new sample with the central `metrics latest` command. To roll back, stop `tg-monitor-agent`, restore `/usr/local/bin/tg-monitor-agent.previous`, start the service, and verify ingestion again. The Agent has no local database to migrate.

## Repository layout

- `cmd/tg-monitor-server` and `cmd/tg-monitor-agent`: signal-aware executables.
- `internal/linuxmetrics`, `internal/agentclient`, `internal/agentapp`, `internal/agentcmd`: Linux Agent layers.
- `internal/httpapi`, `internal/auth`, `internal/monitoring`, `internal/serverapp`, `internal/servercmd`: central-server layers.
- `internal/storage/sqlite`: migrations and persistence.
- `deploy`: hardened systemd and Caddy examples.
- `scripts/build-*.sh` and `scripts/smoke-agent.sh`: reproducible builds and real-process smoke verification.

The project is licensed under the MIT License. See `NOTICE` for inspiration attribution.
