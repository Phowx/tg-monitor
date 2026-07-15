package adminapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const (
	testAdminOrigin  = "https://monitor.example.com"
	testSessionToken = "opaque-session-token"
	testNowMS        = int64(1_700_000_000_000)
)

type repositoryStub struct {
	session           domain.Session
	sessionErr        error
	getSessionCalls   int
	getSessionToken   string
	getSessionNowMS   int64
	servers           []domain.Server
	serversErr        error
	server            domain.Server
	serverErr         error
	getServerID       int64
	createdServer     domain.Server
	createdResult     domain.Server
	createServerErr   error
	updatedServer     domain.Server
	updateServerErr   error
	deletedServerID   int64
	deleteServerErr   error
	rotatedServerID   int64
	rotatedTokenHash  []byte
	rotateTokenErr    error
	latest            []domain.LatestMetrics
	latestErr         error
	samples           []domain.MinuteSample
	samplesErr        error
	historyServerID   int64
	historyFromMS     int64
	historyToMS       int64
	settings          domain.Settings
	settingsErr       error
	updatedSettings   domain.Settings
	updateSettingsErr error
	preference        bool
	preferenceErr     error
	preferenceUserID  int64
	preferenceSet     bool
	setPreferenceErr  error
}

func (stub *repositoryStub) GetSession(_ context.Context, token string, nowMS int64) (domain.Session, error) {
	stub.getSessionCalls++
	stub.getSessionToken = token
	stub.getSessionNowMS = nowMS
	return stub.session, stub.sessionErr
}

func (stub *repositoryStub) ListServers(context.Context) ([]domain.Server, error) {
	return stub.servers, stub.serversErr
}

func (stub *repositoryStub) GetServer(_ context.Context, serverID int64) (domain.Server, error) {
	stub.getServerID = serverID
	return stub.server, stub.serverErr
}

func (stub *repositoryStub) CreateServer(_ context.Context, server domain.Server) (domain.Server, error) {
	stub.createdServer = server
	return stub.createdResult, stub.createServerErr
}

func (stub *repositoryStub) UpdateServer(_ context.Context, server domain.Server) error {
	stub.updatedServer = server
	return stub.updateServerErr
}

func (stub *repositoryStub) DeleteServer(_ context.Context, serverID int64) error {
	stub.deletedServerID = serverID
	return stub.deleteServerErr
}

func (stub *repositoryStub) UpdateServerTokenHash(_ context.Context, serverID int64, hash []byte) error {
	stub.rotatedServerID = serverID
	stub.rotatedTokenHash = append([]byte(nil), hash...)
	return stub.rotateTokenErr
}

func (stub *repositoryStub) ListLatestMetrics(context.Context) ([]domain.LatestMetrics, error) {
	return stub.latest, stub.latestErr
}

func (stub *repositoryStub) QueryMinuteSamples(_ context.Context, serverID, fromMS, toMS int64) ([]domain.MinuteSample, error) {
	stub.historyServerID = serverID
	stub.historyFromMS = fromMS
	stub.historyToMS = toMS
	return stub.samples, stub.samplesErr
}

func (stub *repositoryStub) GetSettings(context.Context) (domain.Settings, error) {
	return stub.settings, stub.settingsErr
}

func (stub *repositoryStub) UpdateSettings(_ context.Context, settings domain.Settings) error {
	stub.updatedSettings = settings
	return stub.updateSettingsErr
}

func (stub *repositoryStub) GetAlertPreference(_ context.Context, telegramUserID int64) (bool, error) {
	stub.preferenceUserID = telegramUserID
	return stub.preference, stub.preferenceErr
}

func (stub *repositoryStub) SetAlertPreference(_ context.Context, telegramUserID int64, enabled bool) error {
	stub.preferenceUserID = telegramUserID
	stub.preferenceSet = enabled
	return stub.setPreferenceErr
}

func mustAdminHandler(t *testing.T, repository Repository, loggerOutput io.Writer) http.Handler {
	t.Helper()
	if loggerOutput == nil {
		loggerOutput = io.Discard
	}
	handler, err := NewHandler(Config{PublicURL: testAdminOrigin}, Dependencies{
		Repository: repository,
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x42}, 4096)),
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

func serveAdmin(handler http.Handler, method, target, token, origin, contentType, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		request.AddCookie(&http.Cookie{Name: "__Host-tg_monitor_session", Value: token})
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
