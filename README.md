# tg-monitor

`tg-monitor` is a self-hosted server monitoring project. The current release includes a deployable central server, a non-root Linux Agent, and optional private Telegram administrator access. Both binaries are CGO-free for amd64 and arm64.

The Telegram webhook, private administrator commands, signed Mini App session bootstrap, embedded operator WebApp, and durable offline/recovery alerts are implemented.

## Current capabilities

- Local server registration and token rotation. Each Agent token is shown once; only its SHA-256 hash is stored.
- A Linux Agent that collects CPU, memory, root filesystem, load, network, uptime, and host identity directly from the kernel.
- `POST /api/v1/metrics` with Bearer authentication, a strict 64 KiB JSON limit, validation, and out-of-order protection.
- `GET /healthz` and SQLite-backed `GET /readyz` probes.
- Latest metrics plus checkpointed UTC-minute history in pure-Go SQLite.
- Startup/daily retention based on the persisted `settings.history_retention_days` value.
- Optional secret-authenticated Telegram webhook handling with update deduplication, administrator allowlisting, `/status`, `/help`, and alert preference commands.
- Durable per-administrator Telegram offline/recovery alerts with restart-safe delivery, exponential retry, and terminal-row cleanup.
- Signed Telegram Mini App login, hash-only trusted sessions, monitoring/administration APIs, an embedded responsive operator UI, and explicit webhook administration commands.
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
TG_MONITOR_TELEGRAM_ENABLED=false
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

## Telegram administrator access

Telegram integration is optional and remains disabled by default. It exposes the webhook, signed session endpoints, embedded `/app*` assets, and authenticated `/api/v1/admin/*` operator API through the same public origin as the readiness and Agent APIs. Keep the application listener on loopback and publish the complete origin through TLS; the reverse proxy must preserve every path.

### 1. Create the bot and prepare secrets

In a private chat with Telegram's `@BotFather`, run `/newbot`, complete the prompts, and store the bot token in a password manager. Obtain each administrator's numeric Telegram user ID through a trusted method; the allowlist accepts comma-separated positive IDs.

Generate a webhook secret using only the Bot API-supported `[A-Za-z0-9_-]` alphabet:

```bash
WEBHOOK_SECRET="$(openssl rand -base64 48 | tr '+/' '_-' | tr -d '=\n')"
printf '%s\n' "$WEBHOOK_SECRET"
unset WEBHOOK_SECRET
```

Edit the root-owned environment file and retain mode `0600`:

```bash
sudoedit /etc/tg-monitor/server.env
sudo chown root:root /etc/tg-monitor/server.env
sudo chmod 0600 /etc/tg-monitor/server.env
```

Uncomment and fill the optional block from `deploy/systemd/server.env.example`:

```dotenv
TG_MONITOR_TELEGRAM_ENABLED=true
TG_MONITOR_PUBLIC_URL=https://monitor.example.com
TG_MONITOR_BOT_TOKEN=<BotFather-token>
TG_MONITOR_WEBHOOK_SECRET=<random-A-Za-z0-9_-secret>
TG_MONITOR_ADMIN_TELEGRAM_IDS=<numeric-user-id>[,<another-id>]
TG_MONITOR_SESSION_TTL=12h
TG_MONITOR_INIT_DATA_MAX_AGE=5m
TG_MONITOR_TELEGRAM_HTTP_TIMEOUT=10s
```

`TG_MONITOR_PUBLIC_URL` must be the public HTTPS origin only, without a path, query, fragment, or credentials.

### 2. Enable routes and register the webhook

Restart the service, verify local readiness, then explicitly register the webhook. Startup never changes Bot API webhook state.

```bash
sudo systemctl restart tg-monitor
curl --fail --silent http://127.0.0.1:8080/readyz
sudo sh -c 'set -a; . /etc/tg-monitor/server.env; set +a; exec runuser --preserve-environment -u tg-monitor -- /usr/local/bin/tg-monitor-server telegram set-webhook'
sudo sh -c 'set -a; . /etc/tg-monitor/server.env; set +a; exec runuser --preserve-environment -u tg-monitor -- /usr/local/bin/tg-monitor-server telegram get-webhook'
```

