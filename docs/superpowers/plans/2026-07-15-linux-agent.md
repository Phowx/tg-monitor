# Deployable Linux Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a non-root, CGO-free Linux Agent that collects the existing host metric contract, posts it safely to the central server in `run` or `once` mode, installs under systemd, and passes a real two-process smoke test.

**Architecture:** Add a focused Agent loader to the existing config package, then layer pure Linux parsers and delta calculation under an injectable collector. Keep authenticated HTTP delivery, scheduling/recovery, command dispatch, and process signals in separate packages so every behavior can be driven by deterministic tests before the real `/proc` and network integration test.

**Tech Stack:** Go 1.26.0 module directive, Go 1.26.5 toolchain, standard-library HTTP/JSON/logging, `golang.org/x/sys/unix`, Linux `/proc`, `statfs`, systemd, Bash deployment verification.

## Global Constraints

- Use `/tmp/go-toolchain/bin/go` for every Go command.
- Follow strict RED→GREEN TDD for every exported behavior and record commands in ignored `.superpowers/sdd/task-3-report.md`.
- Keep both executables CGO-free and build the Agent for `linux/amd64` and `linux/arm64`.
- The Agent runs without root or Linux capabilities and writes no persistent state.
- Use the existing `domain.MetricReport` JSON without adding a server ID or Agent-only field.
- Require HTTPS except for `localhost` or a literal loopback IP; never add an insecure TLS option.
- Never log, format into errors, serialize, persist, or pass the raw Agent token on the command line.
- The run loop has at most one capture/delivery in flight and never creates an offline queue or catch-up burst.
- Production default interval is 15 seconds and HTTP timeout is 10 seconds.
- Root filesystem `/` and aggregate non-loopback network traffic are the only disk/network scopes.
- Do not add Telegram, alerts, UI, process/container/hardware metrics, self-registration, custom CA flags, or automatic upgrades.

## File Map

- `internal/config/agent.go`, `agent_test.go`: Agent environment contract and HTTPS/loopback policy.
- `internal/linuxmetrics/snapshot.go`: snapshot and counter value types.
- `internal/linuxmetrics/parsers.go`, `parsers_test.go`: bounded-input-compatible `/proc` parsers.
- `internal/linuxmetrics/report.go`, `report_test.go`: CPU/network deltas and shared report construction.
- `internal/linuxmetrics/collector_linux.go`, `collector_linux_test.go`: real Linux dependency composition and capture.
- `internal/agentclient/client.go`, `client_test.go`: one-request delivery and permanent/retryable classification.
- `internal/agentapp/runner.go`, `runner_test.go`: once/run scheduling, re-baselining, recovery, and cancellation.
- `internal/agentcmd/run.go`, `run_test.go`: explicit command dispatch and production composition.
- `cmd/tg-monitor-agent/main.go`: signal-aware process entrypoint.
- `deploy/systemd/tg-monitor-agent.service`, `agent.env.example`: non-root hardened service assets.
- `scripts/build-agent.sh`: reproducible Agent target builds.
- `scripts/smoke-agent.sh`: token-safe central-server-plus-Agent process smoke.
- `internal/deploytest/agent_assets_test.go`: deployment/build/smoke asset contract.
- `README.md`: monitored-host installation, validation, operation, rotation, upgrade, and rollback.
- `.superpowers/sdd/task-3-report.md`: ignored RED/GREEN and final verification evidence.

---

### Task 1: Focused Agent Configuration

**Files:**
- Create: `internal/config/agent.go`
- Create: `internal/config/agent_test.go`

**Interfaces:**
- Consumes: existing `envOrDefault` and `durationFromEnv` helpers.
- Produces:

```go
type AgentConfig struct {
    EndpointURL string
    Token string
    Interval time.Duration
    HTTPTimeout time.Duration
}

func LoadAgentFromEnv() (AgentConfig, error)
```

- [ ] **Step 1: Write failing Agent configuration tests**

Create table-driven tests with a helper that clears all four Agent variables. Include these exact assertions:

