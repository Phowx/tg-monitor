# tg-monitor Operator WebApp Design

**Date:** 2026-07-15

## Purpose and Phase Boundary

The central server, Linux Agent, private Telegram webhook, administrator commands, Mini App init-data verification, opaque sessions, deployment examples, and real-process smoke tests are already implemented and pushed. This phase turns the existing authenticated session boundary and monitoring repositories into the first complete browser operator experience.

The Operator WebApp is intentionally separate from offline alert evaluation and delivery. It may expose the already-persisted alert thresholds and administrator alert preference, but it does not evaluate state transitions, enqueue notifications, or deliver alerts. Those behaviors receive their own specification after this phase.

Success means an allowlisted Telegram administrator can open the Mini App from the bot, establish or reuse a trusted session, inspect live and historical monitoring data, administer servers and settings, and log out. Core-only deployments remain unchanged.

## Approaches Considered

### 1. Embedded native SPA plus a dedicated authenticated API — selected

Go embeds versioned HTML, CSS, and JavaScript assets in the server binary. A small native SPA uses fetch, accessible HTML controls, and SVG charts. A dedicated `internal/adminapi` package owns session authorization and operator endpoints, while `internal/webapp` owns only static assets and security headers.

This keeps deployment single-binary and CGO-free, avoids a Node production/build dependency, permits responsive interaction, and preserves a clean boundary between transport, presentation, and SQLite.

### 2. Server-rendered templates

Templates would minimize JavaScript but make periodic dashboard refresh, interactive range selection, charts, modal one-time credentials, and Telegram theme changes awkward. It would also mix presentation rendering with authenticated resource handlers.

### 3. React/Vite or another frontend toolchain

A framework would make a larger UI easier to scale, but the first operator surface is small enough that the dependency graph, lockfile, build artifacts, CSP exceptions, and two-language release toolchain are not justified.

## Architecture and Package Boundaries

### `internal/adminapi`

`adminapi` is an authenticated JSON adapter. Production code depends only on narrow repository interfaces and `io.Reader`; it never imports the concrete SQLite package.

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

type Config struct {
    PublicURL string
}

type Dependencies struct {
    Repository Repository
    Random     io.Reader
    Now        func() time.Time
    Logger     *slog.Logger
}

func NewHandler(Config, Dependencies) (http.Handler, error)
```

The handler derives the same session cookie name as `sessionapi`: `__Host-tg_monitor_session` for HTTPS and `tg_monitor_session` only for loopback HTTP. Cookie parsing and repository session lookup happen before route-specific reads, body decoding, or mutations.

### `internal/webapp`

`webapp` embeds `assets/index.html`, `assets/app.css`, and `assets/app.js` with `//go:embed`. It serves only:

- `/app` as a permanent redirect to `/app/`;
- `/app/` as the HTML shell;
- `/app/app.css` and `/app/app.js` with exact content types and immutable asset caching;
- no directory listing, source map, fallback-to-index behavior, or arbitrary embedded path.

The HTML shell is public and contains no monitoring data or credentials. All data comes from the authenticated API.

### Existing Telegram bot transport

The bot gains `/app`. Command replies become a small structured value:

```go
type Reply struct {
    Text       string
    WebAppURL  string
    ButtonText string
}
```

Ordinary commands keep an empty `WebAppURL`. `/app` returns the validated `${TG_MONITOR_PUBLIC_URL}/app/` URL and button label `Open tg-monitor`. The Bot API client adds a bounded `SendWebAppButton` method that sends an inline keyboard with one `web_app` button and no parse mode. Webhook dispatch selects plain `SendMessage` or `SendWebAppButton` from the structured reply.

No startup or CLI command changes BotFather menu state automatically. Operators may additionally configure the same URL as the bot's Mini App/menu URL in BotFather.

### Application composition

When Telegram is enabled, `serverapp` creates one admin handler and one embedded asset handler in addition to the existing webhook and session handlers. They share the same `*sqlite.Store`, clock, random source, logger, and normalized public origin.

