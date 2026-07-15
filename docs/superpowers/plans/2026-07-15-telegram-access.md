# Telegram Access and Trusted Sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional, production-safe Telegram webhook, private administrator commands, verified Mini App init-data login, and opaque trusted sessions to the existing central server without changing core-only operation.

**Architecture:** Keep one central process, listener, SQLite store, and shutdown path. Add focused configuration, a sequential schema migration, isolated Telegram transport/auth/session packages, and mount them only when the explicit enable flag is true; webhook mutation remains an explicit CLI action.

**Tech Stack:** Go 1.26.0 module directive, Go 1.26.5 toolchain, standard-library HTTP/JSON/HMAC/logging, modernc SQLite, Telegram Bot API, systemd, Caddy, and Bash real-process smoke testing.

## Global Constraints

- Use `/tmp/go-toolchain/bin/go` for every Go command.
- Follow strict RED→GREEN TDD for every behavior and record commands in ignored `.superpowers/sdd/task-4-report.md`.
- Keep the central and Agent executables CGO-free and build both for `linux/amd64` and `linux/arm64`.
- Telegram stays disabled unless `TG_MONITOR_TELEGRAM_ENABLED=true`; core-only serving and administration never require Telegram secrets.
- Only `true`, `false`, or an empty enable flag are accepted; an empty value means disabled.
- Production webhook registration requires HTTPS; HTTP is accepted only for `localhost` or a literal loopback address during local serve/session tests.
- Bot token, webhook secret, init data, HMAC/hash bytes, session token/cookie, Telegram text, Bot API bodies/descriptions, and user JSON must never appear in errors or logs.
- Bot API clients use TLS 1.2 minimum, a finite timeout, no redirects, bounded request/response bodies, and only `sendMessage`, `setWebhook`, `getWebhookInfo`, and `deleteWebhook`.
- Webhook bodies are limited to 256 KiB; auth request bodies are limited to 64 KiB; init data has its own 16 KiB raw-string limit.
- Webhook updates are recorded before command side effects and duplicate update IDs return 204 without another Bot API call.
- Process only allowlisted administrators in private chats; all other Telegram senders and chat types are silently acknowledged.
- Session tokens contain 32 random bytes encoded with unpadded base64url and are persisted only as SHA-256 hashes.
- HTTPS cookies use `__Host-tg_monitor_session`, `HttpOnly`, `Secure`, `SameSite=Strict`, `Path=/`, no `Domain`, and configured expiry; loopback HTTP tests use `tg_monitor_session` without `Secure`.
- No CORS headers, WebApp assets, chart/UI code, alert worker/outbox delivery, group commands, long polling, automatic webhook registration, or custom production Bot API endpoint belongs in this phase.

## File Map

- `internal/config/application.go`, `application_test.go`: optional Telegram module configuration, strict enable flag, public-origin validation, and focused Telegram CLI loading.
- `internal/storage/sqlite/migrations.go`, `sqlite_test.go`: sequential schema migration from version 0 or 1 through version 2.
- `internal/storage/sqlite/telegram.go`, `telegram_test.go`: atomic update dedupe plus expired session/update cleanup.
- `internal/telegramapi/types.go`: minimal Bot API JSON types.
- `internal/telegramapi/client.go`, `client_test.go`: bounded, redirect-free, token-safe Bot API transport.
- `internal/telegramauth/initdata.go`, `initdata_test.go`: strict Telegram Mini App HMAC verification and administrator authorization.
- `internal/sessionapi/handler.go`, `handler_test.go`: login, bootstrap, logout, origin enforcement, and exact cookie contract.
- `internal/telegrambot/commands.go`, `commands_test.go`: command parsing, help/preferences, status aggregation, and output bounding.
- `internal/telegrambot/webhook.go`, `webhook_test.go`: authenticated webhook parsing, update dedupe, silent filtering, and safe dispatch failure behavior.
- `internal/serverapp/access_cleanup.go`, `access_cleanup_test.go`: daily expired-session and old-update cleanup.
- `internal/serverapp/app.go`, `app_test.go`: conditional route composition and shared lifecycle.
- `internal/servercmd/run.go`, `run_test.go`: application-aware serve and explicit webhook CLI operations.
- `deploy/systemd/server.env.example`, `deploy/caddy/Caddyfile.example`: Telegram environment and HTTPS routing notes.
- `scripts/smoke-telegram.sh`: fake Bot API plus real central process, signed init data, session, duplicate webhook, and SIGTERM smoke.
- `internal/deploytest/telegram_assets_test.go`: static safety assertions for deployment/smoke assets.
- `README.md`: production setup, webhook commands, verification, rollback, and secret handling.

---

### Task 1: Optional Application and Telegram Configuration

**Files:**
- Create: `internal/config/application.go`
- Create: `internal/config/application_test.go`

**Interfaces:**
- Consumes: `ServerRuntimeConfig`, `LoadServerRuntimeFromEnv`, `envOrDefault`, `durationFromEnv`, and `parseAdminIDs`.
- Produces:

```go
type ApplicationRuntimeConfig struct {
    Server   ServerRuntimeConfig
    Telegram *TelegramRuntimeConfig
}

type TelegramRuntimeConfig struct {
    PublicURL         string
    BotToken         string
    WebhookSecret    string
    AdminTelegramIDs []int64
    SessionTTL       time.Duration
    InitDataMaxAge   time.Duration
    HTTPTimeout      time.Duration
}

func LoadApplicationRuntimeFromEnv() (ApplicationRuntimeConfig, error)
func LoadTelegramRuntimeFromEnv() (TelegramRuntimeConfig, error)
func ValidateWebhookPublicURL(string) error
```

