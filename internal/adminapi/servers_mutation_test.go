package adminapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestUpdateServerPreservesCredentialsAndStorageMetadata(t *testing.T) {
	tokenHash := auth.HashToken("existing-token")
	repository := &repositoryStub{
		session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000},
		server: domain.Server{
			ID: 7, Name: "old", Group: "old-group", SortOrder: 1, Enabled: false,
			TokenSHA256: tokenHash, CreatedAtMS: 100, UpdatedAtMS: 200,
		},
	}
	response := serveAdmin(
		mustAdminHandler(t, repository, nil),
		http.MethodPut,
		"/api/v1/admin/servers/7",
		testSessionToken,
		testAdminOrigin,
		"application/json",
		`{"name":" new ","group":" prod ","sort_order":9,"enabled":true}`,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", response.Code, response.Body.String())
	}
	updated := repository.updatedServer
	if repository.getServerID != 7 || updated.ID != 7 || updated.Name != "new" || updated.Group != "prod" || updated.SortOrder != 9 || !updated.Enabled {
		t.Fatalf("updated server = %#v", updated)
	}
	if !bytes.Equal(updated.TokenSHA256, tokenHash) || updated.CreatedAtMS != 100 || updated.UpdatedAtMS != 200 {
		t.Fatalf("update did not preserve storage metadata: %#v", updated)
	}
	var got struct {
		Server serverMetadata `json:"server"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.Server.ID != 7 || got.Server.Name != "new" {
		t.Fatalf("response = %#v, %v", got, err)
	}
}

func TestUpdateServerValidatesAndMapsRepositoryErrors(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		origin     string
		body       string
		serverErr  error
		updateErr  error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid ID", target: "/api/v1/admin/servers/0", origin: testAdminOrigin, body: validServerUpdateBody(), wantStatus: 400, wantCode: "invalid_request"},
		{name: "missing origin", target: "/api/v1/admin/servers/1", body: validServerUpdateBody(), wantStatus: 403, wantCode: "forbidden_origin"},
		{name: "invalid metadata", target: "/api/v1/admin/servers/1", origin: testAdminOrigin, body: `{"name":"","group":"","sort_order":0,"enabled":true}`, wantStatus: 422, wantCode: "validation_failed"},
		{name: "unknown field", target: "/api/v1/admin/servers/1", origin: testAdminOrigin, body: `{"name":"node","group":"","sort_order":0,"enabled":true,"token":"x"}`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "unknown server on get", target: "/api/v1/admin/servers/1", origin: testAdminOrigin, body: validServerUpdateBody(), serverErr: domain.ErrNotFound, wantStatus: 404, wantCode: "not_found"},
		{name: "get failure", target: "/api/v1/admin/servers/1", origin: testAdminOrigin, body: validServerUpdateBody(), serverErr: errors.New("GET-CANARY"), wantStatus: 500, wantCode: "internal_error"},
		{name: "unknown server on update", target: "/api/v1/admin/servers/1", origin: testAdminOrigin, body: validServerUpdateBody(), updateErr: domain.ErrNotFound, wantStatus: 404, wantCode: "not_found"},
		{name: "update failure", target: "/api/v1/admin/servers/1", origin: testAdminOrigin, body: validServerUpdateBody(), updateErr: errors.New("UPDATE-CANARY"), wantStatus: 500, wantCode: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{
				session:         domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000},
				server:          domain.Server{ID: 1, Name: "old", TokenSHA256: auth.HashToken("existing")},
				serverErr:       test.serverErr,
				updateServerErr: test.updateErr,
			}
			response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodPut, test.target, testSessionToken, test.origin, "application/json", test.body)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "CANARY") {
				t.Fatalf("response leaked repository detail: %s", response.Body.String())
			}
		})
	}
}

func TestRotateServerTokenPersistsOnlyHashAndReturnsTokenOnce(t *testing.T) {
	repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
	response := serveAdmin(
		mustAdminHandlerWithTaskRandom(t, repository, bytes.NewReader(bytes.Repeat([]byte{0x24}, 32)), nil),
		http.MethodPost,
		"/api/v1/admin/servers/7/rotate-token",
		testSessionToken,
		testAdminOrigin,
		"application/json",
		`{}`,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", response.Code, response.Body.String())
	}
	wantToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, 32))
	if repository.rotatedServerID != 7 || !bytes.Equal(repository.rotatedTokenHash, auth.HashToken(wantToken)) {
		t.Fatalf("rotation persistence = id %d hash %x", repository.rotatedServerID, repository.rotatedTokenHash)
	}
	var got struct {
		ServerID   int64  `json:"server_id"`
		AgentToken string `json:"agent_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.ServerID != 7 || got.AgentToken != wantToken {
		t.Fatalf("rotation response = %#v, %v", got, err)
	}
}