Exact routes are mounted before the core `/` fallback:

```text
/app
/app/
/api/v1/admin/
/api/v1/auth/*
/telegram/webhook
/
```

When Telegram is disabled, `/app`, `/app/`, and all `/api/v1/admin/*` requests continue to reach the core 404 response.

## Authentication, CSRF, and HTTP Security

Production HTTPS sessions use the existing `__Host-tg_monitor_session` name with `HttpOnly`, `Secure`, `SameSite=None`, and `Partitioned`. Partitioning permits the session to work when Telegram Web hosts the Mini App in a third-party iframe without turning it into an unpartitioned cross-site credential. Loopback HTTP development retains the non-`__Host-` name, `SameSite=Strict`, and no `Partitioned` attribute because secure cookie attributes are unavailable there. Login and cookie-clearing paths use identical attributes.

Changing the production cookie from `SameSite=Strict` is paired with exact public-origin enforcement for every browser mutation, no CORS, and the existing allowlisted signed Telegram init-data login. Read-only JSON cannot be read cross-origin. The cookie remains opaque, host-only, and inaccessible to JavaScript.

Every admin API response sets `Cache-Control: no-store`. No CORS headers are emitted.

Authentication behavior is uniform:

1. Read the deployment-specific session cookie.
2. Call `GetSession(cookie, nowMS)`.
3. Treat missing cookie, unknown token, and expired token as the same JSON 401 response.
4. Clear the cookie for all three unauthorized cases.
5. Treat other repository errors as a generic JSON 500.

State-changing `POST`, `PUT`, and `DELETE` requests require `Origin` to equal the normalized configured public origin exactly. Unlike the login/logout compatibility endpoints, a missing Origin is rejected for admin mutations because these routes are browser-only. All mutation request bodies require `application/json`, are limited to 64 KiB, reject unknown fields and trailing values, and validate field lengths/ranges before repository calls.

The static handler sends:

- `Content-Security-Policy: default-src 'none'; script-src 'self' https://telegram.org; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'self' https://web.telegram.org`;
- `Referrer-Policy: no-referrer`;
- `X-Content-Type-Options: nosniff`;
- `Permissions-Policy: camera=(), microphone=(), geolocation=()`.

`index.html` loads Telegram's official `https://telegram.org/js/telegram-web-app.js` before `/app/app.js`. The application reads only `Telegram.WebApp.initData`; it never makes authorization decisions from `initDataUnsafe`. It calls `ready()` after the shell is initialized and `expand()` when the bridge is available. This follows Telegram's current Mini App initialization contract.

## Admin JSON API

All JSON uses snake_case. Error envelopes retain the existing shape:

```json
{"error":{"code":"invalid_request","message":"request is invalid"}}
```

Internal errors, raw session cookies, Agent tokens, Telegram init data, names from rejected bodies, and repository details never appear in responses or logs.

### `GET /api/v1/admin/overview`

Returns one consistent dashboard envelope:

```json
{
  "now_ms": 1700000000000,
  "telegram_user_id": 42,
  "alerts_enabled": true,
  "settings": {
    "offline_threshold_seconds": 60,
    "alert_threshold_seconds": 120,
    "history_retention_days": 7
  },
  "servers": [
    {
      "server": {"id": 1, "name": "node", "group": "prod", "sort_order": 0, "enabled": true},
      "state": "online",
      "latest": {"server_id": 1, "received_at": 1699999995000, "report": {}}
    }
  ]
}
```

Servers retain repository order. Metrics are merged by server ID. State uses the same rule as `/status`: disabled metadata wins; otherwise a report at or after `now - offline_threshold` is online; missing or older data is offline. Missing metrics serialize as `latest: null`.

### `GET /api/v1/admin/servers/{id}/history?from_ms=...&to_ms=...`

Requires a positive server ID, non-negative inclusive `from_ms`, exclusive `to_ms > from_ms`, and a range no greater than seven days. It first proves the server exists, then returns ordered minute samples. Unknown servers return 404. The seven-day cap bounds repository work and response size to at most 10,080 persisted minute rows.