- [ ] **Step 1: Write failing configuration tests**

Create `clearApplicationEnv` covering every server and Telegram variable. The disabled test must prove partial secrets are ignored:

```go
func TestLoadApplicationRuntimeFromEnvDefaultsToCoreOnly(t *testing.T) {
    clearApplicationEnv(t)
    t.Setenv("TG_MONITOR_BOT_TOKEN", "ignored-canary-token")
    got, err := LoadApplicationRuntimeFromEnv()
    if err != nil { t.Fatal(err) }
    if got.Telegram != nil { t.Fatalf("Telegram = %#v, want nil", got.Telegram) }
    if got.Server.ListenAddr != "127.0.0.1:8080" { t.Fatalf("server = %#v", got.Server) }
}
```

The enabled test sets `https://monitor.example.com`, token `123456:canary-bot-token`, secret `Webhook_Secret-1`, IDs `101,202`, TTL `3h`, max age `90s`, and timeout `4s`, then compares the complete struct. Table tests reject enable values `1`, `yes`, `TRUE`, whitespace-only-plus-text; public URL credentials/query/fragment/non-root paths; remote HTTP; missing token/secret/admin IDs; invalid secret length or alphabet; duplicate/non-positive IDs; and non-positive/invalid durations. Accept `http://localhost:8080`, `http://127.0.0.1:8080`, and `http://[::1]:8080` only for runtime serving. Every error is scanned for both canary secrets.

- [ ] **Step 2: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/config -run 'TestLoadApplication|TestLoadTelegram|TestValidateWebhook' -count=1
```

Expected: compile failure because the new types and loaders do not exist.

- [ ] **Step 3: Implement strict parsing and normalization**

Use explicit enable parsing and load Telegram fields only in the enabled branch:

```go
func LoadApplicationRuntimeFromEnv() (ApplicationRuntimeConfig, error) {
    server, err := LoadServerRuntimeFromEnv()
    if err != nil { return ApplicationRuntimeConfig{}, err }
    raw := strings.TrimSpace(os.Getenv("TG_MONITOR_TELEGRAM_ENABLED"))
    switch raw {
    case "", "false":
        return ApplicationRuntimeConfig{Server: server}, nil
    case "true":
        telegram, err := LoadTelegramRuntimeFromEnv()
        if err != nil { return ApplicationRuntimeConfig{}, err }
        return ApplicationRuntimeConfig{Server: server, Telegram: &telegram}, nil
    default:
        return ApplicationRuntimeConfig{}, errors.New("TG_MONITOR_TELEGRAM_ENABLED must be true or false")
    }
}
```

`LoadTelegramRuntimeFromEnv` trims secrets without formatting their values, reuses positive duration parsing, requires 1–256 secret characters matching `^[A-Za-z0-9_-]+$`, and calls `telegramOrigin`. `telegramOrigin` requires an absolute hierarchical URL, `http` or `https`, host, no user/query/fragment, and only empty or `/` path; normalize to `scheme://host` with lowercase scheme. Remote HTTP fails. `ValidateWebhookPublicURL` calls the same parser and additionally requires the normalized scheme to be HTTPS.

- [ ] **Step 4: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/config/application.go internal/config/application_test.go
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/config -count=1
git add internal/config/application.go internal/config/application_test.go
git commit -m "feat: add optional telegram configuration"
```

Expected: all config tests pass and the legacy `LoadFromEnv` contract remains green.

### Task 2: Sequential Migration, Update Dedupe, and Cleanup Repository

**Files:**
- Modify: `internal/storage/sqlite/migrations.go`
- Modify: `internal/storage/sqlite/sqlite_test.go`
- Create: `internal/storage/sqlite/telegram.go`
- Create: `internal/storage/sqlite/telegram_test.go`

**Interfaces:**
- Produces:

```go
func (s *Store) RecordTelegramUpdate(context.Context, int64, int64) (bool, error)
func (s *Store) DeleteTelegramUpdatesBefore(context.Context, int64) (int64, error)
func (s *Store) DeleteExpiredSessions(context.Context, int64) (int64, error)
```

- [ ] **Step 1: Write failing migration tests**

Add tests that open a fresh store and assert versions `[1,2]`, table columns, primary key, check constraints, and `telegram_updates_received_idx`. Build an explicit version-1 database by creating `schema_migrations`, running every `migrationV1` statement in a transaction, inserting settings and version 1, then call `Migrate` and assert only version 2 is appended. Call `Migrate` twice and assert no duplicate migration rows.

```go
func TestMigrateUpgradesVersionOneToTwo(t *testing.T) {
    store := openUnmigratedTestStore(t)
    applyVersionOneFixture(t, store)
    if err := store.Migrate(context.Background()); err != nil { t.Fatal(err) }
    assertSchemaVersions(t, store, []int{1, 2})
    assertTableExists(t, store, "telegram_updates")
}
```

- [ ] **Step 2: Verify migration RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -run 'TestMigrate.*Two|TestMigrateUpgrades' -count=1
```

Expected: failures because the latest version is 1 and the update table is absent.

- [ ] **Step 3: Implement ordered migration execution**

Set `latestSchemaVersion = 2`, define the exact version-2 statements from the design, and replace the single version check with an ordered loop:

