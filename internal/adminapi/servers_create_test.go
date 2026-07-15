package adminapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestCreateServerPersistsOnlyHashAndReturnsOneTimeToken(t *testing.T) {
	created := domain.Server{ID: 9, Name: "node", Group: "prod", SortOrder: 7, Enabled: true}
	repository := &repositoryStub{
		session:       domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000},
		createdResult: created,
	}
	response := serveAdmin(
		mustAdminHandlerWithTaskRandom(t, repository, bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)), nil),
		http.MethodPost,
		"/api/v1/admin/servers",
		testSessionToken,
		testAdminOrigin,
		"application/json; charset=utf-8",
		`{"name":" node ","group":" prod ","sort_order":7,"enabled":true}`,
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%q", response.Code, response.Body.String())
	}
	wantToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	if repository.createdServer.Name != "node" || repository.createdServer.Group != "prod" || repository.createdServer.SortOrder != 7 || !repository.createdServer.Enabled {
		t.Fatalf("CreateServer input = %#v", repository.createdServer)
	}
	if !bytes.Equal(repository.createdServer.TokenSHA256, auth.HashToken(wantToken)) {
		t.Fatalf("persisted token hash = %x", repository.createdServer.TokenSHA256)
	}
	var got struct {
		Server     serverMetadata `json:"server"`
		AgentToken string         `json:"agent_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if got.Server.ID != 9 || got.Server.Name != "node" || got.AgentToken != wantToken {
		t.Fatalf("create response = %#v", got)
	}
	if strings.Contains(response.Body.String(), base64.RawURLEncoding.EncodeToString(repository.createdServer.TokenSHA256)) || strings.Contains(response.Body.String(), "token_sha256") {
		t.Fatalf("response leaked token hash: %s", response.Body.String())
	}
	assertAdminResponsePolicy(t, response)
}

func TestCreateServerRejectsOriginJSONAndValidationBeforePersistence(t *testing.T) {
	tests := []struct {
		name        string
		origin      string
		contentType string
		body        string
		wantStatus  int
		wantCode    string
	}{
		{name: "missing origin", contentType: "application/json", body: `{"name":"node","group":"","sort_order":0,"enabled":true}`, wantStatus: 403, wantCode: "forbidden_origin"},
		{name: "wrong origin", origin: "https://evil.example.com", contentType: "application/json", body: `{"name":"node","group":"","sort_order":0,"enabled":true}`, wantStatus: 403, wantCode: "forbidden_origin"},
		{name: "missing content type", origin: testAdminOrigin, body: `{}`, wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "wrong content type", origin: testAdminOrigin, contentType: "text/plain", body: `{}`, wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "malformed JSON", origin: testAdminOrigin, contentType: "application/json", body: `{"name":`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "unknown field", origin: testAdminOrigin, contentType: "application/json", body: `{"name":"node","group":"","sort_order":0,"enabled":true,"token":"secret"}`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "trailing JSON", origin: testAdminOrigin, contentType: "application/json", body: `{"name":"node","group":"","sort_order":0,"enabled":true} {}`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "oversized", origin: testAdminOrigin, contentType: "application/json", body: `{"name":"` + strings.Repeat("x", (64<<10)+1) + `","group":"","sort_order":0,"enabled":true}`, wantStatus: 413, wantCode: "request_too_large"},
		{name: "empty name", origin: testAdminOrigin, contentType: "application/json", body: `{"name":" ","group":"","sort_order":0,"enabled":true}`, wantStatus: 422, wantCode: "validation_failed"},
		{name: "missing sort order", origin: testAdminOrigin, contentType: "application/json", body: `{"name":"node","group":"","enabled":true}`, wantStatus: 422, wantCode: "validation_failed"},
		{name: "missing enabled", origin: testAdminOrigin, contentType: "application/json", body: `{"name":"node","group":"","sort_order":0}`, wantStatus: 422, wantCode: "validation_failed"},
		{name: "long name", origin: testAdminOrigin, contentType: "application/json", body: `{"name":"` + strings.Repeat("n", 121) + `","group":"","sort_order":0,"enabled":true}`, wantStatus: 422, wantCode: "validation_failed"},
		{name: "long group", origin: testAdminOrigin, contentType: "application/json", body: `{"name":"node","group":"` + strings.Repeat("g", 121) + `","sort_order":0,"enabled":true}`, wantStatus: 422, wantCode: "validation_failed"},
		{name: "sort overflow", origin: testAdminOrigin, contentType: "application/json", body: `{"name":"node","group":"","sort_order":2147483648,"enabled":true}`, wantStatus: 422, wantCode: "validation_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
			response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodPost, "/api/v1/admin/servers", testSessionToken, test.origin, test.contentType, test.body)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", response.Code, test.wantStatus, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("body = %q, want code %q", response.Body.String(), test.wantCode)
			}
			if repository.createdServer.Name != "" {
				t.Fatalf("invalid request reached CreateServer: %#v", repository.createdServer)
			}
		})
	}
}

func TestCreateServerRandomAndPersistenceFailuresDoNotExposeToken(t *testing.T) {
	wantToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	tests := []struct {
		name       string
		random     io.Reader
		repository *repositoryStub
	}{
		{
			name:       "random",
			random:     strings.NewReader("short"),
			repository: &repositoryStub{},
		},
		{
			name:   "persistence",
			random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)),
			repository: &repositoryStub{
				createServerErr: errors.New("PERSISTENCE-CANARY"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			test.repository.session = domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}
			response := serveAdmin(
				mustAdminHandlerWithTaskRandom(t, test.repository, test.random, &logs),
				http.MethodPost,
				"/api/v1/admin/servers",
				testSessionToken,
				testAdminOrigin,
				"application/json",
				`{"name":"node","group":"","sort_order":0,"enabled":true}`,
			)
			if response.Code != http.StatusInternalServerError || response.Body.String() != `{"error":{"code":"internal_error","message":"internal server error"}}`+"\n" {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			for _, output := range []string{response.Body.String(), logs.String()} {
				for _, forbidden := range []string{wantToken, "PERSISTENCE-CANARY"} {
					if strings.Contains(output, forbidden) {
						t.Fatalf("output %q leaked %q", output, forbidden)
					}
				}
			}
		})
	}
}

func TestCreateServerRejectsUnsupportedMethod(t *testing.T) {
	repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
	response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodGet, "/api/v1/admin/servers", testSessionToken, "", "", "")
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("response = %d Allow=%q body=%q", response.Code, response.Header().Get("Allow"), response.Body.String())
	}
}

func mustAdminHandlerWithTaskRandom(t *testing.T, repository Repository, random io.Reader, loggerOutput io.Writer) http.Handler {
	t.Helper()
	if loggerOutput == nil {
		loggerOutput = io.Discard
	}
	handler, err := NewHandler(Config{PublicURL: testAdminOrigin}, Dependencies{
		Repository: repository,
		Random:     random,
		Now:        func() time.Time { return time.UnixMilli(testNowMS) },
		Logger: slog.New(slog.NewJSONHandler(loggerOutput, &slog.HandlerOptions{
			ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
				if attribute.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return attribute
			},
		})),
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}
