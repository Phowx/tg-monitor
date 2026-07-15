# tg-monitor Durable Alerts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver restart-safe Telegram offline and recovery notifications with per-administrator preferences, ordered durable retry, safe cleanup, and real-process verification.

**Architecture:** SQLite owns the atomic state-transition and per-recipient outbox transaction. A focused `internal/alerting` package renders immutable payloads, applies retry policy, and runs one sequential evaluate/deliver worker only when Telegram is enabled; `serverapp` composes and drains that worker with the existing Telegram sender and graceful lifecycle.

**Tech Stack:** Go 1.26, `database/sql`, modernc SQLite, `log/slog`, the existing Telegram HTTP client, embedded native JavaScript, Bash real-process smoke tests; no new runtime or production dependency.

## Global Constraints

- Production remains a CGO-free single server binary for `linux/amd64` and `linux/arm64`.
- Only enabled servers alert; availability uses server-side `received_at_ms`, or `created_at_ms` before the first report, never Agent capture time.
- Persisted settings are authoritative and require `1 <= offline_threshold_seconds <= alert_threshold_seconds <= 86400`.
- State mutation and all per-recipient outbox inserts commit in one SQLite transaction; one continuous outage creates at most one offline transition.
- Recipients are current unique positive `TG_MONITOR_ADMIN_TELEGRAM_IDS` with alerts enabled; missing preferences default enabled.
- Delivery is ordered per server/recipient and at-least-once; retry is `5s * 2^(attempt-1)`, capped at `15m`, forever.
- Disabling/deleting a server, disabling alerts, or removing an administrator terminally suppresses pending matching rows and never replays them later.
- Messages use plain text without parse mode and never contain tokens, URLs, raw telemetry, recipient IDs, dependency errors, or Telegram response bodies.
- Alert workers run only when Telegram integration is enabled, evaluate immediately, repeat every five seconds, and finish before SQLite closes.
- Delivered/suppressed rows older than 30 days are removed; active retries are never age-deleted.
- Each implementation task follows RED → observed expected failure → GREEN → focused regression tests → commit; append evidence to `.superpowers/sdd/task-4-report.md`.

## File Structure

- `internal/domain/alerts.go`: storage-neutral alert kind, immutable payload, due-outbox item, and evaluation-count types.
- `internal/alerting/render.go`: payload validation, exact plain-text rendering, and deterministic retry calculation.
- `internal/alerting/dispatcher.go`: current-recipient recheck and one bounded ordered delivery cycle.
- `internal/alerting/worker.go`: immediate evaluation/delivery plus non-overlapping periodic lifecycle and transition logs.
- `internal/storage/sqlite/alerts.go`: transactional transition fan-out and outbox delivery mutations.
- `internal/storage/sqlite/servers.go`: enable/disable/delete alert-state reset and pending-row suppression in the metadata transaction.
- `internal/serverapp/app.go`: optional Telegram alert worker composition and graceful shutdown.
- `internal/serverapp/access_cleanup.go`: fixed 30-day terminal outbox cleanup.
- `internal/adminapi/settings.go` and `internal/webapp/assets/app.js`: the shared threshold-order invariant.
- `scripts/smoke-alerts.sh`: real server/fake Telegram outage-restart-recovery proof.

---

### Task 1: Enforce the threshold-order invariant end to end

**Files:**
- Modify: `internal/storage/sqlite/access.go`
- Modify: `internal/storage/sqlite/access_test.go`
- Modify: `internal/adminapi/settings.go`
- Modify: `internal/adminapi/settings_test.go`
- Modify: `internal/webapp/assets/index.html`
- Modify: `internal/webapp/assets/app.js`
- Modify: `internal/deploytest/webapp_browser_assets_test.go`

**Interfaces:**
- Produces: one invariant, `OfflineThresholdSeconds >= 1 && AlertThresholdSeconds >= OfflineThresholdSeconds`, enforced by storage, HTTP validation, and browser form constraints.
- Consumed by: the evaluator in Task 3, which may assume an alert never precedes the visible offline boundary.

