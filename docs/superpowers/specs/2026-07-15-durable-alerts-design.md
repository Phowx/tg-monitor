# tg-monitor Durable Offline and Recovery Alerts Design

**Date:** 2026-07-15

## Purpose and Phase Boundary

The central server, Linux Agent, private Telegram administrator access, signed Mini App sessions, and embedded operator WebApp are implemented and pushed. This phase turns the existing `alert_states`, `alert_outbox`, administrator allowlist, alert preferences, and Telegram transport into durable offline and recovery notifications.

Success means every enabled server produces at most one offline transition for a continuous outage, every administrator who is currently allowlisted and has alerts enabled receives durable Telegram delivery, and a later report produces one recovery transition. Evaluation and queue creation survive process restarts without duplicate transition rows. Delivery is intentionally at-least-once because Telegram does not expose an idempotency key for `sendMessage`.

Actual deployment to a remote host remains a separate operational step requiring the target host and SSH access or an equivalent authenticated deployment path.

## Approaches Considered

### 1. Transactional transition evaluator plus per-recipient outbox — selected

A focused `internal/alerting` service asks the SQLite repository to evaluate all enabled servers and persist state transitions plus one immutable outbox row per recipient in a single transaction. A separate dispatcher reads due rows, sends plain Telegram messages, and records success or a bounded exponential retry.

This directly uses the schema already reserved for alerts, preserves transition state across restarts, isolates Telegram latency from evaluation, supports per-administrator preferences, and keeps the HTTP ingestion path fast.

### 2. Send directly from metric ingestion

Direct delivery would notice recovery quickly, but offline detection has no incoming request to trigger it. Telegram latency and failure would also become part of the Agent ingestion path, and process restarts could duplicate or lose messages.

### 3. Stateless periodic scans with only in-memory deduplication

A stateless scan is easy to implement, but it repeats alerts after every restart and loses delivery attempts when Telegram is unavailable. It does not meet the existing durable-outbox schema or the deployment reliability goal.

## Alert Semantics

Only enabled servers participate. For each server, the inactivity baseline is the latest server-side `received_at_ms`; before the first report, it is the server's `created_at_ms`. Agent-provided capture time is never trusted for availability decisions.

The persisted settings remain the single runtime source of truth:

- `offline_threshold_seconds` controls dashboard and `/status` presentation.
- `alert_threshold_seconds` controls alert creation.
- updates require `alert_threshold_seconds >= offline_threshold_seconds`, so a notification cannot precede the visible offline state.

An enabled server becomes alertable when `now_ms - baseline_ms >= alert_threshold_seconds * 1000`. Crossing that boundary atomically records `offline_since_ms = baseline_ms`, records `alert_sent_at_ms = now_ms`, and enqueues one `offline` row for each current recipient. Repeated evaluations during the same outage do not enqueue again.

If a report newer than `offline_since_ms` arrives:

- no recovery is emitted when the outage ended before an offline alert was created;
- otherwise `recovered_at_ms = now_ms` is recorded and one `recovery` row is queued for each current recipient;
- offline state fields are reset so a later outage can produce a new pair.

Disabling a server atomically resets its alert state and suppresses pending, undelivered rows for that server without emitting recovery. Re-enabling records a fresh episode baseline in `alert_states.updated_at_ms`; evaluation uses the later of that baseline and the most recent report, giving a full alert-threshold grace period without treating an ordinary name/group edit as activity. Server deletion continues to cascade state and sets historical outbox associations to null through the existing foreign keys.

Lowering a threshold may cause an immediate transition on the next evaluation. Raising it never retracts a transition already queued. Wall-clock rollback cannot create a transition until the clock again reaches the stored baseline and threshold.

## Recipients and Preferences

Recipients are the unique positive IDs in the currently loaded `TG_MONITOR_ADMIN_TELEGRAM_IDS` allowlist. For each evaluation, a missing `user_preferences` row means alerts enabled, matching the existing command and WebApp behavior. An explicit disabled preference excludes only that administrator.

The dispatcher rechecks both the current allowlist and current preference immediately before sending. If a recipient was removed or disabled alerts after queue creation, the row is terminally suppressed instead of sent. Re-enabling alerts does not replay suppressed historical notifications.

Recovery rows are created only for current recipients for whom the corresponding offline row was created. Rows for one recipient and server are delivered in outbox ID order, so a recovery cannot overtake its offline message. If Telegram remains unavailable through the recovery, the eventual delivery order is offline then recovery; the timestamps in each immutable payload make the historical sequence explicit.

## Durable Storage Contract

The existing tables are retained. New repository methods own transactions and do not expose `*sql.Tx` outside `internal/storage/sqlite`.

`alert_states` is the transition guard. `alert_outbox` stores one recipient-specific event with:

- `kind` restricted by production code to `offline` or `recovery`;
- `payload_json` containing a versioned, immutable message payload;
- `attempts`, `next_attempt_at_ms`, `delivered_at_ms`, and a safe error class;
- no Bot token, webhook secret, Agent token, session token, metric body, or raw Telegram response.

Evaluation reads settings, ordered servers, latest receive times, preferences, and states inside one write transaction. State mutation and all recipient rows commit together. A failure rolls back the whole transition.

The dispatcher leases no row because one process owns one sequential dispatcher. It fetches a bounded due batch ordered by `id`, and a row is due only when no earlier undelivered row exists for the same recipient and server. Each send is followed by exactly one repository update:

- success sets `delivered_at_ms` and clears `last_error`;
- suppression sets `delivered_at_ms` and a safe terminal class;
- failure increments `attempts`, records a safe class, and advances `next_attempt_at_ms`.

This is at-least-once, not exactly-once: a process can terminate after Telegram accepts a request but before SQLite records success. The next process may repeat that message. The design prefers a possible duplicate over losing an outage notification.

Delivered and suppressed outbox rows older than 30 days are removed by the existing daily cleanup worker. Active retry rows are never removed by age.

## Message Payload and Rendering

Payload JSON is versioned and contains only the values needed to render a stable message:

```json
{
  "version": 1,
  "server_name": "node-1",
  "server_group": "production",
  "offline_since_ms": 1784080000000,
  "event_at_ms": 1784080120000
}
```

Recovery also includes `recovered_at_ms`. Names and groups are snapshotted at transition time so later edits do not rewrite queued history. Repository validation bounds JSON size and rejects malformed payloads before delivery.

Messages use plain text through the existing `SendMessage` method with no Telegram parse mode:

```text
🔴 node-1 is offline
Group: production
Last received: 2026-07-15 01:46:40 UTC
Alerted after: 2m0s
```

```text
🟢 node-1 recovered
Group: production
Recovered: 2026-07-15 01:51:10 UTC
Outage duration: 4m30s
```

The optional group line is omitted when empty. Durations are non-negative and rounded down to seconds. Rendering never includes host telemetry, URLs, tokens, recipient IDs, dependency errors, or untrusted Telegram markup.

## Workers, Retry, and Lifecycle

Alert workers exist only when Telegram integration is enabled. Application composition passes the same Telegram sender used by webhook replies, the configured administrator IDs, the store, the clock, and the logger to `internal/alerting`.

At startup the worker evaluates once and then attempts due delivery. It repeats every five seconds. Evaluation failure does not block delivery, and delivery failure does not block later server evaluation. Work is sequential and bounded to avoid overlapping scans or unbounded goroutines.

Retry delay after the failed attempt is deterministic exponential backoff:

```text
5s, 10s, 20s, 40s, 80s, ... capped at 15m
```

Retries continue indefinitely at the cap. Telegram transport details are reduced to the safe class `telegram_send_failed`; raw error strings are neither persisted nor logged. Successful recovery after one or more failures logs one safe delivery-recovered event.

Cancellation stops new evaluations and sends. `serverapp` waits for the worker before closing SQLite, using the existing graceful-shutdown deadline. A canceled in-flight Telegram request remains pending for the next process.

## Logging and Observability

Structured logs may contain event name, alert kind, server ID, outbox ID, attempts, and safe outcome class. They never contain message text, server name/group, Telegram ID, payload JSON, Telegram error text, or any credential.

Repeated dependency failures are transition-logged rather than emitted every five seconds: one failure event, one recovery event when the operation succeeds again. This applies separately to evaluation and delivery.

Readiness continues to mean SQLite is reachable; a temporary Telegram outage does not make metric ingestion unready. Worker failures remain visible through logs and pending outbox state.

The simplified HTTP Bot API `sendMessage` request has no caller-provided idempotency key. Its official success result is a sent `Message`, so the dispatcher cannot safely distinguish an accepted request whose response was lost from a request Telegram never accepted. This is the reason for the explicit at-least-once contract.

## Settings and API Adjustment

SQLite `UpdateSettings` and the authenticated admin settings endpoint enforce the same invariant: all values remain in their existing ranges and `alert_threshold_seconds >= offline_threshold_seconds`. The WebApp shows a concise validation error and constrains the alert input minimum to the currently entered offline threshold.

No public alert queue API is added. Existing `/alerts_on`, `/alerts_off`, and `PUT /api/v1/admin/alert-preference` continue to control the authenticated administrator only.

## Testing Strategy

Strict TDD applies to every behavior.

- Pure alerting tests prove threshold boundaries, no-data baselines, enabled/disabled servers, recovery gating, repeated scans, re-enable grace, recipient filtering, immutable payload rendering, and clock rollback.
- SQLite tests prove transition plus fan-out atomicity, restart deduplication, per-recipient order, preference defaults, suppression, retry updates, cleanup, cascade behavior, and rollback on injected failure.
- Dispatcher tests prove success, cancellation, current preference/allowlist recheck, bounded batches, safe exponential backoff, ordering, and secret-safe logs.
- `serverapp` tests prove workers exist only with Telegram enabled, run immediately, stop cleanly, and do not interfere with HTTP serving or final metric checkpointing.
- Settings tests prove the new cross-field invariant in storage, API, and browser form behavior.
- A real-process smoke test uses a fake loopback Telegram Bot API, a short threshold, one enabled administrator, an Agent report, a forced outage, restart during pending delivery, recovery, and scans output/logs for credential canaries.

Final verification runs formatting, all Go tests without cache, vet, race tests, both CGO-free target builds, the existing Agent/Telegram/WebApp smoke suites, the new alert smoke suite, shell syntax checks, Git diff checks, and secret/log scans before commit and push.

## Out of Scope

- CPU, memory, disk, load, or custom threshold alerts.
- Email, SMS, Slack, or multi-bot delivery.
- Alert acknowledgement, escalation policies, maintenance windows, or schedules.
- Exactly-once Telegram delivery.
- Public outbox administration endpoints or manual replay.
- Remote-host deployment without the user's target and authenticated access.
