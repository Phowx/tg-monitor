# Deployable Central Server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a CGO-free central server binary that can run under systemd, register monitored servers locally, accept authenticated metric reports, persist latest/minute data, and prove the flow with CLI and HTTP smoke tests.

**Architecture:** Extend the existing config and SQLite packages with the two minimal runtime operations, then layer token/authentication, monitoring, HTTP, application lifecycle, and CLI packages over domain-facing interfaces. Keep remote administration out of HTTP; local CLI commands own bootstrap and inspection, while systemd and Caddy assets make the binary deployable.

**Tech Stack:** Go 1.26.0 module directive, Go 1.26.5 toolchain, standard-library HTTP/CLI/logging, `modernc.org/sqlite`, systemd, Caddy example configuration, Bash build script.

## Global Constraints

- Use `/tmp/go-toolchain/bin/go` for every Go command.
- Follow strict RED→GREEN TDD for every exported behavior.
- Keep the build CGO-free and support `linux/amd64` and `linux/arm64`.
- Never persist, log, JSON-serialize, or include raw Agent tokens/token hashes in errors.
- Default listen is `127.0.0.1:8080`; remote traffic requires TLS termination.
- HTTP body limit is exactly 64 KiB and decoding rejects unknown fields and trailing JSON.
- HTTP timeouts are 5s read-header, 15s read, 15s write, 60s idle, 1 MiB headers, and 10s graceful shutdown.
- SQLite `settings.history_retention_days` is the only retention source.
- Do not add Linux collection, Telegram, browser UI, alerts, Docker, or in-process TLS.

## File Map

- `internal/config/server_runtime.go`: central-server environment subset.
- `internal/config/server_runtime_test.go`: runtime defaults/overrides/validation.
- `internal/storage/sqlite/runtime.go`: readiness and token-hash lookup.
- `internal/storage/sqlite/runtime_test.go`: real SQLite runtime tests.
- `internal/auth/token.go`, `token_test.go`: raw token generation/hash.
- `internal/auth/authenticator.go`, `authenticator_test.go`: Authorization parsing and server lookup.
- `internal/monitoring/service.go`, `service_test.go`: latest writes and active minute aggregation.
- `internal/httpapi/handler.go`, `handler_test.go`: health/readiness/ingestion contract.
- `internal/httpapi/logging.go`, `logging_test.go`: secret-safe structured access logging.
- `internal/serverapp/app.go`, `app_test.go`: process composition, listener, timeouts, shutdown.
- `internal/serverapp/retention.go`, `retention_test.go`: startup/daily history cleanup.
- `internal/servercmd/run.go`, `run_test.go`: CLI subcommands and output.
- `cmd/tg-monitor-server/main.go`: signal-aware executable entrypoint.
- `deploy/systemd/tg-monitor.service`, `deploy/systemd/server.env.example`, `deploy/caddy/Caddyfile.example`: deployment assets.
- `scripts/build-server.sh`: reproducible target builds.
- `internal/deploytest/assets_test.go`: deployment asset contract test.
- `README.md`: install and smoke-test runbook.
- `.superpowers/sdd/task-2-report.md`: ignored RED/GREEN and final evidence.

---

### Task 1: Server Runtime Configuration and SQLite Runtime Operations

**Files:**
- Create: `internal/config/server_runtime.go`
- Create: `internal/config/server_runtime_test.go`
- Create: `internal/storage/sqlite/runtime.go`
- Create: `internal/storage/sqlite/runtime_test.go`

**Interfaces:**
- Produces: `config.ServerRuntimeConfig`, `config.LoadServerRuntimeFromEnv()`, `Store.Ping(ctx)`, and `Store.GetServerByTokenHash(ctx, hash)`.
- `ServerRuntimeConfig` fields are `DatabasePath string`, `ListenAddr string`, and `CheckpointInterval time.Duration`.

- [ ] **Step 1: Write failing focused tests**

