package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/monitoring"
	"github.com/tg-monitor/tg-monitor/internal/storage/sqlite"
)

type readinessStub struct {
	err error
}

func (stub readinessStub) Ping(context.Context) error {
	return stub.err
}

type authenticatorStub struct {
	server domain.Server
	err    error
	header string
}

func (stub *authenticatorStub) Authenticate(_ context.Context, header string) (domain.Server, error) {
	stub.header = header
	return stub.server, stub.err
}

type ingestorStub struct {
	serverID     int64
	receivedAtMS int64
	report       domain.MetricReport
	err          error
	calls        int
}

func (stub *ingestorStub) Ingest(_ context.Context, serverID, receivedAtMS int64, report domain.MetricReport) error {
	stub.calls++
	stub.serverID = serverID
	stub.receivedAtMS = receivedAtMS
	stub.report = report
	return stub.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func validMetricReport() domain.MetricReport {
	return domain.MetricReport{
		CapturedAtMS:            1_700_000_000_000,
		CPUPct:                  25,
		MemoryTotalBytes:        16_000,
		MemoryUsedBytes:         8_000,
		RootDiskTotalBytes:      100_000,
		RootDiskUsedBytes:       40_000,
		Load1:                   0.1,
		Load5:                   0.2,
		Load15:                  0.3,
		NetworkRXTotalBytes:     1_000,
		NetworkTXTotalBytes:     2_000,
		NetworkRXBytesPerSecond: 10,
		NetworkTXBytesPerSecond: 20,
		UptimeSeconds:           3_600,
		System:                  domain.SystemInfo{Hostname: "metric-host", OS: "linux", Kernel: "6.12", Arch: "amd64"},
	}
}

func reportBody(t *testing.T, report domain.MetricReport) string {
	t.Helper()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(encoded)
}

func newTestHandler(readinessErr, authenticationErr, ingestionErr error) (http.Handler, *authenticatorStub, *ingestorStub) {
	authenticator := &authenticatorStub{server: domain.Server{ID: 7, Enabled: true}, err: authenticationErr}
	ingestor := &ingestorStub{err: ingestionErr}
	handler := NewHandler(Dependencies{
		Readiness:     readinessStub{err: readinessErr},
		Authenticator: authenticator,
		Ingestor:      ingestor,
		Now:           func() time.Time { return time.UnixMilli(1_700_000_000_500) },
		Logger:        discardLogger(),
	})
	return handler, authenticator, ingestor
}

func TestHealthAndReadiness(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		readinessErr error
		status       int
		body         string
	}{
		{name: "health", path: "/healthz", status: http.StatusOK, body: `{"status":"ok"}` + "\n"},
		{name: "ready", path: "/readyz", status: http.StatusOK, body: `{"status":"ok"}` + "\n"},
		{name: "not ready", path: "/readyz", readinessErr: errors.New("database unavailable"), status: http.StatusServiceUnavailable, body: `{"error":{"code":"not_ready","message":"service is not ready"}}` + "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, _, _ := newTestHandler(tt.readinessErr, nil, nil)
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != tt.status || response.Body.String() != tt.body {
				t.Fatalf("response = %d %q, want %d %q", response.Code, response.Body.String(), tt.status, tt.body)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}
		})
	}
}

func TestHandlersRejectUnsupportedMethodsWithAllow(t *testing.T) {
	tests := []struct {
		path  string
		allow string
	}{
		{path: "/healthz", allow: http.MethodGet},
		{path: "/readyz", allow: http.MethodGet},
		{path: "/api/v1/metrics", allow: http.MethodPost},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			handler, _, _ := newTestHandler(nil, nil, nil)
			request := httptest.NewRequest(http.MethodPut, tt.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", response.Code)
			}
			if got := response.Header().Get("Allow"); got != tt.allow {
				t.Fatalf("Allow = %q, want %q", got, tt.allow)
			}
		})
	}
}

