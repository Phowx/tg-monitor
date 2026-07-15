package telegramapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestClientMenuButtonRequestShapes(t *testing.T) {
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot"+testBotToken+"/setChatMenuButton" {
			t.Errorf("path = %q", request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(server.Close)
	client, err := NewForTest(server.URL, testBotToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	if err := client.SetMenuButton(context.Background(), "打开监控面板", "https://monitor.example.com/app/"); err != nil {
		t.Fatalf("SetMenuButton() error = %v", err)
	}
	if err := client.ResetMenuButton(context.Background()); err != nil {
		t.Fatalf("ResetMenuButton() error = %v", err)
	}

	wantSet := map[string]any{"menu_button": map[string]any{
		"type": "web_app", "text": "打开监控面板",
		"web_app": map[string]any{"url": "https://monitor.example.com/app/"},
	}}
	wantReset := map[string]any{"menu_button": map[string]any{"type": "default"}}
	if got := <-requests; !reflect.DeepEqual(got, wantSet) {
		t.Fatalf("set request = %#v, want %#v", got, wantSet)
	}
	if got := <-requests; !reflect.DeepEqual(got, wantReset) {
		t.Fatalf("reset request = %#v, want %#v", got, wantReset)
	}
}

func TestClientSetMenuButtonRejectsInvalidInputWithoutTransport(t *testing.T) {
	transportCalls := 0
	client, err := NewForTest("https://api.telegram.org", testBotToken, &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		transportCalls++
		return nil, errors.New("unexpected transport")
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ text, url string }{
		{"", "https://monitor.example.com/app/"},
		{"打开", "http://monitor.example.com/app/"},
		{"打开", "https://user@monitor.example.com/app/"},
		{"打开", "https://monitor.example.com/app/#secret"},
	} {
		if err := client.SetMenuButton(context.Background(), test.text, test.url); err == nil {
			t.Fatalf("SetMenuButton(%q, %q) error = nil", test.text, test.url)
		}
	}
	if transportCalls != 0 {
		t.Fatalf("transport calls = %d, want 0", transportCalls)
	}
}