```go
func TestLoadServerRuntimeFromEnvDefaults(t *testing.T) {
    t.Setenv("TG_MONITOR_DATABASE_PATH", "")
    t.Setenv("TG_MONITOR_LISTEN_ADDR", "")
    t.Setenv("TG_MONITOR_CHECKPOINT_INTERVAL", "")
    got, err := LoadServerRuntimeFromEnv()
    if err != nil { t.Fatal(err) }
    want := ServerRuntimeConfig{"/var/lib/tg-monitor/monitor.db", "127.0.0.1:8080", 15*time.Second}
    if got != want { t.Fatalf("got %#v, want %#v", got, want) }
}

func TestGetServerByTokenHash(t *testing.T) {
    store := openTestStore(t)
    created := createTestServer(t, store, "server", 0, 7)
    got, err := store.GetServerByTokenHash(context.Background(), created.TokenSHA256)
    if err != nil || got.ID != created.ID { t.Fatalf("got %#v, error %v", got, err) }
}
```

Also assert runtime overrides, invalid listen/duration, a 31-byte hash rejection, `ErrNotFound`, and `Ping` success.

- [ ] **Step 2: Verify RED**

Run: `PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/config ./internal/storage/sqlite -run 'TestLoadServerRuntime|TestGetServerByTokenHash|TestStorePing' -count=1`

Expected: compilation fails because the new types/methods are undefined.

- [ ] **Step 3: Implement exact behavior**

```go
type ServerRuntimeConfig struct {
    DatabasePath string
    ListenAddr string
    CheckpointInterval time.Duration
}

func LoadServerRuntimeFromEnv() (ServerRuntimeConfig, error) {
    checkpoint, err := durationFromEnv("TG_MONITOR_CHECKPOINT_INTERVAL", 15*time.Second)
    if err != nil { return ServerRuntimeConfig{}, err }
    cfg := ServerRuntimeConfig{
        DatabasePath: envOrDefault("TG_MONITOR_DATABASE_PATH", "/var/lib/tg-monitor/monitor.db"),
        ListenAddr: envOrDefault("TG_MONITOR_LISTEN_ADDR", "127.0.0.1:8080"),
        CheckpointInterval: checkpoint,
    }
    if err := validateListenAddr(cfg.ListenAddr); err != nil { return ServerRuntimeConfig{}, err }
    return cfg, nil
}

func (s *Store) Ping(ctx context.Context) error {
    if err := s.db.PingContext(ctx); err != nil { return fmt.Errorf("ping sqlite: %w", err) }
    return nil
}
```

`GetServerByTokenHash` requires `sha256.Size`, selects `serverColumns`, uses `scanServer`, maps `sql.ErrNoRows` to `ErrNotFound`, and never formats hash bytes.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command; expected PASS.

### Task 2: Token Generation and Agent Authentication

**Files:**
- Create: `internal/auth/token.go`
- Create: `internal/auth/token_test.go`
- Create: `internal/auth/authenticator.go`
- Create: `internal/auth/authenticator_test.go`

**Interfaces:**
- Consumes: `domain.Server`, `sqlite.ErrNotFound` through `errors.Is`.
- Produces: `GenerateToken(random io.Reader) (string, []byte, error)`, `HashToken(raw string) []byte`, `NewAuthenticator(lookup ServerLookup) *Authenticator`, `Authenticator.Authenticate(ctx context.Context, authorization string) (domain.Server, error)`, `ErrMalformedAuthorization`, `ErrUnauthorized`, and `ErrDisabled`.

- [ ] **Step 1: Write failing token and authentication tests**

```go
func TestGenerateToken(t *testing.T) {
    raw, hash, err := GenerateToken(bytes.NewReader(bytes.Repeat([]byte{0x2a}, 32)))
    if err != nil { t.Fatal(err) }
    decoded, err := base64.RawURLEncoding.DecodeString(raw)
    if err != nil || len(decoded) != 32 { t.Fatalf("raw=%q decoded=%d error=%v", raw, len(decoded), err) }
    if !bytes.Equal(hash, HashToken(raw)) { t.Fatal("hash mismatch") }
}
```

