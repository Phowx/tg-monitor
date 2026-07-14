# Backend Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the tested Go domain, configuration, minute aggregation, and SQLite repository foundation required by later Telegram monitor phases.

**Architecture:** Keep transport-independent contracts in `internal/domain`, environment parsing in `internal/config`, and all SQLite connection, migration, and repository behavior in `internal/storage/sqlite`. Persist complete reports and minute samples as typed columns, hash opaque session tokens before storage, and expose sentinel errors for missing and expired rows.

**Tech Stack:** Go 1.26.0 module directive, Go 1.26.5 toolchain, standard library, `database/sql`, and the pure-Go `modernc.org/sqlite` driver.

## Global Constraints

- Module path is `github.com/tg-monitor/tg-monitor`; Go directive is `1.26.0`.
- Prepend `/tmp/go-toolchain/bin` to `PATH` for every Go command.
- Strict TDD: record each focused RED command before adding implementation, then record GREEN.
- SQLite uses WAL, foreign keys, a five-second busy timeout, and short transactions.
- Timestamps are UTC Unix milliseconds, sizes are integer bytes, and percentages are floats in `0..100`.
- Do not implement HTTP handlers, Telegram calls, Linux collection, or the Vue application.
- Do not serialize server token hashes or include any secret/raw token in errors.
- Record RED/GREEN and final output in `.superpowers/sdd/task-1-report.md`.

## File Map

- `go.mod`, `go.sum`: module identity and pure-Go SQLite dependency.
- `internal/domain/metrics.go`: metric, sample, and latest-metric contracts.
- `internal/domain/metrics_test.go`: validation and JSON secrecy tests.
- `internal/domain/server.go`: server, settings, and session contracts.
- `internal/domain/accumulator.go`: minute-bucket accumulation.
- `internal/domain/accumulator_test.go`: average/latest/bucket tests.
- `internal/config/config.go`: environment defaults, parsing, and validation.
- `internal/config/config_test.go`: default, override, and safe-error tests.
- `internal/storage/sqlite/sqlite.go`: open, pragma, close, and shared store behavior.
- `internal/storage/sqlite/migrations.go`: versioned schema.
- `internal/storage/sqlite/sqlite_test.go`: pragma, migration, and cascade tests.
- `internal/storage/sqlite/servers.go`: server repository operations.
- `internal/storage/sqlite/servers_test.go`: CRUD, order, and token update tests.
- `internal/storage/sqlite/metrics.go`: latest and minute metric persistence.
- `internal/storage/sqlite/metrics_test.go`: round-trip, upsert, range, and cleanup tests.
- `internal/storage/sqlite/access.go`: sessions, settings, and preferences.
- `internal/storage/sqlite/access_test.go`: hashing, expiry, settings, and preference tests.
- `README.md`, `LICENSE`, `NOTICE`: project usage and attribution.

---

### Task 1: Module and Domain Contracts

**Files:**
- Create: `go.mod`
- Create: `internal/domain/metrics.go`
- Create: `internal/domain/server.go`
- Test: `internal/domain/metrics_test.go`

**Interfaces:**
- Produces: `SystemInfo`, `MetricReport`, `LatestMetrics`, `MinuteSample`, `Server`, `Settings`, `Session`, and `MetricReport.Validate() error`.
- `Server.TokenSHA256` is `[]byte` tagged `json:"-"`.

- [ ] **Step 1: Initialize the module and write failing domain tests**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go mod init github.com/tg-monitor/tg-monitor
PATH=/tmp/go-toolchain/bin:$PATH go mod edit -go=1.26.0
```

Create table-driven tests which construct a valid report and then mutate CPU to `math.NaN()`, `math.Inf(1)`, `-1`, and `101`; make used bytes exceed totals; make totals non-positive; make loads/rates/counters/uptime negative; and blank each system field. Assert every mutation fails while the baseline passes. Marshal a `Server` and assert neither `token_sha256` nor its bytes appear.

- [ ] **Step 2: Run RED and record it**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/domain -run 'TestMetricReportValidate|TestServerTokenHashIsNotJSON' -count=1
```

Expected: compilation fails because the domain types do not exist.

- [ ] **Step 3: Add the minimal domain contracts and validation**

