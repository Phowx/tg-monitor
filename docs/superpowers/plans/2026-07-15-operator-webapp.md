# tg-monitor Operator WebApp Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a single-binary Telegram Mini App and authenticated administrator API for live/history visibility, server administration, settings, alert preference, and logout.

**Architecture:** A shared `websession` policy owns origin and cookie attributes, `adminapi` exposes a repository-agnostic authenticated JSON API, and `webapp` embeds a native HTML/CSS/JavaScript SPA. Existing Telegram command and Bot API boundaries gain a structured `/app` reply, while `serverapp` composes all routes only when Telegram is enabled.

**Tech Stack:** Go 1.26, `net/http`, `embed`, SQLite through the existing store, native browser APIs and SVG, Bash real-process smoke tests, Playwright CLI for browser verification; no Node/npm production or build dependency.

## Global Constraints

- Production remains a CGO-free single server binary; `/app` and `/api/v1/admin/*` remain 404 when Telegram is disabled.
- Production HTTPS cookie is `__Host-tg_monitor_session; Path=/; HttpOnly; Secure; SameSite=None; Partitioned`; loopback HTTP uses `tg_monitor_session; SameSite=Strict` without `Secure` or `Partitioned`.
- Every admin response uses `Cache-Control: no-store`; mutations require exact `Origin == TG_MONITOR_PUBLIC_URL`; no CORS headers are emitted.
- Mutation JSON is `application/json`, at most 64 KiB, rejects unknown fields and trailing values, and never exposes repository errors or credentials.
- The static CSP is exactly `default-src 'none'; script-src 'self' https://telegram.org; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'self' https://web.telegram.org`.
- Agent tokens and Telegram init data never enter logs, URLs, browser storage, console output, or HTML attributes; raw Agent tokens appear only in successful create/rotate JSON and the one-time modal.
- Server names and groups are trimmed and limited to 120 UTF-8 bytes; sort order fits signed 32-bit; history requests are bounded to seven days.
- UI works at 320px, supports light/dark Telegram theme variables, keyboard focus, reduced motion, semantic controls, and minimum 44px touch targets.
- Each implementation task follows RED → observed expected failure → GREEN → focused tests → commit, with evidence appended to `.superpowers/sdd/task-4-report.md`.

---

### Task 1: Shared browser-session cookie policy

**Files:**
- Create: `internal/websession/policy.go`
- Create: `internal/websession/policy_test.go`
- Modify: `internal/sessionapi/handler.go`
- Modify: `internal/sessionapi/lifecycle.go`
- Modify: `internal/sessionapi/handler_test.go`
- Modify: `internal/sessionapi/lifecycle_test.go`

**Interfaces:**
- Produces: `websession.Policy`, `websession.NewPolicy(string) (Policy, error)`, `Policy.Set(string, time.Time, time.Duration) *http.Cookie`, and `Policy.Clear() *http.Cookie`.
- Consumed by: `sessionapi` in this task and `adminapi` in Tasks 3–4.

- [ ] **Step 1: Write the failing policy and lifecycle tests**

```go
func TestProductionPolicyUsesHostOnlyPartitionedCookie(t *testing.T) {
    policy, err := NewPolicy("https://monitor.example.com")
    if err != nil { t.Fatal(err) }
    cookie := policy.Set("opaque", time.Unix(1700000000, 0), 12*time.Hour)
    if policy.Origin != "https://monitor.example.com" || cookie.Name != "__Host-tg_monitor_session" || !cookie.HttpOnly || !cookie.Secure || !cookie.Partitioned || cookie.SameSite != http.SameSiteNoneMode || cookie.Domain != "" || cookie.Path != "/" {
        t.Fatalf("production policy = %#v, cookie = %#v", policy, cookie)
    }
    cleared := policy.Clear()
    if cleared.Name != cookie.Name || !cleared.Secure || !cleared.Partitioned || cleared.SameSite != cookie.SameSite || cleared.MaxAge != -1 {
        t.Fatalf("clear attributes differ: %#v", cleared)
    }
}

func TestLoopbackPolicyUsesStrictDevelopmentCookie(t *testing.T) {
    policy, err := NewPolicy("http://127.0.0.1:8080/")
    if err != nil { t.Fatal(err) }
    cookie := policy.Set("opaque", time.Now(), time.Hour)
    if policy.Origin != "http://127.0.0.1:8080" || cookie.Name != "tg_monitor_session" || cookie.Secure || cookie.Partitioned || cookie.SameSite != http.SameSiteStrictMode {
        t.Fatalf("loopback policy = %#v, cookie = %#v", policy, cookie)
    }
}
```