```go
func TestLoadAgentFromEnvDefaultsAndNormalizesEndpoint(t *testing.T) {
    clearAgentEnv(t)
    t.Setenv("TG_MONITOR_AGENT_SERVER_URL", " https://monitor.example.com/ ")
    t.Setenv("TG_MONITOR_AGENT_TOKEN", " canary-agent-token ")
    got, err := LoadAgentFromEnv()
    if err != nil { t.Fatal(err) }
    want := AgentConfig{
        EndpointURL: "https://monitor.example.com/api/v1/metrics",
        Token: "canary-agent-token",
        Interval: 15*time.Second,
        HTTPTimeout: 10*time.Second,
    }
    if got != want { t.Fatalf("got %#v, want %#v", got, want) }
}
```

Also test interval/timeout overrides, `http://127.0.0.1:8080`, `http://[::1]:8080`, and case-insensitive `http://localhost:8080` success. Reject missing URL/token, `http://monitor.example.com`, relative/opaque URLs, credentials, query, fragment, `/prefix`, zero/invalid durations, and errors containing `canary-agent-token`.

- [ ] **Step 2: Verify RED**

Run:

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/config -run 'TestLoadAgent' -count=1
```

Expected: compile failure because `AgentConfig` and `LoadAgentFromEnv` are undefined.

- [ ] **Step 3: Implement exact URL and duration behavior**

Use this validation shape:

```go
func LoadAgentFromEnv() (AgentConfig, error) {
    interval, err := durationFromEnv("TG_MONITOR_AGENT_INTERVAL", 15*time.Second)
    if err != nil { return AgentConfig{}, err }
    timeout, err := durationFromEnv("TG_MONITOR_AGENT_HTTP_TIMEOUT", 10*time.Second)
    if err != nil { return AgentConfig{}, err }
    token := strings.TrimSpace(os.Getenv("TG_MONITOR_AGENT_TOKEN"))
    if token == "" { return AgentConfig{}, errors.New("TG_MONITOR_AGENT_TOKEN is required") }
    endpoint, err := agentEndpoint(os.Getenv("TG_MONITOR_AGENT_SERVER_URL"))
    if err != nil { return AgentConfig{}, err }
    return AgentConfig{EndpointURL: endpoint, Token: token, Interval: interval, HTTPTimeout: timeout}, nil
}
```

`agentEndpoint` uses `url.Parse`, requires scheme/host, rejects `Opaque`, `User`, `RawQuery`, `ForceQuery`, `Fragment`, and any path other than empty or `/`. Lowercase the scheme. Permit `http` only when `strings.EqualFold(parsed.Hostname(), "localhost")` or `net.ParseIP(parsed.Hostname()).IsLoopback()`. Set `Path` to `/api/v1/metrics`; clear `RawPath`.

- [ ] **Step 4: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/config/agent.go internal/config/agent_test.go
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/config -count=1
git add internal/config/agent.go internal/config/agent_test.go
git commit -m "feat: add agent configuration"
```

Expected: tests pass and the commit contains only Agent config files.

### Task 2: Linux `/proc` Parsers and Snapshot Types

**Files:**
- Create: `internal/linuxmetrics/snapshot.go`
- Create: `internal/linuxmetrics/parsers.go`
- Create: `internal/linuxmetrics/parsers_test.go`

**Interfaces:**
- Produces:

```go
type CPUCounters struct { Total, Idle uint64 }
type NetworkCounters struct { RXBytes, TXBytes uint64 }

type Snapshot struct {
    CapturedAt time.Time
    CPU CPUCounters
    MemoryTotalBytes int64
    MemoryUsedBytes int64
    RootDiskTotalBytes int64
    RootDiskUsedBytes int64
    Load1, Load5, Load15 float64
    Network NetworkCounters
    UptimeSeconds int64
    System domain.SystemInfo
}
```

- Internal parser signatures:

```go
func parseCPU(io.Reader) (CPUCounters, error)
func parseMemory(io.Reader) (totalBytes, usedBytes int64, err error)
func parseLoad(io.Reader) (load1, load5, load15 float64, err error)
func parseNetwork(io.Reader) (NetworkCounters, error)
func parseUptime(io.Reader) (int64, error)
```

- [ ] **Step 1: Write failing parser tests using literal fixtures**

Use exact deterministic cases:

