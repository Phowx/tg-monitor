package telegramapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSendWebAppButtonAllowsLoopbackHTTPForDevelopment(t *testing.T) {
	var gotURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			ReplyMarkup struct {
				InlineKeyboard [][]struct {
					WebApp struct {
						URL string `json:"url"`
					} `json:"web_app"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotURL = body.ReplyMarkup.InlineKeyboard[0][0].WebApp.URL
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(server.Close)

	client, err := NewForTest(server.URL, testBotToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SendWebAppButton(context.Background(), 101, "open", "button", "http://127.0.0.1:8080/app/"); err != nil {
		t.Fatalf("SendWebAppButton() error = %v", err)
	}
	if gotURL != "http://127.0.0.1:8080/app/" {
		t.Fatalf("web_app URL = %q", gotURL)
	}
}
