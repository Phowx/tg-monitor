# Deployable Linux Agent Design

## Objective

Build the second deployable vertical slice of `tg-monitor`: a native, non-root Linux Agent that periodically collects host metrics, sends the existing `domain.MetricReport` contract to the central server over authenticated HTTPS, runs safely under systemd, and supports a deterministic one-shot mode for installation checks and end-to-end testing.

The central server is already deployed independently and fixes the ingestion protocol. This phase adds the monitored-host side only. Telegram Bot/WebApp, alerts, browser UI, offline sample queues, automatic upgrades, and expanded collectors remain later projects.

## Approach Decision

Three approaches were considered:

1. **Dedicated non-root daemon managed by systemd (selected).** It continuously reads host-level files under `/proc`, calls `statfs` for `/`, computes deltas in memory, and posts directly to the central server. This gives accurate CPU/network rates, clean signal handling, and a small security boundary without requiring root.
2. **Privileged root Agent.** Root would make future per-process, hardware, and protected filesystem collection easier, but none of the first Agent metrics require it. The larger blast radius is unjustified.
3. **One-shot collector invoked by cron or a systemd timer.** This has a simpler process model, but cross-run CPU/network deltas need persistent state, missed executions are harder to diagnose, and retry/recovery behavior becomes fragmented.

The selected design also exposes an explicit `once` command, so operators retain the useful diagnostic property of option 3 without making it the production runtime.

## Scope and Success Criteria

The Agent phase is complete when all of the following are true:

- `tg-monitor-agent` builds with `CGO_ENABLED=0` for `linux/amd64` and `linux/arm64`.
- `tg-monitor-agent run` continuously samples at the configured interval, posts valid reports, handles signals cleanly, and never overlaps requests.
- `tg-monitor-agent once` takes two snapshots separated by one interval, posts exactly one report, and exits success only after HTTP 204.
- The Agent runs as a dedicated unprivileged `tg-monitor-agent` user under a hardened systemd unit.
- It collects aggregate CPU utilization, memory, root filesystem usage, load averages, aggregate non-loopback network totals/rates, uptime, hostname, kernel, OS, and architecture using Linux-native sources.
- Production endpoints require HTTPS. Plain HTTP is accepted only for literal loopback addresses and `localhost`, enabling local smoke tests without weakening remote deployments.
- A production Agent token enters through the root-owned environment file and is used only in process memory to set the outgoing Authorization header. It never appears in command arguments, logs, errors, repository fixtures, or generated JSON.
- Network/timeout/server failures do not create an unbounded queue or overlapping retry loop. Authentication and protocol failures are surfaced as permanent failures.
- Deployment assets and README steps cover registration on the central host, Agent installation on a monitored host, one-shot verification, service startup, journal inspection, token rotation, upgrades, and rollback.
- Automated tests cover configuration, parsers, delta formulas, collection, HTTP classification, runner lifecycle, command behavior, deployment contracts, and a real central-server-plus-Agent process smoke test.

## Executable and Commands

`cmd/tg-monitor-agent` is the only new executable. It has two explicit commands:

- `tg-monitor-agent run`
- `tg-monitor-agent once`

An empty or unknown command returns a usage error before reading the token or starting collection. Both commands load the same focused Agent configuration. The executable creates a `signal.NotifyContext` for `SIGINT` and `SIGTERM`; cancellation during sampling or HTTP delivery exits without printing the token.

`run` is the systemd entrypoint. `once` is used during installation and in the process smoke test. It uses the configured interval, so tests can use a short interval without adding a production-only sampling flag.

## Configuration

Agent configuration is added to the existing `internal/config` package as a focused loader that is independent of Telegram and central-server database settings:

| Environment variable | Default | Validation |
| --- | --- | --- |
| `TG_MONITOR_AGENT_SERVER_URL` | none | Required absolute origin URL; no credentials, query, fragment, or non-root path |
| `TG_MONITOR_AGENT_TOKEN` | none | Required non-whitespace value; never included in an error |
| `TG_MONITOR_AGENT_INTERVAL` | `15s` | Positive Go duration |
| `TG_MONITOR_AGENT_HTTP_TIMEOUT` | `10s` | Positive Go duration |