Authenticator tests cover missing/unknown as `ErrUnauthorized`, malformed syntax as `ErrMalformedAuthorization`, case-insensitive `Bearer`, disabled as `ErrDisabled`, enabled success, storage error propagation, and errors containing neither raw token nor hash.

- [ ] **Step 2: Verify RED**

Run: `PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/auth -count=1`

Expected: compilation fails because package behavior is undefined.

- [ ] **Step 3: Implement token/authentication behavior**

```go
func GenerateToken(random io.Reader) (string, []byte, error) {
    value := make([]byte, 32)
    if _, err := io.ReadFull(random, value); err != nil { return "", nil, fmt.Errorf("generate agent token: %w", err) }
    raw := base64.RawURLEncoding.EncodeToString(value)
    return raw, HashToken(raw), nil
}

func HashToken(raw string) []byte {
    sum := sha256.Sum256([]byte(raw))
    return append([]byte(nil), sum[:]...)
}
```

`Authenticate` parses `strings.Fields`, rejects any non-two-field/mismatched scheme syntax, hashes the second field, calls `GetServerByTokenHash`, maps not-found generically, and rejects `!server.Enabled`.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command; expected PASS.

### Task 3: Monitoring Ingestion and Minute Checkpoints

**Files:**
- Create: `internal/monitoring/service.go`
- Create: `internal/monitoring/service_test.go`

**Interfaces:**
- Consumes: `domain.MetricReport`, `domain.LatestMetrics`, `domain.MinuteAccumulator`, and a repository with `UpsertLatestMetrics`/`UpsertMinuteSample`.
- Produces: `NewService(repository)`, `Ingest(ctx, serverID, receivedAtMS, report)`, `Checkpoint(ctx)`, and `ErrOutOfOrder`.

- [ ] **Step 1: Write failing monitoring tests with a recording repository**

```go
func TestIngestWritesLatestAndCheckpoint(t *testing.T) {
    repo := &recordingRepository{}
    service := NewService(repo)
    first, second := validReportAt(120_001), validReportAt(120_002)
    first.CPUPct, second.CPUPct = 20, 40
    if err := service.Ingest(context.Background(), 7, 200_000, first); err != nil { t.Fatal(err) }
    if err := service.Ingest(context.Background(), 7, 200_001, second); err != nil { t.Fatal(err) }
    if err := service.Checkpoint(context.Background()); err != nil { t.Fatal(err) }
    if repo.latest.Report != second || repo.sample.CPUPct != 30 || repo.sample.BucketMS != 120_000 { t.Fatal("wrong persistence") }
}
```

Also prove rollover flushes before starting the next bucket, an older bucket returns `ErrOutOfOrder` without writes, invalid reports do not mutate state, repository failures preserve retryable state, and checkpoint attempts all servers with `errors.Join`.

- [ ] **Step 2: Verify RED**

Run: `PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/monitoring -count=1`

Expected: compilation fails because `Service` is undefined.

- [ ] **Step 3: Implement synchronized service**

```go
type Repository interface {
    UpsertLatestMetrics(context.Context, domain.LatestMetrics) error
    UpsertMinuteSample(context.Context, domain.MinuteSample) error
}

type Service struct {
    mu sync.Mutex
    repository Repository
    active map[int64]*domain.MinuteAccumulator
    bucket map[int64]int64
}
```

`Ingest` validates IDs/times/report, locks, rejects older buckets, flushes an older active accumulator before rollover, writes latest synchronously, then adds the report to the current/new accumulator. `Checkpoint` locks, snapshots every accumulator, attempts every upsert, and returns `errors.Join` of failures.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command; expected PASS.

### Task 4: HTTP API, Strict Decoding, and Secret-Safe Logging