```go
func TestParseCPU(t *testing.T) {
    got, err := parseCPU(strings.NewReader("cpu  100 20 30 400 10 5 6 7 8 9\ncpu0 1 2 3 4\n"))
    if err != nil { t.Fatal(err) }
    if got != (CPUCounters{Total: 595, Idle: 410}) { t.Fatalf("got %#v", got) }
}

func TestParseNetworkExcludesLoopback(t *testing.T) {
    fixture := "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n" +
        " lo: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n" +
        "eth0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0\n" +
        " wlan0: 3000 0 0 0 0 0 0 0 4000 0 0 0 0 0 0 0\n"
    got, err := parseNetwork(strings.NewReader(fixture))
    if err != nil { t.Fatal(err) }
    if got != (NetworkCounters{RXBytes: 4000, TXBytes: 6000}) { t.Fatalf("got %#v", got) }
}
```

Memory tests cover `MemAvailable` and the fallback formula. Load/uptime tests cover valid values. Every parser gets missing field, invalid number, negative float, `NaN`/`Inf`, and unsigned-sum/multiply overflow tests. Unknown meminfo keys without `kB` must be ignored; required keys with a wrong unit must fail.

- [ ] **Step 2: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/linuxmetrics -run 'TestParse' -count=1
```

Expected: compile failure because the package/types/parsers do not exist.

- [ ] **Step 3: Implement strict parsers**

CPU: scan the first line whose first field is exactly `cpu`, require at least user/nice/system/idle, parse all following fields with `strconv.ParseUint`, add with overflow checks, and add fields 4 and optional 5 for idle+iowait.

Memory: parse `key: value unit`, process only the required names, require `kB`, multiply with `math.MaxInt64/1024` protection, prefer `MemAvailable`, otherwise calculate `MemFree+Buffers+Cached+SReclaimable-Shmem`, clamp available to `[0,total]`, and return `used=total-available`.

Load/uptime: use `strconv.ParseFloat`; reject NaN, infinity, negative values, missing fields, and uptime above `math.MaxInt64`; floor uptime.

Network: split at the first colon, trim the interface name, require 16 data fields, skip exactly `lo`, parse receive field 0 and transmit field 8, and use checked uint64 addition.

- [ ] **Step 4: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/linuxmetrics
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/linuxmetrics -run 'TestParse' -count=1
git add internal/linuxmetrics/snapshot.go internal/linuxmetrics/parsers.go internal/linuxmetrics/parsers_test.go
git commit -m "feat: parse linux host metrics"
```

### Task 3: Metric Report Delta Builder

**Files:**
- Create: `internal/linuxmetrics/report.go`
- Create: `internal/linuxmetrics/report_test.go`

**Interfaces:**
- Consumes: `Snapshot`, `domain.MetricReport.Validate`.
- Produces:

```go
var ErrCounterReset = errors.New("metric counter reset")
var ErrInvalidElapsed = errors.New("invalid snapshot elapsed time")

func BuildReport(previous, current Snapshot) (domain.MetricReport, error)
```

- [ ] **Step 1: Write failing delta tests**

Build two complete valid snapshots separated by two seconds. Assert:

```go
report, err := BuildReport(previous, current)
if err != nil { t.Fatal(err) }
if report.CPUPct != 75 { t.Fatalf("CPU = %v, want 75", report.CPUPct) }
if report.NetworkRXBytesPerSecond != 200 || report.NetworkTXBytesPerSecond != 400 {
    t.Fatalf("rates = %v/%v", report.NetworkRXBytesPerSecond, report.NetworkTXBytesPerSecond)
}
if report.CapturedAtMS != current.CapturedAt.UTC().UnixMilli() || report.System != current.System {
    t.Fatalf("report identity/time = %#v", report)
}
```

Choose counters so total delta is 400 and idle delta is 100; RX delta 400 and TX delta 800. Add tests for CPU total/idle decrease, zero total delta, zero/negative elapsed, RX-only and TX-only resets yielding zero rate in only that direction, current totals above `math.MaxInt64`, floating rounding clamp, instantaneous field copying, and final domain validation.