### `POST /api/v1/admin/servers`

Body:

```json
{"name":"node","group":"prod","sort_order":0,"enabled":true}
```

Names are trimmed, required, and at most 120 UTF-8 bytes; groups are trimmed and at most 120 bytes. Sort order must fit a signed 32-bit integer. The handler generates exactly 32 random bytes through `auth.GenerateToken`, stores only the returned SHA-256 hash, and responds 201:

```json
{"server":{},"agent_token":"one-time-value"}
```

The response is `no-store`. Persistence must succeed before the raw token is written. Output failures do not cause a second token to be generated or stored.

### `PUT /api/v1/admin/servers/{id}`

Accepts the same metadata body as create. It loads the current server, replaces only name/group/sort order/enabled, and preserves ID, token hash, and timestamps managed by storage. Unknown IDs return 404.

### `POST /api/v1/admin/servers/{id}/rotate-token`

Uses an empty JSON object, generates 32 random bytes, persists only the new hash, and returns `{"server_id":1,"agent_token":"one-time-value"}`. A random-source or persistence failure returns a generic 500 and no token.

### `DELETE /api/v1/admin/servers/{id}`

Deletes the server and cascade-owned latest/history/alert-state data, then returns 204. Unknown IDs return 404. The UI requires an explicit typed-name confirmation before issuing the request; the server does not rely on that presentation control for authorization.

### `GET` and `PUT /api/v1/admin/settings`

GET returns the singleton settings. PUT requires all three positive fields. Offline and alert thresholds are limited to 1–86,400 seconds; retention is limited to 1–365 days. Success returns the normalized settings.

### `PUT /api/v1/admin/alert-preference`

Body is `{"enabled":true}`. The authenticated Telegram user ID, never a request field, selects the preference row. Success returns `{"enabled":true}`. Evaluation and delivery remain out of scope.

## Operator WebApp Experience

### Bootstrap

The shell immediately initializes the Telegram bridge when present, applies Telegram theme CSS variables, and renders a non-sensitive loading state. It then:

1. calls `GET /api/v1/auth/session`;
2. on 200, loads the overview;
3. on 401, reads `Telegram.WebApp.initData` and posts it to `/api/v1/auth/telegram`;
4. retries session bootstrap once after a 204 login;
5. shows a stable “Open this Mini App from the configured Telegram bot” state if no trusted session and no init data exist.

It never stores init data, the session cookie, or Agent tokens in `localStorage`, `sessionStorage`, IndexedDB, URLs, analytics, console output, or DOM attributes.

### Dashboard

The default view shows total, online, offline, and disabled counts followed by the ordered server list. Each row shows state, name/group, CPU, memory, root disk, last-seen age, load, and a details action. Missing metrics are explicit rather than zero-filled.

The overview refreshes every 15 seconds only while `document.visibilityState == "visible"`. Refresh errors leave the last good data visible and show one retryable banner. A 401 returns to bootstrap; it does not loop login requests.

### History

Server details offer 1 hour, 6 hour, 24 hour, and 7 day ranges. Native SVG paths render CPU and memory percentages; a compact table exposes exact timestamps, disk, load, network rates, uptime, and host identity. Empty history has an explicit state. Charts have textual summaries and do not rely on color alone.

### Administration

The Servers view creates and edits metadata, enables/disables servers, rotates tokens, and deletes after typed-name confirmation. Create/rotate displays the Agent token in a modal exactly once with copy and close controls. Closing irreversibly removes the token from application state and DOM.

The Settings view edits thresholds/retention and the current administrator's alert preference. Forms disable while submitting, show field-specific validation without echoing secrets, and update from the successful server response.

### Accessibility and responsive behavior

The UI uses semantic landmarks, native buttons/inputs/dialog behavior, visible keyboard focus, `aria-live` for status messages, minimum 44px touch targets, and reduced-motion media queries. It works from 320px mobile width through desktop. Telegram light/dark theme variables are preferred with safe local fallbacks.