func TestMetricIngestionStatusMapping(t *testing.T) {
	validBody := reportBody(t, validMetricReport())
	invalidReport := validMetricReport()
	invalidReport.CPUPct = 101
	tests := []struct {
		name       string
		header     string
		content    string
		body       string
		authErr    error
		ingestErr  error
		wantStatus int
		wantCode   string
	}{
		{name: "missing credentials", content: "application/json", body: validBody, authErr: auth.ErrUnauthorized, wantStatus: 401, wantCode: "unauthorized"},
		{name: "unknown credentials", header: "Bearer unknown", content: "application/json", body: validBody, authErr: auth.ErrUnauthorized, wantStatus: 401, wantCode: "unauthorized"},
		{name: "malformed authorization", header: "Bearer", content: "application/json", body: validBody, authErr: auth.ErrMalformedAuthorization, wantStatus: 400, wantCode: "malformed_authorization"},
		{name: "disabled server", header: "Bearer disabled", content: "application/json", body: validBody, authErr: auth.ErrDisabled, wantStatus: 403, wantCode: "forbidden"},
		{name: "unsupported content type", header: "Bearer token", content: "text/plain", body: validBody, wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "malformed content type", header: "Bearer token", content: "application/json; charset", body: validBody, wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "malformed JSON", header: "Bearer token", content: "application/json", body: `{`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "unknown field", header: "Bearer token", content: "application/json", body: `{"unknown":true}`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "trailing JSON", header: "Bearer token", content: "application/json", body: validBody + `{}`, wantStatus: 400, wantCode: "invalid_json"},
		{name: "invalid report", header: "Bearer token", content: "application/json", body: reportBody(t, invalidReport), wantStatus: 422, wantCode: "invalid_report"},
		{name: "out of order", header: "Bearer token", content: "application/json", body: validBody, ingestErr: monitoring.ErrOutOfOrder, wantStatus: 409, wantCode: "out_of_order"},
		{name: "storage failure", header: "Bearer token", content: "application/json", body: validBody, ingestErr: errors.New("storage failed"), wantStatus: 500, wantCode: "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, _, _ := newTestHandler(nil, tt.authErr, tt.ingestErr)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", strings.NewReader(tt.body))
			if tt.header != "" {
				request.Header.Set("Authorization", tt.header)
			}
			if tt.content != "" {
				request.Header.Set("Content-Type", tt.content)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), tt.wantStatus)
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error envelope: %v; body=%q", err, response.Body.String())
			}
			if envelope.Error.Code != tt.wantCode {
				t.Fatalf("error code = %q, want %q", envelope.Error.Code, tt.wantCode)
			}
		})
	}
}

func TestMetricIngestionEnforces64KiBBodyLimit(t *testing.T) {
	t.Run("accepts exact limit", func(t *testing.T) {
		handler, _, _ := newTestHandler(nil, nil, nil)
		encoded := reportBody(t, validMetricReport())
		body := encoded + strings.Repeat(" ", (64<<10)-len(encoded))
		request := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d body=%s, want 204", response.Code, response.Body.String())
		}
	})

	t.Run("rejects one byte over", func(t *testing.T) {
		handler, _, _ := newTestHandler(nil, nil, nil)
		body := `{"padding":"` + strings.Repeat("x", (64<<10)+1) + `"}`
		request := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d body=%s, want 413", response.Code, response.Body.String())
		}
	})
}

func TestMetricIngestionSuccessUsesServerAndReceiveTime(t *testing.T) {
	handler, authenticator, ingestor := newTestHandler(nil, nil, nil)
	report := validMetricReport()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", strings.NewReader(reportBody(t, report)))
	request.Header.Set("Authorization", "Bearer valid-token")
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("response = %d %q, want 204 empty", response.Code, response.Body.String())
	}
	if authenticator.header != "Bearer valid-token" {
		t.Fatalf("Authorization passed to authenticator = %q", authenticator.header)
	}
	if ingestor.calls != 1 || ingestor.serverID != 7 || ingestor.receivedAtMS != 1_700_000_000_500 || ingestor.report != report {
		t.Fatalf("ingestion = calls %d server %d received %d report %#v", ingestor.calls, ingestor.serverID, ingestor.receivedAtMS, ingestor.report)
	}
}

func TestMetricIngestionRealSQLiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("store.Close() error = %v", err)
		}
	})
	rawToken := "integration-agent-token"
	server, err := store.CreateServer(ctx, domain.Server{Name: "integration", Enabled: true, TokenSHA256: auth.HashToken(rawToken)})
	if err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	service := monitoring.NewService(store)
	handler := NewHandler(Dependencies{
		Readiness:     store,
		Authenticator: auth.NewAuthenticator(store),
		Ingestor:      service,
		Now:           func() time.Time { return time.UnixMilli(1_700_000_000_500) },
		Logger:        discardLogger(),
	})
	report := validMetricReport()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewBufferString(reportBody(t, report)))
	request.Header.Set("Authorization", "Bearer "+rawToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s, want 204", response.Code, response.Body.String())
	}

	latest, err := store.GetLatestMetrics(ctx, server.ID)
	if err != nil {
		t.Fatalf("GetLatestMetrics() error = %v", err)
	}
	want := domain.LatestMetrics{ServerID: server.ID, ReceivedAtMS: 1_700_000_000_500, Report: report}
	if latest != want {
		t.Fatalf("GetLatestMetrics() = %#v, want %#v", latest, want)
	}
}