```go
type MetricReport struct {
    CapturedAtMS int64      `json:"captured_at"`
    CPUPct float64          `json:"cpu_pct"`
    MemoryTotalBytes int64  `json:"memory_total_bytes"`
    MemoryUsedBytes int64   `json:"memory_used_bytes"`
    RootDiskTotalBytes int64 `json:"root_disk_total_bytes"`
    RootDiskUsedBytes int64 `json:"root_disk_used_bytes"`
    Load1 float64           `json:"load_1"`
    Load5 float64           `json:"load_5"`
    Load15 float64          `json:"load_15"`
    NetworkRXTotalBytes int64 `json:"network_rx_total_bytes"`
    NetworkTXTotalBytes int64 `json:"network_tx_total_bytes"`
    NetworkRXBytesPerSecond float64 `json:"network_rx_bytes_per_second"`
    NetworkTXBytesPerSecond float64 `json:"network_tx_bytes_per_second"`
    UptimeSeconds int64     `json:"uptime_seconds"`
    System SystemInfo       `json:"system"`
}

func (r MetricReport) Validate() error {
    // Check finite floats first, then capture time, percentage bounds,
    // positive totals, used<=total, non-negative loads/rates/counters/uptime,
    // and strings.TrimSpace for all system fields. Return field-name errors.
}
```

Define `MinuteSample` with int64 averaged used-byte fields and int64 latest totals/counters, plus `Server`, `Settings`, and `Session` exactly as described in the design.

- [ ] **Step 4: Run GREEN**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/domain -run 'TestMetricReportValidate|TestServerTokenHashIsNotJSON' -count=1
```

Expected: PASS.

### Task 2: Minute Accumulator

**Files:**
- Create: `internal/domain/accumulator.go`
- Test: `internal/domain/accumulator_test.go`

**Interfaces:**
- Consumes: `MetricReport`, `MinuteSample`, and `MetricReport.Validate`.
- Produces: `NewMinuteAccumulator(atMS int64) *MinuteAccumulator`, `Add(MetricReport) error`, and `Sample(serverID int64) (MinuteSample, bool)`.

- [ ] **Step 1: Write failing accumulator tests**

Tests must assert an empty accumulator returns `false`; `NewMinuteAccumulator(1710000059123)` produces bucket `1710000000000`; two reports average CPU, memory used, disk used, loads, and rates; and all totals, counters, uptime, and system fields come from the second report. A rejected report must not change the sample.

- [ ] **Step 2: Run RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/domain -run TestMinuteAccumulator -count=1
```

Expected: compilation fails because the accumulator is undefined.

- [ ] **Step 3: Implement running sums plus last report**

```go
func NewMinuteAccumulator(atMS int64) *MinuteAccumulator {
    return &MinuteAccumulator{bucketMS: atMS - atMS%60_000}
}

func (a *MinuteAccumulator) Add(report MetricReport) error {
    if err := report.Validate(); err != nil { return err }
    a.count++
    a.cpu += report.CPUPct
    a.memoryUsed += float64(report.MemoryUsedBytes)
    a.diskUsed += float64(report.RootDiskUsedBytes)
    a.load1 += report.Load1
    a.load5 += report.Load5
    a.load15 += report.Load15
    a.rxRate += report.NetworkRXBytesPerSecond
    a.txRate += report.NetworkTXBytesPerSecond
    a.last = report
    return nil
}
```

`Sample` divides every sum by `float64(count)` and copies the last report's totals, counters, uptime, and system metadata.

- [ ] **Step 4: Run GREEN**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/domain -run TestMinuteAccumulator -count=1
```

Expected: PASS.

### Task 3: Environment Configuration

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Config` and `LoadFromEnv() (Config, error)`.
- Durations are `time.Duration`; admin IDs are `[]int64`.

- [ ] **Step 1: Write failing defaults, override, and validation tests**

Set the five required non-default values in a helper, assert exact defaults, then cover duration overrides, malformed URLs, URL credentials/fragments, invalid listen addresses, blank/duplicate/non-positive admin IDs, and malformed durations. Put recognizable values in bot token and webhook secret and assert returned error strings do not contain them.