## Error Handling and Logging

API errors have stable public operation classes. Dependency errors are not wrapped into public strings. Access logs include only method, route pattern, status, duration, and authenticated administrator ID. They exclude URL queries, headers, bodies, cookies, init data, Agent tokens, server names, and response payloads.

The frontend maps 400/422 to validation messages, 401 to bootstrap, 404 to a stale-resource refresh, 409 to a conflict banner, and 500 to a retryable generic message. It never renders server-provided strings as HTML; all user-controlled names use `textContent`.

## Testing and Verification

### Go unit and integration tests

- Authentication normalization, cookie clearing, exact Origin enforcement, no CORS, and safe access logs.
- Production session creation and clearing use matching secure partitioned attributes; loopback HTTP keeps strict non-partitioned development cookies.
- Strict body/content-type/size/trailing JSON handling for every mutation.
- Overview merge ordering and exact online boundary, disabled precedence, missing metrics, and current administrator preference.
- History bounds, order, unknown server, and seven-day maximum.
- Create/update/delete/rotate/settings/preference success and every repository/random failure, including raw-token leak scans.
- Static path allowlist, content types, cache policy, CSP/security headers, and absence of inline secrets.
- Bot `/app` structured reply, safe URL validation, inline keyboard request shape, redirect refusal, and no parse mode.
- Server application composition proves Telegram-enabled routes share one store and core-only routes remain 404.

### Browser verification

A deterministic local browser test injects a minimal fake `window.Telegram.WebApp` before application code, uses a local application server and signed init data, and verifies bootstrap, dashboard, history range changes, create/edit/disable/rotate/delete, settings/preference, token modal clearing, logout, keyboard navigation, mobile layout, console errors, and network requests. Screenshots cover mobile light, mobile dark, and desktop dashboard/admin states.

### Real-process smoke

The existing Telegram smoke is extended to fetch `/app/`, assert security headers and referenced assets, and call authenticated overview/history endpoints with the cookie jar. It continues to use the unmodified production Bot API client through the loopback TLS interceptor and recursively scans artifacts for bot token, webhook secret, init data, HMAC, session cookie, Agent token, message text, one-time rotated token, and fake Bot response descriptions.

### Release gates

Fresh unit, vet, race, cross-build, systemd, Bash syntax, Agent smoke, Telegram smoke, browser test, rendered screenshot inspection, canary scans, clean worktree, push, and remote SHA equality are required before this phase is complete.

## Deployment and Rollback

The Mini App URL is `${TG_MONITOR_PUBLIC_URL}/app/`. The runbook adds BotFather menu configuration, readiness, private `/app` verification, authenticated API smoke, CSP inspection, and journal canary scanning.

Rollback is configuration-first: disable Telegram and restart to remove all WebApp/admin routes while preserving core ingestion. No schema migration is required by this phase. Restoring the previous binary is therefore compatible with the current schema version 2.

## Explicit Non-Goals

- Offline/recovery transition evaluation.
- Alert outbox creation, retry scheduling, or Telegram notification delivery.
- Multi-user roles beyond the configured Telegram administrator allowlist.
- Password, OAuth, or non-Telegram login.
- Public dashboards, sharing links, server-side HTML rendering, or CORS clients.
- Node/npm production or build dependencies.
- Arbitrary custom JavaScript, themes, plugins, or localization framework.
- Automatic BotFather/menu configuration.

## Authoritative External Contract

Telegram's official Mini Apps documentation defines the `telegram-web-app.js` initialization bridge, the trusted raw `Telegram.WebApp.initData` field, and `ready()`/`expand()` behavior: <https://core.telegram.org/bots/webapps>. Telegram's client-side Mini App contract requires clients to open the returned URL in a webview and documents iframe event handling for web clients: <https://core.telegram.org/api/bots/webapps> and <https://core.telegram.org/api/web-events>.