```go
for version < latestSchemaVersion {
    switch version + 1 {
    case 1:
        if err := s.applyMigrationV1(ctx); err != nil { return err }
    case 2:
        if err := s.applyMigration(ctx, 2, migrationV2); err != nil { return err }
    }
    version++
}
return nil
```

`applyMigration` begins a transaction, executes every statement, inserts the version with `s.nowMS()`, commits, and wraps errors with the numeric migration only. Preserve version-1 settings seeding inside `applyMigrationV1`.

- [ ] **Step 4: Verify migration GREEN**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/storage/sqlite/migrations.go internal/storage/sqlite/sqlite_test.go
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -run 'TestMigrate|TestSchema' -count=1
```

Expected: fresh, upgrade, idempotence, and newer-version rejection tests pass.

- [ ] **Step 5: Write failing repository tests**

Test first insertion returns true, duplicate returns false without changing `received_at_ms`, invalid IDs/times fail, and 32 concurrent calls for one update ID yield exactly one true result and one row. Insert sessions and update IDs immediately before/at the cutoff and assert strict `< cutoff` deletion.

```go
inserted, err := store.RecordTelegramUpdate(ctx, 9001, 10_000)
if err != nil || !inserted { t.Fatalf("first = %v, %v", inserted, err) }
inserted, err = store.RecordTelegramUpdate(ctx, 9001, 20_000)
if err != nil || inserted { t.Fatalf("duplicate = %v, %v", inserted, err) }
```

- [ ] **Step 6: Verify repository RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -run 'TestTelegramUpdate|TestExpiredSession' -count=1
```

Expected: compile failure because the repository methods do not exist.

- [ ] **Step 7: Implement atomic insertion and bounded cleanup**

Use `INSERT ... ON CONFLICT(update_id) DO NOTHING`, inspect `RowsAffected`, and never perform a read-before-write dedupe. Cleanup uses `DELETE ... WHERE received_at_ms < ?` and `DELETE ... WHERE expires_at_ms <= ?`; validate positive cutoffs and return affected row counts with operation-class wrappers.

- [ ] **Step 8: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/storage/sqlite
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/storage/sqlite -count=1
git add internal/storage/sqlite/migrations.go internal/storage/sqlite/sqlite_test.go internal/storage/sqlite/telegram.go internal/storage/sqlite/telegram_test.go
git commit -m "feat: persist telegram update dedupe"
```

### Task 3: Safe Minimal Telegram Bot API Client

**Files:**
- Create: `internal/telegramapi/types.go`
- Create: `internal/telegramapi/client.go`
- Create: `internal/telegramapi/client_test.go`

**Interfaces:**
- Produces:

```go
type WebhookInfo struct {
    URL                  string `json:"url"`
    PendingUpdateCount   int    `json:"pending_update_count"`
    LastErrorDate        int64  `json:"last_error_date,omitempty"`
}

type Client struct { /* token and bounded HTTP transport */ }

func New(string, time.Duration) (*Client, error)
func NewForTest(string, string, *http.Client) (*Client, error)
func (c *Client) SendMessage(context.Context, int64, string) error
func (c *Client) SetWebhook(context.Context, string, string) error
func (c *Client) GetWebhookInfo(context.Context) (WebhookInfo, error)
func (c *Client) DeleteWebhook(context.Context) error
```

- [ ] **Step 1: Write failing request-shape and hardening tests**

Use `httptest.Server` to capture method, escaped path, content type, and JSON. Assert `sendMessage` posts `{"chat_id":101,"text":"hello"}` without parse mode; `setWebhook` posts the exact `${PublicURL}/telegram/webhook`, secret token, and `allowed_updates:["message"]`; `getWebhookInfo` decodes only the safe result fields; and `deleteWebhook` posts an empty JSON object.

Add tests for malformed/non-2xx/`ok:false` responses, response over 64 KiB, request context cancellation, 307 redirects, and a custom RoundTripper returning `url.Error` whose URL contains `123456:canary-bot-token`. Assert returned errors and captured logs/formatting never contain token, URL, Bot description, message text, webhook secret, or response body. Assert the production transport has `TLSClientConfig.MinVersion == tls.VersionTLS12`, timeout equals configuration, and `CheckRedirect` returns `http.ErrUseLastResponse`.

- [ ] **Step 2: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/telegramapi -count=1
```

Expected: package/type compile failure.

- [ ] **Step 3: Implement the bounded operation client**

Use a single private call path:

```go
func (c *Client) call(ctx context.Context, method string, requestValue, result any) error {
    body, err := json.Marshal(requestValue)
    if err != nil { return fmt.Errorf("telegram %s: encode request", method) }
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), bytes.NewReader(body))
    if err != nil { return fmt.Errorf("telegram %s: build request", method) }
    req.Header.Set("Content-Type", "application/json")
    response, err := c.httpClient.Do(req)
    if err != nil { return fmt.Errorf("telegram %s: transport failure", method) }
    defer response.Body.Close()
    limited := io.LimitReader(response.Body, (64<<10)+1)
    encoded, err := io.ReadAll(limited)
    if err != nil || len(encoded) > 64<<10 { return fmt.Errorf("telegram %s: invalid response", method) }
    // Decode {ok,result} and intentionally discard description/body details.
}
```

Validate non-empty token without echoing it. Production base URL is `https://api.telegram.org`; test construction accepts loopback only and is not used by application config. The production `http.Transport` sets `Proxy: http.ProxyFromEnvironment` so standard deployment proxy controls and the isolated smoke interceptor work without adding a custom API-base setting. `SetWebhook` calls `config.ValidateWebhookPublicURL` before transport.

