package telegramapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testBotToken = "123456:canary-bot-token"

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestClientSendMessageRequestShape(t *testing.T) {
	var gotPath string
	var gotContentType string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.EscapedPath()
		gotContentType = request.Header.Get("Content-Type")
		if err := json.NewDecoder(request.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(server.Close)

	client, err := NewForTest(server.URL, testBotToken, server.Client())
	if err != nil {
		t.Fatalf("NewForTest() error = %v", err)
	}
	if err := client.SendMessage(context.Background(), 101, "hello <plain text>"); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if gotPath != "/bot123456:canary-bot-token/sendMessage" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q", gotContentType)
	}
	wantBody := map[string]any{"chat_id": float64(101), "text": "hello <plain text>"}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Fatalf("body = %#v, want %#v", gotBody, wantBody)
	}
	if _, exists := gotBody["parse_mode"]; exists {
		t.Fatal("sendMessage unexpectedly set parse_mode")
	}
}

func TestClientWebhookOperationRequestShapes(t *testing.T) {
	type capturedRequest struct {
		method string
		body   map[string]any
	}
	requests := make(chan capturedRequest, 3)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		method := strings.TrimPrefix(request.URL.Path, "/bot"+testBotToken+"/")
		requests <- capturedRequest{method: method, body: body}
		writer.Header().Set("Content-Type", "application/json")
		if method == "getWebhookInfo" {
			_, _ = io.WriteString(writer, `{"ok":true,"result":{"url":"https://monitor.example.com/telegram/webhook","pending_update_count":3,"last_error_date":1700000000,"last_error_message":"must-not-surface"}}`)
			return
		}
		_, _ = io.WriteString(writer, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(server.Close)
	client, err := NewForTest(server.URL, testBotToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	if err := client.SetWebhook(context.Background(), "https://monitor.example.com", "Webhook_Secret-1"); err != nil {
		t.Fatalf("SetWebhook() error = %v", err)
	}
	info, err := client.GetWebhookInfo(context.Background())
	if err != nil {
		t.Fatalf("GetWebhookInfo() error = %v", err)
	}
	wantInfo := WebhookInfo{URL: "https://monitor.example.com/telegram/webhook", PendingUpdateCount: 3, LastErrorDate: 1_700_000_000}
	if info != wantInfo {
		t.Fatalf("WebhookInfo = %#v, want %#v", info, wantInfo)
	}
	if err := client.DeleteWebhook(context.Background()); err != nil {
		t.Fatalf("DeleteWebhook() error = %v", err)
	}

	setRequest := <-requests
	getRequest := <-requests
	deleteRequest := <-requests
	if setRequest.method != "setWebhook" {
		t.Fatalf("first method = %q", setRequest.method)
	}
	wantSetBody := map[string]any{
		"url":             "https://monitor.example.com/telegram/webhook",
		"secret_token":    "Webhook_Secret-1",
		"allowed_updates": []any{"message"},
	}
	if !reflect.DeepEqual(setRequest.body, wantSetBody) {
		t.Fatalf("setWebhook body = %#v, want %#v", setRequest.body, wantSetBody)
	}
	if getRequest.method != "getWebhookInfo" || len(getRequest.body) != 0 {
		t.Fatalf("getWebhookInfo request = %#v", getRequest)
	}
	if deleteRequest.method != "deleteWebhook" || len(deleteRequest.body) != 0 {
		t.Fatalf("deleteWebhook request = %#v", deleteRequest)
	}
}

func TestNewConfiguresDefensiveProductionHTTPClient(t *testing.T) {
	client, err := New(testBotToken, 4*time.Second)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client.httpClient.Timeout != 4*time.Second {
		t.Fatalf("HTTP timeout = %v", client.httpClient.Timeout)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.httpClient.Transport)
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS config = %#v", transport.TLSClientConfig)
	}
	if transport.Proxy == nil {
		t.Fatal("Proxy = nil, want ProxyFromEnvironment")
	}
	redirectErr := client.httpClient.CheckRedirect(&http.Request{}, nil)
	if !errors.Is(redirectErr, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() error = %v", redirectErr)
	}
}

func TestClientRefusesRedirects(t *testing.T) {
	redirectTargetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectTargetCalls++
		_, _ = io.WriteString(writer, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)

	client, err := NewForTest(redirect.URL, testBotToken, redirect.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.SendMessage(context.Background(), 101, "redirect-canary-message")
	if err == nil {
		t.Fatal("SendMessage() error = nil, want redirect refusal")
	}
	if redirectTargetCalls != 0 {
		t.Fatalf("redirect target calls = %d, want 0", redirectTargetCalls)
	}
}

func TestClientErrorsNeverLeakSecretsOrRemoteDetails(t *testing.T) {
	canaries := []string{
		testBotToken,
		"Webhook_Secret-1",
		"secret-message-text",
		"remote-private-description",
		"remote-private-body",
		"https://api.telegram.org/bot" + testBotToken,
	}
	tests := []struct {
		name      string
		handler   http.Handler
		transport http.RoundTripper
	}{
		{
			name: "HTTP status",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(writer, "remote-private-body")
			}),
		},
		{
			name: "Bot rejection",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = io.WriteString(writer, `{"ok":false,"description":"remote-private-description"}`)
			}),
		},
		{
			name: "invalid JSON",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = io.WriteString(writer, `{"ok":`)
			}),
		},
		{
			name: "oversized response",
			handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = io.Copy(writer, bytes.NewReader(bytes.Repeat([]byte("x"), (64<<10)+1)))
			}),
		},
		{
			name: "URL transport error",
			transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				return nil, &url.Error{Op: "Post", URL: "https://api.telegram.org/bot" + testBotToken, Err: errors.New("remote-private-description")}
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *Client
			var closeServer func()
			if tt.transport != nil {
				httpClient := &http.Client{Transport: tt.transport, Timeout: time.Second}
				client, _ = NewForTest("https://api.telegram.org", testBotToken, httpClient)
			} else {
				server := httptest.NewServer(tt.handler)
				closeServer = server.Close
				client, _ = NewForTest(server.URL, testBotToken, server.Client())
			}
			if closeServer != nil {
				defer closeServer()
			}
			err := client.SendMessage(context.Background(), 101, "secret-message-text")
			if err == nil {
				t.Fatal("SendMessage() error = nil")
			}
			for _, canary := range canaries {
				if strings.Contains(err.Error(), canary) {
					t.Fatalf("error leaked %q: %v", canary, err)
				}
			}
		})
	}
}

