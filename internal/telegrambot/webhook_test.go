package telegrambot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testWebhookSecret = "webhook-secret-123"

type webhookUpdatesStub struct {
	inserted bool
	err      error
	calls    int
	updateID int64
	nowMS    int64
	events   *[]string
}

func (stub *webhookUpdatesStub) RecordTelegramUpdate(_ context.Context, updateID, nowMS int64) (bool, error) {
	stub.calls++
	stub.updateID = updateID
	stub.nowMS = nowMS
	if stub.events != nil {
		*stub.events = append(*stub.events, "record")
	}
	return stub.inserted, stub.err
}

type webhookSenderStub struct {
	err    error
	calls  int
	chatID int64
	text   string
	events *[]string
}

func (stub *webhookSenderStub) SendMessage(_ context.Context, chatID int64, text string) error {
	stub.calls++
	stub.chatID = chatID
	stub.text = text
	if stub.events != nil {
		*stub.events = append(*stub.events, "send")
	}
	return stub.err
}

type webhookReplierStub struct {
	reply  string
	err    error
	calls  int
	userID int64
	text   string
	events *[]string
}

func (stub *webhookReplierStub) Reply(_ context.Context, userID int64, text string) (string, error) {
	stub.calls++
	stub.userID = userID
	stub.text = text
	if stub.events != nil {
		*stub.events = append(*stub.events, "reply")
	}
	return stub.reply, stub.err
}

func TestWebhookAcceptsValidUpdateWithUnknownFields(t *testing.T) {
	handler, _, _, _ := newWebhookTestHandler(t)
	body := `{
		"update_id": 9001,
		"future_top_level": {"enabled": true},
		"message": {
			"message_id": 1,
			"from": {"id": 42, "is_bot": false, "future_user": "ok"},
			"chat": {"id": 4242, "type": "private", "future_chat": 7},
			"text": "/help",
			"future_message": [1, 2, 3]
		}
	}`

	response := serveWebhook(handler, http.MethodPost, testWebhookSecret, strings.NewReader(body))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %q", response.Code, response.Body.String())
	}
}

func TestWebhookWrongSecretsMatchGenericNotFoundAndHaveNoSideEffects(t *testing.T) {
	tests := []struct {
		name   string
		secret string
	}{
		{name: "missing", secret: ""},
		{name: "same length", secret: strings.Repeat("x", len(testWebhookSecret))},
		{name: "different length", secret: "wrong"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, updates, sender, replier := newWebhookTestHandler(t)
			response := serveWebhook(handler, http.MethodPost, tt.secret, strings.NewReader(validWebhookUpdate(9001)))

			absent := httptest.NewRecorder()
			http.NotFoundHandler().ServeHTTP(absent, httptest.NewRequest(http.MethodPost, "/absent", nil))
			if response.Code != absent.Code || response.Body.String() != absent.Body.String() {
				t.Fatalf("wrong-secret response = (%d, %q), absent route = (%d, %q)", response.Code, response.Body.String(), absent.Code, absent.Body.String())
			}
			if got, want := response.Header().Get("Content-Type"), absent.Header().Get("Content-Type"); got != want {
				t.Fatalf("Content-Type = %q, want absent-route value %q", got, want)
			}
			assertNoWebhookCalls(t, updates, sender, replier)
		})
	}
}

func TestWebhookRejectsUnsupportedMethod(t *testing.T) {
	handler, updates, sender, replier := newWebhookTestHandler(t)
	response := serveWebhook(handler, http.MethodGet, testWebhookSecret, nil)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.Code)
	}
	if got := response.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", got)
	}
	assertNoWebhookCalls(t, updates, sender, replier)
}

func TestWebhookRejectsMalformedOrTrailingJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"update_id":`},
		{name: "trailing object", body: validWebhookUpdate(9001) + ` {}`},
		{name: "trailing token", body: validWebhookUpdate(9001) + ` true`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, updates, sender, replier := newWebhookTestHandler(t)
			response := serveWebhook(handler, http.MethodPost, testWebhookSecret, strings.NewReader(tt.body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %q", response.Code, response.Body.String())
			}
			assertNoWebhookCalls(t, updates, sender, replier)
		})
	}
}

func TestWebhookRejectsOversizedBody(t *testing.T) {
	handler, updates, sender, replier := newWebhookTestHandler(t)
	body := `{"update_id":9001,"padding":"` + strings.Repeat("x", (256<<10)+1) + `"}`
	response := serveWebhook(handler, http.MethodPost, testWebhookSecret, strings.NewReader(body))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %q", response.Code, response.Body.String())
	}
	assertNoWebhookCalls(t, updates, sender, replier)
}

func TestWebhookRejectsNonPositiveUpdateID(t *testing.T) {
	for _, updateID := range []int64{0, -1} {
		t.Run(strconv.FormatInt(updateID, 10), func(t *testing.T) {
			handler, updates, sender, replier := newWebhookTestHandler(t)
			response := serveWebhook(handler, http.MethodPost, testWebhookSecret, strings.NewReader(validWebhookUpdate(updateID)))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.Code)
			}
			assertNoWebhookCalls(t, updates, sender, replier)
		})
	}
}

func newWebhookTestHandler(t *testing.T) (http.Handler, *webhookUpdatesStub, *webhookSenderStub, *webhookReplierStub) {
	t.Helper()
	updates := &webhookUpdatesStub{inserted: true}
	sender := &webhookSenderStub{}
	replier := &webhookReplierStub{reply: "safe reply"}
	handler, err := NewWebhookHandler(WebhookDependencies{
		Updates:  updates,
		Sender:   sender,
		Replier:  replier,
		Secret:   testWebhookSecret,
		AdminIDs: []int64{42},
		Now:      func() time.Time { return time.UnixMilli(1_700_000_000_000) },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}
	return handler, updates, sender, replier
}

func serveWebhook(handler http.Handler, method, secret string, body io.Reader) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/telegram/webhook", body)
	if secret != "" {
		request.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func validWebhookUpdate(updateID int64) string {
	return `{"update_id":` + strconv.FormatInt(updateID, 10) + `,"message":{"from":{"id":42},"chat":{"id":4242,"type":"private"},"text":"/help"}}`
}

func assertNoWebhookCalls(t *testing.T, updates *webhookUpdatesStub, sender *webhookSenderStub, replier *webhookReplierStub) {
	t.Helper()
	if updates.calls != 0 || sender.calls != 0 || replier.calls != 0 {
		t.Fatalf("unexpected calls: updates=%d sender=%d replier=%d", updates.calls, sender.calls, replier.calls)
	}
}

func TestWebhookRecordsBeforeReplyAndSend(t *testing.T) {
	events := make([]string, 0, 3)
	updates := &webhookUpdatesStub{inserted: true, events: &events}
	sender := &webhookSenderStub{events: &events}
	replier := &webhookReplierStub{reply: "administrator reply", events: &events}
	handler := mustWebhookHandler(t, updates, sender, replier, nil)

	response := serveWebhook(handler, http.MethodPost, testWebhookSecret, strings.NewReader(validWebhookUpdate(9001)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
	if got := strings.Join(events, ","); got != "record,reply,send" {
		t.Fatalf("call order = %q, want record,reply,send", got)
	}
	if updates.updateID != 9001 || updates.nowMS != 1_700_000_000_000 {
		t.Fatalf("RecordTelegramUpdate(%d, %d), want (9001, 1700000000000)", updates.updateID, updates.nowMS)
	}
	if replier.userID != 42 || replier.text != "/help" {
		t.Fatalf("Reply(%d, %q), want (42, /help)", replier.userID, replier.text)
	}
	if sender.chatID != 4242 || sender.text != "administrator reply" {
		t.Fatalf("SendMessage(%d, %q), want private chat 4242 and reply", sender.chatID, sender.text)
	}
}

func TestWebhookDuplicateAcknowledgesWithoutCommandSideEffects(t *testing.T) {
	updates := &webhookUpdatesStub{inserted: false}
	sender := &webhookSenderStub{}
	replier := &webhookReplierStub{reply: "must not send"}
	handler := mustWebhookHandler(t, updates, sender, replier, nil)

	response := serveWebhook(handler, http.MethodPost, testWebhookSecret, strings.NewReader(validWebhookUpdate(9001)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
	if updates.calls != 1 || sender.calls != 0 || replier.calls != 0 {
		t.Fatalf("calls: updates=%d sender=%d replier=%d", updates.calls, sender.calls, replier.calls)
	}
}

func TestWebhookStoreFailureRequestsRetryWithoutSideEffects(t *testing.T) {
	updates := &webhookUpdatesStub{err: errors.New("STORE-ERROR-CANARY")}
	sender := &webhookSenderStub{}
	replier := &webhookReplierStub{reply: "must not send"}
	handler := mustWebhookHandler(t, updates, sender, replier, nil)

	response := serveWebhook(handler, http.MethodPost, testWebhookSecret, strings.NewReader(validWebhookUpdate(9001)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	if strings.Contains(response.Body.String(), "STORE-ERROR-CANARY") {
		t.Fatalf("response leaked repository error: %q", response.Body.String())
	}
	if updates.calls != 1 || sender.calls != 0 || replier.calls != 0 {
		t.Fatalf("calls: updates=%d sender=%d replier=%d", updates.calls, sender.calls, replier.calls)
	}
}

func TestWebhookSilentlyFiltersUntrustedOrUnsupportedUpdatesAfterRecording(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing message", body: `{"update_id":9001}`},
		{name: "missing from", body: `{"update_id":9001,"message":{"chat":{"id":4242,"type":"private"},"text":"/help"}}`},
		{name: "missing chat", body: `{"update_id":9001,"message":{"from":{"id":42},"text":"/help"}}`},
		{name: "non-private chat", body: `{"update_id":9001,"message":{"from":{"id":42},"chat":{"id":4242,"type":"group"},"text":"/help"}}`},
		{name: "non-administrator", body: `{"update_id":9001,"message":{"from":{"id":99},"chat":{"id":4242,"type":"private"},"text":"/help"}}`},
		{name: "missing text", body: `{"update_id":9001,"message":{"from":{"id":42},"chat":{"id":4242,"type":"private"}}}`},
		{name: "empty text", body: `{"update_id":9001,"message":{"from":{"id":42},"chat":{"id":4242,"type":"private"},"text":""}}`},
		{name: "channel post", body: `{"update_id":9001,"channel_post":{"chat":{"id":-1001,"type":"channel"},"text":"/help"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updates := &webhookUpdatesStub{inserted: true}
			sender := &webhookSenderStub{}
			replier := &webhookReplierStub{reply: "must not send"}
			handler := mustWebhookHandler(t, updates, sender, replier, nil)

			response := serveWebhook(handler, http.MethodPost, testWebhookSecret, strings.NewReader(tt.body))
			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", response.Code)
			}
			if updates.calls != 1 || sender.calls != 0 || replier.calls != 0 {
				t.Fatalf("calls: updates=%d sender=%d replier=%d", updates.calls, sender.calls, replier.calls)
			}
		})
	}
}