- [ ] **Step 2: Run RED and record the expected missing-package failure**

Run: `/tmp/go-toolchain/bin/go test ./internal/websession ./internal/sessionapi`

Expected: FAIL because `internal/websession` and `NewPolicy` do not exist or because production session tests still observe `SameSite=Strict` without `Partitioned`.

- [ ] **Step 3: Implement the policy and refactor session creation/clearing to use it**

```go
type Policy struct {
    Origin      string
    CookieName  string
    Secure      bool
    SameSite    http.SameSite
    Partitioned bool
}

func NewPolicy(raw string) (Policy, error) {
    origin, secure, err := normalizeOrigin(raw)
    if err != nil { return Policy{}, err }
    policy := Policy{Origin: origin, CookieName: "tg_monitor_session", SameSite: http.SameSiteStrictMode}
    if secure {
        policy.CookieName = "__Host-tg_monitor_session"
        policy.Secure = true
        policy.SameSite = http.SameSiteNoneMode
        policy.Partitioned = true
    }
    return policy, nil
}

func (p Policy) Clear() *http.Cookie {
    return &http.Cookie{Name: p.CookieName, Value: "", Path: "/", Expires: time.Unix(1, 0).UTC(), MaxAge: -1, HttpOnly: true, Secure: p.Secure, SameSite: p.SameSite, Partitioned: p.Partitioned}
}
```

Move existing origin validation into `websession`; store the policy on `sessionapi.Handler`; call `policy.Set` on login and `policy.Clear` on every unauthorized/logout path.

- [ ] **Step 4: Run GREEN and regression tests**

Run: `/tmp/go-toolchain/bin/go test ./internal/websession ./internal/sessionapi ./internal/serverapp`

Expected: PASS with matching create/clear attributes on HTTPS and unchanged loopback session behavior.

- [ ] **Step 5: Commit**

```bash
git add internal/websession internal/sessionapi
git commit -m "feat: support partitioned web sessions"
```

### Task 2: Telegram `/app` command and Web App button transport

**Files:**
- Modify: `internal/telegrambot/commands.go`
- Modify: `internal/telegrambot/commands_test.go`
- Modify: `internal/telegrambot/webhook.go`
- Modify: `internal/telegrambot/webhook_test.go`
- Modify: `internal/telegramapi/client.go`
- Modify: `internal/telegramapi/client_test.go`
- Modify: `internal/serverapp/app.go`
- Modify: `internal/serverapp/app_telegram_test.go`

**Interfaces:**
- Produces: `telegrambot.Reply{Text, WebAppURL, ButtonText string}`, `Replier.Reply(...) (Reply, error)`, `Sender.SendWebAppButton(context.Context, int64, string, string, string) error`.
- Consumes: normalized `TelegramRuntimeConfig.PublicURL` and existing safe Bot API `call` transport.

- [ ] **Step 1: Write failing command, webhook-dispatch, and request-shape tests**