- [ ] **Step 1: Write failing storage, API, and embedded-asset tests**

```go
func TestSettingsRejectAlertThresholdBeforeOfflineThreshold(t *testing.T) {
    store := openTestStore(t)
    err := store.UpdateSettings(context.Background(), domain.Settings{
        OfflineThresholdSeconds: 120,
        AlertThresholdSeconds:   60,
        HistoryRetentionDays:    7,
    })
    if err == nil || !strings.Contains(err.Error(), "alert threshold") {
        t.Fatalf("UpdateSettings() error = %v, want safe threshold-order error", err)
    }
}

func TestSettingsRejectAlertThresholdBeforeOfflineAtHTTPBoundary(t *testing.T) {
    response := putSettings(t, domain.Settings{OfflineThresholdSeconds: 120, AlertThresholdSeconds: 60, HistoryRetentionDays: 7})
    if response.Code != http.StatusUnprocessableEntity {
        t.Fatalf("status = %d, want 422", response.Code)
    }
}
```

Extend `TestWebAppBrowserAssetContract` to require `alertInput.min = offlineInput.value`, an input listener, and `setCustomValidity` copy for the ordered fields.

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/storage/sqlite ./internal/adminapi ./internal/deploytest -run 'Test(SettingsReject|WebAppBrowserAssetContract)' -count=1`

Expected: FAIL because storage/API currently accept `alert < offline` and the browser does not synchronize the minimum.

- [ ] **Step 3: Implement the invariant and accessible form feedback**

```go
if settings.OfflineThresholdSeconds <= 0 ||
    settings.AlertThresholdSeconds < settings.OfflineThresholdSeconds ||
    settings.HistoryRetentionDays <= 0 {
    return errors.New("update settings: alert threshold must be at least the offline threshold")
}
```

Add the same comparison to `adminapi.validSettings`. Give the inputs stable IDs, then synchronize them without storing values outside the form:

```js
function syncThresholdConstraint() {
  const form = byID("settings-form");
  const offline = field(form, "offline_threshold_seconds");
  const alert = field(form, "alert_threshold_seconds");
  alert.min = offline.value || "1";
  alert.setCustomValidity(Number(alert.value) < Number(offline.value)
    ? "告警阈值不能小于离线判定。"
    : "");
}
```

Call it from `syncSettingsForm`, on both threshold inputs, and before `reportValidity()` in `saveSettings`.

- [ ] **Step 4: Run GREEN and focused browser smoke**

Run: `/tmp/go-toolchain/bin/go test ./internal/storage/sqlite ./internal/adminapi ./internal/deploytest -count=1`

Run: `GO=/tmp/go-toolchain/bin/go bash scripts/smoke-webapp.sh`

Expected: all tests PASS and browser output ends with `webapp_browser=ok`, `keyboard=ok`, and `secret_log_scan=clean`.

- [ ] **Step 5: Commit**

```bash
git add internal/storage/sqlite/access.go internal/storage/sqlite/access_test.go internal/adminapi/settings.go internal/adminapi/settings_test.go internal/webapp/assets/index.html internal/webapp/assets/app.js internal/deploytest/webapp_browser_assets_test.go
git commit -m "fix: order monitoring alert thresholds"
```

### Task 2: Define immutable alert payloads, rendering, and retry policy

**Files:**
- Create: `internal/domain/alerts.go`
- Create: `internal/alerting/render.go`
- Create: `internal/alerting/render_test.go`

**Interfaces:**
- Produces: `domain.AlertKind`, `domain.AlertOffline`, `domain.AlertRecovery`, `domain.AlertPayload`, `domain.AlertOutboxItem`, `domain.AlertEvaluationResult`, `alerting.Render(domain.AlertKind, string) (string, error)`, and `alerting.RetryDelay(int) time.Duration`.
- Consumed by: SQLite in Tasks 3–4 and dispatcher/worker in Task 5.

- [ ] **Step 1: Write failing exact-render and retry-boundary tests**

```go
func TestRenderOfflineAndRecoveryPlainText(t *testing.T) {
    offline := domain.AlertPayload{Version: 1, ServerName: "node-1", ServerGroup: "production", OfflineSinceMS: 1784080000000, EventAtMS: 1784080120000}
    recovery := offline
    recovery.RecoveredAtMS = 1784080270000
    assertRendered(t, domain.AlertOffline, offline, "🔴 node-1 is offline\nGroup: production\nLast received: 2026-07-15 01:46:40 UTC\nAlerted after: 2m0s")
    assertRendered(t, domain.AlertRecovery, recovery, "🟢 node-1 recovered\nGroup: production\nRecovered: 2026-07-15 01:51:10 UTC\nOutage duration: 4m30s")
}