- [ ] **Step 4: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/telegramapi
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/telegramapi -count=1
git add internal/telegramapi/types.go internal/telegramapi/client.go internal/telegramapi/client_test.go
git commit -m "feat: add safe telegram api client"
```

### Task 4: Telegram Mini App Init-Data Verification

**Files:**
- Create: `internal/telegramauth/initdata.go`
- Create: `internal/telegramauth/initdata_test.go`

**Interfaces:**
- Produces:

```go
var ErrInvalidInitData = errors.New("invalid Telegram init data")
var ErrUnauthorizedUser = errors.New("Telegram user is not authorized")

type User struct {
    ID        int64  `json:"id"`
    FirstName string `json:"first_name,omitempty"`
    Username  string `json:"username,omitempty"`
}

func VerifyInitData(raw, botToken string, now time.Time, maxAge time.Duration, adminIDs []int64) (User, error)
```

- [ ] **Step 1: Write failing verifier tests with an independent signer**

The test signer URL-encodes fields, sorts all keys except `hash`, derives `secretKey := HMAC-SHA256(key="WebAppData", data=botToken)`, and returns a lowercase hex HMAC. Do not call production helpers from the signer. Verify reordered fields and Unicode JSON:

```go
fields := url.Values{
    "auth_date": {strconv.FormatInt(now.Unix(), 10)},
    "query_id":  {"AAE-canary"},
    "user":      {`{"id":101,"first_name":"管理员","username":"ops"}`},
}
raw := signInitDataForTest(fields, "123456:canary-bot-token")
user, err := VerifyInitData(raw, "123456:canary-bot-token", now, 5*time.Minute, []int64{101})
if err != nil || user.ID != 101 || user.FirstName != "管理员" { t.Fatalf("user=%#v err=%v", user, err) }
```

Reject raw data above 16 KiB; invalid percent encoding; duplicate fields including unrelated fields; missing `hash`, `auth_date`, or `user`; malformed/non-64-character hex; altered signed fields; stale timestamps at more than max age; timestamps over 30 seconds future; zero/negative IDs; unknown user JSON fields; trailing user JSON; non-admin IDs; empty token; and non-positive max age. Scan every error for the bot token, raw init data, hash, and user JSON.

- [ ] **Step 2: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/telegramauth -count=1
```

Expected: package/type compile failure.

- [ ] **Step 3: Implement strict decoding and official HMAC ordering**

Use `url.ParseQuery`, reject any key whose value count is not exactly one, require the three mandatory fields, and sort keys excluding only `hash`. Parse the supplied hash into 32 bytes before `hmac.Equal`. Decode user JSON with `DisallowUnknownFields` and require EOF after one object.

```go
secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
_, _ = secretMAC.Write([]byte(botToken))
checkMAC := hmac.New(sha256.New, secretMAC.Sum(nil))
_, _ = checkMAC.Write([]byte(strings.Join(dataCheckLines, "\n")))
if !hmac.Equal(checkMAC.Sum(nil), suppliedHash) { return User{}, ErrInvalidInitData }
```

Check HMAC before returning timestamp/user authorization details. Normalize all malformed, stale, future, JSON, and signature failures to `ErrInvalidInitData`; use only `ErrUnauthorizedUser` for a valid signed non-admin user.

- [ ] **Step 4: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/telegramauth
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/telegramauth -count=1
git add internal/telegramauth/initdata.go internal/telegramauth/initdata_test.go
git commit -m "feat: verify telegram mini app identity"
```

### Task 5: Trusted Session HTTP Endpoints

**Files:**
- Create: `internal/sessionapi/handler.go`
- Create: `internal/sessionapi/handler_test.go`

**Interfaces:**
- Consumes: `domain.Session`, `telegramauth.VerifyInitData`, and the existing hash-only session repository.
- Produces:

```go
type Repository interface {
    CreateSession(context.Context, string, domain.Session) error
    GetSession(context.Context, string, int64) (domain.Session, error)
    DeleteSession(context.Context, string) error
}

type Config struct {
    PublicURL         string
    BotToken          string
    AdminTelegramIDs []int64
    SessionTTL        time.Duration
    InitDataMaxAge   time.Duration
}

type Dependencies struct {
    Repository Repository
    Random     io.Reader
    Now        func() time.Time
    Verify     func(string, string, time.Time, time.Duration, []int64) (telegramauth.User, error)
    Logger     *slog.Logger
}

func NewHandler(Config, Dependencies) (http.Handler, error)
```

- [ ] **Step 1: Write failing login and cookie tests**

Inject fixed time, verifier returning user 101, and 32 bytes of `0x2a`. Post JSON to `/api/v1/auth/telegram` with an exact origin and assert 204, one repository create containing the base64url token and timestamps, and this HTTPS cookie:

```go
cookie := response.Result().Cookies()[0]
if cookie.Name != "__Host-tg_monitor_session" || cookie.Value != base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x2a}, 32)) {
    t.Fatalf("cookie identity = %#v", cookie)
}
if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
    t.Fatalf("cookie flags = %#v", cookie)
}
```

Assert `MaxAge` and `Expires`, empty response body, no token in logs, and loopback HTTP cookie name/secure behavior. Reject wrong method, wrong/malformed content type, wrong/opaque origin, JSON unknown field, trailing JSON, body over 64 KiB, verifier failure, random short read, and repository failure with stable codes only.

- [ ] **Step 2: Verify login RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/sessionapi -run 'TestTelegramLogin|TestSessionCookie' -count=1
```