**Files:**
- Create: `internal/httpapi/handler.go`
- Create: `internal/httpapi/handler_test.go`
- Create: `internal/httpapi/logging.go`
- Create: `internal/httpapi/logging_test.go`

**Interfaces:**
- Consumes: readiness, authentication, and ingestion interfaces.
- Produces: `NewHandler(Dependencies) http.Handler` and stable JSON errors.

- [ ] **Step 1: Write failing table-driven HTTP tests**

```go
type Dependencies struct {
    Readiness interface{ Ping(context.Context) error }
    Authenticator interface{ Authenticate(context.Context, string) (domain.Server, error) }
    Ingestor interface{ Ingest(context.Context, int64, int64, domain.MetricReport) error }
    Now func() time.Time
    Logger *slog.Logger
}
```

Tests cover 200 liveness/readiness, 503 readiness failure, 405+Allow, 400 malformed Authorization/JSON/trailing JSON, 401 missing/unknown, 403 disabled, 413 over 64 KiB, 415 content type, 422 validation, 409 out of order, 500 storage failure, and 204 success with injected receive time. A real temporary SQLite integration test wires `auth.Authenticator` and `monitoring.Service`, posts a report, and reads the exact `LatestMetrics` back.

Logging tests write JSON slog output to a buffer and assert it contains method/status/server ID but not Authorization, raw token, hash, or body hostname.

- [ ] **Step 2: Verify RED**

Run: `PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/httpapi -count=1`

Expected: compilation fails because HTTP handler behavior is undefined.

- [ ] **Step 3: Implement the handler**

```go
const maxMetricBodyBytes int64 = 64 << 10

func NewHandler(deps Dependencies) http.Handler {
    handler := &Handler{deps: deps}
    return accessLog(deps.Logger, http.HandlerFunc(handler.serveHTTP))
}
```

Route by exact path, enforce methods manually, use `mime.ParseMediaType`, authenticate before body parsing, wrap with `http.MaxBytesReader`, call `json.Decoder.DisallowUnknownFields`, require second decode to return `io.EOF`, validate the report, pass `deps.Now().UTC().UnixMilli()`, and map sentinel errors exactly as the specification. `writeError`/`writeJSON` set `application/json` and never write internal error text.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command; expected PASS.

### Task 5: Retention and Server Application Lifecycle

**Files:**
- Create: `internal/serverapp/retention.go`
- Create: `internal/serverapp/retention_test.go`
- Create: `internal/serverapp/app.go`
- Create: `internal/serverapp/app_test.go`

**Interfaces:**
- Consumes: runtime config, SQLite store, auth, monitoring, and HTTP packages.
- Produces: `RunRetentionOnce(ctx context.Context, repository RetentionRepository, now time.Time) (int64, error)`, `New(ctx context.Context, cfg config.ServerRuntimeConfig, logger *slog.Logger) (*App, error)`, `App.Serve(ctx context.Context, listener net.Listener) error`, and `App.Close(ctx context.Context) error`.

- [ ] **Step 1: Write failing retention and lifecycle tests**

Retention tests assert a 7-day setting produces `nowMS - 7*24h`, exact delete count propagation, invalid setting error, and store errors. Lifecycle tests use a real `net.Listener` on `127.0.0.1:0`, verify configured timeout fields, receive health/readiness, cancel context, and assert Serve returns cleanly after final checkpoint. Use a 10ms checkpoint in tests.

- [ ] **Step 2: Verify RED**

Run: `PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/serverapp -count=1`

Expected: compilation fails because app lifecycle types are undefined.

- [ ] **Step 3: Implement retention and lifecycle**

```go
type RetentionRepository interface {
    GetSettings(context.Context) (domain.Settings, error)
    DeleteMetricSamplesBefore(context.Context, int64) (int64, error)
}

func RunRetentionOnce(ctx context.Context, repository RetentionRepository, now time.Time) (int64, error) {
    settings, err := repository.GetSettings(ctx)
    if err != nil { return 0, fmt.Errorf("load retention settings: %w", err) }
    if settings.HistoryRetentionDays <= 0 { return 0, errors.New("history retention days must be positive") }
    cutoff := now.UTC().Add(-time.Duration(settings.HistoryRetentionDays)*24*time.Hour).UnixMilli()
    return repository.DeleteMetricSamplesBefore(ctx, cutoff)
}
```