- [ ] **Step 2: Run RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/config -count=1
```

Expected: compilation fails because `Config` and `LoadFromEnv` are undefined.

- [ ] **Step 3: Implement explicit parsing helpers**

```go
func LoadFromEnv() (Config, error) {
    cfg := Config{
        ListenAddr: envOr("TG_MONITOR_LISTEN_ADDR", "127.0.0.1:8080"),
        DatabasePath: envOr("TG_MONITOR_DATABASE_PATH", "/var/lib/tg-monitor/monitor.db"),
        SessionTTL: 12*time.Hour,
        InitDataMaxAge: 5*time.Minute,
        CheckpointInterval: 15*time.Second,
        HistoryRetention: 7*24*time.Hour,
        OfflineThreshold: 60*time.Second,
        AlertThreshold: 120*time.Second,
    }
    // Read required strings, parse admin IDs and optional duration variables,
    // validate both URLs and the listen host/port, and return cfg.
}
```

Use `url.ParseRequestURI`, require `http` or `https`, non-empty host, and no user info or fragment. Use `net.SplitHostPort` and require port `1..65535`. Secret errors name only the environment variable.

- [ ] **Step 4: Run GREEN**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/config -count=1
```

Expected: PASS.

### Task 4: SQLite Open and Schema Migration

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `internal/storage/sqlite/sqlite.go`
- Create: `internal/storage/sqlite/migrations.go`
- Test: `internal/storage/sqlite/sqlite_test.go`

**Interfaces:**
- Produces: `Open(ctx context.Context, path string) (*Store, error)`, `Close() error`, `Migrate(ctx context.Context) error`, `ErrNotFound`, and `ErrSessionExpired`.
- `Store` owns one `*sql.DB` connection and an injectable `nowMS func() int64` for deterministic repository tests.

- [ ] **Step 1: Add the driver and write failing connection/migration tests**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go get modernc.org/sqlite@latest
```

Open a temporary database and query `PRAGMA journal_mode`, `foreign_keys`, and `busy_timeout`; expect `wal`, `1`, and `5000`. Assert all nine tables and both required indexes exist, exactly version `1` is recorded, a second `Migrate` succeeds without another row, seeded settings are `60/120/7`, and deleting a server cascades latest/sample/alert-state rows.

- [ ] **Step 2: Run RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -run 'TestOpenPragmas|TestMigrate|TestForeignKeyCascade' -count=1
```

Expected: compilation fails because `Open` is undefined.

- [ ] **Step 3: Implement open policy and transactional version 1**

```go
func Open(ctx context.Context, path string) (*Store, error) {
    db, err := sql.Open("sqlite", path)
    if err != nil { return nil, fmt.Errorf("open sqlite: %w", err) }
    db.SetMaxOpenConns(1)
    db.SetMaxIdleConns(1)
    for _, statement := range []string{
        "PRAGMA journal_mode = WAL",
        "PRAGMA foreign_keys = ON",
        "PRAGMA busy_timeout = 5000",
    } {
        if _, err := db.ExecContext(ctx, statement); err != nil {
            db.Close()
            return nil, fmt.Errorf("configure sqlite: %w", err)
        }
    }
    store := &Store{db: db, nowMS: func() int64 { return time.Now().UTC().UnixMilli() }}
    if err := store.Migrate(ctx); err != nil { db.Close(); return nil, err }
    return store, nil
}
```

`Migrate` creates `schema_migrations`, checks the maximum version, and applies one transaction containing the exact schema and settings seed. Store all byte-size columns as `INTEGER`; use `INTEGER NOT NULL CHECK` constraints for non-negative and singleton fields, `ON DELETE CASCADE` for server-owned current/history/state rows, unique `(server_id,bucket_ms)`, `metric_samples_server_bucket_idx`, and `alert_outbox_delivery_idx(delivered_at_ms,next_attempt_at_ms)`.