The commands print `webhook=registered` and safe webhook metadata. They do not open SQLite or print the bot token, webhook secret, or Telegram's last error description.

Configure the persistent Mini App menu in `@BotFather` after the public TLS endpoint is live:

```text
/setmenubutton
<select the bot>
Button text: Open tg-monitor
Web App URL: ${TG_MONITOR_PUBLIC_URL}/app/
```

Replace the variable with the same HTTPS origin configured on the server. The trailing `/app/` is required; do not configure an internal loopback URL.

Open a private chat with the bot from an allowlisted account and send `/status`, then send `/app` and open the returned Web App button. Group chats, channel posts, missing senders, and non-administrators are acknowledged without command side effects. `/help`, `/alerts_on`, and `/alerts_off` are also available.

### 3. Verify the local workflow and logs

The deterministic smoke compiles and runs the unmodified server against a loopback HTTPS CONNECT interceptor with a short-lived CA and `api.telegram.org` certificate. Its final output must be exactly:

```bash
GO=/path/to/go bash scripts/smoke-telegram.sh
```

```text
telegram_webhook=ok duplicate=ok
telegram_webapp=ok
telegram_session=ok logout=ok
telegram_sigterm=clean secret_log_scan=clean
```

For the deterministic real-browser journey, install Node.js/npm only on the development machine, then install the Playwright CLI and its Chromium build. These are test dependencies; the production binaries remain CGO-free and have no Node.js runtime requirement.

```bash
npm install --global @playwright/cli@latest
playwright-cli install-browser chromium --with-deps
GO=/path/to/go bash scripts/smoke-webapp.sh
```

The browser smoke starts the production server on loopback, injects a signed fake Telegram Mini App bridge, and exercises login, 1-hour/7-day history, server create/edit/disable/token rotation/delete, settings, alert preference, keyboard focus, 320px layout, dark theme, and logout. Its final output is:

```text
webapp_browser=ok screenshots=3
keyboard=ok network_origin=clean
secret_log_scan=clean
```

The ignored artifacts are written under `output/playwright/webapp/` as `mobile-light.png`, `mobile-dark.png`, and `desktop-admin.png`. Inspect all three for clipping, overlap, contrast, focus visibility, and dialog overflow before release.

Verify the public shell and its security policy independently of Telegram authentication:

```bash
TG_MONITOR_PUBLIC_URL=https://monitor.example.com
curl --fail --silent --show-error --dump-header /tmp/tg-monitor-webapp.headers \
  --output /tmp/tg-monitor-webapp.html "${TG_MONITOR_PUBLIC_URL}/app/"
grep -Fi 'Content-Security-Policy:' /tmp/tg-monitor-webapp.headers
grep -F '/app/app.js' /tmp/tg-monitor-webapp.html
rm -f /tmp/tg-monitor-webapp.headers /tmp/tg-monitor-webapp.html
```

The response must include the documented `Content-Security-Policy` and reference same-origin CSS/JavaScript assets. A request to plain `/app` should redirect permanently to `/app/`.

Then open the BotFather menu button or the `/app` reply from an allowlisted private chat. In browser developer tools, confirm the signed bootstrap completes, `GET /api/v1/admin/overview` returns 200, the server list renders, and requests remain on the configured origin. Do not copy session cookies or Telegram init data into the shell.

After production verification, scan the journal for a token or secret canary without putting it in shell history:

```bash
read -rsp 'Exact canary to scan for: ' CANARY; echo
if sudo journalctl -u tg-monitor --since '30 minutes ago' --no-pager | grep -F -- "$CANARY"; then
  echo 'secret canary found in journal' >&2
else
  echo 'secret canary absent from journal'
fi
unset CANARY
```

