package adminapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestSettingsGetAndPutRoundTrip(t *testing.T) {
	repository := &repositoryStub{
		session:  domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000},
		settings: domain.Settings{OfflineThresholdSeconds: 60, AlertThresholdSeconds: 120, HistoryRetentionDays: 7},
	}
	handler := mustAdminHandler(t, repository, nil)
	getResponse := serveAdmin(handler, http.MethodGet, "/api/v1/admin/settings", testSessionToken, "", "", "")
	if getResponse.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%q", getResponse.Code, getResponse.Body.String())
	}
	var got domain.Settings
	if err := json.Unmarshal(getResponse.Body.Bytes(), &got); err != nil || got != repository.settings {
		t.Fatalf("GET settings = %#v, %v", got, err)
	}

	putResponse := serveAdmin(
		handler,
		http.MethodPut,
		"/api/v1/admin/settings",
		testSessionToken,
		testAdminOrigin,
		"application/json",
		`{"offline_threshold_seconds":90,"alert_threshold_seconds":180,"history_retention_days":30}`,
	)
	want := domain.Settings{OfflineThresholdSeconds: 90, AlertThresholdSeconds: 180, HistoryRetentionDays: 30}
	if putResponse.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%q", putResponse.Code, putResponse.Body.String())
	}
	if repository.updatedSettings != want {
		t.Fatalf("UpdateSettings = %#v, want %#v", repository.updatedSettings, want)
	}
	got = domain.Settings{}
	if err := json.Unmarshal(putResponse.Body.Bytes(), &got); err != nil || got != want {
		t.Fatalf("PUT settings response = %#v, %v", got, err)
	}
}

func TestSettingsPutValidatesEveryBoundBeforePersistence(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "offline zero", body: `{"offline_threshold_seconds":0,"alert_threshold_seconds":1,"history_retention_days":1}`},
		{name: "offline high", body: `{"offline_threshold_seconds":86401,"alert_threshold_seconds":1,"history_retention_days":1}`},
		{name: "alert zero", body: `{"offline_threshold_seconds":1,"alert_threshold_seconds":0,"history_retention_days":1}`},
		{name: "alert high", body: `{"offline_threshold_seconds":1,"alert_threshold_seconds":86401,"history_retention_days":1}`},
		{name: "retention zero", body: `{"offline_threshold_seconds":1,"alert_threshold_seconds":1,"history_retention_days":0}`},
		{name: "retention high", body: `{"offline_threshold_seconds":1,"alert_threshold_seconds":1,"history_retention_days":366}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
			response := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodPut, "/api/v1/admin/settings", testSessionToken, testAdminOrigin, "application/json", test.body)
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"validation_failed"`) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if repository.updatedSettings != (domain.Settings{}) {
				t.Fatalf("invalid settings reached repository: %#v", repository.updatedSettings)
			}
		})
	}
}

func TestSettingsOriginJSONRepositoryAndMethodErrors(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		origin      string
		contentType string
		body        string
		settingsErr error
		updateErr   error
		wantStatus  int
		wantCode    string
		wantAllow   string
	}{
		{name: "missing origin", method: http.MethodPut, contentType: "application/json", body: validSettingsBody(), wantStatus: 403, wantCode: "forbidden_origin"},
		{name: "unknown field", method: http.MethodPut, origin: testAdminOrigin, contentType: "application/json", body: `{"offline_threshold_seconds":1,"alert_threshold_seconds":1,"history_retention_days":1,"extra":1}`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "get failure", method: http.MethodGet, settingsErr: errors.New("GET-SETTINGS-CANARY"), wantStatus: 500, wantCode: "internal_error"},
		{name: "update failure", method: http.MethodPut, origin: testAdminOrigin, contentType: "application/json", body: validSettingsBody(), updateErr: errors.New("PUT-SETTINGS-CANARY"), wantStatus: 500, wantCode: "internal_error"},
		{name: "wrong method", method: http.MethodPost, wantStatus: 405, wantCode: "method_not_allowed", wantAllow: "GET, PUT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{
				session:           domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000},
				settingsErr:       test.settingsErr,
				updateSettingsErr: test.updateErr,
			}
			response := serveAdmin(mustAdminHandler(t, repository, nil), test.method, "/api/v1/admin/settings", testSessionToken, test.origin, test.contentType, test.body)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) || response.Header().Get("Allow") != test.wantAllow {
				t.Fatalf("response = %d Allow=%q body=%q", response.Code, response.Header().Get("Allow"), response.Body.String())
			}
			if strings.Contains(response.Body.String(), "CANARY") {
				t.Fatalf("response leaked dependency detail: %s", response.Body.String())
			}
		})
	}
}

func TestAlertPreferenceUsesAuthenticatedUserAndReturnsStoredChoice(t *testing.T) {
	repository := &repositoryStub{session: domain.Session{TelegramUserID: 4242, ExpiresAtMS: testNowMS + 60_000}}
	response := serveAdmin(
		mustAdminHandler(t, repository, nil),
		http.MethodPut,
		"/api/v1/admin/alert-preference",
		testSessionToken,
		testAdminOrigin,
		"application/json",
		`{"enabled":true}`,
	)
	if response.Code != http.StatusOK || response.Body.String() != `{"enabled":true}`+"\n" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	if repository.preferenceUserID != 4242 || !repository.preferenceSet {
		t.Fatalf("SetAlertPreference user=%d enabled=%v", repository.preferenceUserID, repository.preferenceSet)
	}
}

func TestAlertPreferenceStrictMutationAndFailureMapping(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		origin        string
		body          string
		preferenceErr error
		wantStatus    int
		wantCode      string
		wantAllow     string
	}{
		{name: "missing origin", method: http.MethodPut, body: `{"enabled":false}`, wantStatus: 403, wantCode: "forbidden_origin"},
		{name: "missing enabled", method: http.MethodPut, origin: testAdminOrigin, body: `{}`, wantStatus: 422, wantCode: "validation_failed"},
		{name: "unknown field", method: http.MethodPut, origin: testAdminOrigin, body: `{"enabled":true,"telegram_user_id":9}`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "failure", method: http.MethodPut, origin: testAdminOrigin, body: `{"enabled":true}`, preferenceErr: errors.New("PREFERENCE-CANARY"), wantStatus: 500, wantCode: "internal_error"},
		{name: "wrong method", method: http.MethodGet, wantStatus: 405, wantCode: "method_not_allowed", wantAllow: "PUT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &repositoryStub{
				session:          domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000},
				setPreferenceErr: test.preferenceErr,
			}
			contentType := "application/json"
			if test.method != http.MethodPut {
				contentType = ""
			}
			response := serveAdmin(mustAdminHandler(t, repository, nil), test.method, "/api/v1/admin/alert-preference", testSessionToken, test.origin, contentType, test.body)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) || response.Header().Get("Allow") != test.wantAllow {
				t.Fatalf("response = %d Allow=%q body=%q", response.Code, response.Header().Get("Allow"), response.Body.String())
			}
			if strings.Contains(response.Body.String(), "CANARY") {
				t.Fatalf("response leaked dependency detail: %s", response.Body.String())
			}
		})
	}
}

func validSettingsBody() string {
	return `{"offline_threshold_seconds":60,"alert_threshold_seconds":120,"history_retention_days":7}`
}