func TestClientHonorsCanceledContext(t *testing.T) {
	transportCalls := 0
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		transportCalls++
		return nil, request.Context().Err()
	})}
	client, err := NewForTest("https://api.telegram.org", testBotToken, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.SendMessage(ctx, 101, "message"); err == nil {
		t.Fatal("SendMessage(canceled) error = nil")
	}
	if transportCalls > 1 {
		t.Fatalf("transport calls = %d, want at most 1", transportCalls)
	}
}

func TestClientRejectsInvalidInputsWithoutTransport(t *testing.T) {
	if _, err := New("", time.Second); err == nil {
		t.Fatal("New(empty token) error = nil")
	}
	if _, err := New(testBotToken, 0); err == nil {
		t.Fatal("New(zero timeout) error = nil")
	}
	if _, err := NewForTest("http://example.com", testBotToken, &http.Client{}); err == nil {
		t.Fatal("NewForTest(remote HTTP) error = nil")
	}

	transportCalls := 0
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		transportCalls++
		return nil, errors.New("unexpected transport")
	})}
	client, err := NewForTest("https://api.telegram.org", testBotToken, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	operations := []func() error{
		func() error { return client.SendMessage(context.Background(), 0, "message") },
		func() error { return client.SendMessage(context.Background(), 101, "") },
		func() error { return client.SendMessage(context.Background(), 101, strings.Repeat("x", (64<<10)+1)) },
		func() error {
			return client.SetWebhook(context.Background(), "http://localhost:8080", "Webhook_Secret-1")
		},
		func() error { return client.SetWebhook(context.Background(), "https://monitor.example.com", "") },
	}
	for index, operation := range operations {
		if err := operation(); err == nil {
			t.Fatalf("invalid operation %d error = nil", index)
		}
	}
	if transportCalls != 0 {
		t.Fatalf("transport calls = %d, want 0", transportCalls)
	}
}
