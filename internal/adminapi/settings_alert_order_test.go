package adminapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestSettingsRejectAlertThresholdBeforeOfflineAtHTTPBoundary(t *testing.T) {
	repository := &repositoryStub{
		session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000},
	}
	response := serveAdmin(
		mustAdminHandler(t, repository, nil),
		http.MethodPut,
		"/api/v1/admin/settings",
		testSessionToken,
		testAdminOrigin,
		"application/json",
		`{"offline_threshold_seconds":120,"alert_threshold_seconds":60,"history_retention_days":7}`,
	)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"validation_failed"`) {
		t.Fatalf("response = %d %q, want validation failure", response.Code, response.Body.String())
	}
	if repository.updatedSettings != (domain.Settings{}) {
		t.Fatalf("invalid settings reached repository: %#v", repository.updatedSettings)
	}
}