`App.Serve` starts checkpoint and 24-hour retention goroutines, runs startup retention, serves the provided listener, reacts to context cancellation with a ten-second `Shutdown`, stops workers, checkpoints once more, closes the store exactly once, and treats `http.ErrServerClosed` as success. Construct `http.Server` with every specified timeout and `MaxHeaderBytes`.

- [ ] **Step 4: Verify GREEN**

Run the Step 2 command; expected PASS.

### Task 6: Local Administration CLI and Executable

**Files:**
- Create: `internal/servercmd/run.go`
- Create: `internal/servercmd/run_test.go`
- Create: `cmd/tg-monitor-server/main.go`

**Interfaces:**
- Consumes: runtime config, token generator, SQLite repository, and `serverapp` runner.
- Produces: `servercmd.Run(ctx, args, Dependencies) error` and the executable commands defined by the spec.

- [ ] **Step 1: Write failing CLI tests**

Use injected stdout/stderr, deterministic random bytes, fake store opener, and fake serve runner. Assert:

- Empty/unknown commands return usage errors.
- `server add` requires name, defaults enabled, stores only the hash, and prints `server_id=<id>` plus `agent_token=<raw>` once.
- `server list` JSON contains metadata but neither hash nor token.
- `server rotate-token` updates only the hash and prints a new token once.
- `metrics latest` emits JSON for the requested server.
- `metrics history` passes inclusive/exclusive bounds and emits ascending JSON.
- `serve` passes the focused runtime config to the app runner.

- [ ] **Step 2: Verify RED**

Run: `PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/servercmd ./cmd/tg-monitor-server -count=1`

Expected: compilation fails because command packages are undefined.

- [ ] **Step 3: Implement command dispatch**

```go
type Store interface {
    Close() error
    CreateServer(context.Context, domain.Server) (domain.Server, error)
    ListServers(context.Context) ([]domain.Server, error)
    UpdateServerTokenHash(context.Context, int64, []byte) error
    GetLatestMetrics(context.Context, int64) (domain.LatestMetrics, error)
    QueryMinuteSamples(context.Context, int64, int64, int64) ([]domain.MinuteSample, error)
}

type Dependencies struct {
    Stdout io.Writer
    Stderr io.Writer
    Random io.Reader
    LoadConfig func() (config.ServerRuntimeConfig, error)
    OpenStore func(context.Context, string) (Store, error)
    Serve func(context.Context, config.ServerRuntimeConfig, *slog.Logger) error
}
```

Use `flag.NewFlagSet(..., flag.ContinueOnError)` per subcommand. Open SQLite only after successful parsing. Encode list/latest/history with indented JSON. The executable creates `signal.NotifyContext` for `os.Interrupt` and `syscall.SIGTERM`, calls `servercmd.Run`, writes errors without secret values, and exits nonzero on failure.

- [ ] **Step 4: Verify GREEN and build**