```go
func TestCommandAppReturnsStructuredButton(t *testing.T) {
    commander, err := NewCommander(&commandRepositoryStub{}, "https://monitor.example.com", time.Now)
    if err != nil { t.Fatal(err) }
    got, err := commander.Reply(context.Background(), 42, "/app")
    if err != nil { t.Fatal(err) }
    want := Reply{Text: "Open the tg-monitor operator app.", WebAppURL: "https://monitor.example.com/app/", ButtonText: "Open tg-monitor"}
    if got != want { t.Fatalf("Reply() = %#v, want %#v", got, want) }
}

func TestClientSendWebAppButtonRequestShape(t *testing.T) {
    err := client.SendWebAppButton(context.Background(), 101, "open", "Open tg-monitor", "https://monitor.example.com/app/")
    if err != nil { t.Fatal(err) }
    want := map[string]any{"chat_id": float64(101), "text": "open", "reply_markup": map[string]any{"inline_keyboard": []any{[]any{map[string]any{"text": "Open tg-monitor", "web_app": map[string]any{"url": "https://monitor.example.com/app/"}}}}}}
    if !reflect.DeepEqual(gotBody, want) { t.Fatalf("body = %#v, want %#v", gotBody, want) }
    if _, ok := gotBody["parse_mode"]; ok { t.Fatal("unexpected parse_mode") }
}
```

Add a webhook test whose replier returns a non-empty `WebAppURL`, asserting `SendWebAppButton` is called once and `SendMessage` is not; keep the inverse assertion for plain replies.

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/telegrambot ./internal/telegramapi ./internal/serverapp`

Expected: FAIL because structured replies, `/app`, and `SendWebAppButton` do not exist.

- [ ] **Step 3: Implement structured replies and bounded button sending**

```go
type Reply struct { Text, WebAppURL, ButtonText string }

type Sender interface {
    SendMessage(context.Context, int64, string) error
    SendWebAppButton(context.Context, int64, string, string, string) error
}

func (client *Client) SendWebAppButton(ctx context.Context, chatID int64, text, buttonText, webAppURL string) error {
    if chatID <= 0 || strings.TrimSpace(text) == "" || strings.TrimSpace(buttonText) == "" { return errors.New("telegram sendMessage web app: invalid input") }
    parsed, err := url.Parse(webAppURL)
    if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil { return errors.New("telegram sendMessage web app: invalid URL") }
    request := struct { ChatID int64 `json:"chat_id"`; Text string `json:"text"`; ReplyMarkup struct { InlineKeyboard [][]struct { Text string `json:"text"`; WebApp struct { URL string `json:"url"` } `json:"web_app"` } `json:"inline_keyboard"` } `json:"reply_markup"` }{ChatID: chatID, Text: text}
    request.ReplyMarkup.InlineKeyboard = [][]struct { Text string `json:"text"`; WebApp struct { URL string `json:"url"` } `json:"web_app"` }{{{Text: buttonText, WebApp: struct { URL string `json:"url"` }{URL: webAppURL}}}}
    var accepted bool
    if err := client.call(ctx, "sendMessage", request, &accepted); err != nil { return err }
    if !accepted { return errors.New("telegram sendMessage web app: rejected response") }
    return nil
}
```

Validate the command origin once in `NewCommander`, append `/app/`, include `/app` in help text, and have webhook dispatch on `reply.WebAppURL != ""`.

- [ ] **Step 4: Run GREEN**

Run: `/tmp/go-toolchain/bin/go test ./internal/telegrambot ./internal/telegramapi ./internal/serverapp`

Expected: PASS, including redirect refusal, invalid input without transport, no parse mode, and server composition using the configured public URL.

- [ ] **Step 5: Commit**

```bash
git add internal/telegrambot internal/telegramapi internal/serverapp
git commit -m "feat: open operator app from Telegram"
```

### Task 3: Authenticated overview and history API

**Files:**
- Create: `internal/adminapi/handler.go`
- Create: `internal/adminapi/http.go`
- Create: `internal/adminapi/overview.go`
- Create: `internal/adminapi/history.go`
- Create: `internal/adminapi/handler_test.go`
- Create: `internal/adminapi/overview_test.go`
- Create: `internal/adminapi/history_test.go`

**Interfaces:**
- Produces: `adminapi.NewHandler(Config, Dependencies) (http.Handler, error)` and `Repository` methods named in the design.
- Consumes: `websession.Policy`, `domain.Server`, `domain.LatestMetrics`, `domain.MinuteSample`, `domain.Settings`, `domain.Session`, and `domain.ErrNotFound`/`ErrSessionExpired`.

- [ ] **Step 1: Write failing authorization and read-route tests with a complete repository stub**

```go
type repositoryStub struct {
    session domain.Session; sessionErr error
    servers []domain.Server; serversErr error
    latest []domain.LatestMetrics; latestErr error
    settings domain.Settings; settingsErr error
    preference bool; preferenceErr error
    samples []domain.MinuteSample; samplesErr error
    server domain.Server; serverErr error
}

