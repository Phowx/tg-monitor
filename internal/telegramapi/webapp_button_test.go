package telegramapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestClientSendWebAppButtonRequestShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.EscapedPath()
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
	if err := client.SendWebAppButton(context.Background(), 101, "Open the operator app.", "Open tg-monitor", "https://monitor.example.com/app/"); err != nil {
		t.Fatalf("SendWebAppButton() error = %v", err)
	}

	if gotPath != "/bot123456:canary-bot-token/sendMessage" {
		t.Fatalf("path = %q", gotPath)
	}
	wantBody := map[string]any{
		"chat_id": float64(101),
		"text":    "Open the operator app.",
		"reply_markup": map[string]any{
			"inline_keyboard": []any{
				[]any{
					map[string]any{
						"text": "Open tg-monitor",
						"web_app": map[string]any{
							"url": "https://monitor.example.com/app/",
						},
					},
				},
			},
		},
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Fatalf("body = %#v, want %#v", gotBody, wantBody)
	}
	if _, exists := gotBody["parse_mode"]; exists {
		t.Fatal("sendMessage web app unexpectedly set parse_mode")
	}
}

func TestClientSendWebAppButtonRejectsInvalidInputsWithoutTransport(t *testing.T) {
	transportCalls := 0
	client, err := NewForTest("https://api.telegram.org", testBotToken, &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		transportCalls++
		return nil, errors.New("unexpected transport")
	})})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		chatID     int64
		text       string
		buttonText string
		webAppURL  string
	}{
		{name: "chat", chatID: 0, text: "open", buttonText: "button", webAppURL: "https://monitor.example.com/app/"},
		{name: "message", chatID: 101, text: " ", buttonText: "button", webAppURL: "https://monitor.example.com/app/"},
		{name: "button", chatID: 101, text: "open", buttonText: " ", webAppURL: "https://monitor.example.com/app/"},
		{name: "remote HTTP", chatID: 101, text: "open", buttonText: "button", webAppURL: "http://monitor.example.com/app/"},
		{name: "userinfo", chatID: 101, text: "open", buttonText: "button", webAppURL: "https://user@monitor.example.com/app/"},
		{name: "fragment", chatID: 101, text: "open", buttonText: "button", webAppURL: "https://monitor.example.com/app/#secret"},
		{name: "oversized text", chatID: 101, text: strings.Repeat("x", (64<<10)+1), buttonText: "button", webAppURL: "https://monitor.example.com/app/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := client.SendWebAppButton(context.Background(), test.chatID, test.text, test.buttonText, test.webAppURL); err == nil {
				t.Fatal("SendWebAppButton() error = nil")
			}
		})
	}
	if transportCalls != 0 {
		t.Fatalf("transport calls = %d, want 0", transportCalls)
	}
}