Expected: package/type compile failure.

- [ ] **Step 3: Implement login, stable envelopes, and cookie helpers**

`sameOrigin` permits an absent Origin and otherwise requires exactly the configured normalized origin. Parse content type with `mime.ParseMediaType`; use `http.MaxBytesReader`; disallow unknown JSON fields and trailing values. Generate with `io.ReadFull`, encode raw URL base64, create the repository session, and set the cookie only after persistence succeeds.

Errors use the existing envelope shape:

```json
{"error":{"code":"unauthorized","message":"authentication is required"}}
```

Use fixed public codes `method_not_allowed`, `unsupported_media_type`, `forbidden_origin`, `request_too_large`, `invalid_json`, `unauthorized`, and `internal_error`; never forward dependency strings.

- [ ] **Step 4: Write failing bootstrap/logout tests**

GET session with a valid cookie returns exactly `{"telegram_user_id":101,"expires_at":...}\n`. Missing, unknown, and expired cookies return the same 401 body and an expired clearing cookie. POST logout checks origin, deletes a known token, treats missing cookie plus `sqlite.ErrNotFound` or `sqlite.ErrSessionExpired` as 204, always clears the cookie, and maps other repository failures to 500. Assert no endpoint emits `Access-Control-Allow-Origin`.

- [ ] **Step 5: Verify bootstrap/logout RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/sessionapi -run 'TestSessionBootstrap|TestLogout|TestNoCORS' -count=1
```

Expected: failures until the two route branches exist.

- [ ] **Step 6: Implement bootstrap and idempotent logout**

Route only the three exact paths; unknown paths return `not_found`. `clearCookie` repeats the correct name/security/path flags with empty value, `MaxAge=-1`, and an epoch expiry. For bootstrap, use current UTC milliseconds and normalize not-found/expired into one unauthorized response. For logout, clear even when deletion returns an idempotent absence error.

- [ ] **Step 7: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/sessionapi
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/sessionapi -count=1
git add internal/sessionapi/handler.go internal/sessionapi/handler_test.go
git commit -m "feat: add trusted telegram sessions"
```

### Task 6: Administrator Commands and Bounded Status Text

**Files:**
- Create: `internal/telegrambot/commands.go`
- Create: `internal/telegrambot/commands_test.go`

**Interfaces:**
- Consumes: ordered `ListServers`, `ListLatestMetrics`, `GetSettings`, and `SetAlertPreference` repository operations.
- Produces:

```go
type CommandRepository interface {
    ListServers(context.Context) ([]domain.Server, error)
    ListLatestMetrics(context.Context) ([]domain.LatestMetrics, error)
    GetSettings(context.Context) (domain.Settings, error)
    SetAlertPreference(context.Context, int64, bool) error
}

type Commander struct {
    repository CommandRepository
    now        func() time.Time
}

func NewCommander(CommandRepository, func() time.Time) *Commander
func (c *Commander) Reply(context.Context, int64, string) (string, error)
```

- [ ] **Step 1: Write failing parser/help/preference tests**

Assert `/start`, `/help`, `/help@my_bot`, and unknown slash commands return the exact concise help; non-command input returns the exact `/help` prompt. `/alerts_on` and `/alerts_off` call `SetAlertPreference` with administrator ID and expected boolean, then return confirmation text. Repository errors return an operation-class error that excludes text and IDs.

- [ ] **Step 2: Write failing status tests**

At fixed time `1_700_000_000_000`, use threshold 60 seconds and ordered servers covering enabled/fresh, enabled/exact-boundary, enabled/stale, enabled/no-data, and disabled. Assert states, CPU with one decimal, memory percentage from used/total, last-seen age, preserved repository order, and disabled precedence. Include hostile names such as `<node>&*_[]` and assert they appear literally because no parse mode is requested.

Generate enough long server names to exceed the budget; assert `len([]byte(reply)) <= 3800`, every included line is complete UTF-8, and the final line reports the omitted count.

- [ ] **Step 3: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/telegrambot -run 'TestCommand|TestStatus' -count=1
```

Expected: package/type compile failure.

- [ ] **Step 4: Implement exact command routing and status aggregation**

Parse `strings.Fields(text)[0]`, lowercase it, remove one optional suffix beginning at `@`, and switch only on the documented commands. Build a metrics map keyed by server ID. Online uses `ReceivedAtMS >= nowMS-thresholdMS`; missing metrics is offline. Use `memory = 100*used/total`, safe because repository/domain validation guarantees positive totals.

Append whole UTF-8 lines while reserving bytes for `… and N more server(s)`. If the next line plus final suffix would exceed 3,800 bytes, stop and append only the suffix. Never truncate inside a line or rune.

- [ ] **Step 5: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/telegrambot
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/telegrambot -run 'TestCommand|TestStatus' -count=1
git add internal/telegrambot/commands.go internal/telegrambot/commands_test.go
git commit -m "feat: add telegram administrator commands"
```

### Task 7: Authenticated Telegram Webhook Handler

**Files:**
- Create: `internal/telegrambot/webhook.go`
- Create: `internal/telegrambot/webhook_test.go`

**Interfaces:**
- Consumes: `Commander.Reply` and `telegramapi.Client.SendMessage` through interfaces.
- Produces:

```go
type UpdateRepository interface {
    RecordTelegramUpdate(context.Context, int64, int64) (bool, error)
}

type Sender interface {
    SendMessage(context.Context, int64, string) error
}

type Replier interface {
    Reply(context.Context, int64, string) (string, error)
}

type WebhookDependencies struct {
    Updates    UpdateRepository
    Sender     Sender
    Replier    Replier
    Secret     string
    AdminIDs   []int64
    Now        func() time.Time
    Logger     *slog.Logger
}

func NewWebhookHandler(WebhookDependencies) (http.Handler, error)
```

- [ ] **Step 1: Write failing authentication/parsing tests**

POST a minimal valid update containing unknown top-level/message fields and assert it remains accepted. Wrong/missing secret must return a generic 404 identical to an absent route and cause no repository/sender/replier call. Unsupported methods return 405 with `Allow: POST`; malformed/trailing JSON returns 400; a body over 256 KiB returns 413; non-positive update ID returns 400.

The comparison test sends same-length and different-length wrong secrets and only asserts identical public behavior. Production code compares SHA-256 digests with `subtle.ConstantTimeCompare` so length does not create a direct comparison branch.

- [ ] **Step 2: Write failing dedupe/filter/dispatch tests**

For first insert, assert `RecordTelegramUpdate(updateID, nowMS)` happens before `Reply`, then `SendMessage(chatID, reply)`; a duplicate returns 204 without side effects. Store failure returns 503 and no side effects. Missing message/from, non-private chat, non-admin, and channel post all return 204 with no outgoing call. Replier failure and send failure after insertion return 204 and log only `operation`, `update_id`, and trusted admin ID; scan logs for webhook secret, message text, reply, token canary, and dependency error strings.

- [ ] **Step 3: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/telegrambot -run 'TestWebhook' -count=1
```

Expected: compile failure because the webhook handler is absent.

- [ ] **Step 4: Implement bounded decoding, record-first dispatch, and safe logging**

Define only required update/message/from/chat fields and leave decoder unknown-field acceptance enabled. Use `http.MaxBytesReader`, decode one object plus EOF, require positive update ID, and record before inspecting sender authorization.

```go
want := sha256.Sum256([]byte(dependencies.Secret))
got := sha256.Sum256([]byte(request.Header.Get("X-Telegram-Bot-Api-Secret-Token")))
if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
    http.NotFound(writer, request)
    return
}
```

After dedupe, acknowledge silently unless `message.chat.type == "private"`, `message.from.id` is allowlisted, and text is present. Always use the private chat ID for reply delivery. Never log update JSON or text.

- [ ] **Step 5: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/telegrambot
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/telegrambot -count=1
git add internal/telegrambot/webhook.go internal/telegrambot/webhook_test.go
git commit -m "feat: handle telegram administrator webhooks"
```

### Task 8: Application Composition and Daily Access Cleanup

**Files:**
- Create: `internal/serverapp/access_cleanup.go`
- Create: `internal/serverapp/access_cleanup_test.go`
- Modify: `internal/serverapp/app.go`
- Modify: `internal/serverapp/app_test.go`

**Interfaces:**
- Produces:

```go
type AccessCleanupRepository interface {
    DeleteExpiredSessions(context.Context, int64) (int64, error)
    DeleteTelegramUpdatesBefore(context.Context, int64) (int64, error)
}

type AccessCleanupResult struct {
    Sessions int64
    Updates  int64
}

func RunAccessCleanupOnce(context.Context, AccessCleanupRepository, time.Time) (AccessCleanupResult, error)
func New(context.Context, config.ApplicationRuntimeConfig, *slog.Logger) (*App, error)

type telegramSenderFactory func(string, time.Duration) (telegrambot.Sender, error)

type appDependencies struct {
    newTelegramSender telegramSenderFactory
    random            io.Reader
    now               func() time.Time
}

func newWithDependencies(
    context.Context, config.ApplicationRuntimeConfig, *slog.Logger, appDependencies,
) (*App, error)
```

- [ ] **Step 1: Write failing access cleanup tests**

At a fixed UTC time, assert sessions receive `now.UnixMilli()` and updates receive `now.Add(-7*24*time.Hour).UnixMilli()`. If one delete fails, still attempt the other and return joined operation-class errors. Verify counts on success.

- [ ] **Step 2: Verify cleanup RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/serverapp -run 'TestRunAccessCleanup' -count=1
```

Expected: compile failure because access cleanup does not exist.

- [ ] **Step 3: Implement access cleanup and call it at startup/daily tick**

Keep the existing two workers. At startup call both `RunRetentionOnce` and `RunAccessCleanupOnce`, log failures independently, then have the daily retention worker call both functions on each tick. Cancellation, checkpointing, shutdown timeout, and store close behavior remain unchanged.

- [ ] **Step 4: Write failing route composition tests**

Update the core helper to wrap `ServerRuntimeConfig` in `ApplicationRuntimeConfig`. Prove core-only still serves health/readiness and returns 404 for Telegram/auth routes. For Telegram-enabled config with loopback public URL and injected local Bot API transport, assert all four routes are mounted, metrics still ingest, the same store observes sessions/update IDs, and cancellation cleanly shuts down. Assert construction failure for invalid Telegram dependencies prevents startup rather than falling back.

- [ ] **Step 5: Verify composition RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/serverapp -run 'TestNew|TestAppServe.*Telegram|TestAppServeExposes' -count=1
```

Expected: compile failures from the constructor signature until application config and conditional mux wiring are complete.

