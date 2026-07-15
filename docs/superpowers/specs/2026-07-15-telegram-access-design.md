# Telegram Access and Trusted Session Design

**Date:** 2026-07-15
**Status:** Approved under the standing instruction to use recommended choices and continue the original plan

## Context and Scope

The deployable central server and Linux Agent are already implemented and pushed. The repository foundation also contains administrator Telegram IDs, session TTL and init-data age configuration, hash-only session storage, alert preferences, and future alert tables. This phase turns those foundations into a secure Telegram entry point without coupling the first operator UI or alert-delivery worker to the transport layer.

This is the first of three remaining subprojects:

1. Telegram webhook, administrator commands, Mini App init-data authentication, and trusted sessions (this specification).
2. The operator WebApp and its authenticated monitoring/administration API.
3. Offline alert evaluation, durable outbox delivery, recovery messages, and alert controls.

The split is intentional. Telegram identity and session security can be verified independently before a browser UI consumes it, while alert scheduling and delivery remain independently testable background work.

## Chosen Approach and Alternatives

### Chosen: optional module in the existing central process

The existing `tg-monitor-server serve` process conditionally mounts Telegram routes when `TG_MONITOR_TELEGRAM_ENABLED=true`. Core-only operation remains the default and retains its current configuration and behavior. When enabled, the same process and SQLite store serve Agent ingestion, Telegram webhook requests, and session endpoints.

This keeps deployment at one central binary and one listener, avoids cross-process coordination, and reuses the existing WAL-backed store and graceful shutdown path.

### Rejected: separate Telegram sidecar

A sidecar would isolate failures, but adds a second systemd unit, listener, configuration surface, SQLite connection pool, log stream, and reverse-proxy route. The project is a small private installation; that operational cost is not justified.

### Rejected: validate Telegram init data on every API request

Stateless validation avoids server sessions but repeatedly exposes init data, makes age expiry disruptive during an open Mini App, and couples every future API to Telegram-specific parsing. A short-lived opaque server session creates a clean authorization boundary for the WebApp and later APIs.

## Configuration

Add focused runtime types rather than making core-only serving depend on all fields in the legacy full `Config`:

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
```

`LoadApplicationRuntimeFromEnv` always loads the existing server runtime config. Telegram is disabled by default. `TG_MONITOR_TELEGRAM_ENABLED` is parsed strictly: only `true` enables the module and only `false` disables it; any other non-empty value is rejected. When enabled, it requires and validates:

- `TG_MONITOR_PUBLIC_URL`: an origin URL with no credentials, query, fragment, or non-root path. HTTPS is required except for literal loopback IPs and `localhost` in deterministic local tests.
- `TG_MONITOR_BOT_TOKEN`: non-empty and never formatted into errors or logs.
- `TG_MONITOR_WEBHOOK_SECRET`: 1–256 characters from `A-Z`, `a-z`, `0-9`, `_`, and `-`, matching the Bot API contract.
- `TG_MONITOR_ADMIN_TELEGRAM_IDS`: unique positive comma-separated integers.
- `TG_MONITOR_SESSION_TTL`: default 12 hours, positive.
- `TG_MONITOR_INIT_DATA_MAX_AGE`: default 5 minutes, positive.
- `TG_MONITOR_TELEGRAM_HTTP_TIMEOUT`: default 10 seconds, positive.

The existing core-only environment remains valid without Telegram secrets. Partial Telegram configuration while the explicit enable flag is false is ignored rather than accidentally activating the module. The legacy full `LoadFromEnv` stays compatible until all later consumers migrate.

## Process and HTTP Composition

`serverapp` retains one `http.Server`, one SQLite store, checkpoint/retention workers, and one shutdown path. Its constructor builds the existing core handler, then—when Telegram is enabled—mounts more-specific handlers before the core fallback:

- `/telegram/webhook`
- `/api/v1/auth/telegram`
- `/api/v1/auth/session`
- `/api/v1/auth/logout`
- `/` as the existing central handler fallback

The composition boundary passes interfaces into Telegram packages; Telegram code does not import concrete SQLite types except in the top-level application wiring. Core-only tests remain unchanged, and Telegram-enabled composition has dedicated integration tests.

## Telegram Bot API Client

`internal/telegramapi` owns minimal Bot API types and an HTTP client. Production requests use `https://api.telegram.org/bot<TOKEN>/<method>` with TLS 1.2 or newer, a finite timeout, redirects disabled, bounded response reads, and JSON request/response bodies.

Supported methods are deliberately limited:

- `sendMessage`
- `setWebhook`
- `getWebhookInfo`
- `deleteWebhook`

The token necessarily appears in the Telegram request URL, so transport errors are converted into operation classes without returning `url.Error`, response bodies, request bodies, or the URL. Bot API descriptions are not copied into logs. Tests use an injected RoundTripper/base URL and canary secrets to prove that errors and logs stay clean.

`setWebhook` uses `${PublicURL}/telegram/webhook`, the configured `secret_token`, and `allowed_updates=["message"]`. Webhook mutation is explicit through CLI commands, never an automatic startup side effect.