### 4. Disable or roll back Telegram

To stop new Telegram traffic while preserving core monitoring, set `TG_MONITOR_TELEGRAM_ENABLED=false` and restart. This removes the webhook, auth endpoints, embedded `/app*` assets, and `/api/v1/admin/*` routes while readiness and Agent ingestion remain available. Optionally delete the remote webhook while the token values are still present:

```bash
sudo sh -c 'set -a; . /etc/tg-monitor/server.env; set +a; exec runuser --preserve-environment -u tg-monitor -- /usr/local/bin/tg-monitor-server telegram delete-webhook'
sudoedit /etc/tg-monitor/server.env # set TG_MONITOR_TELEGRAM_ENABLED=false
sudo systemctl restart tg-monitor
curl --fail --silent http://127.0.0.1:8080/readyz
```

After restart, verify `/readyz` and Agent ingestion still succeed while `/app/` and `/api/v1/admin/overview` return 404. This WebApp rollback requires no database migration or data deletion.

Schema version 2 is additive: it only adds the Telegram update-deduplication table and index; existing server, metric, session, and preference data are not rewritten. An older binary that supports only schema version 1 still rejects a version-2 database, so restore the pre-upgrade database backup when rolling the binary back across this version boundary.

## Offline and recovery alerts

Durable alerts run in the central server only while Telegram integration is enabled. The worker evaluates immediately at startup and every five seconds, using the server-side metric receive time so an Agent clock cannot manufacture or delay an outage.

The offline threshold, alert threshold, and history retention are persisted in SQLite and edited in the WebApp settings screen. Values are not environment variables. Both thresholds must be between 1 and 86,400 seconds, and the alert threshold cannot be lower than the offline threshold.

- Recipients are the current `TG_MONITOR_ADMIN_TELEGRAM_IDS` allowlist members whose alert preference is enabled. A missing preference defaults to enabled; `/alerts_on`, `/alerts_off`, and the WebApp switch update it.
- Offline and recovery messages are plain text with UTC timestamps. Delivery retries forever at 5 seconds with exponential backoff capped at 15 minutes.
- Disabling or deleting a server, disabling a recipient's alerts, or removing an administrator terminally suppresses matching pending delivery. Suppressed rows are never replayed after re-enabling.
- Delivered and suppressed outbox rows are retained for 30 days and removed by startup/daily access cleanup. Pending retries are never removed by age.

Delivery is at-least-once. If Telegram accepts a request but the process stops before SQLite records success, the retry may produce a duplicate message. The Bot API does not provide a caller idempotency key for `sendMessage`.

Alert logs contain only operation classes and aggregate counts; they omit Telegram user IDs, message text, payload JSON, Bot API response bodies, and dependency error details. Inspect them without exporting the root-owned environment file:

```bash
sudo journalctl -u tg-monitor --since '30 minutes ago' --no-pager
```

The deterministic alert smoke proves first-attempt failure, restart retry, recovery, cleanup, graceful termination, and log-secret safety:

```bash
GO=/path/to/go bash scripts/smoke-alerts.sh
```

```text
alert_offline=ok retry_restart=ok recovery=ok cleanup=ok
alert_sigterm=clean secret_log_scan=clean
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

If verification fails, stop the service, restore `/usr/local/bin/tg-monitor-server.previous`, and start it again. Migrations are forward-only and idempotent. Schema version 2 is additive, but a version-1 binary rejects the newer schema marker; restore the matching pre-upgrade database backup when crossing that boundary.

For a Telegram-only rollback, keep the current binary and database, set `TG_MONITOR_TELEGRAM_ENABLED=false`, optionally run `telegram delete-webhook`, restart, and verify `/readyz` plus Agent ingestion.

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
- `scripts/build-*.sh`, `scripts/smoke-agent.sh`, and `scripts/smoke-telegram.sh`: reproducible builds and real-process smoke verification.

The project is licensed under the MIT License. See `NOTICE` for inspiration attribution.