func TestOverviewAuthenticatesAndMergesOrderedState(t *testing.T) {
    repo := &repositoryStub{session: domain.Session{TelegramUserID: 42}, settings: domain.Settings{OfflineThresholdSeconds: 60}, preference: true, servers: []domain.Server{{ID: 1, Name: "fresh", Enabled: true}, {ID: 2, Name: "disabled", Enabled: false}, {ID: 3, Name: "missing", Enabled: true}}, latest: []domain.LatestMetrics{{ServerID: 1, ReceivedAtMS: 1699999940000}}}
    response := serveAdmin(t, repo, http.MethodGet, "/api/v1/admin/overview", "opaque", "")
    if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Access-Control-Allow-Origin") != "" { t.Fatalf("response = %d %#v", response.Code, response.Header()) }
    var got overviewResponse
    if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil { t.Fatal(err) }
    if got.TelegramUserID != 42 || got.Servers[0].State != "online" || got.Servers[1].State != "disabled" || got.Servers[2].State != "offline" || got.Servers[2].Latest != nil { t.Fatalf("overview = %#v", got) }
}
```

Add table tests for missing/unknown/expired cookie uniform 401 plus matching clear cookie, unexpected session error 500, exact online boundary, each repository failure, history parameter parsing, positive ID, inclusive/exclusive bounds, seven-day maximum, server 404, and ordered sample response.

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/adminapi`

Expected: FAIL because `internal/adminapi` does not exist.

- [ ] **Step 3: Implement handler construction, authentication, JSON helpers, overview, and history**

```go
type Repository interface {
    GetSession(context.Context, string, int64) (domain.Session, error)
    ListServers(context.Context) ([]domain.Server, error)
    GetServer(context.Context, int64) (domain.Server, error)
    CreateServer(context.Context, domain.Server) (domain.Server, error)
    UpdateServer(context.Context, domain.Server) error
    DeleteServer(context.Context, int64) error
    UpdateServerTokenHash(context.Context, int64, []byte) error
    ListLatestMetrics(context.Context) ([]domain.LatestMetrics, error)
    QueryMinuteSamples(context.Context, int64, int64, int64) ([]domain.MinuteSample, error)
    GetSettings(context.Context) (domain.Settings, error)
    UpdateSettings(context.Context, domain.Settings) error
    GetAlertPreference(context.Context, int64) (bool, error)
    SetAlertPreference(context.Context, int64, bool) error
}

type overviewServer struct { Server domain.Server `json:"server"`; State string `json:"state"`; Latest *domain.LatestMetrics `json:"latest"` }
```

Use a private ServeMux with exact method patterns, wrap every request with `Cache-Control: no-store`, authenticate before route work, merge metrics by server ID without reordering servers, and map only `domain.ErrNotFound` to public 404.

- [ ] **Step 4: Run GREEN plus architecture checks**

Run: `/tmp/go-toolchain/bin/go test ./internal/adminapi && ! rg -n 'internal/storage/sqlite' internal/adminapi`

Expected: PASS and no concrete SQLite import.

- [ ] **Step 5: Commit**

```bash
git add internal/adminapi
git commit -m "feat: add operator monitoring API"
```

### Task 4: Server, token, settings, and preference mutations

**Files:**
- Create: `internal/adminapi/servers.go`
- Create: `internal/adminapi/settings.go`
- Create: `internal/adminapi/servers_test.go`
- Create: `internal/adminapi/settings_test.go`
- Modify: `internal/adminapi/handler.go`
- Modify: `internal/adminapi/http.go`

**Interfaces:**
- Produces: POST/PUT/DELETE server routes, rotate-token, GET/PUT settings, and PUT alert-preference.
- Consumes: Task 3 authenticated session context and repository; `auth.GenerateToken(io.Reader)`.

- [ ] **Step 1: Write failing mutation matrix tests**