## Webhook Authentication and Parsing

`POST /telegram/webhook` is the only accepted method. The handler:

1. Compares `X-Telegram-Bot-Api-Secret-Token` to the configured value in constant time. A mismatch receives a generic 404 response.
2. Limits the request body to 256 KiB.
3. Decodes the subset of Bot API update/message/user/chat fields used by the service. Unknown fields remain allowed for forward compatibility.
4. Requires a positive `update_id` and records it in SQLite before command side effects.
5. Returns 204 immediately for a duplicate update.
6. Processes only private-chat messages whose `from.id` is in the configured administrator set. Group/channel messages, missing users, and non-administrators are silently acknowledged with 204 and produce no Bot API call.

The service records an update before calling `sendMessage`, providing at-most-once command side effects under Telegram webhook retries. If recording fails, the handler returns 503 so Telegram may retry. If the outbound Bot API call fails after recording, the failure is logged only as a safe operation class and the webhook still returns 204; the administrator can repeat the command, while automatic retries cannot produce duplicate messages.

Malformed JSON returns 400, an oversized body returns 413, and unsupported methods return 405. Access logs never include request headers or bodies.

## Schema Migration and Cleanup

Schema version 2 adds:

```sql
CREATE TABLE telegram_updates (
    update_id INTEGER PRIMARY KEY CHECK(update_id > 0),
    received_at_ms INTEGER NOT NULL CHECK(received_at_ms > 0)
);
CREATE INDEX telegram_updates_received_idx ON telegram_updates(received_at_ms);
```

Migration execution becomes sequential: a new database applies version 1 then version 2, while an existing version-1 database applies only version 2. The repository exposes an atomic `RecordTelegramUpdate` operation that returns whether insertion occurred, plus deletion of old update IDs.

Expired sessions and update IDs older than seven days are deleted from the existing daily retention worker. Cleanup failure is logged safely and does not stop serving.

## Administrator Commands

`internal/telegrambot` parses the first whitespace-delimited token, accepts an optional `@botname` suffix, and supports only:

- `/start` and `/help`: concise command help and confirmation that this is a private administrator interface.
- `/status`: server state summary.
- `/alerts_on`: persist `alerts_enabled=true` for the administrator and confirm the future alert preference.
- `/alerts_off`: persist `alerts_enabled=false` and confirm.

Unknown slash commands receive the same help text. Non-command messages receive a short prompt to use `/help`.

`/status` reads ordered servers, ordered latest metrics, and the singleton offline threshold. It merges metrics by server ID so servers without a first report remain visible. State is:

- `online`: enabled and latest `received_at_ms` is newer than or equal to `now - offline_threshold`.
- `offline`: enabled with a missing/stale latest report.
- `disabled`: server metadata is disabled.

Each line contains state, server name, CPU percentage, memory percentage, and last-seen age when available. Output is plain text (no parse mode), so user-controlled names require no Markdown/HTML escaping. It is constructed line-by-line under 3,800 UTF-8 bytes, leaving room below Telegram's 4,096-character message limit; omitted servers are summarized in a final line.

## Mini App Init-Data Verification

`internal/telegramauth` validates the raw string from `Telegram.WebApp.initData` on the server. It never trusts `initDataUnsafe`.

The verifier:

1. Enforces a small raw length limit and strict query decoding.
2. Rejects duplicate fields and requires `hash`, `auth_date`, and `user` exactly once.
3. Builds the sorted newline-separated data-check string from received fields other than `hash`.
4. Derives the secret key with HMAC-SHA-256 using `WebAppData` as the key and the bot token as data, then calculates the data-check HMAC and compares decoded hash bytes in constant time.
5. Parses `auth_date`, rejects dates older than `InitDataMaxAge`, and rejects dates more than 30 seconds in the future.
6. Strictly decodes the user JSON, requires a positive user ID, and requires that ID in the administrator allowlist.

The HMAC path uses the bot token directly because this is the bot's own backend. The newer third-party Ed25519 validation path is unnecessary and would add public-key/environment selection without improving this trust boundary.

## Session Endpoints and Cookie

### `POST /api/v1/auth/telegram`

Requires `Content-Type: application/json`, same-origin `Origin` when the header is present, and a body no larger than 64 KiB:

```json
{"init_data":"<raw Telegram.WebApp.initData>"}
```

After validation, the handler generates 32 random bytes, encodes them with unpadded base64url, stores only the SHA-256 through the existing session repository, and returns 204 with a session cookie.

### Cookie contract

- Name: `__Host-tg_monitor_session` for HTTPS; `tg_monitor_session` for loopback HTTP tests.
- `HttpOnly=true`
- `Secure=true` for HTTPS
- `SameSite=Strict`
- `Path=/`
- no `Domain`
- expiry and `Max-Age` match the configured session TTL

The raw token is never returned in JSON, persisted, or logged.

### `GET /api/v1/auth/session`

Looks up the cookie with the current time. A valid session returns:

```json
{"telegram_user_id":123,"expires_at":1700000000000}
```

Missing, unknown, or expired sessions return the same 401 envelope and clear the cookie. This endpoint is the future WebApp bootstrap authorization check.

### `POST /api/v1/auth/logout`

Requires same-origin `Origin` when present, deletes the hash-only session when it exists, clears the cookie, and returns 204. Missing/expired sessions are treated idempotently as success.

No CORS headers are emitted. Future state-changing WebApp APIs will use the same cookie authentication and same-origin rule.

## CLI Contract

The existing executable gains:

```text
tg-monitor-server telegram set-webhook
tg-monitor-server telegram get-webhook
tg-monitor-server telegram delete-webhook
```

Commands load focused Telegram configuration, create the safe Bot API client, and print only non-secret structured results. Production `set-webhook` requires an HTTPS public URL before making a request. Loopback HTTP remains valid only for local `serve` and session smoke tests and is never registered with Telegram. Invalid/extra arguments fail before loading secrets or contacting Telegram.

`serve` loads `ApplicationRuntimeConfig`: core-only by default, Telegram-enabled only when explicitly requested. Existing server/metrics administration commands continue loading only `ServerRuntimeConfig` and therefore never require Telegram secrets.

## Error Handling and Logging

- Externally visible errors use stable status/code envelopes and never forward SQLite, JSON, crypto, Bot API, or network error strings.
- Logs contain request method/path/status/duration, authenticated server ID for Agent ingestion, Telegram operation class, update ID, and administrator ID only after successful trust checks.
- Bot token, webhook secret, init data, hashes, session token/cookie, Telegram message text, response body, and user JSON are forbidden in errors and logs.
- Telegram command send failures do not terminate the central server.
- A Telegram module construction/configuration failure prevents Telegram-enabled startup rather than silently falling back to core-only mode.

## Deployment

The central environment example adds a commented Telegram section with the enable flag and required values. The systemd unit remains the same non-root service and uses the same listener. Caddy already forwards the public origin to the central listener; documentation calls out the webhook and auth paths and requires HTTPS.

The runbook sequence is:

1. Create the Bot with BotFather and set the Mini App public URL for the later UI phase.
2. Generate a webhook secret using only the Bot API-allowed character set.
3. Add administrator IDs and Telegram variables to root-owned mode-0600 `server.env`.
4. Restart and verify readiness.
5. Run `telegram set-webhook`, then `telegram get-webhook`.
6. Send `/status` from an allowlisted private chat.
7. Exercise a deterministic init-data/session smoke command locally before attaching the future WebApp.

Rollback disables the Telegram flag, restarts the same service, and optionally calls `telegram delete-webhook`. Schema version 2 is additive and remains compatible with core-only service operation.

## Testing and Acceptance

Every behavior is implemented with strict red-green TDD. Coverage includes:

- focused config disabled/default/enabled/partial/secret-safe validation;
- migration from empty and version-1 databases, idempotence, dedupe concurrency, and cleanup;
- official init-data HMAC construction, reordered fields, Unicode user JSON, duplicate/missing fields, bad hash, malformed hex, stale/future dates, non-admins, and secret-safe errors;
- random session creation, hash-only persistence, exact cookie attributes, session bootstrap, expiry, logout idempotence, origin/content-type/body limits, and no raw value in logs;
- webhook method/header/body checks, unknown-field compatibility, duplicate updates, silent non-admin/group handling, and retry classification;
- `/status` online/offline/disabled/no-data calculations, ordering, percentages, length bounding, and plain-text handling of hostile server names;
- Bot API request shapes, TLS minimum, timeout, redirect refusal, bounded responses, webhook operations, and canary leak scans;
- core-only regression tests and Telegram-enabled application composition/shutdown;
- command invalid-argument behavior and build tests;
- deterministic process smoke using a fake local Bot API server, a real SQLite database, the real central process, signed synthetic init data, session cookie requests, duplicate webhook delivery, and SIGTERM;
- full `go test`, race, vet, CGO-free amd64/arm64 builds, systemd verification, and repository-wide secret scans.

Acceptance requires a clean worktree, all gates passing, safe smoke output with no secret material, and identical local/remote Git SHA after push.

## Explicitly Out of Scope

- WebApp HTML/CSS/JavaScript, charts, server administration screens, or static asset hosting.
- Web App keyboard/inline buttons; these arrive with the operator UI so no command links to a missing page.
- Offline alert state evaluation, outbox production, Telegram alert delivery, or recovery messages.
- Non-administrator/role-based access, group commands, long polling, arbitrary Bot API methods, file uploads, payments, or Telegram OAuth/OpenID login.
- External session stores, multi-central clustering, custom Telegram API endpoints in production, or automatic webhook registration at startup.

## Authoritative Telegram References

- Telegram Bot API webhook and `secret_token`: <https://core.telegram.org/bots/api#setwebhook>
- Telegram Mini App init-data validation: <https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app>
- Telegram Web App URL/private-chat constraints: <https://core.telegram.org/bots/api#webappinfo>
