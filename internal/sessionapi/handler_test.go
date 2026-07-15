package sessionapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/telegramauth"
)

type sessionRepositoryStub struct {
	createdToken   string
	createdSession domain.Session
	createCalls    int
	createErr      error
	getToken       string
	getNowMS       int64
	getCalls       int
	getSession     domain.Session
	getErr         error
	deleteToken    string
	deleteCalls    int
	deleteErr      error
}

func (repository *sessionRepositoryStub) CreateSession(_ context.Context, token string, session domain.Session) error {
	repository.createCalls++
	repository.createdToken = token
	repository.createdSession = session
	return repository.createErr
}

func (repository *sessionRepositoryStub) GetSession(_ context.Context, token string, nowMS int64) (domain.Session, error) {
	repository.getCalls++
	repository.getToken = token
	repository.getNowMS = nowMS
	return repository.getSession, repository.getErr
}

func (repository *sessionRepositoryStub) DeleteSession(_ context.Context, token string) error {
	repository.deleteCalls++
	repository.deleteToken = token
	return repository.deleteErr
}

type verifierRecorder struct {
	raw      string
	token    string
	now      time.Time
	maxAge   time.Duration
	adminIDs []int64
	user     telegramauth.User
	err      error
	calls    int
}

func (verifier *verifierRecorder) verify(raw, token string, now time.Time, maxAge time.Duration, adminIDs []int64) (telegramauth.User, error) {
	verifier.calls++
	verifier.raw = raw
	verifier.token = token
	verifier.now = now
	verifier.maxAge = maxAge
	verifier.adminIDs = append([]int64(nil), adminIDs...)
	return verifier.user, verifier.err
}

type loginHarness struct {
	repository *sessionRepositoryStub
	verifier   *verifierRecorder
	logs       bytes.Buffer
	now        time.Time
	config     Config
	random     io.Reader
}

func newLoginHarness(publicURL string) *loginHarness {
	return &loginHarness{
		repository: &sessionRepositoryStub{},
		verifier:   &verifierRecorder{user: telegramauth.User{ID: 101}},
		now:        time.UnixMilli(1_700_000_000_000).UTC(),
		config: Config{
			PublicURL:        publicURL,
			BotToken:         "123456:canary-session-bot-token",
			AdminTelegramIDs: []int64{101, 202},
			SessionTTL:       2 * time.Hour,
			InitDataMaxAge:   5 * time.Minute,
		},
		random: bytes.NewReader(bytes.Repeat([]byte{0x2a}, 32)),
	}
}

func (harness *loginHarness) handler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := NewHandler(harness.config, Dependencies{
		Repository: harness.repository,
		Random:     harness.random,
		Now:        func() time.Time { return harness.now },
		Verify:     harness.verifier.verify,
		Logger:     slog.New(slog.NewJSONHandler(&harness.logs, nil)),
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}

func performTelegramLogin(handler http.Handler, origin, contentType, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/telegram", strings.NewReader(body))
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestTelegramLoginCreatesHashOnlyRepositorySessionAndHTTPSCookie(t *testing.T) {
	harness := newLoginHarness("https://monitor.example.com")
	response := performTelegramLogin(
		harness.handler(t),
		"https://monitor.example.com",
		"application/json; charset=utf-8",
		`{"init_data":"signed-canary-init-data"}`,
	)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("response = %d %q, want 204 empty", response.Code, response.Body.String())
	}

	wantToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x2a}, 32))
	wantSession := domain.Session{
		TelegramUserID: 101,
		CreatedAtMS:    harness.now.UnixMilli(),
		ExpiresAtMS:    harness.now.Add(2 * time.Hour).UnixMilli(),
	}
	if harness.repository.createCalls != 1 || harness.repository.createdToken != wantToken || harness.repository.createdSession != wantSession {
		t.Fatalf("repository create = calls %d token %q session %#v", harness.repository.createCalls, harness.repository.createdToken, harness.repository.createdSession)
	}
	if harness.verifier.raw != "signed-canary-init-data" || harness.verifier.token != harness.config.BotToken || !harness.verifier.now.Equal(harness.now) || harness.verifier.maxAge != harness.config.InitDataMaxAge || !reflect.DeepEqual(harness.verifier.adminIDs, harness.config.AdminTelegramIDs) {
		t.Fatalf("verifier call = %#v", harness.verifier)
	}

	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v, want one", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != "__Host-tg_monitor_session" || cookie.Value != wantToken {
		t.Fatalf("cookie identity = %#v", cookie)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("cookie flags = %#v", cookie)
	}
	if cookie.MaxAge != 7_200 || !cookie.Expires.Equal(harness.now.Add(2*time.Hour)) {
		t.Fatalf("cookie expiry = MaxAge %d Expires %v", cookie.MaxAge, cookie.Expires)
	}
	for _, canary := range []string{wantToken, "signed-canary-init-data", harness.config.BotToken, `{"id":101}`} {
		if strings.Contains(harness.logs.String(), canary) {
			t.Fatalf("logs leaked %q: %s", canary, harness.logs.String())
		}
	}
}