func TestRetryDelayIsExponentialAndCapped(t *testing.T) {
    want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 15 * time.Minute}
    attempts := []int{1, 2, 3, 99}
    for index := range attempts {
        if got := RetryDelay(attempts[index]); got != want[index] { t.Fatalf("attempt %d = %v", attempts[index], got) }
    }
}
```

Add tables rejecting unknown kind/version, empty or over-120-byte name/group, non-positive times, recovery without `recovered_at_ms`, negative durations, trailing JSON, and payload JSON above 4 KiB. Scan rendered text/errors for a token canary and raw JSON.

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/alerting -count=1`

Expected: FAIL because the package and domain types do not exist.

- [ ] **Step 3: Implement exact types, strict decode, safe render, and capped backoff**

```go
type AlertKind string

const (
    AlertOffline  AlertKind = "offline"
    AlertRecovery AlertKind = "recovery"
)

type AlertPayload struct {
    Version        int    `json:"version"`
    ServerName     string `json:"server_name"`
    ServerGroup    string `json:"server_group,omitempty"`
    OfflineSinceMS int64  `json:"offline_since_ms"`
    EventAtMS      int64  `json:"event_at_ms"`
    RecoveredAtMS  int64  `json:"recovered_at_ms,omitempty"`
}

type AlertOutboxItem struct {
    ID int64; ServerID *int64; TelegramUserID int64; Kind AlertKind
    PayloadJSON string; Attempts int; NextAttemptAtMS int64; CreatedAtMS int64
}

type AlertEvaluationResult struct {
    Offline, Recovery, Queued int
}

func RetryDelay(attempt int) time.Duration {
    if attempt <= 1 { return 5 * time.Second }
    shift := min(attempt-1, 8)
    delay := 5 * time.Second * time.Duration(1<<shift)
    return min(delay, 15*time.Minute)
}
```

Decode with a 4 KiB `io.LimitReader`, `DisallowUnknownFields`, and an EOF check. Format UTC with `2006-01-02 15:04:05 UTC`; omit the group line when blank and never enable parse mode.

- [ ] **Step 4: Run GREEN**

Run: `/tmp/go-toolchain/bin/go test ./internal/domain ./internal/alerting -count=1`

Expected: PASS for exact text, strict JSON, second-rounded non-negative duration, and retry cap.

- [ ] **Step 5: Commit**

```bash
git add internal/domain/alerts.go internal/alerting/render.go internal/alerting/render_test.go
git commit -m "feat: define durable alert events"
```

### Task 3: Persist atomic offline/recovery transitions and server lifecycle resets

**Files:**
- Create: `internal/storage/sqlite/alerts.go`
- Create: `internal/storage/sqlite/alerts_test.go`
- Modify: `internal/storage/sqlite/servers.go`
- Modify: `internal/storage/sqlite/servers_test.go`

