package serverapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/config"
	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/telegramapi"
	"github.com/tg-monitor/tg-monitor/internal/telegrambot"
)

func TestAppServeComposesTelegramRoutesWithSharedStore(t *testing.T) {
	now := time.Date(2026, time.July, 15, 9, 30, 0, 0, time.UTC)
	botRequests := make(chan struct {
		path string
		body map[string]any
	}, 1)
	botAPI := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		encoded, _ := io.ReadAll(request.Body)
		var body map[string]any
		_ = json.Unmarshal(encoded, &body)
		botRequests <- struct {
			path string
			body map[string]any
		}{path: request.URL.Path, body: body}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"ok":true,"result":{"message_id":123}}`)
	}))
	t.Cleanup(botAPI.Close)

	cfg := telegramApplicationConfig(filepath.Join(t.TempDir(), "monitor.db"))
	var factoryToken string
	var factoryTimeout time.Duration
	app, err := newWithDependencies(context.Background(), cfg, testLogger(), appDependencies{
		newTelegramSender: func(token string, timeout time.Duration) (telegrambot.Sender, error) {
			factoryToken = token
			factoryTimeout = timeout
			return telegramapi.NewForTest(botAPI.URL, token, botAPI.Client())
		},
		random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)),
		now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("newWithDependencies() error = %v", err)
	}
	if factoryToken != cfg.Telegram.BotToken || factoryTimeout != cfg.Telegram.HTTPTimeout {
		t.Fatalf("Telegram sender factory got token %q timeout %v", factoryToken, factoryTimeout)
	}

	rawAgentToken := "integration-agent-token"
	server, err := app.store.CreateServer(context.Background(), domain.Server{
		Name:        "integration-server",
		Enabled:     true,
		TokenSHA256: auth.HashToken(rawAgentToken),
	})
	if err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	if err := app.store.SetAlertPreference(context.Background(), 42, false); err != nil {
		t.Fatalf("SetAlertPreference(false) error = %v", err)
	}
	if err := app.store.CreateSession(context.Background(), "expired-session", domain.Session{
		TelegramUserID: 42,
		CreatedAtMS:    now.Add(-2 * time.Hour).UnixMilli(),
		ExpiresAtMS:    now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatalf("CreateSession(expired) error = %v", err)
	}
	if inserted, err := app.store.RecordTelegramUpdate(context.Background(), 8000, now.Add(-8*24*time.Hour).UnixMilli()); err != nil || !inserted {
		t.Fatalf("RecordTelegramUpdate(old) = %v, %v", inserted, err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		_ = app.Close(context.Background())
	})
	baseURL := "http://" + listener.Addr().String()
	waitForAppReady(t, baseURL)
	if deleted, err := app.store.DeleteExpiredSessions(context.Background(), now.UnixMilli()); err != nil || deleted != 0 {
		t.Fatalf("DeleteExpiredSessions(after startup cleanup) = %d, %v; want 0, nil", deleted, err)
	}
	if deleted, err := app.store.DeleteTelegramUpdatesBefore(
		context.Background(), now.Add(-7*24*time.Hour).UnixMilli(),
	); err != nil || deleted != 0 {
		t.Fatalf("DeleteTelegramUpdatesBefore(after startup cleanup) = %d, %v; want 0, nil", deleted, err)
	}
	client := &http.Client{Timeout: 2 * time.Second}

	metricBody, err := json.Marshal(validApplicationMetricReport(now.UnixMilli()))
	if err != nil {
		t.Fatalf("json.Marshal(metric) error = %v", err)
	}
	metricRequest, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/metrics", bytes.NewReader(metricBody))
	if err != nil {
		t.Fatalf("NewRequest(metric) error = %v", err)
	}
	metricRequest.Header.Set("Authorization", "Bearer "+rawAgentToken)
	metricRequest.Header.Set("Content-Type", "application/json")
	metricResponse, err := client.Do(metricRequest)
	if err != nil {
		t.Fatalf("POST metrics error = %v", err)
	}
	metricResponse.Body.Close()
	if metricResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("POST metrics status = %d, want 204", metricResponse.StatusCode)
	}
	if latest, err := app.store.GetLatestMetrics(context.Background(), server.ID); err != nil || latest.ServerID != server.ID {
		t.Fatalf("shared store latest metrics = %#v, %v", latest, err)
	}

	webhookRequest, err := http.NewRequest(http.MethodPost, baseURL+"/telegram/webhook", strings.NewReader(
		`{"update_id":9001,"message":{"from":{"id":42},"chat":{"id":4242,"type":"private"},"text":"/help"}}`,
	))
	if err != nil {
		t.Fatalf("NewRequest(webhook) error = %v", err)
	}
	webhookRequest.Header.Set("X-Telegram-Bot-Api-Secret-Token", cfg.Telegram.WebhookSecret)
	webhookResponse, err := client.Do(webhookRequest)
	if err != nil {
		t.Fatalf("POST webhook error = %v", err)
	}
	webhookResponse.Body.Close()
	if webhookResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("POST webhook status = %d, want 204", webhookResponse.StatusCode)
	}
	select {
	case sent := <-botRequests:
		if sent.path != "/bot"+cfg.Telegram.BotToken+"/sendMessage" || sent.body["chat_id"] != float64(4242) {
			t.Fatalf("Bot API request = %s %#v", sent.path, sent.body)
		}
	case <-time.After(time.Second):
		t.Fatal("Bot API did not receive sendMessage")
	}

	appWebhookRequest, err := http.NewRequest(http.MethodPost, baseURL+"/telegram/webhook", strings.NewReader(
		`{"update_id":9002,"message":{"from":{"id":42},"chat":{"id":4242,"type":"private"},"text":"/app"}}`,
	))
	if err != nil {
		t.Fatalf("NewRequest(app webhook) error = %v", err)
	}
	appWebhookRequest.Header.Set("X-Telegram-Bot-Api-Secret-Token", cfg.Telegram.WebhookSecret)
	appWebhookResponse, err := client.Do(appWebhookRequest)
	if err != nil {
		t.Fatalf("POST app webhook error = %v", err)
	}
	appWebhookResponse.Body.Close()
	if appWebhookResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("POST app webhook status = %d, want 204", appWebhookResponse.StatusCode)
	}
	select {
	case sent := <-botRequests:
		wantBody := map[string]any{
			"chat_id": float64(4242),
			"text":    "Open the tg-monitor operator app.",
			"reply_markup": map[string]any{"inline_keyboard": []any{[]any{map[string]any{
				"text":    "Open tg-monitor",
				"web_app": map[string]any{"url": "http://127.0.0.1/app/"},
			}}}},
		}
		if sent.path != "/bot"+cfg.Telegram.BotToken+"/sendMessage" || !reflect.DeepEqual(sent.body, wantBody) {
			t.Fatalf("Bot API app request = %s %#v, want %#v", sent.path, sent.body, wantBody)
		}
	case <-time.After(time.Second):
		t.Fatal("Bot API did not receive Web App sendMessage")
	}
	if inserted, err := app.store.RecordTelegramUpdate(context.Background(), 9001, now.Add(time.Second).UnixMilli()); err != nil || inserted {
		t.Fatalf("shared store duplicate update = %v, %v; want false, nil", inserted, err)
	}

	initData := signedInitData(t, cfg.Telegram.BotToken, now, 42)
	loginBody, _ := json.Marshal(map[string]string{"init_data": initData})
	loginRequest, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/auth/telegram", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("NewRequest(login) error = %v", err)
	}
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse, err := client.Do(loginRequest)
	if err != nil {
		t.Fatalf("POST login error = %v", err)
	}
	loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusNoContent || len(loginResponse.Cookies()) != 1 {
		t.Fatalf("POST login = %d cookies=%d", loginResponse.StatusCode, len(loginResponse.Cookies()))
	}
	sessionCookie := loginResponse.Cookies()[0]
	if session, err := app.store.GetSession(context.Background(), sessionCookie.Value, now.UnixMilli()); err != nil || session.TelegramUserID != 42 {
		t.Fatalf("shared store session = %#v, %v", session, err)
	}

	sessionRequest, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/auth/session", nil)
	sessionRequest.AddCookie(sessionCookie)
	sessionResponse, err := client.Do(sessionRequest)
	if err != nil {
		t.Fatalf("GET session error = %v", err)
	}
	sessionResponse.Body.Close()
	if sessionResponse.StatusCode != http.StatusOK {
		t.Fatalf("GET session status = %d, want 200", sessionResponse.StatusCode)
	}

	appResponse, err := client.Get(baseURL + "/app/")
	if err != nil {
		t.Fatalf("GET /app/ error = %v", err)
	}
	appBody, _ := io.ReadAll(appResponse.Body)
	appResponse.Body.Close()
	if appResponse.StatusCode != http.StatusOK || appResponse.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("GET /app/ = %d CSP=%q body=%q", appResponse.StatusCode, appResponse.Header.Get("Content-Security-Policy"), appBody)
	}
	for _, asset := range []string{"/app/app.css", "/app/app.js"} {
		if !bytes.Contains(appBody, []byte(asset)) {
			t.Fatalf("GET /app/ body missing %q", asset)
		}
	}

	overviewRequest, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/admin/overview", nil)
	overviewRequest.AddCookie(sessionCookie)
	overviewResponse, err := client.Do(overviewRequest)
	if err != nil {
		t.Fatalf("GET overview error = %v", err)
	}
	overviewBody, _ := io.ReadAll(overviewResponse.Body)
	overviewResponse.Body.Close()
	if overviewResponse.StatusCode != http.StatusOK || !bytes.Contains(overviewBody, []byte(`"name":"integration-server"`)) || !bytes.Contains(overviewBody, []byte(`"cpu_pct":25`)) {
		t.Fatalf("GET overview = %d body=%q", overviewResponse.StatusCode, overviewBody)
	}

	fromMS := now.Add(-time.Hour).UnixMilli()
	toMS := now.Add(time.Second).UnixMilli()
	historyRequest, _ := http.NewRequest(
		http.MethodGet,
		baseURL+"/api/v1/admin/servers/"+strconv.FormatInt(server.ID, 10)+"/history?from_ms="+strconv.FormatInt(fromMS, 10)+"&to_ms="+strconv.FormatInt(toMS, 10),
		nil,
	)
	historyRequest.AddCookie(sessionCookie)
	historyResponse, err := client.Do(historyRequest)
	if err != nil {
		t.Fatalf("GET history error = %v", err)
	}
	historyBody, _ := io.ReadAll(historyResponse.Body)
	historyResponse.Body.Close()
	if historyResponse.StatusCode != http.StatusOK || !bytes.Contains(historyBody, []byte(`"name":"integration-server"`)) {
		t.Fatalf("GET history = %d body=%q", historyResponse.StatusCode, historyBody)
	}
	logoutRequest, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/auth/logout", nil)
	logoutRequest.AddCookie(sessionCookie)
	logoutResponse, err := client.Do(logoutRequest)
	if err != nil {
		t.Fatalf("POST logout error = %v", err)
	}
	logoutResponse.Body.Close()
	if logoutResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("POST logout status = %d, want 204", logoutResponse.StatusCode)
	}
	if _, err := app.store.GetSession(context.Background(), sessionCookie.Value, now.UnixMilli()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetSession(after logout) error = %v, want not found", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Telegram-enabled Serve() did not stop after cancellation")
	}
}

func TestNewWithDependenciesDoesNotFallBackWhenTelegramConstructionFails(t *testing.T) {
	want := errors.New("TELEGRAM-FACTORY-CANARY")
	cfg := telegramApplicationConfig(filepath.Join(t.TempDir(), "monitor.db"))
	app, err := newWithDependencies(context.Background(), cfg, testLogger(), appDependencies{
		newTelegramSender: func(string, time.Duration) (telegrambot.Sender, error) {
			return nil, want
		},
		random: bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)),
		now:    time.Now,
	})
	if app != nil || !errors.Is(err, want) {
		t.Fatalf("newWithDependencies() = %#v, %v; want nil app and factory error", app, err)
	}
}

func telegramApplicationConfig(databasePath string) config.ApplicationRuntimeConfig {
	cfg := testRuntimeConfig(databasePath, 10*time.Millisecond)
	cfg.Telegram = &config.TelegramRuntimeConfig{
		PublicURL:        "http://127.0.0.1",
		BotToken:         "123456:integration-bot-token",
		WebhookSecret:    "integration_webhook_secret",
		AdminTelegramIDs: []int64{42},
		SessionTTL:       12 * time.Hour,
		InitDataMaxAge:   5 * time.Minute,
		HTTPTimeout:      time.Second,
	}
	return cfg
}

func waitForAppReady(t *testing.T, baseURL string) {
	t.Helper()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(time.Second)
	for {
		response, err := client.Get(baseURL + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("application did not become ready: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func validApplicationMetricReport(capturedAtMS int64) domain.MetricReport {
	return domain.MetricReport{
		CapturedAtMS:            capturedAtMS,
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
		System: domain.SystemInfo{
			Hostname: "integration-host", OS: "linux", Kernel: "6.12", Arch: "amd64",
		},
	}
}

func signedInitData(t *testing.T, botToken string, now time.Time, userID int64) string {
	t.Helper()
	values := url.Values{
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
		"user":      {`{"id":` + strconv.FormatInt(userID, 10) + `,"first_name":"Admin"}`},
	}
	checkString := "auth_date=" + values.Get("auth_date") + "\nuser=" + values.Get("user")
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(checkString))
	values.Set("hash", hex.EncodeToString(check.Sum(nil)))
	return values.Encode()
}