- [ ] **Step 6: Implement the shared mux and Telegram dependencies**

Construct the core handler first. When `cfg.Telegram != nil`, construct one `telegramapi.Client`, one `telegrambot.Commander`, one webhook handler, and one `sessionapi` handler using the same `*sqlite.Store`. Mount exact paths before `/` fallback:

```go
mux := http.NewServeMux()
mux.Handle("/telegram/webhook", webhook)
mux.Handle("/api/v1/auth/telegram", sessions)
mux.Handle("/api/v1/auth/session", sessions)
mux.Handle("/api/v1/auth/logout", sessions)
mux.Handle("/", core)
```

`New` calls `newWithDependencies` with `telegramapi.New`, `crypto/rand.Reader`, and `time.Now`. Tests call `newWithDependencies` with a local `telegramapi.NewForTest` sender, deterministic random bytes, and a fixed clock. This exact unexported seam injects Bot API transport without exposing a production environment variable. Preserve the existing HTTP server timeout values and shared access logger behavior.

- [ ] **Step 7: Verify GREEN and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/serverapp
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/serverapp -count=1
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/httpapi ./internal/sessionapi ./internal/telegrambot -count=1
git add internal/serverapp/access_cleanup.go internal/serverapp/access_cleanup_test.go internal/serverapp/app.go internal/serverapp/app_test.go
git commit -m "feat: compose telegram server routes"
```

### Task 9: Explicit Webhook CLI Operations

**Files:**
- Modify: `internal/servercmd/run.go`
- Modify: `internal/servercmd/run_test.go`

**Interfaces:**
- Changes `serve` to consume `config.ApplicationRuntimeConfig` while core administration retains `config.ServerRuntimeConfig`.
- Adds:

```go
type TelegramWebhookClient interface {
    SetWebhook(context.Context, string, string) error
    GetWebhookInfo(context.Context) (telegramapi.WebhookInfo, error)
    DeleteWebhook(context.Context) error
}
```

- [ ] **Step 1: Write failing command-dispatch tests**

Extend the harness with separate `LoadServerConfig`, `LoadApplicationConfig`, `LoadTelegramConfig`, `NewTelegramClient`, and serve capture fields. Assert `serve` invokes only application loading; `server` and `metrics` invoke only server loading; webhook commands invoke Telegram config/client only after exact argument validation.

Invalid inputs include `telegram`, unknown subcommand, and one extra argument for each operation. They must fail before any loader/client/store call.

- [ ] **Step 2: Write failing webhook operation tests**

`set-webhook` validates HTTPS before client creation, passes public URL/secret, and prints exactly `webhook=registered\n`. `get-webhook` emits indented JSON with only safe `WebhookInfo`; `delete-webhook` prints `webhook=deleted\n`. Client/config errors are propagated without secret output. Core-only `serve` remains valid without Telegram environment.

- [ ] **Step 3: Verify RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/servercmd -run 'TestServe|TestTelegram|TestInvalidCommands' -count=1
```

Expected: failures because Telegram dispatch and split loaders are absent.

- [ ] **Step 4: Implement split dependency loading and explicit operations**

Top-level dispatch becomes `serve|server|metrics|telegram`. Keep `withStore` bound to `LoadServerConfig`. `runServe` loads `ApplicationRuntimeConfig`. Telegram commands call `LoadTelegramRuntimeFromEnv`, then `telegramapi.New`, and never open SQLite. Validate `set-webhook` HTTPS before factory invocation.

- [ ] **Step 5: Verify GREEN, command regression, and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/servercmd
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/servercmd -count=1
PATH=/tmp/go-toolchain/bin:$PATH go test ./cmd/tg-monitor-server ./internal/config -count=1
git add internal/servercmd/run.go internal/servercmd/run_test.go
git commit -m "feat: add telegram webhook commands"
```

### Task 10: Deployment Assets, Real-Process Smoke, and Runbook

**Files:**
- Modify: `deploy/systemd/server.env.example`
- Modify: `deploy/caddy/Caddyfile.example`
- Create: `scripts/smoke-telegram.sh`
- Create: `internal/deploytest/telegram_assets_test.go`
- Modify: `README.md`

**Interfaces:**
- Produces this stable smoke output and no other secret-bearing output:

```text
telegram_webhook=ok duplicate=ok
telegram_session=ok logout=ok
telegram_sigterm=clean secret_log_scan=clean
```

- [ ] **Step 1: Write failing deployment asset tests**

Assert the environment example contains a commented Telegram block with enable flag, HTTPS origin, token/secret/admin placeholders, session TTL, init-data max age, and HTTP timeout; contains no real token-shaped values; and retains mode guidance. Assert Caddy forwards the entire origin without header/body logging. Assert the smoke script uses `mktemp -d`, a cleanup trap, dynamically allocated loopback ports, real compiled server, real SQLite file, fake local Bot API, independently signed init data, cookie jar, duplicate update ID, SIGTERM wait, and final recursive canary scan.

- [ ] **Step 2: Verify asset RED**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/deploytest -run 'TestTelegram' -count=1
```

Expected: failure because the Telegram deployment/smoke assets are absent.

- [ ] **Step 3: Implement the deterministic smoke script**