**Interfaces:**
- Produces: `(*Store).EvaluateAlerts(context.Context, int64, []int64) (domain.AlertEvaluationResult, error)`.
- Produces lifecycle behavior: `UpdateServer` and `DeleteServer` transact metadata changes with state reset and pending-row suppression.
- Consumes: Task 2 alert types and existing `settings`, `servers`, `latest_metrics`, `user_preferences`, `alert_states`, and `alert_outbox` tables.

- [ ] **Step 1: Write failing transactional transition tests against temporary SQLite**

```go
func TestEvaluateAlertsFansOutOnceAndRecoversOnceAcrossRestart(t *testing.T) {
    store, server := alertStoreWithServer(t, true, 1_000)
    updateSettings(t, store, 60, 120)
    got, err := store.EvaluateAlerts(context.Background(), 121_000, []int64{22, 11})
    if err != nil || got.Offline != 1 || got.Queued != 2 { t.Fatalf("first = %#v, %v", got, err) }
    got, err = store.EvaluateAlerts(context.Background(), 122_000, []int64{11, 22})
    if err != nil || got != (domain.AlertEvaluationResult{}) { t.Fatalf("repeat = %#v, %v", got, err) }
    reopened := reopenStore(t, store)
    got, err = reopened.EvaluateAlerts(context.Background(), 123_000, []int64{11, 22})
    if err != nil || got != (domain.AlertEvaluationResult{}) { t.Fatalf("restart = %#v, %v", got, err) }
    ingestLatest(t, reopened, server.ID, 130_000)
    got, err = reopened.EvaluateAlerts(context.Background(), 130_000, []int64{11, 22})
    if err != nil || got.Recovery != 1 || got.Queued != 2 { t.Fatalf("recovery = %#v, %v", got, err) }
}
```

Add separate tests for exact threshold equality, first-report baseline, server-side receive time, disabled and deleted servers, disabled/default preferences, no recipients, clock rollback, lowered threshold, recovery before alert (silent), second outage, deterministic recipient order, 4 KiB payload bound, rollback after an injected insert trigger failure, re-enable grace based on state time, and ordinary rename not extending grace.

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/storage/sqlite -run 'Test(EvaluateAlerts|ServerAlertLifecycle)' -count=1`

Expected: FAIL because `EvaluateAlerts` and lifecycle transactions do not exist.

- [ ] **Step 3: Implement one write transaction and episode identity**

Use `alert_sent_at_ms` as the episode event timestamp and `created_at_ms` on its offline rows. Within one `BEGIN` transaction:

```go
baselineMS := server.CreatedAtMS
if latest.Valid { baselineMS = latest.Int64 }
if stateExists && state.OfflineSinceMS == nil && state.UpdatedAtMS > baselineMS {
    baselineMS = state.UpdatedAtMS
}
if state.AlertSentAtMS != nil && latest.Valid && latest.Int64 > *state.OfflineSinceMS {
    // enqueue recovery only for current enabled recipients whose episode offline row
    // is pending/failed or successfully delivered, excluding terminal suppression classes
} else if state.AlertSentAtMS == nil && nowMS-baselineMS >= settings.AlertThresholdSeconds*1000 {
    // upsert state and enqueue one immutable offline row per current enabled recipient
}
```

Validate `nowMS > 0` and unique positive administrator IDs before beginning. Sort IDs. Read all preferences in the transaction, where a missing row means enabled. Commit state and rows together; return only counts, never names or IDs.

Refactor `UpdateServer` to load prior `enabled` in its transaction. On disable or a false→true transition, upsert a cleared state at `nowMS`; suppress pending rows with terminal class `server_disabled` only on disable. Refactor `DeleteServer` to suppress pending rows before deletion so no retry becomes an orphan with `server_id = NULL`.

- [ ] **Step 4: Run GREEN and repository regressions**

Run: `/tmp/go-toolchain/bin/go test ./internal/storage/sqlite -count=1`

Expected: PASS, including migration/cascade/readiness tests and transaction rollback evidence.

- [ ] **Step 5: Commit**

```bash
git add internal/storage/sqlite/alerts.go internal/storage/sqlite/alerts_test.go internal/storage/sqlite/servers.go internal/storage/sqlite/servers_test.go
git commit -m "feat: persist alert state transitions"
```

### Task 4: Add ordered outbox mutations and terminal cleanup

**Files:**
- Modify: `internal/storage/sqlite/alerts.go`
- Modify: `internal/storage/sqlite/alerts_test.go`
- Modify: `internal/serverapp/access_cleanup.go`
- Modify: `internal/serverapp/access_cleanup_test.go`

**Interfaces:**
- Produces: `ListDueAlertOutbox(context.Context, int64, int) ([]domain.AlertOutboxItem, error)`.
- Produces: `MarkAlertDelivered(context.Context, int64, int64) error`, `MarkAlertSuppressed(context.Context, int64, int64, string) error`, `MarkAlertFailed(context.Context, int64, int64, int64, string) error`, and `DeleteTerminalAlertOutboxBefore(context.Context, int64) (int64, error)`.
- Consumed by: dispatcher in Task 5 and daily access cleanup.

- [ ] **Step 1: Write failing due-order, mutation, and cleanup tests**

```go
func TestListDueAlertOutboxBlocksRecoveryBehindEarlierRetry(t *testing.T) {
    store := outboxStore(t)
    insertOutbox(t, store, 1, 42, domain.AlertOffline, 10, 100)
    insertOutbox(t, store, 1, 42, domain.AlertRecovery, 11, 0)
    insertOutbox(t, store, 2, 42, domain.AlertOffline, 12, 0)
    got, err := store.ListDueAlertOutbox(context.Background(), 50, 100)
    if err != nil { t.Fatal(err) }
    if ids(got) != "3" { t.Fatalf("due IDs = %s, want 3", ids(got)) }
}
```

Test positive `nowMS`, limits 1–100, nullable deleted server scan, ID ordering, successful delivery, suppression classes, failure increment/next time, stale mutation returns `ErrNotFound`, terminal rows older/equal/newer than cutoff, active retry preservation, and `RunAccessCleanupOnce` attempting sessions/updates/outbox independently with only safe operation-class errors.

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/storage/sqlite ./internal/serverapp -run 'Test(ListDueAlert|MarkAlert|DeleteTerminalAlert|RunAccessCleanup)' -count=1`