- [ ] **Step 4: Run GREEN**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -run 'TestOpenPragmas|TestMigrate|TestForeignKeyCascade' -count=1
```

Expected: PASS.

### Task 5: Server Repository

**Files:**
- Create: `internal/storage/sqlite/servers.go`
- Test: `internal/storage/sqlite/servers_test.go`

**Interfaces:**
- Produces: `CreateServer(ctx, domain.Server) (domain.Server, error)`, `ListServers(ctx) ([]domain.Server, error)`, `GetServer(ctx, id int64) (domain.Server, error)`, `UpdateServer(ctx, domain.Server) error`, `UpdateServerTokenHash(ctx, id int64, hash []byte) error`, and `DeleteServer(ctx, id int64) error`.

- [ ] **Step 1: Write failing CRUD/order tests**

Create three servers with different sort order/name, assert database-assigned IDs and timestamps, stable `sort_order,name,id` listing, full get, editable-field update, separate 32-byte token update, and delete/not-found behavior. Assert create/token update rejects hashes not exactly 32 bytes.

- [ ] **Step 2: Run RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -run TestServerRepository -count=1
```

Expected: compilation fails because repository methods are undefined.

- [ ] **Step 3: Implement parameterized statements and shared scanning**

```go
const serverColumns = `id,name,group_name,sort_order,enabled,token_sha256,created_at_ms,updated_at_ms`

func (s *Store) CreateServer(ctx context.Context, server domain.Server) (domain.Server, error) {
    if len(server.TokenSHA256) != sha256.Size { return domain.Server{}, fmt.Errorf("create server: token hash must be 32 bytes") }
    now := s.nowMS()
    row := s.db.QueryRowContext(ctx, `INSERT INTO servers (...) VALUES (?,?,?,?,?,?,?) RETURNING `+serverColumns,
        server.Name, server.Group, server.SortOrder, server.Enabled, server.TokenSHA256, now, now)
    return scanServer(row)
}
```

Map `sql.ErrNoRows` to `ErrNotFound`; check `RowsAffected` for update/delete. Never interpolate values or include hash bytes in errors.

- [ ] **Step 4: Run GREEN**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -run TestServerRepository -count=1
```

Expected: PASS.

### Task 6: Metrics Repository

**Files:**
- Create: `internal/storage/sqlite/metrics.go`
- Test: `internal/storage/sqlite/metrics_test.go`

**Interfaces:**
- Produces latest metric upsert/get/list, minute sample upsert/range, and `DeleteMetricSamplesBefore(ctx, cutoffMS int64) (int64, error)`.
- Range semantics are `bucket_ms >= fromMS AND bucket_ms < toMS ORDER BY bucket_ms ASC`.

- [ ] **Step 1: Write failing latest/sample tests**

Assert full latest-metric round trip, conflict replacement, list order matching servers, missing-row behavior, full minute-sample round trip, conflict replacement without duplicate rows, ascending inclusive/exclusive range behavior, and exact cleanup count with boundary preservation.

- [ ] **Step 2: Run RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -run 'TestLatestMetricsRepository|TestMinuteSampleRepository' -count=1
```

Expected: compilation fails because metric repository methods are undefined.

- [ ] **Step 3: Implement typed column lists and upserts**

```go
func (s *Store) UpsertLatestMetrics(ctx context.Context, latest domain.LatestMetrics) error {
    if err := latest.Report.Validate(); err != nil { return fmt.Errorf("upsert latest metrics: %w", err) }
    _, err := s.db.ExecContext(ctx, latestUpsertSQL, latest.ServerID, latest.ReceivedAtMS,
        latest.Report.CapturedAtMS, latest.Report.CPUPct /* every remaining report field */)
    if err != nil { return fmt.Errorf("upsert latest metrics: %w", err) }
    return nil
}

func (s *Store) QueryMinuteSamples(ctx context.Context, serverID, fromMS, toMS int64) ([]domain.MinuteSample, error) {
    rows, err := s.db.QueryContext(ctx, sampleSelectSQL+` WHERE server_id=? AND bucket_ms>=? AND bucket_ms<? ORDER BY bucket_ms`, serverID, fromMS, toMS)
    // Scan every row, check rows.Err, and return a non-nil empty slice.
}
```

Use explicit column lists for all inserts/selects, `ON CONFLICT ... DO UPDATE`, one shared scan helper per row shape, and a direct delete with `RowsAffected`.