func TestWebhookCommandFailuresAreAcknowledgedAndLoggedSafely(t *testing.T) {
	tests := []struct {
		name            string
		replierErr      error
		senderErr       error
		wantOperation   string
		wantSenderCalls int
	}{
		{
			name:          "reply failure",
			replierErr:    errors.New("REPLIER-ERROR-CANARY"),
			wantOperation: "reply_failed",
		},
		{
			name:            "send failure",
			senderErr:       errors.New("SENDER-ERROR-CANARY"),
			wantOperation:   "send_message_failed",
			wantSenderCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			updates := &webhookUpdatesStub{inserted: true}
			sender := &webhookSenderStub{err: tt.senderErr}
			replier := &webhookReplierStub{reply: "REPLY-CANARY", err: tt.replierErr}
			handler := mustWebhookHandler(t, updates, sender, replier, testWebhookLogger(&logs))
			body := `{"update_id":9001,"token":"TOKEN-CANARY","message":{"from":{"id":42},"chat":{"id":4242,"type":"private"},"text":"MESSAGE-TEXT-CANARY"}}`

			response := serveWebhook(handler, http.MethodPost, testWebhookSecret, strings.NewReader(body))
			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", response.Code)
			}
			if updates.calls != 1 || replier.calls != 1 || sender.calls != tt.wantSenderCalls {
				t.Fatalf("calls: updates=%d replier=%d sender=%d", updates.calls, replier.calls, sender.calls)
			}
			assertSafeWebhookLog(t, logs.String(), tt.wantOperation)
		})
	}
}

func mustWebhookHandler(t *testing.T, updates UpdateRepository, sender Sender, replier Replier, logger *slog.Logger) http.Handler {
	t.Helper()
	handler, err := NewWebhookHandler(WebhookDependencies{
		Updates:  updates,
		Sender:   sender,
		Replier:  replier,
		Secret:   testWebhookSecret,
		AdminIDs: []int64{42},
		Now:      func() time.Time { return time.UnixMilli(1_700_000_000_000) },
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}
	return handler
}

func testWebhookLogger(destination io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(destination, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
			if attribute.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attribute
		},
	}))
}

func assertSafeWebhookLog(t *testing.T, encoded, wantOperation string) {
	t.Helper()
	for _, canary := range []string{
		testWebhookSecret,
		"TOKEN-CANARY",
		"MESSAGE-TEXT-CANARY",
		"REPLY-CANARY",
		"REPLIER-ERROR-CANARY",
		"SENDER-ERROR-CANARY",
	} {
		if strings.Contains(encoded, canary) {
			t.Fatalf("log leaked %q: %s", canary, encoded)
		}
	}
	lines := strings.Split(strings.TrimSpace(encoded), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %d, want 1: %q", len(lines), encoded)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	allowed := map[string]bool{
		"level": true, "msg": true, "operation": true, "update_id": true, "admin_id": true,
	}
	for key := range entry {
		if !allowed[key] {
			t.Fatalf("log contains unexpected key %q: %v", key, entry)
		}
	}
	if entry["operation"] != wantOperation || entry["update_id"] != float64(9001) || entry["admin_id"] != float64(42) {
		t.Fatalf("safe log fields = %v", entry)
	}
}
