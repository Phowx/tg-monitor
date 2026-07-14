# Backend Foundation Design

## Scope

This phase establishes the Go backend foundation for a private Telegram server-monitoring system. It includes shared domain contracts, environment configuration, minute aggregation, SQLite schema management, and the repository operations needed by later HTTP, Agent, Telegram Bot, and frontend phases. Those later integrations are explicitly out of scope.

The module is `github.com/tg-monitor/tg-monitor` with Go directive `1.26.0`. Commands use `/tmp/go-toolchain/bin/go`, which is Go 1.26.5 in the development environment. SQLite must use a pure-Go driver so future builds do not require CGO.

## Package and File Boundaries

- `internal/domain` owns transport-independent server, metric, sample, session, settings, and preference contracts plus metric validation and minute aggregation.
- `internal/config` owns environment parsing, defaults, and validation. It does not open databases or contact external services.
- `internal/storage/sqlite` owns connection policy, migrations, row mapping, persistence errors, and repository operations. Callers depend on domain values rather than SQL rows.
- Root documentation explains the architecture, development checks, licensing, and project inspiration.

Files stay responsibility-focused: domain structs, validation, and aggregation are separate; configuration code is split from its tests; SQLite connection/migration code is separate from server, metric, session, and settings repositories.

## Domain Contracts

All timestamps are UTC Unix milliseconds, sizes and cumulative counters are integer bytes, and percentages are floating-point values in the inclusive range `0..100`.

`MetricReport` contains capture time, CPU percentage, memory total/used bytes, root-disk total/used bytes, load averages for 1/5/15 minutes, cumulative network receive/transmit bytes, receive/transmit bytes per second, uptime seconds, and `SystemInfo` (`hostname`, `os`, `kernel`, `arch`). `Validate` rejects non-positive capture time, NaN or infinity in all floats, percentages outside `0..100`, non-positive totals, used values above totals, negative loads, rates, counters, or uptime, and blank system fields.

`Server` contains ID, name, group, sort order, enabled status, a 32-byte SHA-256 token hash, and created/updated times. Its token hash uses `json:"-"` so accidental JSON serialization cannot expose it. Repository queries order servers by sort order, then name, then ID.

`LatestMetrics` combines a server ID, server receive time, and a full `MetricReport`. `MinuteSample` combines a server ID and UTC minute bucket with averaged CPU, memory-used bytes, disk-used bytes, loads, and network speeds; the last report supplies totals, cumulative counters, uptime, and system metadata.

`MinuteAccumulator` is constructed with any Unix-millisecond timestamp and normalizes it to the start of that UTC minute. `Add` validates and accumulates reports. `Sample(serverID)` returns `(MinuteSample, false)` until a report has been added; afterwards it averages gauges/rates and copies last-report values for totals, counters, uptime, and system metadata.

## Configuration

`LoadFromEnv` reads:

- `TG_MONITOR_DATABASE_PATH` (default `/var/lib/tg-monitor/monitor.db`)
- `TG_MONITOR_PUBLIC_URL` (required absolute HTTP(S) URL)
- `TG_MONITOR_BOT_TOKEN` (required)
- `TG_MONITOR_WEBHOOK_SECRET` (required)
- `TG_MONITOR_ADMIN_TELEGRAM_IDS` (required comma-separated, positive, deduplicated int64 IDs)
- `TG_MONITOR_LISTEN_ADDR` (default `127.0.0.1:8080`, valid host/port)
- `TG_MONITOR_AGENT_DOWNLOAD_BASE_URL` (required absolute HTTP(S) URL)
- Optional duration overrides for session TTL, init-data max age, checkpoint interval, history retention, offline threshold, and alert threshold.

Duration defaults are 12 hours, 5 minutes, 15 seconds, 7 days, 60 seconds, and 120 seconds respectively. Invalid-secret errors identify the environment field but never include secret values. URL validation rejects credentials, fragments, and non-HTTP(S) schemes.

## SQLite Connection and Schema

`sqlite.Open` opens the pure-Go SQLite driver, limits the pool to one connection so connection-scoped pragmas remain reliable, enables WAL mode, foreign keys, and a five-second busy timeout, pings the database, and runs migrations. Repository mutations use single statements or short transactions.

Schema version 1 contains:

- `schema_migrations(version, applied_at_ms)`.
- `servers`, including the private token hash and sort metadata.
- `latest_metrics`, one row per server with the complete report and receive time; deleting a server cascades.
- `metric_samples`, unique by `(server_id, bucket_ms)` with an index for ascending server/range scans; deleting a server cascades.
- `sessions`, keyed by token SHA-256 with creation/expiry indexes.
- `user_preferences`, keyed by Telegram user ID.
- Singleton `settings`, seeded with offline `60`, alert `120`, and retention `7`.
- `alert_states`, one row per server with cascade deletion.
- `alert_outbox`, with optional server association and an index on `(delivered_at_ms, next_attempt_at_ms)`.

Migration application is transactional and recorded in `schema_migrations`; re-running it performs no changes. Foreign-key behavior is verified using real database operations.

## Repository Interfaces and Errors

The SQLite store provides create/list/get/update/delete server operations plus a separate token-hash update; latest-metric upsert/get/list; minute-sample upsert, inclusive-start/exclusive-end range query, and history deletion before a cutoff; settings get/update; opaque session create/get/delete; and Telegram alert-preference get/update.

The repository hashes raw session tokens with SHA-256 before every database lookup or mutation, so raw tokens are never persisted. Session lookup distinguishes missing and expired sessions with `ErrNotFound` and `ErrSessionExpired`; an expired row is rejected even before cleanup. Missing user preferences default to alerts enabled without requiring a row.

SQLite constraint, context, and scan errors are wrapped with operation context while preserving `errors.Is` behavior. Repository APIs never format bot tokens, webhook secrets, raw session tokens, or server token hashes in errors.

## Testing and Verification

Strict TDD is used for every exported behavior: add a focused test, run it and capture the expected failure, implement the minimum behavior, then rerun for green. Tests use temporary SQLite files and cover validation, minute aggregation, config defaults/overrides/secret-safe failures, pragma state, idempotent migration, cascades, server CRUD and ordering, JSON secrecy, latest-metric round trips, minute upsert/range order, cleanup, settings persistence, expired sessions, opaque token storage, and default preferences.

Final verification uses Go 1.26.5:

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./...
PATH=/tmp/go-toolchain/bin:$PATH go vet ./...
```

The full RED/GREEN command history and final outputs are recorded in `.superpowers/sdd/task-1-report.md`. The implementation ends with a diff review for scope, schema consistency, and secret leakage, then a conventional commit on `codex/telegram-monitor`.