func TestSessionCookieUsesLoopbackHTTPTestContract(t *testing.T) {
	for _, publicURL := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		t.Run(publicURL, func(t *testing.T) {
			harness := newLoginHarness(publicURL)
			response := performTelegramLogin(harness.handler(t), "", "application/json", `{"init_data":"signed"}`)
			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
			cookie := response.Result().Cookies()[0]
			if cookie.Name != "tg_monitor_session" || cookie.Secure {
				t.Fatalf("loopback cookie = %#v", cookie)
			}
		})
	}
}

func TestTelegramLoginRejectsInvalidRequestsWithStableErrors(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		origin      string
		contentType string
		body        string
		verifierErr error
		random      io.Reader
		repository  error
		wantStatus  int
		wantCode    string
	}{
		{name: "method", method: http.MethodGet, contentType: "application/json", body: `{}`, wantStatus: 405, wantCode: "method_not_allowed"},
		{name: "missing content type", method: http.MethodPost, body: `{}`, wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "wrong content type", method: http.MethodPost, contentType: "text/plain", body: `{}`, wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "malformed content type", method: http.MethodPost, contentType: "application/json; charset", body: `{}`, wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "wrong origin", method: http.MethodPost, origin: "https://evil.example.com", contentType: "application/json", body: `{}`, wantStatus: 403, wantCode: "forbidden_origin"},
		{name: "opaque origin", method: http.MethodPost, origin: "null", contentType: "application/json", body: `{}`, wantStatus: 403, wantCode: "forbidden_origin"},
		{name: "malformed JSON", method: http.MethodPost, contentType: "application/json", body: `{`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "unknown field", method: http.MethodPost, contentType: "application/json", body: `{"init_data":"signed","extra":true}`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "trailing JSON", method: http.MethodPost, contentType: "application/json", body: `{"init_data":"signed"}{}`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "oversized", method: http.MethodPost, contentType: "application/json", body: `{"init_data":"` + strings.Repeat("x", (64<<10)+1) + `"}`, wantStatus: 413, wantCode: "request_too_large"},
		{name: "invalid init data", method: http.MethodPost, contentType: "application/json", body: `{"init_data":"signed"}`, verifierErr: telegramauth.ErrInvalidInitData, wantStatus: 401, wantCode: "unauthorized"},
		{name: "short random", method: http.MethodPost, contentType: "application/json", body: `{"init_data":"signed"}`, random: bytes.NewReader([]byte{1}), wantStatus: 500, wantCode: "internal_error"},
		{name: "repository", method: http.MethodPost, contentType: "application/json", body: `{"init_data":"signed"}`, repository: errors.New("database-private-canary"), wantStatus: 500, wantCode: "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newLoginHarness("https://monitor.example.com")
			if tt.verifierErr != nil {
				harness.verifier.err = tt.verifierErr
			}
			if tt.random != nil {
				harness.random = tt.random
			}
			harness.repository.createErr = tt.repository
			request := httptest.NewRequest(tt.method, "/api/v1/auth/telegram", strings.NewReader(tt.body))
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}
			if tt.contentType != "" {
				request.Header.Set("Content-Type", tt.contentType)
			}
			response := httptest.NewRecorder()
			harness.handler(t).ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), tt.wantStatus)
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != tt.wantCode {
				t.Fatalf("error envelope = %#v decode=%v body=%q", envelope, err, response.Body.String())
			}
			if len(response.Result().Cookies()) != 0 {
				t.Fatalf("failure set cookies: %#v", response.Result().Cookies())
			}
			for _, canary := range []string{"database-private-canary", "signed", harness.config.BotToken} {
				if strings.Contains(response.Body.String(), canary) || strings.Contains(harness.logs.String(), canary) {
					t.Fatalf("response/log leaked %q", canary)
				}
			}
		})
	}
}