Expected: FAIL because the repository methods and `AccessCleanupResult.Outbox` do not exist.

- [ ] **Step 3: Implement ordered selection, compare-and-set mutations, and 30-day cleanup**

Use this ordering predicate so a future retry blocks later recovery only for the same recipient/server:

```sql
WHERE current.delivered_at_ms IS NULL
  AND current.next_attempt_at_ms <= ?
  AND NOT EXISTS (
    SELECT 1 FROM alert_outbox earlier
    WHERE earlier.telegram_user_id = current.telegram_user_id
      AND earlier.server_id = current.server_id
      AND earlier.delivered_at_ms IS NULL
      AND earlier.id < current.id
  )
ORDER BY current.id
LIMIT ?
```

All mark methods require `delivered_at_ms IS NULL` and safe constant classes from `recipient_disabled`, `recipient_removed`, `server_disabled`, `invalid_payload`, or `telegram_send_failed`. Cleanup deletes only `delivered_at_ms IS NOT NULL AND delivered_at_ms < cutoffMS`.

Add `const alertOutboxRetention = 30 * 24 * time.Hour`, `Outbox int64`, and a third independent cleanup call whose dependency error becomes exactly `delete old alert outbox failed`.

- [ ] **Step 4: Run GREEN**

Run: `/tmp/go-toolchain/bin/go test ./internal/storage/sqlite ./internal/serverapp -count=1`

Expected: PASS, including safe joined cleanup failures and active retry preservation.

- [ ] **Step 5: Commit**

```bash
git add internal/storage/sqlite/alerts.go internal/storage/sqlite/alerts_test.go internal/serverapp/access_cleanup.go internal/serverapp/access_cleanup_test.go
git commit -m "feat: manage alert outbox delivery"
```