Build the server with `CGO_ENABLED=0`. Build a temporary Go fake Bot API HTTPS CONNECT interceptor that creates a short-lived test CA and a DNS certificate valid only for `api.telegram.org`, binds only to loopback, accepts the standard `HTTPS_PROXY` CONNECT flow, records method payloads without printing them, and writes the CA certificate under the smoke temporary directory. Start the unmodified real server with `HTTPS_PROXY` pointing at the interceptor and `SSL_CERT_FILE` pointing at that CA; the application must still request `https://api.telegram.org/bot<TOKEN>/...` and complete normal TLS hostname verification. Enable Telegram with a loopback public URL and canary secrets only in environment variables. Seed one server through the real CLI/database, POST a valid `/status` update twice, verify one fake `sendMessage`, sign init data independently, authenticate with a cookie jar, bootstrap the session, log out, verify the cleared session is 401, send SIGTERM, and wait for exit.

The final scan searches captured stdout/stderr and temporary artifacts for the exact bot token, webhook secret, raw init data, HMAC, raw session cookie, Telegram message text, and fake Bot response descriptions; abort if any match. Print only the three stable lines after all assertions pass.

- [ ] **Step 4: Update deployment examples and README**

Document BotFather creation, mode-0600 `server.env`, secret generation using `[A-Za-z0-9_-]`, enable/restart/readiness, `telegram set-webhook`, `telegram get-webhook`, private `/status`, local smoke, journal canary scan, rollback by disabling Telegram plus optional `delete-webhook`, and that version 2 is additive. Remove the statement that all Telegram capability is merely planned; state WebApp UI and alert delivery remain subsequent phases.

- [ ] **Step 5: Verify assets, script syntax, smoke, and commit**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w internal/deploytest/telegram_assets_test.go
PATH=/tmp/go-toolchain/bin:$PATH go test ./internal/deploytest -count=1
bash -n scripts/smoke-telegram.sh
GO=/tmp/go-toolchain/bin/go bash scripts/smoke-telegram.sh
git add deploy/systemd/server.env.example deploy/caddy/Caddyfile.example scripts/smoke-telegram.sh internal/deploytest/telegram_assets_test.go README.md
git commit -m "feat: add telegram deployment workflow"
```

Expected: asset tests pass and the smoke prints exactly the three stable lines.

### Task 11: Full Verification, Security Review, and Push

**Files:**
- Modify only files proven necessary by failures found in this task.
- Update ignored evidence: `.superpowers/sdd/task-4-report.md`.

**Interfaces:**
- Verifies all earlier interfaces together; introduces no new product behavior.

- [ ] **Step 1: Run formatting and repository diff gates**

```bash
PATH=/tmp/go-toolchain/bin:$PATH gofmt -w cmd internal
git diff --check
git status --short
```

Expected: no formatting/diff errors; only intended tracked changes, if any, are shown.

- [ ] **Step 2: Run fresh unit, integration, vet, and race gates**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test -count=1 ./...
PATH=/tmp/go-toolchain/bin:$PATH go vet ./...
PATH=/tmp/go-toolchain/bin:$PATH go test -race -count=1 ./...
```

Expected: all packages pass; vet is silent; race detector reports no race.

- [ ] **Step 3: Run cross-build and deployment gates**

```bash
GO=/tmp/go-toolchain/bin/go bash scripts/build-server.sh
GO=/tmp/go-toolchain/bin/go bash scripts/build-agent.sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 /tmp/go-toolchain/bin/go build -o /tmp/tg-monitor-server-amd64 ./cmd/tg-monitor-server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 /tmp/go-toolchain/bin/go build -o /tmp/tg-monitor-server-arm64 ./cmd/tg-monitor-server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 /tmp/go-toolchain/bin/go build -o /tmp/tg-monitor-agent-amd64 ./cmd/tg-monitor-agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 /tmp/go-toolchain/bin/go build -o /tmp/tg-monitor-agent-arm64 ./cmd/tg-monitor-agent
systemd-analyze verify deploy/systemd/tg-monitor.service deploy/systemd/tg-monitor-agent.service
bash -n scripts/smoke-agent.sh scripts/smoke-telegram.sh
```

Expected: every build succeeds; systemd reports at most the expected missing `/usr/local/bin` installed-binary warning.

- [ ] **Step 4: Run both real-process smokes**

```bash
GO=/tmp/go-toolchain/bin/go bash scripts/smoke-agent.sh
GO=/tmp/go-toolchain/bin/go bash scripts/smoke-telegram.sh
```

Expected: Agent smoke ends `token_log_scan=clean`; Telegram smoke ends `telegram_sigterm=clean secret_log_scan=clean`.

- [ ] **Step 5: Perform manual security and scope review**

Inspect every diff for raw-secret formatting, headers/bodies logged, redirects, unbounded reads, permissive remote HTTP, automatic webhook registration, non-admin/group processing, session token persistence, missing origin checks, and changes belonging to WebApp UI or alert delivery. Use repository searches for canary literals, token-shaped fixtures outside tests/examples, `InsecureSkipVerify`, request/response body logging, and cookie values. Classify each match in the evidence report.

- [ ] **Step 6: Commit any review corrections and rerun affected/full gates**

For each discovered defect, first add a failing regression test, run it to record RED, implement the minimal correction, run targeted GREEN, then rerun Steps 1–4. Commit one coherent correction with a specific message; if no defect is found, create no empty commit.

- [ ] **Step 7: Push and verify remote identity**

```bash
git status --short --branch
git push origin codex/telegram-monitor
git rev-parse HEAD
git ls-remote --heads origin codex/telegram-monitor
```

Expected: clean worktree, push succeeds, and local HEAD exactly equals the remote branch SHA.
