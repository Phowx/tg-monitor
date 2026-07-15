package adminapi

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestHandlerNormalizesUnauthorizedSessionsAndClearsCookie(t *testing.T) {
	tests := []struct {
		name          string
		token         string
		sessionErr    error
		wantRepoCalls int
	}{
		{name: "missing cookie", wantRepoCalls: 0},
		{name: "unknown token", token: testSessionToken, sessionErr: domain.ErrNotFound, wantRepoCalls: 1},
		{name: "expired token", token: testSessionToken, sessionErr: domain.ErrSessionExpired, wantRepoCalls: 1},
	}

	var wantBody string
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{sessionErr: test.sessionErr}
			response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodGet, "/api/v1/admin/overview", test.token, "", "", "")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%q", response.Code, response.Body.String())
			}
			if wantBody == "" {
				wantBody = response.Body.String()
			} else if response.Body.String() != wantBody {
				t.Fatalf("body = %q, want normalized %q", response.Body.String(), wantBody)
			}
			if repository.getSessionCalls != test.wantRepoCalls {
				t.Fatalf("GetSession calls = %d, want %d", repository.getSessionCalls, test.wantRepoCalls)
			}
			cookies := response.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("clear cookies = %#v", cookies)
			}
			cookie := cookies[0]
			if cookie.Name != "__Host-tg_monitor_session" || cookie.Value != "" || cookie.MaxAge != -1 || !cookie.HttpOnly || !cookie.Secure || !cookie.Partitioned || cookie.SameSite != http.SameSiteNoneMode || cookie.Domain != "" || cookie.Path != "/" {
				t.Fatalf("clear cookie = %#v", cookie)
			}
			assertAdminResponsePolicy(t, response)
		})
	}
	if wantBody != `{"error":{"code":"unauthorized","message":"authentication is required"}}`+"\n" {
		t.Fatalf("unauthorized body = %q", wantBody)
	}
}

func TestHandlerAuthenticatesBeforeRoutingAndLogsOnlySafeFields(t *testing.T) {
	var logs bytes.Buffer
	repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
	response := serveAdmin(
		mustAdminHandler(t, repository, &logs),
		http.MethodGet,
		"/api/v1/admin/not-found?query=QUERY-CANARY",
		testSessionToken,
		"",
		"",
		"",
	)
	if response.Code != http.StatusNotFound || response.Body.String() != `{"error":{"code":"not_found","message":"resource was not found"}}`+"\n" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if repository.getSessionCalls != 1 || repository.getSessionToken != testSessionToken || repository.getSessionNowMS != testNowMS {
		t.Fatalf("GetSession = calls %d token %q now %d", repository.getSessionCalls, repository.getSessionToken, repository.getSessionNowMS)
	}
	encodedLogs := logs.String()
	for _, required := range []string{`"method":"GET"`, `"route":"not_found"`, `"status":404`, `"admin_id":42`} {
		if !strings.Contains(encodedLogs, required) {
			t.Fatalf("logs %q missing %q", encodedLogs, required)
		}
	}
	for _, forbidden := range []string{"QUERY-CANARY", testSessionToken, "/api/v1/admin/not-found?"} {
		if strings.Contains(encodedLogs, forbidden) {
			t.Fatalf("logs %q contain %q", encodedLogs, forbidden)
		}
	}
	assertAdminResponsePolicy(t, response)
}

func TestHandlerMapsUnexpectedSessionFailureWithoutLeak(t *testing.T) {
	var logs bytes.Buffer
	repository := &repositoryStub{sessionErr: errors.New("REPOSITORY-DETAIL-CANARY")}
	response := serveAdmin(mustAdminHandler(t, repository, &logs), http.MethodGet, "/api/v1/admin/overview", testSessionToken, "", "", "")
	if response.Code != http.StatusInternalServerError || response.Body.String() != `{"error":{"code":"internal_error","message":"internal server error"}}`+"\n" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "REPOSITORY-DETAIL-CANARY") || strings.Contains(logs.String(), "REPOSITORY-DETAIL-CANARY") || strings.Contains(logs.String(), testSessionToken) {
		t.Fatalf("failure leaked detail: body=%q logs=%q", response.Body.String(), logs.String())
	}
	assertAdminResponsePolicy(t, response)
}

func TestNewHandlerRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		config     Config
		repository Repository
	}{
		{name: "unsafe public URL", config: Config{PublicURL: "http://monitor.example.com"}, repository: &repositoryStub{}},
		{name: "missing repository", config: Config{PublicURL: testAdminOrigin}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if handler, err := NewHandler(test.config, Dependencies{Repository: test.repository}); err == nil || handler != nil {
				t.Fatalf("NewHandler() = %#v, %v; want nil, error", handler, err)
			}
		})
	}
}

func assertAdminResponsePolicy(t *testing.T, response interface {
	Header() http.Header
}) {
	t.Helper()
	header := response.Header()
	if header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", header.Get("Cache-Control"))
	}
	if header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unexpected CORS header = %q", header.Get("Access-Control-Allow-Origin"))
	}
}