- [ ] **Step 2: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/linuxmetrics -run 'TestBuildReport' -count=1
```

Expected: compile failure because `BuildReport` and sentinels are undefined.

- [ ] **Step 3: Implement the exact formulas**

```go
func BuildReport(previous, current Snapshot) (domain.MetricReport, error) {
    elapsed := current.CapturedAt.Sub(previous.CapturedAt).Seconds()
    if elapsed <= 0 || math.IsNaN(elapsed) || math.IsInf(elapsed, 0) {
        return domain.MetricReport{}, ErrInvalidElapsed
    }
    if current.CPU.Total <= previous.CPU.Total || current.CPU.Idle < previous.CPU.Idle {
        return domain.MetricReport{}, ErrCounterReset
    }
    totalDelta := current.CPU.Total - previous.CPU.Total
    idleDelta := current.CPU.Idle - previous.CPU.Idle
    if idleDelta > totalDelta { return domain.MetricReport{}, ErrCounterReset }
    cpu := 100 * float64(totalDelta-idleDelta) / float64(totalDelta)
    rxRate := counterRate(previous.Network.RXBytes, current.Network.RXBytes, elapsed)
    txRate := counterRate(previous.Network.TXBytes, current.Network.TXBytes, elapsed)
    report := domain.MetricReport{
        CapturedAtMS: current.CapturedAt.UTC().UnixMilli(),
        CPUPct: cpu,
        MemoryTotalBytes: current.MemoryTotalBytes,
        MemoryUsedBytes: current.MemoryUsedBytes,
        RootDiskTotalBytes: current.RootDiskTotalBytes,
        RootDiskUsedBytes: current.RootDiskUsedBytes,
        Load1: current.Load1,
        Load5: current.Load5,
        Load15: current.Load15,
        NetworkRXTotalBytes: int64(current.Network.RXBytes),
        NetworkTXTotalBytes: int64(current.Network.TXBytes),
        NetworkRXBytesPerSecond: rxRate,
        NetworkTXBytesPerSecond: txRate,
        UptimeSeconds: current.UptimeSeconds,
        System: current.System,
    }
    if err := report.Validate(); err != nil {
        return domain.MetricReport{}, fmt.Errorf("build metric report: %w", err)
    }
    return report, nil
}
```

Before conversion to `int64`, reject cumulative totals above `math.MaxInt64`. `counterRate` returns zero on a decrease. Clamp CPU only if a rounding artifact crosses zero or 100 by less than `1e-9`; otherwise reject.

- [ ] **Step 4: Verify GREEN, full package, and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/linuxmetrics
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/linuxmetrics -count=1
git add internal/linuxmetrics/report.go internal/linuxmetrics/report_test.go
git commit -m "feat: build agent metric reports"
```

### Task 4: Injectable Real Linux Collector

**Files:**
- Create: `internal/linuxmetrics/collector_linux.go`
- Create: `internal/linuxmetrics/collector_linux_test.go`
- Modify: `go.mod`
- Modify: `go.sum` only if `go mod tidy` changes it.

**Interfaces:**
- Produces:

```go
type Collector struct {
    dependencies collectorDependencies
}

func NewCollector() *Collector
func (c *Collector) Capture(context.Context) (Snapshot, error)
```

- Internal testable types:

```go
type fileSystemStats struct { Blocks, Bfree, Bsize uint64 }
type collectorDependencies struct {
    open func(string) (io.ReadCloser, error)
    statFS func(string) (fileSystemStats, error)
    hostname func() (string, error)
    kernel func() (string, error)
    arch func() string
    now func() time.Time
}
func newCollector(collectorDependencies) *Collector
```

- [ ] **Step 1: Write failing collector tests**

Use a map-backed opener returning distinct fixtures and a close-recording reader. Inject `fileSystemStats{Blocks:1000,Bfree:250,Bsize:4096}`, hostname `host-a`, kernel `6.12.0`, arch `amd64`, and a fixed time. Assert every `Snapshot` field, every file close, and the exact five paths.

Add tests for each opener/parser/statfs/hostname/kernel error, context canceled before and during capture, an input of exactly each bound, one byte over each bound, empty identity, and multiplication overflow. Add a Linux integration test:

```go
func TestCollectorCapturesRealLinuxHost(t *testing.T) {
    snapshot, err := NewCollector().Capture(context.Background())
    if err != nil { t.Fatal(err) }
    if snapshot.CPU.Total == 0 || snapshot.MemoryTotalBytes <= 0 || snapshot.RootDiskTotalBytes <= 0 || snapshot.System.OS != "linux" {
        t.Fatalf("invalid real snapshot: %#v", snapshot)
    }
}
```