The configured origin is normalized by removing a trailing slash, then `/api/v1/metrics` is appended internally. HTTPS is mandatory unless the hostname is `localhost` or parses as a loopback IP. A hostname that merely resolves to loopback does not bypass the HTTPS requirement; this keeps validation deterministic and prevents DNS rebinding from changing the security decision.

The systemd environment file is `/etc/tg-monitor/agent.env`, owned by `root:root` with mode `0600`. systemd reads it before dropping privileges. The token is not accepted as a CLI flag.

## Package Boundaries

- `internal/config/agent.go` loads and validates the focused environment contract.
- `internal/linuxmetrics` reads Linux sources, parses bounded fixture-compatible inputs, represents cumulative snapshots, and builds `domain.MetricReport` values from two snapshots.
- `internal/agentclient` serializes a report and performs one authenticated HTTP request. It owns TLS settings and response classification, not scheduling.
- `internal/agentapp` owns the baseline/sample/deliver loop, recovery state, cancellation, and structured operational logging.
- `internal/agentcmd` parses `run`/`once`, supplies production dependencies, and contains no parser or HTTP business logic.
- `cmd/tg-monitor-agent` owns signals and process exit only.

Each layer depends on a narrow interface. Parser tests do not need HTTP, HTTP tests do not read the host, and runner tests use injected collectors, senders, timers, and loggers.

## Linux Metric Collection

### Snapshot Sources and Bounds

Each capture reads:

- `/proc/stat` for aggregate CPU counters, bounded to 1 MiB.
- `/proc/meminfo` for memory, bounded to 64 KiB.
- `/proc/loadavg` for load averages, bounded to 4 KiB.
- `/proc/net/dev` for interface counters, bounded to 1 MiB.
- `/proc/uptime` for uptime, bounded to 4 KiB.
- `statfs("/")` for root filesystem capacity.
- `os.Hostname`, `unix.Uname`, and `runtime.GOARCH` for system identity.

Missing files, malformed units, numeric overflow, impossible values, or context cancellation return wrapped errors that identify the source but never configuration secrets.

### CPU

The first aggregate `cpu` line in `/proc/stat` is parsed as monotonically increasing tick fields. Total is the sum of every numeric field. Idle is `idle + iowait`. Given two snapshots:

```text
cpu_pct = 100 * (delta_total - delta_idle) / delta_total
```

If total counters decrease or `delta_total` is zero, the pair is rejected as a counter reset and the runner re-baselines without sending that cycle. The final percentage is clamped only for floating-point rounding at the `[0, 100]` boundaries; structurally invalid deltas are not hidden.

### Memory

Values from `/proc/meminfo` must use the `kB` unit and are converted with `value * 1024`.

- Total is `MemTotal`.
- Available is `MemAvailable` when present.
- On older kernels without `MemAvailable`, available falls back to `MemFree + Buffers + Cached + SReclaimable - Shmem` and is clamped to `[0, total]`.
- Used is `total - available`.

### Root Filesystem

`statfs("/")` supplies:

- Total bytes: `Blocks * Bsize`.
- Used bytes: `(Blocks - Bfree) * Bsize`.

This matches the filesystem's allocated-block view. Only `/` is reported in this phase.

### Load and Uptime

The first three finite, non-negative fields of `/proc/loadavg` become `load_1`, `load_5`, and `load_15`. The first finite, non-negative field of `/proc/uptime` is floored to whole seconds.

### Network

`/proc/net/dev` receive and transmit byte counters are summed across all interfaces except exactly `lo`. The current cumulative totals are reported directly. Rates use the monotonic elapsed duration between snapshots:

```text
bytes_per_second = delta_bytes / elapsed_seconds
```

If a direction's cumulative counter decreases because of interface replacement or reset, that direction's rate is zero for the cycle while the current total becomes the next baseline. Zero or negative elapsed duration rejects the snapshot pair.

