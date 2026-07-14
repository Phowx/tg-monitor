# Deployable Central Server Design

## Objective

Build the first deployable vertical slice of `tg-monitor`: a Linux-native central server that can be installed under systemd, register monitored servers locally, accept authenticated metric reports over HTTP, persist latest metrics and minute history in SQLite, and expose enough local administration commands to prove the complete flow on a real machine before a Linux Agent exists.

This is the first of three follow-on projects. The Linux Agent is next; Telegram Bot/WebApp and the full operator UI remain separate later projects. Keeping those concerns out of this specification makes the central protocol and deployment behavior independently testable.

## Approach Decision

Three approaches were considered:

1. **Central server first, validated with `curl` and a local CLI (selected).** This fixes the ingestion contract, authentication, persistence, lifecycle, and deployment surface before an Agent depends on them.
2. **Central server and Agent together.** This produces an earlier visual end-to-end demo, but failures in collection, networking, authentication, and deployment become difficult to isolate.
3. **Docker-first deployment.** This is convenient for some hosts, but it does not match the selected first target of a Linux binary managed directly by systemd.

The selected approach yields a useful, deployable artifact on its own and creates a stable boundary for the Agent phase.

## Scope and Success Criteria

The phase is complete when all of the following are true:

- `tg-monitor-server` builds as a static, CGO-free Linux-compatible Go binary.
- `tg-monitor-server serve` opens the configured SQLite database, applies migrations, starts HTTP with defensive timeouts, and shuts down gracefully on `SIGINT` or `SIGTERM`.
- Local CLI commands can create/list servers, rotate a server token, and show latest metrics and minute history. Raw Agent tokens are generated cryptographically, shown exactly once, and never stored.
- `GET /healthz` reports process liveness and `GET /readyz` verifies SQLite readiness.
- `POST /api/v1/metrics` authenticates an enabled server with a Bearer token, accepts a bounded strict `MetricReport`, records server receive time, updates latest metrics, and contributes to a UTC minute sample.
- A periodic checkpoint and graceful shutdown persist active minute aggregates.
- A retention worker reads the SQLite `settings` row as the single source of truth and removes expired minute history on startup and every 24 hours.
- A systemd unit, environment example, build/install instructions, and reverse-proxy/TLS guidance are present.
- Automated tests cover lifecycle components, token handling, repository lookup, HTTP status/error behavior, ingestion, aggregation checkpoints, and command behavior. `go test ./...`, `go vet ./...`, and `go build ./cmd/tg-monitor-server` pass.

## Architecture and Boundaries

### Executable and Commands

`cmd/tg-monitor-server` is the only new executable. It dispatches these subcommands:

- `serve`
- `server add --name <name> [--group <group>] [--sort-order <n>]`
- `server list`
- `server rotate-token --id <id>`
- `metrics latest --server-id <id>`
- `metrics history --server-id <id> --from-ms <inclusive> --to-ms <exclusive>`

The command layer parses flags, loads only the configuration needed by the selected command, calls focused application services, and formats output. It contains no SQL and no HTTP business logic.

`serve` uses runtime configuration for database path, listen address, and checkpoint interval. Existing full `config.LoadFromEnv` behavior remains unchanged for future Telegram integration. A focused `LoadServerRuntimeFromEnv` is added so this phase does not require unused Telegram secrets. History retention comes only from the persisted `settings.history_retention_days` value, avoiding conflicting environment and database configuration.

### Application Services

`internal/serverapp` owns process composition and lifecycle:

- Opens the SQLite store.
- Constructs token authentication, metric ingestion, the HTTP handler, and the checkpoint worker.
- Starts `http.Server` with read-header, read, write, and idle timeouts.
- Coordinates graceful HTTP shutdown and a final aggregate flush with a ten-second deadline.
- Deletes expired minute history on startup and every 24 hours using the persisted retention setting. Cleanup failures are logged and retried on the next run without stopping ingestion.

`internal/monitoring` owns report ingestion and active minute aggregation. It depends on a small repository interface rather than SQLite directly. Per server, it maintains at most one active UTC minute accumulator:

- A report for the current bucket is added.
- A report for a newer bucket flushes the previous sample and starts the new bucket.
- A report older than the active bucket is rejected as out of order so it cannot corrupt history.
- Every checkpoint snapshots each active accumulator using `UpsertMinuteSample`.
- Shutdown performs the same flush.

Latest metrics are written synchronously before the HTTP request succeeds. Minute samples are operational history; a process restart within a minute can produce a partial sample for that minute because schema version 1 does not persist accumulator counts. This limitation is explicit for the first deployment slice and will be removed only if real-machine testing shows it materially affects alerting or charts.

### Authentication and Token Lifecycle

`internal/auth` generates 32 cryptographically random bytes and encodes them with unpadded URL-safe Base64. It hashes the exact encoded token with SHA-256 for storage.

Server creation and rotation return the raw token to the CLI once. Logs, errors, database rows, JSON, and later list/get operations expose neither the token nor its hash. The SQLite repository adds lookup by token SHA-256; the HTTP authenticator computes the hash and looks up the server. Missing, malformed, and disabled-server credentials produce generic responses that do not reveal which condition occurred.

### SQLite Additions