Run:

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/servercmd ./cmd/tg-monitor-server -count=1
CGO_ENABLED=0 /tmp/go-toolchain/bin/go build ./cmd/tg-monitor-server
```

Expected: PASS and build exit 0.

### Task 7: systemd, Caddy, Builds, and Deployment Runbook

**Files:**
- Create: `deploy/systemd/tg-monitor.service`
- Create: `deploy/systemd/server.env.example`
- Create: `deploy/caddy/Caddyfile.example`
- Create: `scripts/build-server.sh`
- Modify: `README.md`
- Create: `internal/deploytest/assets_test.go`

**Interfaces:**
- Produces repeatable amd64/arm64 artifacts and exact first-host deployment steps.

- [ ] **Step 1: Write verification fixtures before assets**

Create `internal/deploytest/assets_test.go` which resolves the repository root from `runtime.Caller`, reads each file, and checks these exact required substrings:

```go
required := map[string][]string{
    "deploy/systemd/tg-monitor.service": {
        "User=tg-monitor", "StateDirectory=tg-monitor", "EnvironmentFile=/etc/tg-monitor/server.env",
        "ExecStart=/usr/local/bin/tg-monitor-server serve", "Restart=on-failure", "NoNewPrivileges=true",
        "PrivateTmp=true", "ProtectSystem=strict", "ProtectHome=true",
    },
    "deploy/systemd/server.env.example": {"TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db", "TG_MONITOR_LISTEN_ADDR=127.0.0.1:8080"},
    "deploy/caddy/Caddyfile.example": {"reverse_proxy 127.0.0.1:8080"},
    "scripts/build-server.sh": {"set -euo pipefail", "CGO_ENABLED=0", "GOARCH=amd64", "GOARCH=arm64"},
}
```

- [ ] **Step 2: Verify RED**

Run: `PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/deploytest -count=1`; expected FAIL because deployment files are missing.

- [ ] **Step 3: Add exact assets and README runbook**

The build script uses `set -euo pipefail`, resolves repository root, creates `dist`, and runs:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO" build -trimpath -ldflags='-s -w' -o dist/tg-monitor-server-linux-amd64 ./cmd/tg-monitor-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 "$GO" build -trimpath -ldflags='-s -w' -o dist/tg-monitor-server-linux-arm64 ./cmd/tg-monitor-server
```

README documents user/directory creation, binary/unit/env installation, permissions, daemon reload/start/status, Caddy TLS, server registration, curl report fixture, latest/history verification, journal inspection, rotation, upgrades, and rollback.

- [ ] **Step 4: Verify GREEN**

Run:

```bash
bash -n scripts/build-server.sh
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/deploytest -count=1
PATH=/tmp/go-toolchain/bin:$PATH bash scripts/build-server.sh
```

If `systemd-analyze` exists, also run `systemd-analyze verify deploy/systemd/tg-monitor.service` and record output.

### Task 8: Process Smoke Test, Review, and Publish

**Files:**
- Create/update (ignored): `.superpowers/sdd/task-2-report.md`
- Modify only if a smoke regression requires a TDD fix: the focused source/test pair.

- [ ] **Step 1: Run full verification with fresh output**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w cmd internal
PATH=/tmp/go-toolchain/bin:$PATH go test -count=1 ./...
PATH=/tmp/go-toolchain/bin:$PATH go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 /tmp/go-toolchain/bin/go build -o /tmp/tg-monitor-server-amd64 ./cmd/tg-monitor-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 /tmp/go-toolchain/bin/go build -o /tmp/tg-monitor-server-arm64 ./cmd/tg-monitor-server
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 2: Run a local binary smoke test**

Use a temporary SQLite file and an available loopback port. Run `server add`, start `serve`, poll `/readyz`, POST a valid metric fixture using the emitted token, run `metrics latest`, wait past the checkpoint, run `metrics history`, send SIGTERM, and verify clean exit. Record commands and outputs without recording the token.

- [ ] **Step 3: Review requirements and secret leakage**

```bash
rg -n 'Authorization|agent_token|token_sha256|BotToken|WebhookSecret' cmd internal README.md deploy scripts
git status --short
git diff --stat
git diff --check
```

Confirm every specification success criterion has direct test/build/smoke evidence and no error/log path formats secret values.

- [ ] **Step 4: Record, commit, and push**

Write every RED/GREEN command and final evidence to `.superpowers/sdd/task-2-report.md`, then:

```bash
git add cmd internal deploy scripts README.md docs/superpowers/plans/2026-07-14-deployable-central-server.md go.mod go.sum
git commit -m "feat: add deployable central server"
git push
```
