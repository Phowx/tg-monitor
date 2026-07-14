# tg-monitor

`tg-monitor` is a self-hosted server monitoring project. The current release is the deployable central-server slice: a CGO-free Go binary accepts authenticated metric reports, persists current and UTC-minute data in SQLite, and provides local administration commands.

The Linux Agent, Telegram Bot/WebApp, alert delivery, and browser UI are planned follow-on phases and are not part of this slice yet.

## Current capabilities

- Local server registration and token rotation. Each Agent token is shown once; only its SHA-256 hash is stored.
- `POST /api/v1/metrics` with Bearer authentication, a strict 64 KiB JSON limit, validation, and out-of-order protection.
- `GET /healthz` and SQLite-backed `GET /readyz` probes.
- Latest metrics plus checkpointed UTC-minute history in pure-Go SQLite.
- Startup/daily retention based on the persisted `settings.history_retention_days` value.
- Defensive HTTP timeouts, graceful `SIGINT`/`SIGTERM` shutdown, systemd hardening, and a Caddy TLS example.

The server listens on `127.0.0.1:8080` by default. Do not expose its plain HTTP listener publicly: remote Agents must use TLS through Caddy, nginx, or another trusted reverse proxy.

## Build

Go 1.26 or newer is required. Build both supported Linux targets:

```bash
GO=/path/to/go bash scripts/build-server.sh
ls -lh dist/tg-monitor-server-linux-*
```

For local development:

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./...
PATH=/tmp/go-toolchain/bin:$PATH go vet ./...
CGO_ENABLED=0 /tmp/go-toolchain/bin/go build -o /tmp/tg-monitor-server ./cmd/tg-monitor-server
```

## First Linux host deployment

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

Rotate a compromised or lost token; the old token stops authenticating immediately:

```bash
sudo -u tg-monitor env TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db \
  /usr/local/bin/tg-monitor-server server rotate-token --id 1
```

The replacement token is also displayed exactly once.

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

## Repository layout

- `cmd/tg-monitor-server`: signal-aware executable.
- `internal/httpapi`, `internal/auth`, `internal/monitoring`, `internal/serverapp`, `internal/servercmd`: central-server layers.
- `internal/storage/sqlite`: migrations and persistence.
- `deploy`: systemd and Caddy examples.
- `scripts/build-server.sh`: reproducible Linux builds.

The project is licensed under the MIT License. See `NOTICE` for inspiration attribution.
