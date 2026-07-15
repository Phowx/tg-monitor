package adminapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestHistoryProvesServerAndReturnsOrderedExclusiveRange(t *testing.T) {
	fromMS := testNowMS - 60*60*1_000
	toMS := testNowMS
	repository := &repositoryStub{
		session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000},
		server:  domain.Server{ID: 7, Name: "node", Group: "prod", SortOrder: 3, Enabled: true, TokenSHA256: []byte("must-not-leak")},
		samples: []domain.MinuteSample{
			{ServerID: 7, BucketMS: fromMS, CPUPct: 10},
			{ServerID: 7, BucketMS: fromMS + 60_000, CPUPct: 20},
		},
	}
	target := "/api/v1/admin/servers/7/history?from_ms=" + strconv.FormatInt(fromMS, 10) + "&to_ms=" + strconv.FormatInt(toMS, 10)
	response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodGet, target, testSessionToken, "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", response.Code, response.Body.String())
	}
	var got struct {
		Server  serverMetadata        `json:"server"`
		FromMS  int64                 `json:"from_ms"`
		ToMS    int64                 `json:"to_ms"`
		Samples []domain.MinuteSample `json:"samples"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if got.Server.ID != 7 || got.FromMS != fromMS || got.ToMS != toMS || len(got.Samples) != 2 || got.Samples[0].BucketMS != fromMS || got.Samples[1].BucketMS != fromMS+60_000 {
		t.Fatalf("history = %#v", got)
	}
	if repository.getServerID != 7 || repository.historyServerID != 7 || repository.historyFromMS != fromMS || repository.historyToMS != toMS {
		t.Fatalf("repository calls = server %d history (%d, %d, %d)", repository.getServerID, repository.historyServerID, repository.historyFromMS, repository.historyToMS)
	}
	if strings.Contains(response.Body.String(), "must-not-leak") || strings.Contains(response.Body.String(), "token_sha256") {
		t.Fatalf("history leaked server token: %s", response.Body.String())
	}
	assertAdminResponsePolicy(t, response)
}

func TestHistoryValidatesIDQueryAndSevenDayBoundBeforeRepository(t *testing.T) {
	sevenDaysMS := int64(7 * 24 * 60 * 60 * 1_000)
	tests := []struct {
		name   string
		target string
	}{
		{name: "zero ID", target: "/api/v1/admin/servers/0/history?from_ms=1&to_ms=2"},
		{name: "negative ID", target: "/api/v1/admin/servers/-1/history?from_ms=1&to_ms=2"},
		{name: "non numeric ID", target: "/api/v1/admin/servers/node/history?from_ms=1&to_ms=2"},
		{name: "missing from", target: "/api/v1/admin/servers/1/history?to_ms=2"},
		{name: "missing to", target: "/api/v1/admin/servers/1/history?from_ms=1"},
		{name: "negative from", target: "/api/v1/admin/servers/1/history?from_ms=-1&to_ms=2"},
		{name: "equal range", target: "/api/v1/admin/servers/1/history?from_ms=2&to_ms=2"},
		{name: "reverse range", target: "/api/v1/admin/servers/1/history?from_ms=3&to_ms=2"},
		{name: "over seven days", target: "/api/v1/admin/servers/1/history?from_ms=1&to_ms=" + strconv.FormatInt(1+sevenDaysMS+1, 10)},
		{name: "duplicate from", target: "/api/v1/admin/servers/1/history?from_ms=1&from_ms=2&to_ms=3"},
		{name: "unknown query", target: "/api/v1/admin/servers/1/history?from_ms=1&to_ms=2&extra=3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
			response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodGet, test.target, testSessionToken, "", "", "")
			if response.Code != http.StatusBadRequest || response.Body.String() != `{"error":{"code":"invalid_request","message":"request is invalid"}}`+"\n" {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if repository.getServerID != 0 || repository.historyServerID != 0 {
				t.Fatalf("invalid request reached repository: %#v", repository)
			}
		})
	}
}

func TestHistoryAcceptsExactSevenDayRange(t *testing.T) {
	sevenDaysMS := int64(7 * 24 * 60 * 60 * 1_000)
	repository := &repositoryStub{
		session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000},
		server:  domain.Server{ID: 1, Name: "node", Enabled: true},
	}
	target := "/api/v1/admin/servers/1/history?from_ms=1&to_ms=" + strconv.FormatInt(1+sevenDaysMS, 10)
	response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodGet, target, testSessionToken, "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"samples":[]`) {
		t.Fatalf("empty samples must be []: %s", response.Body.String())
	}
}

func TestHistoryMapsUnknownServerAndRepositoryFailures(t *testing.T) {
	tests := []struct {
		name       string
		serverErr  error
		samplesErr error
		wantStatus int
		wantBody   string
	}{
		{name: "unknown server", serverErr: domain.ErrNotFound, wantStatus: http.StatusNotFound, wantBody: `{"error":{"code":"not_found","message":"resource was not found"}}` + "\n"},
		{name: "server failure", serverErr: errors.New("SERVER-CANARY"), wantStatus: http.StatusInternalServerError, wantBody: `{"error":{"code":"internal_error","message":"internal server error"}}` + "\n"},
		{name: "samples failure", samplesErr: errors.New("SAMPLES-CANARY"), wantStatus: http.StatusInternalServerError, wantBody: `{"error":{"code":"internal_error","message":"internal server error"}}` + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{
				session:    domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000},
				server:     domain.Server{ID: 1, Name: "node"},
				serverErr:  test.serverErr,
				samplesErr: test.samplesErr,
			}
			response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodGet, "/api/v1/admin/servers/1/history?from_ms=1&to_ms=2", testSessionToken, "", "", "")
			if response.Code != test.wantStatus || response.Body.String() != test.wantBody {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "CANARY") {
				t.Fatalf("response leaked repository error: %s", response.Body.String())
			}
		})
	}
}

func TestHistoryRejectsUnsupportedMethod(t *testing.T) {
	repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
	response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodDelete, "/api/v1/admin/servers/1/history?from_ms=1&to_ms=2", testSessionToken, testAdminOrigin, "", "")
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("response = %d Allow=%q body=%q", response.Code, response.Header().Get("Allow"), response.Body.String())
	}
}