### Task 5: Implement bounded dispatcher and periodic alert worker

**Files:**
- Create: `internal/alerting/dispatcher.go`
- Create: `internal/alerting/dispatcher_test.go`
- Create: `internal/alerting/worker.go`
- Create: `internal/alerting/worker_test.go`

**Interfaces:**
- Produces: `alerting.Repository`, `alerting.Sender`, `alerting.Config`, `alerting.New(Config, Repository, Sender, func() time.Time, *slog.Logger) (*Worker, error)`, `(*Worker).Run(context.Context)`.
- Consumes: Tasks 2–4 repository methods and the existing `SendMessage(context.Context, int64, string) error` transport.

- [ ] **Step 1: Write failing dispatcher and worker tests with complete fakes**

```go
type Repository interface {
    EvaluateAlerts(context.Context, int64, []int64) (domain.AlertEvaluationResult, error)
    ListDueAlertOutbox(context.Context, int64, int) ([]domain.AlertOutboxItem, error)
    GetAlertPreference(context.Context, int64) (bool, error)
    MarkAlertDelivered(context.Context, int64, int64) error
    MarkAlertSuppressed(context.Context, int64, int64, string) error
    MarkAlertFailed(context.Context, int64, int64, int64, string) error
}

func TestDispatcherRechecksRecipientAndRetriesSafely(t *testing.T) {
    // ID 1 is removed, ID 2 disabled, ID 3 send fails once, ID 4 succeeds.
    // Assert no send for 1/2; exact terminal classes; ID 3 attempts=1 and next=now+5s;
    // ID 4 delivered; logs contain IDs/counts only and exclude payload/message/error canaries.
}
```

Add tests for malformed payload terminal suppression, repository failure, batch limit 100, context cancellation before and during send, exact per-item order, immediate startup evaluation before delivery, five-second ticks through an injected timer channel, no overlap, evaluation failure not blocking delivery, delivery failure not blocking next evaluation, one failure-transition log plus one recovery log, and prompt cancellation.

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/alerting -run 'Test(Dispatcher|Worker)' -count=1`

Expected: FAIL because dispatcher and worker do not exist.

- [ ] **Step 3: Implement one bounded cycle and non-overlapping lifecycle**

```go
type Config struct {
    AdminTelegramIDs []int64
    Interval         time.Duration
    BatchSize        int
}

func (worker *Worker) runOnce(ctx context.Context) {
    nowMS := worker.now().UTC().UnixMilli()
    evaluation, evalErr := worker.repository.EvaluateAlerts(ctx, nowMS, worker.adminIDs)
    worker.logEvaluationTransition(evalErr, evaluation)
    delivered, deliveryErr := worker.dispatcher.RunOnce(ctx, nowMS)
    worker.logDeliveryTransition(deliveryErr, delivered)
}
```

`New` copies/sorts the allowlist, validates unique positive IDs, defaults to five seconds and batch 100, and uses a discard logger when nil. Dispatcher checks allowlist, then `GetAlertPreference`, then strict render, then `SendMessage`; each path performs exactly one mark call. A send error stores only `telegram_send_failed` and schedules `nowMS + RetryDelay(item.Attempts+1)`.

Run once synchronously inside `Run`, then select on one ticker and `ctx.Done()`. Never start per-item goroutines. Log only event, kind, server/outbox ID, attempt, counts, and safe class; transition-suppress repeated failures.

- [ ] **Step 4: Run GREEN plus race test**

Run: `/tmp/go-toolchain/bin/go test ./internal/alerting -count=1`

Run: `/tmp/go-toolchain/bin/go test -race ./internal/alerting -count=1`

Expected: PASS with deterministic cancellation, order, retry times, and secret-safe logs.

- [ ] **Step 5: Commit**

```bash
git add internal/alerting/dispatcher.go internal/alerting/dispatcher_test.go internal/alerting/worker.go internal/alerting/worker_test.go
git commit -m "feat: dispatch durable Telegram alerts"
```

### Task 6: Compose alerts into the Telegram-enabled server lifecycle

**Files:**
- Modify: `internal/serverapp/app.go`
- Modify: `internal/serverapp/app_telegram_test.go`
- Modify: `internal/serverapp/app_test.go`

**Interfaces:**
- Consumes: `alerting.New`, the concrete `*sqlite.Store`, the existing Telegram sender, configured administrator IDs, clock, logger, and graceful context.
- Produces: no new public API; core-only mode remains byte-for-byte behavior compatible and Telegram mode owns one additional worker.

- [ ] **Step 1: Write failing composition/lifecycle tests**

```go
func TestTelegramAppRunsImmediateAlertAndStopsWorkerBeforeStoreClose(t *testing.T) {
    // Seed an enabled stale server before New, inject a recording sender and 5ms interval,
    // Serve on loopback, wait for exactly one offline message, cancel, and assert Serve returns nil.
    // Reopen SQLite and prove the offline state/outbox delivery survived shutdown.
}