- [ ] **Step 2: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/linuxmetrics -run 'TestCollector' -count=1
```

Expected: compile failure because `Collector` and constructors are undefined.

- [ ] **Step 3: Implement bounded capture and Linux dependencies**

Define limits exactly: stat/net 1 MiB, meminfo 64 KiB, loadavg/uptime 4 KiB. `readBounded` reads through `io.LimitReader(reader, limit+1)`, returns an error when length exceeds `limit`, and checks `ctx.Err()` before and after read.

Production `statFS` calls `unix.Statfs("/", &value)` and converts non-negative `Bsize`; `kernel` calls `unix.Uname` and copies bytes until NUL; `arch` returns `runtime.GOARCH`; `now` returns `time.Now()` after all sources are collected. Calculate disk byte products with overflow checks.

Run:

```bash
PATH=/tmp/go-toolchain/bin:$PATH go mod tidy
```

Confirm `golang.org/x/sys` becomes a direct dependency and no unrelated dependency is added.

- [ ] **Step 4: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/linuxmetrics
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/linuxmetrics -count=1
git add internal/linuxmetrics/collector_linux.go internal/linuxmetrics/collector_linux_test.go go.mod go.sum
git commit -m "feat: collect linux host snapshots"
```

### Task 5: Authenticated Agent HTTP Client

**Files:**
- Create: `internal/agentclient/client.go`
- Create: `internal/agentclient/client_test.go`

**Interfaces:**
- Consumes: normalized endpoint/token/timeout and `domain.MetricReport`.
- Produces:

```go
var ErrPermanent = errors.New("permanent agent delivery failure")
var ErrRetryable = errors.New("retryable agent delivery failure")

type Client struct {
    endpointURL string
    token string
    httpClient *http.Client
}

func New(endpointURL, token string, timeout time.Duration) (*Client, error)
func (c *Client) Send(context.Context, domain.MetricReport) error
```

- Internal constructor for tests:

```go
func newClient(endpointURL, token string, timeout time.Duration, transport http.RoundTripper) (*Client, error)
```

- [ ] **Step 1: Write failing request and classification tests**

A recording transport asserts POST, exact endpoint, `application/json`, `Bearer canary-agent-token`, stable `User-Agent`, one valid decoded report, and configured timeout. Assert error/log text never contains the canary, Authorization, encoded body hostname, or response canary.

Use a status table:

```go
tests := []struct{ status int; want error }{
    {204, nil}, {200, ErrPermanent}, {301, ErrPermanent},
    {400, ErrPermanent}, {401, ErrPermanent}, {403, ErrPermanent},
    {404, ErrPermanent}, {405, ErrPermanent}, {408, ErrRetryable},
    {409, ErrRetryable}, {413, ErrPermanent}, {415, ErrPermanent},
    {422, ErrPermanent}, {425, ErrRetryable}, {429, ErrRetryable},
    {500, ErrRetryable}, {503, ErrRetryable},
}
```

Also test invalid local reports never call transport, network errors wrap `ErrRetryable`, cancellation, response body close, a body over 64 KiB not appearing in errors, redirect refusal with no second request, and production transport TLS `MinVersion == tls.VersionTLS12`.

