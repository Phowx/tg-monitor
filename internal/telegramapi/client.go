package telegramapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/config"
)

const (
	productionBaseURL = "https://api.telegram.org"
	maxRequestBytes   = 64 << 10
	maxResponseBytes  = 64 << 10
)

type Client struct {
	baseURL    *url.URL
	botToken   string
	httpClient *http.Client
}

type sentMessageResult struct {
	MessageID int64 `json:"message_id"`
}

type webAppButtonRequest struct {
	ChatID      int64                `json:"chat_id"`
	Text        string               `json:"text"`
	ReplyMarkup inlineKeyboardMarkup `json:"reply_markup"`
}

type inlineKeyboardMarkup struct {
	InlineKeyboard [][]inlineKeyboardButton `json:"inline_keyboard"`
}

type inlineKeyboardButton struct {
	Text   string       `json:"text"`
	WebApp webAppTarget `json:"web_app"`
}

type webAppTarget struct {
	URL string `json:"url"`
}

func New(botToken string, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		return nil, errors.New("create Telegram client: timeout must be positive")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return newClient(productionBaseURL, botToken, httpClient)
}

func NewForTest(baseURL, botToken string, httpClient *http.Client) (*Client, error) {
	if httpClient == nil {
		return nil, errors.New("create Telegram client: HTTP client is required")
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return newClient(baseURL, botToken, &clientCopy)
}

func newClient(baseURL, botToken string, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(botToken) == "" {
		return nil, errors.New("create Telegram client: bot token is required")
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Opaque != "" || parsed.Host == "" {
		return nil, errors.New("create Telegram client: API base URL is invalid")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("create Telegram client: API base URL must be an origin")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && !(scheme == "http" && loopbackHost(parsed.Hostname())) {
		return nil, errors.New("create Telegram client: API base URL must use HTTPS")
	}
	parsed.Scheme = scheme
	parsed.Path = ""
	parsed.RawPath = ""
	return &Client{baseURL: parsed, botToken: strings.TrimSpace(botToken), httpClient: httpClient}, nil
}

func (client *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	if chatID <= 0 {
		return errors.New("telegram sendMessage: chat ID must be positive")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("telegram sendMessage: text is required")
	}
	var sent sentMessageResult
	if err := client.call(ctx, "sendMessage", struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}{ChatID: chatID, Text: text}, &sent); err != nil {
		return err
	}
	if sent.MessageID <= 0 {
		return errors.New("telegram sendMessage: rejected response")
	}
	return nil
}

func (client *Client) SendWebAppButton(ctx context.Context, chatID int64, text, buttonText, webAppURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(webAppURL))
	secure := err == nil && parsed.Scheme == "https"
	loopbackHTTP := err == nil && parsed.Scheme == "http" && loopbackHost(parsed.Hostname())
	if chatID <= 0 || strings.TrimSpace(text) == "" || strings.TrimSpace(buttonText) == "" || err != nil || parsed.Opaque != "" || (!secure && !loopbackHTTP) || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("telegram sendMessage web app: invalid input")
	}
	request := webAppButtonRequest{
		ChatID: chatID,
		Text:   text,
		ReplyMarkup: inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{{{
			Text: buttonText,
			WebApp: webAppTarget{
				URL: parsed.String(),
			},
		}}}},
	}
	var sent sentMessageResult
	if err := client.call(ctx, "sendMessage", request, &sent); err != nil {
		return err
	}
	if sent.MessageID <= 0 {
		return errors.New("telegram sendMessage web app: rejected response")
	}
	return nil
}

func (client *Client) SetWebhook(ctx context.Context, publicURL, secret string) error {
	if err := config.ValidateWebhookPublicURL(publicURL); err != nil {
		return errors.New("telegram setWebhook: public URL must use HTTPS")
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("telegram setWebhook: secret token is required")
	}
	webhookURL := strings.TrimSuffix(strings.TrimSpace(publicURL), "/") + "/telegram/webhook"
	var accepted bool
	if err := client.call(ctx, "setWebhook", struct {
		URL            string   `json:"url"`
		SecretToken    string   `json:"secret_token"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{URL: webhookURL, SecretToken: secret, AllowedUpdates: []string{"message"}}, &accepted); err != nil {
		return err
	}
	if !accepted {
		return errors.New("telegram setWebhook: rejected response")
	}
	return nil
}

func (client *Client) GetWebhookInfo(ctx context.Context) (WebhookInfo, error) {
	var info WebhookInfo
	if err := client.call(ctx, "getWebhookInfo", struct{}{}, &info); err != nil {
		return WebhookInfo{}, err
	}
	return info, nil
}

func (client *Client) DeleteWebhook(ctx context.Context) error {
	var accepted bool
	if err := client.call(ctx, "deleteWebhook", struct{}{}, &accepted); err != nil {
		return err
	}
	if !accepted {
		return errors.New("telegram deleteWebhook: rejected response")
	}
	return nil
}

func (client *Client) call(ctx context.Context, method string, requestValue, result any) error {
	body, err := json.Marshal(requestValue)
	if err != nil {
		return fmt.Errorf("telegram %s: encode request", method)
	}
	if len(body) > maxRequestBytes {
		return fmt.Errorf("telegram %s: request too large", method)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.methodURL(method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: build request", method)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("telegram %s: transport failure", method)
	}
	defer response.Body.Close()

	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(encoded) > maxResponseBytes {
		return fmt.Errorf("telegram %s: invalid response", method)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("telegram %s: HTTP failure", method)
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil || !envelope.OK || len(envelope.Result) == 0 {
		return fmt.Errorf("telegram %s: rejected response", method)
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("telegram %s: invalid result", method)
	}
	return nil
}

func (client *Client) methodURL(method string) string {
	endpoint := *client.baseURL
	endpoint.Path = "/bot" + client.botToken + "/" + method
	return endpoint.String()
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