```go
func TestCreateServerPersistsOnlyHashAndReturnsOneTimeToken(t *testing.T) {
    repo := &repositoryStub{session: domain.Session{TelegramUserID: 42}}
    handler := mustHandler(t, repo, bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)))
    response := requestAdmin(handler, http.MethodPost, "/api/v1/admin/servers", `{"name":" node ","group":" prod ","sort_order":7,"enabled":true}`, "opaque", "https://monitor.example.com")
    if response.Code != http.StatusCreated { t.Fatalf("status = %d body=%s", response.Code, response.Body.String()) }
    if repo.created.Name != "node" || repo.created.Group != "prod" || len(repo.created.TokenSHA256) != sha256.Size { t.Fatalf("persisted = %#v", repo.created) }
    if bytes.Contains(response.Body.Bytes(), repo.created.TokenSHA256) { t.Fatal("response leaked token hash") }
    var got struct { Server domain.Server `json:"server"`; AgentToken string `json:"agent_token"` }
    if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.AgentToken == "" { t.Fatalf("response = %#v, %v", got, err) }
}
```

Add table tests for wrong/missing Origin, media type, unknown field, trailing JSON, oversized body, empty and 121-byte names/groups, int32 overflow, random failure, repository failure without raw-token response/log, update preservation of token/timestamps, rotate empty-object contract, unknown IDs, delete 204/404, all settings bounds, and preference using authenticated user ID rather than input.

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/adminapi -run 'Test(Create|Update|Rotate|Delete|Settings|AlertPreference|Mutation)'`

Expected: FAIL with 404 or missing mutation handlers.

- [ ] **Step 3: Implement strict decoding, validation, mutation handlers, and safe errors**

```go
func decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
    mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
    if err != nil || mediaType != "application/json" { writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json"); return false }
    request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
    decoder := json.NewDecoder(request.Body)
    decoder.DisallowUnknownFields()
    if err := decoder.Decode(destination); err != nil { writeDecodeError(writer, err); return false }
    var trailing any
    if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) { writeDecodeError(writer, err); return false }
    return true
}

func validMetadata(input serverInput) (serverInput, bool) {
    input.Name, input.Group = strings.TrimSpace(input.Name), strings.TrimSpace(input.Group)
    return input, input.Name != "" && len([]byte(input.Name)) <= 120 && len([]byte(input.Group)) <= 120 && int64(input.SortOrder) >= -2147483648 && int64(input.SortOrder) <= 2147483647
}
```

Generate a token before create/rotate, never log it, persist only the hash, and write the raw token only after persistence succeeds. Return 422 for semantic validation and 400 for malformed JSON.

- [ ] **Step 4: Run GREEN and leak scans**

Run: `/tmp/go-toolchain/bin/go test ./internal/adminapi && ! rg -n 'AgentToken|agent_token' internal/adminapi --glob='*.go' | rg 'slog|Printf|Errorf'`

Expected: PASS; token fields exist only in response types/tests and not logging calls.

- [ ] **Step 5: Commit**

```bash
git add internal/adminapi
git commit -m "feat: add operator administration API"
```

### Task 5: Embedded responsive Mini App

**Files:**
- Create: `internal/webapp/handler.go`
- Create: `internal/webapp/handler_test.go`
- Create: `internal/webapp/assets/index.html`
- Create: `internal/webapp/assets/app.css`
- Create: `internal/webapp/assets/app.js`
- Create: `internal/webapp/assets_test.go`

**Interfaces:**
- Produces: `webapp.NewHandler() http.Handler` serving only `/app`, `/app/`, `/app/app.css`, `/app/app.js`.
- Consumes: auth and admin JSON endpoints from Tasks 1, 3, and 4; `window.Telegram.WebApp.initData`, theme params, `ready()`, and `expand()`.

- [ ] **Step 1: Write failing static-route and asset-contract tests**

```go
func TestHandlerServesOnlyAllowlistedAssetsWithSecurityHeaders(t *testing.T) {
    handler := NewHandler()
    html := httptest.NewRecorder()
    handler.ServeHTTP(html, httptest.NewRequest(http.MethodGet, "/app/", nil))
    if html.Code != http.StatusOK || html.Header().Get("Content-Type") != "text/html; charset=utf-8" || html.Header().Get("Content-Security-Policy") != contentSecurityPolicy { t.Fatalf("html = %d %#v", html.Code, html.Header()) }
    for _, path := range []string{"/app/index.html", "/app/assets", "/app/app.js.map", "/app/unknown"} {
        response := httptest.NewRecorder(); handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
        if response.Code != http.StatusNotFound { t.Fatalf("%s status = %d", path, response.Code) }
    }
}