func TestRotateServerTokenRejectsInvalidRequestsAndNeverLeaksToken(t *testing.T) {
	wantToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	tests := []struct {
		name       string
		target     string
		origin     string
		body       string
		random     io.Reader
		rotateErr  error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid ID", target: "/api/v1/admin/servers/0/rotate-token", origin: testAdminOrigin, body: `{}`, random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)), wantStatus: 400, wantCode: "invalid_request"},
		{name: "missing origin", target: "/api/v1/admin/servers/1/rotate-token", body: `{}`, random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)), wantStatus: 403, wantCode: "forbidden_origin"},
		{name: "empty body", target: "/api/v1/admin/servers/1/rotate-token", origin: testAdminOrigin, body: ``, random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)), wantStatus: 400, wantCode: "invalid_json"},
		{name: "unknown field", target: "/api/v1/admin/servers/1/rotate-token", origin: testAdminOrigin, body: `{"token":"x"}`, random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)), wantStatus: 400, wantCode: "invalid_json"},
		{name: "random failure", target: "/api/v1/admin/servers/1/rotate-token", origin: testAdminOrigin, body: `{}`, random: strings.NewReader("short"), wantStatus: 500, wantCode: "internal_error"},
		{name: "not found", target: "/api/v1/admin/servers/1/rotate-token", origin: testAdminOrigin, body: `{}`, random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)), rotateErr: domain.ErrNotFound, wantStatus: 404, wantCode: "not_found"},
		{name: "storage failure", target: "/api/v1/admin/servers/1/rotate-token", origin: testAdminOrigin, body: `{}`, random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)), rotateErr: errors.New("ROTATE-CANARY"), wantStatus: 500, wantCode: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}, rotateTokenErr: test.rotateErr}
			response := serveAdmin(mustAdminHandlerWithTaskRandom(t, repository, test.random, nil), http.MethodPost, test.target, testSessionToken, test.origin, "application/json", test.body)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			for _, forbidden := range []string{wantToken, "ROTATE-CANARY"} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
				}
			}
		})
	}
}

func TestDeleteServerReturnsNoContentAndMapsErrors(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		origin     string
		deleteErr  error
		wantStatus int
		wantCode   string
	}{
		{name: "success", target: "/api/v1/admin/servers/7", origin: testAdminOrigin, wantStatus: 204},
		{name: "invalid ID", target: "/api/v1/admin/servers/0", origin: testAdminOrigin, wantStatus: 400, wantCode: "invalid_request"},
		{name: "missing origin", target: "/api/v1/admin/servers/1", wantStatus: 403, wantCode: "forbidden_origin"},
		{name: "not found", target: "/api/v1/admin/servers/1", origin: testAdminOrigin, deleteErr: domain.ErrNotFound, wantStatus: 404, wantCode: "not_found"},
		{name: "failure", target: "/api/v1/admin/servers/1", origin: testAdminOrigin, deleteErr: errors.New("DELETE-CANARY"), wantStatus: 500, wantCode: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}, deleteServerErr: test.deleteErr}
			response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodDelete, test.target, testSessionToken, test.origin, "", "")
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantCode == "" {
				if response.Body.Len() != 0 || repository.deletedServerID != 7 {
					t.Fatalf("success response=%q deleted=%d", response.Body.String(), repository.deletedServerID)
				}
			} else if !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) || strings.Contains(response.Body.String(), "CANARY") {
				t.Fatalf("error response = %q", response.Body.String())
			}
		})
	}
}

func TestServerResourceRejectsUnsupportedMethod(t *testing.T) {
	repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
	response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodGet, "/api/v1/admin/servers/1", testSessionToken, "", "", "")
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "PUT, DELETE" {
		t.Fatalf("response = %d Allow=%q body=%q", response.Code, response.Header().Get("Allow"), response.Body.String())
	}
}

func validServerUpdateBody() string {
	return `{"name":"node","group":"prod","sort_order":1,"enabled":true}`
}