### Identity and Capture Time

- `hostname`: `os.Hostname()` after non-empty trimming.
- `os`: literal `linux`.
- `kernel`: release returned by `unix.Uname`.
- `arch`: `runtime.GOARCH`.
- `captured_at`: the second snapshot's UTC Unix milliseconds.

The builder calls `MetricReport.Validate` before returning. The Agent sends exactly the central server's existing JSON contract; no Agent-specific fields or server ID are added.

## HTTP Delivery

`internal/agentclient` accepts the normalized endpoint, raw token, timeout, and an injectable `http.RoundTripper`. The production transport clones `http.DefaultTransport`, retains proxy and connection pooling behavior, and sets TLS minimum version 1.2. The `http.Client` timeout is the configured end-to-end timeout.

Each delivery:

1. Validates the report locally.
2. Encodes one JSON object in memory.
3. Creates a context-bound POST to `/api/v1/metrics`.
4. Sets `Content-Type: application/json`, `Authorization: Bearer <token>`, and a stable `User-Agent`.
5. Executes one request and closes a response body bounded during discard.
6. Treats only HTTP 204 with an empty protocol result as success.

At most 64 KiB of the response body is discarded before close; it is never copied into returned errors or logs. Errors expose only a generic operation, network error class, or HTTP status.

Response classification is explicit:

- **Permanent:** 2xx other than 204, 400, 401, 403, 404, 405, 413, 415, and 422. These indicate credentials, endpoint, client contract, or protocol mismatch.
- **Retryable:** connection/DNS/TLS/timeouts, 408, 409, 425, 429, and all 5xx statuses.
- **Other 3xx/4xx:** permanent to avoid silently following an unexpected endpoint or retrying an unsupported contract.

Redirect following is disabled so the Authorization header cannot be forwarded to another origin.

## Runtime Loop and Error Handling

The runner captures a baseline, waits one interval, captures the next snapshot, builds a report, and performs at most one delivery. It never starts another capture/delivery concurrently.

After delivery completes, the current snapshot is the baseline and a fresh cancelable interval timer starts. HTTP latency therefore extends the cadence instead of creating catch-up samples or overlapping requests. Rate calculations always use the snapshots' actual monotonic elapsed duration.

After a valid report is built, the current snapshot becomes the next baseline whether delivery succeeds or fails. This intentionally avoids replay/backlog behavior and keeps rates tied to adjacent local sampling windows. A failed delivery therefore creates a visible gap at the server rather than a stale burst later.

For `run`:

- Transient collection failures are logged and retried at the next interval while retaining the last valid baseline.
- CPU reset or invalid elapsed-time errors replace the baseline and skip that report.
- Retryable delivery failures are logged without response bodies or tokens, then retried on the next normal interval.
- Repeated equivalent delivery failures are suppressed; a transition into failure and later recovery are logged once.
- Permanent delivery errors terminate `run` nonzero so systemd and operators see a failed service rather than an apparently healthy but unauthenticated Agent.
- Context cancellation interrupts timers and in-flight HTTP requests and exits cleanly.

For `once`, any collection, report-building, or delivery failure is returned immediately and produces a nonzero process exit. It never retries internally.

There is no disk queue. Offline duration will be derived later from the central server's receive timestamps; lost samples are accepted for this first Agent slice.

## Logging and Secret Safety

The Agent uses JSON `slog` output under systemd. Logs may include event name, endpoint origin without userinfo, interval, HTTP status, failure class, and recovery state. They never include:

- The Authorization header or token.
- Encoded request JSON.
- Response bodies.
- Environment dumps.
- Command lines containing credentials (credentials are not accepted there).

Configuration and HTTP tests use distinctive canary tokens and assert the canaries and their SHA-256/base64 representations do not appear in errors or logs.

## Deployment Artifacts

This phase adds:

- `deploy/systemd/tg-monitor-agent.service`
- `deploy/systemd/agent.env.example`
- `scripts/build-agent.sh`
- Agent install, smoke, upgrade, rotation, and rollback sections in `README.md`