func TestAssetsHonorTelegramBootstrapAndSecretStorageContract(t *testing.T) {
    html := string(asset("assets/index.html")); js := string(asset("assets/app.js"))
    if strings.Index(html, "https://telegram.org/js/telegram-web-app.js") > strings.Index(html, "/app/app.js") { t.Fatal("Telegram bridge must load first") }
    for _, forbidden := range []string{"initDataUnsafe", "localStorage", "sessionStorage", "indexedDB", "innerHTML", "console.log"} {
        if strings.Contains(js, forbidden) { t.Fatalf("app.js contains forbidden %q", forbidden) }
    }
}
```

Add assertions for redirect status/location, GET/HEAD only, content types, immutable JS/CSS cache, no-store HTML, CSP/referrer/nosniff/permissions headers, semantic landmarks/dialog/live regions, range controls, token close/copy controls, settings fields, focus CSS, reduced motion, 44px targets, and 320px media behavior.

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/webapp`

Expected: FAIL because the package and assets do not exist.

- [ ] **Step 3: Implement the exact path handler and functional native SPA**

```go
//go:embed assets/index.html assets/app.css assets/app.js
var files embed.FS

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
    setSecurityHeaders(writer.Header())
    if request.Method != http.MethodGet && request.Method != http.MethodHead { writer.Header().Set("Allow", "GET, HEAD"); http.Error(writer, "method not allowed", http.StatusMethodNotAllowed); return }
    switch request.URL.Path {
    case "/app": http.Redirect(writer, request, "/app/", http.StatusPermanentRedirect)
    case "/app/": serveAsset(writer, request, "assets/index.html", "text/html; charset=utf-8", "no-store")
    case "/app/app.css": serveAsset(writer, request, "assets/app.css", "text/css; charset=utf-8", "public, max-age=31536000, immutable")
    case "/app/app.js": serveAsset(writer, request, "assets/app.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable")
    default: http.NotFound(writer, request)
    }
}
```

The SPA uses one `state` object, `textContent` for names, `fetch(...,{credentials:"same-origin"})`, a single login retry, 15-second visible-only refresh, abortable history requests, SVG paths built from numeric samples, native form/dialog controls, typed-name delete confirmation, and immediate token-state clearing on modal close.

- [ ] **Step 4: Run GREEN and syntax/contract scans**

Run: `/tmp/go-toolchain/bin/go test ./internal/webapp && ! rg -n 'innerHTML|initDataUnsafe|localStorage|sessionStorage|indexedDB|console\.' internal/webapp/assets`

Expected: PASS and no forbidden browser secret/storage/rendering APIs.

- [ ] **Step 5: Commit**

```bash
git add internal/webapp
git commit -m "feat: embed operator mini app"
```

### Task 6: Compose routes and extend real-process smoke

**Files:**
- Modify: `internal/serverapp/app.go`
- Modify: `internal/serverapp/app_telegram_test.go`
- Modify: `internal/serverapp/app_test.go`
- Modify: `scripts/smoke-telegram.sh`
- Modify: `internal/deploytest/telegram_assets_test.go`
- Modify: `README.md`

**Interfaces:**
- Produces: Telegram-enabled `/app*` and `/api/v1/admin/*` routes sharing the production SQLite store; smoke assertions for assets and authenticated reads.
- Consumes: `adminapi.NewHandler`, `webapp.NewHandler`, existing signed-init-data fixture, cookie jar, loopback Bot API TLS interceptor, and server process lifecycle.

- [ ] **Step 1: Write failing composition and deployment-contract tests**