func TestCoreOnlyAppNeverCreatesAlertWorker(t *testing.T) {
    app, err := newWithDependencies(context.Background(), coreConfig, testLogger(), deps)
    if err != nil { t.Fatal(err) }
    if app.alertWorker != nil { t.Fatal("core-only alert worker is non-nil") }
}
```

Also assert one sender instance is shared by webhook and alerts, constructor failures close SQLite, Telegram delivery failure does not break `/readyz`, server cancellation waits for a blocked sender until its context is canceled, and existing final metric checkpoint remains persisted.

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/serverapp -run 'Test(TelegramAppRunsImmediateAlert|CoreOnlyAppNeverCreatesAlert|Alert)' -count=1`

Expected: FAIL because `App` has no alert worker and `Serve` starts only checkpoint/retention workers.

- [ ] **Step 3: Build and drain the optional worker**

After constructing the Telegram sender and routes:

```go
alertWorker, err = alerting.New(alerting.Config{
    AdminTelegramIDs: telegram.AdminTelegramIDs,
    Interval: dependencies.alertInterval,
    BatchSize: 100,
}, store, sender, dependencies.now, logger)
```

Default `dependencies.alertInterval` to five seconds. Store the worker on `App`. In `Serve`, add checkpoint and retention workers unconditionally and the alert worker only when non-nil. All share `workerCtx`; after cancellation call `stopWorkers()` and `workers.Wait()` before final checkpoint and `Close` on both server-exit paths.

- [ ] **Step 4: Run GREEN and full package regressions**

Run: `/tmp/go-toolchain/bin/go test ./internal/serverapp ./internal/telegrambot ./internal/telegramapi -count=1`

Run: `/tmp/go-toolchain/bin/go test -race ./internal/serverapp ./internal/alerting -count=1`

Expected: PASS, with core-only 404s unchanged and Telegram delivery failures isolated from readiness.

- [ ] **Step 5: Commit**

```bash
git add internal/serverapp/app.go internal/serverapp/app_telegram_test.go internal/serverapp/app_test.go
git commit -m "feat: run alert worker with Telegram"
```

### Task 7: Prove outage, restart retry, recovery, cleanup, and secret safety

**Files:**
- Create: `scripts/smoke-alerts.sh`
- Modify: `README.md`
- Modify: `deploy/systemd/server.env.example`
- Modify: `.superpowers/sdd/task-4-report.md` (ignored evidence only)

**Interfaces:**
- Consumes: the built server binary, existing server/Agent CLI flow, Telegram HTTPS proxy pattern from `scripts/smoke-telegram.sh`, admin settings API, and all alert worker behavior.
- Produces: deterministic markers `alert_offline=ok retry_restart=ok recovery=ok cleanup=ok`, `alert_sigterm=clean`, and `secret_log_scan=clean`.

