package adminapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestOverviewMergesOrderedServersWithExactStateBoundary(t *testing.T) {
	repository := &repositoryStub{
		session:    domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000},
		settings:   domain.Settings{OfflineThresholdSeconds: 60, AlertThresholdSeconds: 120, HistoryRetentionDays: 7},
		preference: true,
		servers: []domain.Server{
			{ID: 4, Name: "fresh", Group: "prod", SortOrder: 1, Enabled: true, TokenSHA256: []byte("must-not-leak")},
			{ID: 2, Name: "boundary", Group: "prod", SortOrder: 2, Enabled: true},
			{ID: 3, Name: "stale", Group: "dev", SortOrder: 3, Enabled: true},
			{ID: 1, Name: "disabled", Group: "dev", SortOrder: 4, Enabled: false},
			{ID: 5, Name: "missing", Group: "dev", SortOrder: 5, Enabled: true},
		},
		latest: []domain.LatestMetrics{
			{ServerID: 1, ReceivedAtMS: testNowMS - 1_000, Report: domain.MetricReport{CPUPct: 1}},
			{ServerID: 2, ReceivedAtMS: testNowMS - 60_000, Report: domain.MetricReport{CPUPct: 2}},
			{ServerID: 3, ReceivedAtMS: testNowMS - 60_001, Report: domain.MetricReport{CPUPct: 3}},
			{ServerID: 4, ReceivedAtMS: testNowMS - 5_000, Report: domain.MetricReport{CPUPct: 4}},
		},
	}
	response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodGet, "/api/v1/admin/overview", testSessionToken, "", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", response.Code, response.Body.String())
	}
	var got struct {
		NowMS          int64           `json:"now_ms"`
		TelegramUserID int64           `json:"telegram_user_id"`
		AlertsEnabled  bool            `json:"alerts_enabled"`
		Settings       domain.Settings `json:"settings"`
		Servers        []struct {
			Server domain.Server         `json:"server"`
			State  string                `json:"state"`
			Latest *domain.LatestMetrics `json:"latest"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if got.NowMS != testNowMS || got.TelegramUserID != 42 || !got.AlertsEnabled || got.Settings != repository.settings {
		t.Fatalf("overview metadata = %#v", got)
	}
	wantIDs := []int64{4, 2, 3, 1, 5}
	wantStates := []string{"online", "online", "offline", "disabled", "offline"}
	if len(got.Servers) != len(wantIDs) {
		t.Fatalf("servers = %#v", got.Servers)
	}
	for index := range wantIDs {
		if got.Servers[index].Server.ID != wantIDs[index] || got.Servers[index].State != wantStates[index] {
			t.Fatalf("server[%d] = %#v", index, got.Servers[index])
		}
		if index == len(wantIDs)-1 && got.Servers[index].Latest != nil {
			t.Fatalf("missing metrics serialized as %#v", got.Servers[index].Latest)
		}
	}
	if repository.preferenceUserID != 42 {
		t.Fatalf("GetAlertPreference user = %d, want 42", repository.preferenceUserID)
	}
	if strings.Contains(response.Body.String(), "must-not-leak") || strings.Contains(response.Body.String(), "token_sha256") {
		t.Fatalf("overview leaked server token hash: %s", response.Body.String())
	}
	assertAdminResponsePolicy(t, response)
}

func TestOverviewRepositoryFailuresAreGeneric(t *testing.T) {
	tests := []struct {
		name       string
		repository *repositoryStub
	}{
		{name: "settings", repository: &repositoryStub{settingsErr: errors.New("SETTINGS-CANARY")}},
		{name: "servers", repository: &repositoryStub{settings: domain.Settings{OfflineThresholdSeconds: 60}, serversErr: errors.New("SERVERS-CANARY")}},
		{name: "latest", repository: &repositoryStub{settings: domain.Settings{OfflineThresholdSeconds: 60}, latestErr: errors.New("LATEST-CANARY")}},
		{name: "preference", repository: &repositoryStub{settings: domain.Settings{OfflineThresholdSeconds: 60}, preferenceErr: errors.New("PREFERENCE-CANARY")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.repository.session = domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}
			response := serveAdmin(mustAdminHandler(t, test.repository, nil), http.MethodGet, "/api/v1/admin/overview", testSessionToken, "", "", "")
			if response.Code != http.StatusInternalServerError || response.Body.String() != `{"error":{"code":"internal_error","message":"internal server error"}}`+"\n" {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "CANARY") {
				t.Fatalf("response leaked repository detail: %s", response.Body.String())
			}
		})
	}
}

func TestOverviewRejectsUnsupportedMethod(t *testing.T) {
	repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
	response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodPost, "/api/v1/admin/overview", testSessionToken, testAdminOrigin, "application/json", `{}`)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("response = %d Allow=%q body=%q", response.Code, response.Header().Get("Allow"), response.Body.String())
	}
	if response.Body.String() != `{"error":{"code":"method_not_allowed","message":"method is not allowed"}}`+"\n" {
		t.Fatalf("body = %q", response.Body.String())
	}
}