No schema migration is needed because `servers.token_sha256`, `latest_metrics`, and `metric_samples` already contain the required data. The store adds:

- `GetServerByTokenHash(ctx, hash)` for Agent authentication.
- `Ping(ctx)` for readiness.

The existing CRUD and metric methods remain the only persistence implementation. HTTP handlers never receive the concrete `*sql.DB`.

## HTTP Contract

### Health

`GET /healthz` returns status 200 and:

```json
{"status":"ok"}
```

It is a liveness check and does not query SQLite.

`GET /readyz` returns the same body with status 200 when a context-bounded SQLite ping succeeds; otherwise it returns status 503 with a generic error body.

### Metric Ingestion

`POST /api/v1/metrics` requires:

- `Authorization: Bearer <agent-token>`
- `Content-Type: application/json`
- A body no larger than 64 KiB.
- Exactly one JSON object matching `domain.MetricReport`; unknown fields and trailing JSON values are rejected.

Successful synchronous persistence returns status 204 with no body.

Errors use a stable JSON envelope:

```json
{"error":{"code":"invalid_report","message":"metric report is invalid"}}
```

Status mapping:

- 400: malformed JSON, trailing data, or malformed Authorization syntax.
- 401: missing or unknown credentials.
- 403: disabled server.
- 405: unsupported method, with `Allow`.
- 413: request body exceeds 64 KiB.
- 415: unsupported content type.
- 422: a decoded report fails domain validation.
- 409: report is older than the server's active minute bucket.
- 500: persistence or unexpected internal error, without implementation details.

The HTTP layer records `ReceivedAtMS` from an injected UTC clock. Client capture time remains inside the validated report.

## Logging and Security

The server uses `log/slog` structured logs. Request logs may contain method, route, status, duration, remote address, and authenticated server ID. They never contain the Authorization header, raw body, raw token, token hash, bot token, webhook secret, or SQLite query arguments.

Default listen remains `127.0.0.1:8080`. Remote Agent traffic must terminate TLS at a reverse proxy such as Caddy or nginx; documentation includes a minimal proxy example. Plain HTTP exposure on a public interface is explicitly unsupported because Bearer tokens would be visible in transit.

The server configures:

- Read-header timeout: 5 seconds.
- Read timeout: 15 seconds.
- Write timeout: 15 seconds.
- Idle timeout: 60 seconds.
- Maximum header bytes: 1 MiB.
- Graceful shutdown deadline: 10 seconds.

## Deployment Artifacts

The repository adds:

- `deploy/systemd/tg-monitor.service`
- `deploy/systemd/server.env.example`
- `deploy/caddy/Caddyfile.example`
- `scripts/build-server.sh` for `linux/amd64` and `linux/arm64` CGO-free builds.
- README instructions for creating the `tg-monitor` system user, installing the binary, creating `/var/lib/tg-monitor`, installing the unit/env file, registering the first monitored server, starting the service, and running the smoke test.

The systemd service runs as the dedicated `tg-monitor` user, uses `StateDirectory=tg-monitor`, restarts on failure, and enables practical hardening including `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict`, and `ProtectHome=true`. Secrets and runtime configuration live in `/etc/tg-monitor/server.env` with root-only permissions.

## Real-Machine Smoke Test

The documented first deployment test is deterministic:

1. Build and install `tg-monitor-server`.
2. Run `server add` and save the one-time Agent token.
3. Start the systemd service.
4. Verify `/healthz` and `/readyz` through the local listener.
5. POST a valid fixture report with the Agent token.
6. Run `metrics latest --server-id <id>` and verify the posted hostname, capture time, and gauges.
7. Wait past a checkpoint, then run `metrics history` for the current minute and verify the persisted sample.

Actual deployment to a remote host is outside repository mutation authority until the user provides the target host and SSH access. All files and commands required for that deployment are in scope.

## Testing Strategy

Strict TDD applies to each exported behavior.

- Token tests prove length, URL-safe encoding, uniqueness over repeated generation, hashing consistency, and absence from formatted errors.
- Repository tests prove token-hash lookup, disabled-server retrieval, missing behavior, and readiness ping.
- Monitoring tests use a fake clock and fake repository to prove latest writes, per-minute averaging, checkpoint, rollover, out-of-order rejection, and shutdown flush.
- HTTP tests use `httptest` and a real temporary SQLite store for health/readiness, every status mapping, authentication, 64 KiB enforcement, strict decoding, receive-time assignment, and latest-metric round trip.
- Command tests inject stdout/stderr, randomness, and repository factories to prove flag validation, one-time token output, listing, rotation, latest output, and ordered history output without invoking a real service manager.
- Deployment verification renders no artifacts, but checks shell syntax with `bash -n`; runs `systemd-analyze verify` when available; and validates both target builds.
- Final verification runs formatting, `go test ./...`, `go vet ./...`, both Linux builds, a local process smoke test, and a secret scan before commit.

## Out of Scope

- Linux metric collection and Agent installation.
- Telegram Bot commands, webhook handling, WebApp init-data authentication, and sessions over HTTP.
- Browser UI and history chart endpoints.
- Alert evaluation and outbox delivery workers.
- Automatic TLS certificate management inside the Go process.
- Docker images or Compose deployment.
