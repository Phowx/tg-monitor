package sessionapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/storage/sqlite"
)

func sessionRequest(method, path, token, origin string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	if token != "" {
		request.AddCookie(&http.Cookie{Name: "__Host-tg_monitor_session", Value: token})
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	return request
}

func TestSessionBootstrapReturnsTrustedSession(t *testing.T) {
	harness := newLoginHarness("https://monitor.example.com")
	harness.repository.getSession = domain.Session{
		TelegramUserID: 101,
		CreatedAtMS:    harness.now.Add(-time.Hour).UnixMilli(),
		ExpiresAtMS:    harness.now.Add(time.Hour).UnixMilli(),
	}
	response := httptest.NewRecorder()
	harness.handler(t).ServeHTTP(response, sessionRequest(http.MethodGet, "/api/v1/auth/session", "opaque-session-token", ""))

	wantBody := `{"telegram_user_id":101,"expires_at":1700003600000}` + "\n"
	if response.Code != http.StatusOK || response.Body.String() != wantBody {
		t.Fatalf("response = %d %q, want 200 %q", response.Code, response.Body.String(), wantBody)
	}
	if harness.repository.getCalls != 1 || harness.repository.getToken != "opaque-session-token" || harness.repository.getNowMS != harness.now.UnixMilli() {
		t.Fatalf("GetSession call = calls %d token %q now %d", harness.repository.getCalls, harness.repository.getToken, harness.repository.getNowMS)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("CORS header = %q", got)
	}
}

func TestSessionBootstrapNormalizesMissingUnknownAndExpired(t *testing.T) {
	tests := []struct {
		name  string
		token string
		err   error
	}{
		{name: "missing cookie"},
		{name: "unknown token", token: "unknown", err: sqlite.ErrNotFound},
		{name: "expired token", token: "expired", err: sqlite.ErrSessionExpired},
	}
	wantBody := `{"error":{"code":"unauthorized","message":"authentication is required"}}` + "\n"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newLoginHarness("https://monitor.example.com")
			harness.repository.getErr = tt.err
			response := httptest.NewRecorder()
			harness.handler(t).ServeHTTP(response, sessionRequest(http.MethodGet, "/api/v1/auth/session", tt.token, ""))
			if response.Code != http.StatusUnauthorized || response.Body.String() != wantBody {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if tt.token == "" && harness.repository.getCalls != 0 {
				t.Fatalf("missing cookie GetSession calls = %d", harness.repository.getCalls)
			}
			assertClearedSessionCookie(t, response, true)
		})
	}
}

func TestLogoutDeletesSessionAndIsIdempotent(t *testing.T) {
	tests := []struct {
		name        string
		token       string
		deleteErr   error
		wantStatus  int
		wantDeletes int
	}{
		{name: "known", token: "known-token", wantStatus: 204, wantDeletes: 1},
		{name: "missing cookie", wantStatus: 204},
		{name: "already missing", token: "missing-token", deleteErr: sqlite.ErrNotFound, wantStatus: 204, wantDeletes: 1},
		{name: "expired", token: "expired-token", deleteErr: sqlite.ErrSessionExpired, wantStatus: 204, wantDeletes: 1},
		{name: "database failure", token: "private-cookie-canary", deleteErr: errors.New("database-private-canary"), wantStatus: 500, wantDeletes: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newLoginHarness("https://monitor.example.com")
			harness.repository.deleteErr = tt.deleteErr
			response := httptest.NewRecorder()
			harness.handler(t).ServeHTTP(response, sessionRequest(http.MethodPost, "/api/v1/auth/logout", tt.token, "https://monitor.example.com"))
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), tt.wantStatus)
			}
			if harness.repository.deleteCalls != tt.wantDeletes || harness.repository.deleteToken != tt.token && tt.wantDeletes > 0 {
				t.Fatalf("DeleteSession call = calls %d token %q", harness.repository.deleteCalls, harness.repository.deleteToken)
			}
			assertClearedSessionCookie(t, response, true)
			for _, canary := range []string{"private-cookie-canary", "database-private-canary"} {
				if strings.Contains(response.Body.String(), canary) || strings.Contains(harness.logs.String(), canary) {
					t.Fatalf("response/log leaked %q", canary)
				}
			}
		})
	}
}

func TestLogoutRejectsMethodAndForeignOriginBeforeDeletion(t *testing.T) {
	tests := []struct {
		method string
		origin string
		status int
	}{
		{method: http.MethodGet, origin: "https://monitor.example.com", status: 405},
		{method: http.MethodPost, origin: "https://evil.example.com", status: 403},
		{method: http.MethodPost, origin: "null", status: 403},
	}
	for _, tt := range tests {
		harness := newLoginHarness("https://monitor.example.com")
		response := httptest.NewRecorder()
		harness.handler(t).ServeHTTP(response, sessionRequest(tt.method, "/api/v1/auth/logout", "token", tt.origin))
		if response.Code != tt.status || harness.repository.deleteCalls != 0 {
			t.Fatalf("response/delete = %d/%d, want %d/0", response.Code, harness.repository.deleteCalls, tt.status)
		}
	}
}

func TestNoCORSAndUnknownRoutesStayClosed(t *testing.T) {
	harness := newLoginHarness("https://monitor.example.com")
	paths := []string{"/api/v1/auth/session", "/api/v1/auth/logout", "/not-found"}
	for _, path := range paths {
		response := httptest.NewRecorder()
		harness.handler(t).ServeHTTP(response, sessionRequest(http.MethodGet, path, "", ""))
		if response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("%s emitted CORS header", path)
		}
		if path == "/not-found" && response.Code != http.StatusNotFound {
			t.Fatalf("unknown route status = %d, want 404", response.Code)
		}
	}
}

func assertClearedSessionCookie(t *testing.T, response *httptest.ResponseRecorder, secure bool) {
	t.Helper()
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("clearing cookies = %#v, want one", cookies)
	}
	cookie := cookies[0]
	wantName := "tg_monitor_session"
	wantSameSite := http.SameSiteStrictMode
	wantPartitioned := false
	if secure {
		wantName = "__Host-tg_monitor_session"
		wantSameSite = http.SameSiteNoneMode
		wantPartitioned = true
	}
	if cookie.Name != wantName || cookie.Value != "" || cookie.MaxAge != -1 || cookie.Path != "/" || !cookie.HttpOnly || cookie.Secure != secure || cookie.SameSite != wantSameSite || cookie.Partitioned != wantPartitioned || cookie.Domain != "" {
		t.Fatalf("cleared cookie = %#v", cookie)
	}
	if !cookie.Expires.Equal(time.Unix(1, 0).UTC()) {
		t.Fatalf("cleared cookie Expires = %v", cookie.Expires)
	}
}