- [ ] **Step 4: Run GREEN**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -run 'TestLatestMetricsRepository|TestMinuteSampleRepository' -count=1
```

Expected: PASS.

### Task 7: Settings, Sessions, and Preferences

**Files:**
- Create: `internal/storage/sqlite/access.go`
- Test: `internal/storage/sqlite/access_test.go`

**Interfaces:**
- Produces: `GetSettings`, `UpdateSettings`, `CreateSession`, `GetSession`, `DeleteSession`, `GetAlertPreference`, and `SetAlertPreference`.
- Session methods accept a raw opaque token and hash it internally; the domain `Session` never contains a raw token.

- [ ] **Step 1: Write failing access repository tests**

Assert seeded settings and persisted updates; reject non-positive setting values. Create a raw-token session, inspect the database to prove only `sha256.Sum256([]byte(raw))` is stored, retrieve it before expiry, receive `ErrSessionExpired` at the exact expiry boundary, receive `ErrNotFound` for another token and after deletion, default missing preference to enabled, and persist both disabled and re-enabled states.

- [ ] **Step 2: Run RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -run 'TestSettingsRepository|TestSessionRepository|TestAlertPreferenceRepository' -count=1
```

Expected: compilation fails because access repository methods are undefined.

- [ ] **Step 3: Implement hash-only sessions and singleton settings**

```go
func tokenHash(raw string) [sha256.Size]byte { return sha256.Sum256([]byte(raw)) }

func (s *Store) GetSession(ctx context.Context, raw string, nowMS int64) (domain.Session, error) {
    hash := tokenHash(raw)
    var session domain.Session
    err := s.db.QueryRowContext(ctx, `SELECT telegram_user_id,created_at_ms,expires_at_ms FROM sessions WHERE token_sha256=?`, hash[:]).Scan(
        &session.TelegramUserID, &session.CreatedAtMS, &session.ExpiresAtMS)
    if errors.Is(err, sql.ErrNoRows) { return domain.Session{}, ErrNotFound }
    if err != nil { return domain.Session{}, fmt.Errorf("get session: %w", err) }
    if session.ExpiresAtMS <= nowMS { return domain.Session{}, ErrSessionExpired }
    return session, nil
}
```

Use a singleton settings row with `id=1`; preference reads return `true,nil` on `sql.ErrNoRows`; preference writes use an upsert.

- [ ] **Step 4: Run GREEN**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -run 'TestSettingsRepository|TestSessionRepository|TestAlertPreferenceRepository' -count=1
```

Expected: PASS.

### Task 8: Documentation, Full Verification, and Commit

**Files:**
- Create: `README.md`
- Create: `LICENSE`
- Create: `NOTICE`
- Update (ignored): `.superpowers/sdd/task-1-report.md`

**Interfaces:**
- Produces a documented, licensed, fully verified backend foundation.

- [ ] **Step 1: Add concise project documentation**

README content must state that domain/config/SQLite are the current foundation, later adapters consume them, the database uses WAL/foreign keys, and development checks are:

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./...
PATH=/tmp/go-toolchain/bin:$PATH go vet ./...
```

Add the standard MIT License with copyright `2026 tg-monitor contributors`. `NOTICE` must say the project is inspired by `huilang-me/CF-Server-Monitor` and does not copy its code.

- [ ] **Step 2: Run formatting and focused full verification**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal
PATH=/tmp/go-toolchain/bin:$PATH go test ./...
PATH=/tmp/go-toolchain/bin:$PATH go vet ./...
```

Expected: every package passes and vet exits zero.

- [ ] **Step 3: Inspect for scope, schema, and secret leakage**

```bash
git diff --check
git status --short
git diff --stat
rg -n 'BotToken|WebhookSecret|raw token|token_sha256' internal README.md
```

Confirm only intended files changed, every persisted field has matching insert/select scan order, token hashes have `json:"-"`, and errors do not contain secret values.

- [ ] **Step 4: Record evidence and commit**

Append every RED/GREEN command and its actual output plus final test/vet output to `.superpowers/sdd/task-1-report.md`, then commit tracked deliverables:

```bash
git add go.mod go.sum internal README.md LICENSE NOTICE docs/superpowers/plans/2026-07-14-backend-foundation.md
git commit -m "feat: add backend storage foundation"
```
