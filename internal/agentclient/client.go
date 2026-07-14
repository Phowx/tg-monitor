package agentclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const (
	userAgent            = "tg-monitor-agent/1"
	maxResponseBodyBytes = 64 << 10
)

var (
	ErrPermanent = errors.New("permanent agent delivery failure")
	ErrRetryable = errors.New("retryable agent delivery failure")
)

type Client struct {
	endpointURL string
	token       string
	httpClient  *http.Client
}

func New(endpointURL, token string, timeout time.Duration) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	return newClient(endpointURL, token, timeout, transport)
}

func newClient(endpointURL, token string, timeout time.Duration, transport http.RoundTripper) (*Client, error) {
	if strings.TrimSpace(endpointURL) == "" {
		return nil, errors.New("agent endpoint URL must be non-empty")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("agent token must be non-empty")
	}
	if timeout <= 0 {
		return nil, errors.New("agent HTTP timeout must be positive")
	}
	if transport == nil {
		return nil, errors.New("agent HTTP transport must be non-nil")
	}
	return &Client{
		endpointURL: endpointURL,
		token:       token,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (c *Client) Send(ctx context.Context, report domain.MetricReport) error {
	if ctx == nil {
		return errors.New("send context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := report.Validate(); err != nil {
		return fmt.Errorf("%w: invalid metric report", ErrPermanent)
	}
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("%w: encode metric report", ErrPermanent)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpointURL, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("%w: create request", ErrPermanent)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", userAgent)

	response, err := c.httpClient.Do(request)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("%w: request failed", ErrRetryable)
	}
	if response.Body == nil {
		return fmt.Errorf("%w: response body is missing", ErrRetryable)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBodyBytes))
	_ = response.Body.Close()

	return classifyStatus(response.StatusCode)
}

func classifyStatus(status int) error {
	if status == http.StatusNoContent {
		return nil
	}
	switch status {
	case http.StatusRequestTimeout,
		http.StatusConflict,
		http.StatusTooEarly,
		http.StatusTooManyRequests:
		return fmt.Errorf("%w: HTTP status %d", ErrRetryable, status)
	}
	if status >= 500 && status <= 599 {
		return fmt.Errorf("%w: HTTP status %d", ErrRetryable, status)
	}
	return fmt.Errorf("%w: HTTP status %d", ErrPermanent, status)
}