```go
appResponse := get(t, client, baseURL+"/app/")
if appResponse.StatusCode != http.StatusOK || appResponse.Header.Get("Content-Security-Policy") == "" { t.Fatalf("GET /app/ = %d", appResponse.StatusCode) }
overviewRequest, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/admin/overview", nil)
overviewRequest.AddCookie(sessionCookie)
overviewResponse, err := client.Do(overviewRequest)
if err != nil || overviewResponse.StatusCode != http.StatusOK { t.Fatalf("GET overview = %v, %v", overviewResponse, err) }
```

Extend the core-only test to assert `/app`, `/app/`, `/app/app.js`, and `/api/v1/admin/overview` all return the same 404 shape as an absent route. Add a deploy test asserting README contains BotFather `${TG_MONITOR_PUBLIC_URL}/app/`, CSP inspection, authenticated overview smoke, and rollback instructions.

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/serverapp ./internal/deploytest`

Expected: FAIL because routes and runbook entries are absent.

- [ ] **Step 3: Compose handlers and update the production smoke/runbook**

```go
admin, err := adminapi.NewHandler(adminapi.Config{PublicURL: telegram.PublicURL}, adminapi.Dependencies{Repository: store, Random: dependencies.random, Now: dependencies.now, Logger: logger})
if err != nil { return fail(fmt.Errorf("create Telegram routes: admin API: %w", err)) }
mux.Handle("/app", webapp.NewHandler())
mux.Handle("/app/", webapp.NewHandler())
mux.Handle("/api/v1/admin/", admin)
```

Construct the WebApp handler once. Extend `smoke-telegram.sh` to fetch HTML/CSS/JS, verify exact headers, login with signed init data, read overview/history, exercise `/app` webhook button JSON, scan all artifacts for existing and new credential canaries, and require graceful SIGTERM.

- [ ] **Step 4: Run GREEN and real-process smoke**

Run: `/tmp/go-toolchain/bin/go test ./internal/serverapp ./internal/deploytest && bash -n scripts/smoke-telegram.sh && PATH=/tmp/go-toolchain/bin:$PATH bash scripts/smoke-telegram.sh`

Expected: PASS ending with `telegram_webapp=ok`, `telegram_sigterm=clean`, and `secret_log_scan=clean`.

- [ ] **Step 5: Commit**

```bash
git add internal/serverapp internal/deploytest scripts/smoke-telegram.sh README.md
git commit -m "feat: deploy operator web app"
```

### Task 7: Deterministic Playwright operator journey

**Files:**
- Create: `scripts/smoke-webapp.sh`
- Create: `scripts/webapp-browser-test.js`
- Create: `internal/deploytest/webapp_browser_assets_test.go`
- Modify: `README.md`

**Interfaces:**
- Produces: a browser test command and three inspected screenshots under ignored `.superpowers/artifacts/webapp/`.
- Consumes: production server binary, loopback fixture from Task 6, Playwright CLI, fake `window.Telegram.WebApp`, signed init data, and the complete operator API/UI.

- [ ] **Step 1: Read the Playwright skill, then write failing asset-contract tests**

```go
func TestWebAppBrowserSmokeContract(t *testing.T) {
    script := readProjectFile(t, "../../scripts/smoke-webapp.sh")
    for _, required := range []string{"playwright-cli", "mobile-light.png", "mobile-dark.png", "desktop-admin.png", "console", "network", "secret_log_scan"} {
        if !strings.Contains(script, required) { t.Errorf("browser smoke missing %q", required) }
    }
}
```

- [ ] **Step 2: Run RED**

Run: `/tmp/go-toolchain/bin/go test ./internal/deploytest -run WebAppBrowser`

Expected: FAIL because the browser smoke assets are absent.

- [ ] **Step 3: Implement the fixture-driven Playwright journey**

```javascript
await context.addInitScript(({initData, dark}) => {
  window.Telegram = {WebApp: {initData, colorScheme: dark ? 'dark' : 'light', themeParams: dark ? {bg_color:'#111827', text_color:'#f9fafb'} : {bg_color:'#ffffff', text_color:'#111827'}, ready(){window.__tgReady=true}, expand(){window.__tgExpanded=true}}};
}, {initData, dark});
```

Drive bootstrap, dashboard counts, 1h/7d history, create, edit, disable, rotate, copy/close token, assert the raw token leaves DOM, update settings/preference, delete with typed name, logout, keyboard Tab visibility, no horizontal overflow at 320px, zero unexpected console/page errors, same-origin-only requests, and capture mobile light/dark plus desktop admin screenshots.

- [ ] **Step 4: Run browser GREEN and visually inspect screenshots**

Run: `PATH=/tmp/go-toolchain/bin:$PATH bash scripts/smoke-webapp.sh`

Expected: PASS ending with `webapp_browser=ok`, `keyboard=ok`, `network_origin=clean`, and `secret_log_scan=clean`. Inspect all three PNGs for clipping, overlap, unreadable contrast, hidden focus, and modal overflow; correct assets and rerun until clean.

- [ ] **Step 5: Commit**

```bash
git add scripts/smoke-webapp.sh scripts/webapp-browser-test.js internal/deploytest/webapp_browser_assets_test.go README.md
git commit -m "test: verify operator browser journey"
```

### Task 8: WebApp phase release gates and push

**Files:**
- Modify: `.superpowers/sdd/task-4-report.md` (ignored evidence only)
- Review: every file changed since commit `81bf667`

**Interfaces:**
- Produces: verified commits pushed to `origin/codex/telegram-monitor` with exact remote SHA equality.
- Consumes: all task test suites, build scripts, systemd tests, Agent/Telegram/WebApp smoke, screenshots, and Git remote.

- [ ] **Step 1: Run focused and full verification from a clean test cache**

```bash
PATH=/tmp/go-toolchain/bin:$PATH go clean -testcache
PATH=/tmp/go-toolchain/bin:$PATH go test ./...
PATH=/tmp/go-toolchain/bin:$PATH go vet ./...
PATH=/tmp/go-toolchain/bin:$PATH go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 PATH=/tmp/go-toolchain/bin:$PATH go build ./cmd/tg-monitor-server ./cmd/tg-monitor-agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 PATH=/tmp/go-toolchain/bin:$PATH go build ./cmd/tg-monitor-server ./cmd/tg-monitor-agent
PATH=/tmp/go-toolchain/bin:$PATH bash scripts/smoke-agent.sh
PATH=/tmp/go-toolchain/bin:$PATH bash scripts/smoke-telegram.sh
PATH=/tmp/go-toolchain/bin:$PATH bash scripts/smoke-webapp.sh
```

Expected: all commands exit 0; expected systemd asset tests may report only the documented absence of an installed binary, never a unit/configuration error.

- [ ] **Step 2: Run security and scope review**

Run: `git diff 81bf667^..HEAD --check && ! rg -n 'initDataUnsafe|localStorage|sessionStorage|indexedDB|innerHTML|console\.' internal/webapp/assets && ! rg -n 'BotToken|WebhookSecret|AgentToken' internal --glob='*.go' | rg 'slog|Printf|Errorf' && git status --short`

Expected: no whitespace defects, forbidden frontend APIs, credential logging, unexpected generated binaries, or unrelated modifications.

- [ ] **Step 3: Append exact commands/results and screenshot inspection notes to ignored evidence**

Record the observed RED/GREEN outputs, final gate timestamps, Go version, screenshot dimensions/inspection findings, commit SHA, and canary scan results in `.superpowers/sdd/task-4-report.md`; verify `git status --short` remains clean.

- [ ] **Step 4: Push and verify remote equality**

Run: `git push -u origin codex/telegram-monitor && test "$(git rev-parse HEAD)" = "$(git ls-remote origin refs/heads/codex/telegram-monitor | cut -f1)"`

Expected: push succeeds and the equality command exits 0.

- [ ] **Step 5: Report the phase boundary**

Report the public repository/branch, final WebApp commit SHA, exact verification gates, and that offline/recovery evaluation plus durable alert delivery remain the next approved phase; do not mark the overall persistent goal complete at this boundary.
