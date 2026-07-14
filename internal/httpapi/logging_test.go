package httpapi

import (
	"bytes"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestAccessLogContainsRequestMetadataWithoutSecretsOrBody(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	rawToken := "logging-super-secret-token"
	authenticator := &authenticatorStub{server: enabledServer(42)}
	ingestor := &ingestorStub{}
	handler := NewHandler(Dependencies{
		Readiness:     readinessStub{},
		Authenticator: authenticator,
		Ingestor:      ingestor,
		Logger:        logger,
	})
	report := validMetricReport()
	report.System.Hostname = "body-secret-hostname"
	request := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", strings.NewReader(reportBody(t, report)))
	request.Header.Set("Authorization", "Bearer "+rawToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	logOutput := output.String()
	for _, required := range []string{`"method":"POST"`, `"route":"/api/v1/metrics"`, `"status":204`, `"server_id":42`} {
		if !strings.Contains(logOutput, required) {
			t.Fatalf("access log %q missing %q", logOutput, required)
		}
	}
	for _, forbidden := range []string{"Authorization", rawToken, hex.EncodeToString(auth.HashToken(rawToken)), "body-secret-hostname"} {
		if strings.Contains(logOutput, forbidden) {
			t.Fatalf("access log leaked %q: %s", forbidden, logOutput)
		}
	}
}

func enabledServer(id int64) domain.Server {
	return domain.Server{ID: id, Enabled: true}
}