- [ ] **Step 1: Write the failing real-process smoke script**

Use `set -euo pipefail`, a private `mktemp -d`, random loopback ports, and cleanup traps. Build the server and a fake TLS Telegram CONNECT proxy. The proxy must:

```go
switch {
case strings.Contains(payload.Text, " is offline"):
    recorder.offlineAttempts++
    accepted = recorder.offlineAttempts > 1
case strings.Contains(payload.Text, " recovered"):
    recorder.recoveryMessages++
    accepted = true
default:
    accepted = true
}
```

Run the exact scenario: create an enabled `smoke-alert-host`; start Telegram mode with threshold settings changed through the authenticated admin API to `offline=1`, `alert=1`; submit one Agent report; wait for the first offline attempt to fail; terminate and restart the server; verify one accepted offline retry and one durable outbox row; submit a fresh report; verify exactly one recovery after the offline row; age only terminal rows through a small repository helper and invoke cleanup; SIGTERM the server; scan server/proxy/helper output for Bot token, webhook secret, Agent token, session cookie, message canary, raw Telegram error, and payload JSON.

- [ ] **Step 2: Run the uncommitted real-process verification**

Run: `bash -n scripts/smoke-alerts.sh && GO=/tmp/go-toolchain/bin/go bash scripts/smoke-alerts.sh`

Expected: PASS because Tasks 1–6 already provide the behavior; any failure here is an integration defect to diagnose before documentation or commit.

- [ ] **Step 3: Complete the smoke assertions and deployment runbook**

Require exact proxy statistics rather than substring-only success:

```bash
grep -qx 'offlineAttempts=2' "$proxy_stats"
grep -qx 'offlineAccepted=1' "$proxy_stats"
grep -qx 'recoveryMessages=1' "$proxy_stats"
printf '%s\n' 'alert_offline=ok retry_restart=ok recovery=ok cleanup=ok'
printf '%s\n' 'alert_sigterm=clean secret_log_scan=clean'
```

Update README current capabilities and add an “Offline and recovery alerts” section documenting recipient preference, threshold ordering, five-second evaluation, at-least-once duplicate caveat, retry cap, UTC message times, disable/delete suppression, 30-day terminal retention, safe journal inspection, and the smoke command. Keep the systemd environment unchanged except comments clarifying that thresholds are persisted and edited in the WebApp, not environment variables.

- [ ] **Step 4: Run all release gates from a clean test cache**

Run exactly:

```bash
/tmp/go-toolchain/bin/go clean -testcache
/tmp/go-toolchain/bin/go test ./...
/tmp/go-toolchain/bin/go vet ./...
/tmp/go-toolchain/bin/go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 /tmp/go-toolchain/bin/go build ./cmd/tg-monitor-server ./cmd/tg-monitor-agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 /tmp/go-toolchain/bin/go build ./cmd/tg-monitor-server ./cmd/tg-monitor-agent
GO=/tmp/go-toolchain/bin/go bash scripts/smoke-agent.sh
GO=/tmp/go-toolchain/bin/go bash scripts/smoke-telegram.sh
GO=/tmp/go-toolchain/bin/go bash scripts/smoke-webapp.sh
GO=/tmp/go-toolchain/bin/go bash scripts/smoke-alerts.sh
bash -n scripts/*.sh
git diff --check
git status --short
```

Expected: every command exits 0; both architectures build; all four smoke suites emit their success and secret-scan markers; only intended tracked files plus ignored evidence are present.

- [ ] **Step 5: Commit, verify remote equality, and push**

```bash
git add scripts/smoke-alerts.sh README.md deploy/systemd/server.env.example
git commit -m "test: verify durable alert delivery"
git push origin codex/telegram-monitor
git fetch origin codex/telegram-monitor
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/codex/telegram-monitor)"
git status --short --branch
```

Expected: push succeeds, local and remote SHAs are equal, and the tracked worktree is clean.