The unit uses:

- `User=tg-monitor-agent` and `Group=tg-monitor-agent`.
- `EnvironmentFile=/etc/tg-monitor/agent.env`.
- `ExecStart=/usr/local/bin/tg-monitor-agent run`.
- `Restart=on-failure` with a 30-second delay.
- `NoNewPrivileges`, `PrivateTmp`, `PrivateDevices`, `ProtectSystem=strict`, `ProtectHome=true`, kernel/control-group protections, an empty capability set, and `ProtectProc=invisible` while retaining access to global `/proc` metric files.

It does not use `PrivateNetwork` because outbound HTTPS and DNS are required. No writable state directory is needed.

The README flow is deterministic:

1. Register the monitored server on the central host and capture the one-time token.
2. Install the architecture-matched Agent binary, unit, and root-only environment file on the monitored host.
3. Run `tg-monitor-agent once` as the service user and verify HTTP 204 through its exit status.
4. Start/enable the service and inspect the Agent journal.
5. Query `metrics latest` and `metrics history` on the central host.
6. Rotate a token by stopping the Agent, rotating centrally, replacing the environment value, and restarting.

## Testing Strategy

Strict RED-to-GREEN TDD applies to every exported behavior.

- Config tests cover defaults, overrides, URL normalization, HTTPS enforcement, loopback exceptions, forbidden URL components, positive durations, required token, and error secrecy.
- Parser tests use fixed `/proc` fixtures for normal, older-kernel memory fallback, whitespace, interface exclusion, malformed input, overflow, and bounds.
- Delta tests prove CPU formulas, network aggregation/rates, resets, elapsed time, timestamp selection, and final domain validation.
- Collector tests inject file openers, statfs, hostname, uname, architecture, and clock dependencies; one Linux integration test validates a real host snapshot without asserting machine-specific values.
- HTTP client tests use `httptest` and a recording transport to prove exact method/path/headers/body, redirect refusal, TLS minimum, timeout/cancellation, status classification, body closure, and secret-free errors.
- Runner tests use fake snapshots, timers, sender outcomes, and logs to prove baseline timing, no overlap, transient recovery, suppression, re-baselining, permanent termination, once semantics, and cancellation.
- Command tests prove validation before collection, `run`/`once` dispatch, signal-neutral dependency behavior, and secret-free errors.
- Deployment tests assert unit hardening, root-only environment guidance, loopback development exception documentation, and both CGO-free target builds.
- Final process smoke starts the real central server on loopback, registers a server, runs the real Agent in `once` mode against actual Linux `/proc`, verifies latest/history in SQLite, checks Agent/server logs for the canary token, then proves the long-running Agent exits cleanly on SIGTERM.

Final verification runs `gofmt`, `go test -count=1 ./...`, `go vet ./...`, shell syntax checks, both central-server and Agent target builds, the full process smoke, secret scans, and `git diff --check` before commit and push.

## Real-Machine Acceptance

Repository-local process smoke proves the same binary protocol and lifecycle used on a host. Actual remote deployment becomes authorized when the user supplies the target host and SSH access. On that host, acceptance requires:

- systemd reports the Agent active under the non-root account;
- the Agent journal shows startup and a successful-delivery transition without credentials;
- the central CLI shows the monitored host's actual hostname and plausible current metrics;
- minute history appears after the central checkpoint interval;
- stopping networking produces one failure transition, restoration produces one recovery transition, and no backlog burst occurs;
- `systemctl stop` exits within the HTTP timeout and leaves no Agent process.

## Explicit Out of Scope

- Root privileges or privileged capabilities.
- Per-process, per-container, GPU, temperature, SMART, or package metrics.
- Multiple filesystem/mount metrics.
- Offline disk queues or historical replay.
- Automatic discovery, self-registration, token retrieval, or remote configuration.
- Automatic binary upgrade or rollback.
- Custom CA files, mutual TLS, or an insecure TLS bypass.
- Telegram notifications, alert evaluation, and browser UI.