- [ ] **Step 2: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/agentclient -count=1
```

Expected: compile failure because the package/client does not exist.

- [ ] **Step 3: Implement one-attempt delivery**

`New` validates non-empty endpoint/token and positive timeout without formatting token. Clone `http.DefaultTransport`, clone/create its TLS config, set minimum TLS 1.2, and configure:

```go
http.Client{
    Transport: transport,
    Timeout: timeout,
    CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
```

`Send` validates, JSON-encodes, creates the request, sets headers, performs one `Do`, drains at most `64<<10` bytes through `io.LimitReader`, closes the body, and returns only a class/status-safe error. The class function treats exactly the status table above; all other statuses are permanent.

- [ ] **Step 4: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/agentclient
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/agentclient -count=1
git add internal/agentclient
git commit -m "feat: send agent metric reports"
```

### Task 6: Agent Once/Run Lifecycle

**Files:**
- Create: `internal/agentapp/runner.go`
- Create: `internal/agentapp/runner_test.go`

**Interfaces:**
- Consumes:

```go
type Collector interface { Capture(context.Context) (linuxmetrics.Snapshot, error) }
type Sender interface { Send(context.Context, domain.MetricReport) error }
```

- Produces:

```go
type Runner struct {
    collector Collector
    sender Sender
    interval time.Duration
    logger *slog.Logger
    build buildFunc
    wait waitFunc
}

func New(Collector, Sender, time.Duration, *slog.Logger) *Runner
func (r *Runner) Once(context.Context) error
func (r *Runner) Run(context.Context) error
```

- Internal injected functions:

```go
type buildFunc func(linuxmetrics.Snapshot, linuxmetrics.Snapshot) (domain.MetricReport, error)
type waitFunc func(context.Context, time.Duration) error
```

- [ ] **Step 1: Write failing once/run lifecycle tests**

`Once` test records call order exactly `capture, wait(15s), capture, build, send`, asserts one send, and propagates capture/wait/build/send failures. Context cancellation returns nil only when `errors.Is(err, context.Canceled)`.

`Run` tests use a waiter that cancels after a bounded number of calls. Prove:

- initial capture failure logs once, suppresses identical repeats, then recovers;
- capture failure retains the previous baseline;
- `ErrCounterReset`/`ErrInvalidElapsed` replaces the baseline and skips send;
- a valid current snapshot becomes baseline even when delivery fails;
- repeated `ErrRetryable` logs one failure transition and a later success logs one recovery;
- the first success logs one established transition, later successes are silent;
- `ErrPermanent` returns immediately;
- capture/build/send calls never overlap (atomic in-flight counter never exceeds one);
- cancellation during wait and sender cancellation return nil.

Logging assertions parse JSON slog records and forbid a canary token and body hostname.

- [ ] **Step 2: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/agentapp -count=1
```

Expected: compile failure because `Runner` does not exist.

- [ ] **Step 3: Implement serial scheduling and transition logging**

Default waiter:

```go
func wait(ctx context.Context, interval time.Duration) error {
    timer := time.NewTimer(interval)
    defer timer.Stop()
    select {
    case <-ctx.Done(): return ctx.Err()
    case <-timer.C: return nil
    }
}
```

`Run` maintains `*Snapshot previous`, `lastFailureClass string`, and `established bool`. At loop top, obtain a baseline if nil; otherwise wait, capture, build, set the current baseline after valid build, then send. Collection errors retain baseline. Counter/elapsed sentinels replace it. Retryable errors continue only after the next loop's waiter. Permanent errors return. Helper `canceledAsSuccess(ctx, err)` maps only cancellation caused by the supplied context to nil.

- [ ] **Step 4: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/agentapp
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/agentapp -count=1
git add internal/agentapp
git commit -m "feat: add agent runtime loop"
```

### Task 7: Agent Command Layer and Executable

**Files:**
- Create: `internal/agentcmd/run.go`
- Create: `internal/agentcmd/run_test.go`
- Create: `cmd/tg-monitor-agent/main.go`

**Interfaces:**
- Produces:

```go
type Runner interface {
    Run(context.Context) error
    Once(context.Context) error
}

type Dependencies struct {
    Stdout io.Writer
    Stderr io.Writer
    LoadConfig func() (config.AgentConfig, error)
    BuildRunner func(config.AgentConfig, *slog.Logger) (Runner, error)
}

func Run(context.Context, []string, Dependencies) error
```

- [ ] **Step 1: Write failing command tests**

Assert empty, unknown, and extra arguments fail before `LoadConfig`/`BuildRunner`. Assert `run` loads once, builds once with the exact config and non-nil JSON logger, calls only `Runner.Run`; `once` calls only `Runner.Once`. Propagate config/build/runner errors and assert a canary token is absent from every formatted error and stderr.

Compile test the main package with:

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./cmd/tg-monitor-agent
```

- [ ] **Step 2: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/agentcmd ./cmd/tg-monitor-agent -count=1
```

Expected: command package and executable directory are missing.

- [ ] **Step 3: Implement dispatch and production composition**

`Run` accepts exactly one argument, `run` or `once`. Defaults use `os.Stdout`, `os.Stderr`, `config.LoadAgentFromEnv`, and:

```go
func buildRunner(cfg config.AgentConfig, logger *slog.Logger) (Runner, error) {
    sender, err := agentclient.New(cfg.EndpointURL, cfg.Token, cfg.HTTPTimeout)
    if err != nil { return nil, err }
    return agentapp.New(linuxmetrics.NewCollector(), sender, cfg.Interval, logger), nil
}
```

The main executable creates `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`, calls `agentcmd.Run`, prints secret-safe errors to stderr, and exits nonzero.

- [ ] **Step 4: Verify GREEN, CGO-free build, and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/agentcmd cmd/tg-monitor-agent
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/agentcmd ./cmd/tg-monitor-agent -count=1
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 /tmp/go-toolchain/bin/go build -o /tmp/tg-monitor-agent ./cmd/tg-monitor-agent
git add internal/agentcmd cmd/tg-monitor-agent
git commit -m "feat: add agent commands"
```

### Task 8: systemd, Builds, Smoke Script, and Runbook

**Files:**
- Create: `deploy/systemd/tg-monitor-agent.service`
- Create: `deploy/systemd/agent.env.example`
- Create: `scripts/build-agent.sh`
- Create: `scripts/smoke-agent.sh`
- Create: `internal/deploytest/agent_assets_test.go`
- Modify: `README.md`

**Interfaces:**
- Produces a non-root install contract, two Agent artifacts, and a repeatable central-plus-Agent smoke command.

- [ ] **Step 1: Write failing deployment asset tests**

Resolve repository root with `runtime.Caller` and assert exact substrings:

```go
required := map[string][]string{
    "deploy/systemd/tg-monitor-agent.service": {
        "User=tg-monitor-agent", "Group=tg-monitor-agent",
        "EnvironmentFile=/etc/tg-monitor/agent.env",
        "ExecStart=/usr/local/bin/tg-monitor-agent run",
        "Restart=on-failure", "RestartSec=30s", "NoNewPrivileges=true",
        "PrivateTmp=true", "PrivateDevices=true", "ProtectSystem=strict",
        "ProtectHome=true", "ProtectProc=invisible", "CapabilityBoundingSet=",
    },
    "deploy/systemd/agent.env.example": {
        "TG_MONITOR_AGENT_SERVER_URL=https://monitor.example.com",
        "TG_MONITOR_AGENT_TOKEN=replace-with-one-time-token",
        "TG_MONITOR_AGENT_INTERVAL=15s", "TG_MONITOR_AGENT_HTTP_TIMEOUT=10s",
    },
    "scripts/build-agent.sh": {"set -euo pipefail", "CGO_ENABLED=0", "GOARCH=amd64", "GOARCH=arm64"},
    "scripts/smoke-agent.sh": {"tg-monitor-server", "tg-monitor-agent", "token_log_scan=clean"},
}
```

- [ ] **Step 2: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/deploytest -run TestAgentDeploymentAssets -count=1
```

Expected: failure because Agent assets are absent.

- [ ] **Step 3: Add exact assets and README sections**

The systemd unit mirrors central hardening, adds `ProtectProc=invisible`, omits `PrivateNetwork` and writable directories, and uses the exact service fields above.

`agent.env.example` starts with a comment requiring installation as `/etc/tg-monitor/agent.env`, `root:root`, mode `0600`.

`build-agent.sh` resolves repository root, honors `GO=${GO:-go}`, creates `dist`, and runs:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO" build -trimpath -ldflags='-s -w' -o dist/tg-monitor-agent-linux-amd64 ./cmd/tg-monitor-agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 "$GO" build -trimpath -ldflags='-s -w' -o dist/tg-monitor-agent-linux-arm64 ./cmd/tg-monitor-agent
```

`smoke-agent.sh` uses `set -euo pipefail`, builds both host binaries into a temporary directory, selects an unused loopback high port with Bash `/dev/tcp`, registers a server while keeping token output in a variable, starts the central server with a 100ms checkpoint, runs Agent `once` with a 100ms interval, checks latest/history, starts Agent `run`, sends SIGTERM, and scans both logs for the token. Its only stdout is safe summary lines ending in `token_log_scan=clean`.

README must document central registration, monitored-host user creation, architecture selection, binary/unit/env installation, `once`, enable/start, central latest/history, journals, no-public-HTTP rule, rotation order, upgrade/rollback, and the repository smoke command.

- [ ] **Step 4: Verify GREEN and commit**

```bash
chmod +x scripts/build-agent.sh scripts/smoke-agent.sh
bash -n scripts/build-agent.sh
bash -n scripts/smoke-agent.sh
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/deploytest -count=1
GO=/tmp/go-toolchain/bin/go bash scripts/build-agent.sh
git add deploy/systemd/tg-monitor-agent.service deploy/systemd/agent.env.example scripts/build-agent.sh scripts/smoke-agent.sh internal/deploytest/agent_assets_test.go README.md
git commit -m "feat: add agent deployment assets"
```

If `systemd-analyze` exists, run `systemd-analyze verify deploy/systemd/tg-monitor-agent.service` and record any expected pre-install missing-user/binary warning separately from directive parse errors.

### Task 9: Full Process Smoke, Security Review, and Publish

**Files:**
- Create/update ignored: `.superpowers/sdd/task-3-report.md`
- Modify only a focused source/test pair if a verification failure first has a reproducing test.

**Interfaces:**
- Consumes every prior deliverable.
- Produces fresh evidence and a GitHub-pushed Agent phase.

- [ ] **Step 1: Run the complete fresh gate**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w cmd internal
PATH=/tmp/go-toolchain/bin:$PATH go test -count=1 ./...
PATH=/tmp/go-toolchain/bin:$PATH go vet ./...
bash -n scripts/build-server.sh
bash -n scripts/build-agent.sh
bash -n scripts/smoke-agent.sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 /tmp/go-toolchain/bin/go build -trimpath -o /tmp/tg-monitor-server-amd64 ./cmd/tg-monitor-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 /tmp/go-toolchain/bin/go build -trimpath -o /tmp/tg-monitor-server-arm64 ./cmd/tg-monitor-server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 /tmp/go-toolchain/bin/go build -trimpath -o /tmp/tg-monitor-agent-amd64 ./cmd/tg-monitor-agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 /tmp/go-toolchain/bin/go build -trimpath -o /tmp/tg-monitor-agent-arm64 ./cmd/tg-monitor-agent
git diff --check
```

Expected: every command exits zero. Confirm `go version` reports `linux/amd64` and `linux/arm64` for Agent artifacts.

- [ ] **Step 2: Run the real two-process smoke**

```bash
GO=/tmp/go-toolchain/bin/go bash scripts/smoke-agent.sh
```

Expected safe output includes:

```text
registration=ok server_id=1
agent_once=ok ingestion=ok latest=ok history=ok
agent_sigterm=clean token_log_scan=clean
```

No token value may appear in terminal output or `.superpowers/sdd/task-3-report.md`.

- [ ] **Step 3: Review every specification invariant**

```bash
rg -n 'TG_MONITOR_AGENT_TOKEN|Authorization|agent_token|Token|token' cmd internal deploy scripts README.md --glob '!dist/**'
rg -n 'PrivateNetwork|User=root|AmbientCapabilities=[^$]|CapabilityBoundingSet=[^$]' deploy/systemd/tg-monitor-agent.service
rg -n 'http://monitor|InsecureSkipVerify|TLSClientConfig.*Insecure' cmd internal deploy scripts README.md
git status --short
git diff --stat
git diff --check
```

Classify each match as required one-time output, synthetic test canary, safe variable name, or violation. Confirm direct evidence for both commands, every metric source/formula, URL policy, status class, no-overlap/no-queue behavior, hardening, both builds, log secrecy, and process shutdown.

- [ ] **Step 4: Record evidence, final review, commit any verification-only fix, and push**

Write every RED/GREEN command, systemd analysis, full gate, smoke output, and secret classification to `.superpowers/sdd/task-3-report.md` without token values.

If all task commits already contain the final state, do not create an empty commit. Then:

```bash
git status --short --branch
git log --oneline -10
git push origin codex/telegram-monitor
local_sha=$(git rev-parse HEAD)
remote_sha=$(git ls-remote origin refs/heads/codex/telegram-monitor | awk '{print $1}')
test "$local_sha" = "$remote_sha"
```

Expected: clean working tree, push success, and identical local/remote SHA.
