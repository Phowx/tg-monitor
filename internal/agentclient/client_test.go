package agentclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const canaryToken = "canary-agent-token"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type trackingBody struct {
	reader io.Reader
	closed bool
	read   int
}

func (body *trackingBody) Read(buffer []byte) (int, error) {
	count, err := body.reader.Read(buffer)
	body.read += count
	return count, err
}

func (body *trackingBody) Close() error {
	body.closed = true
	return nil
}

func validReport() domain.MetricReport {
	return domain.MetricReport{
		CapturedAtMS:            1_700_000_000_000,
		CPUPct:                  12.5,
		MemoryTotalBytes:        1_000,
		MemoryUsedBytes:         500,
		RootDiskTotalBytes:      2_000,
		RootDiskUsedBytes:       1_000,
		Load1:                   0.1,
		Load5:                   0.2,
		Load15:                  0.3,
		NetworkRXTotalBytes:     3_000,
		NetworkTXTotalBytes:     4_000,
		NetworkRXBytesPerSecond: 30,
		NetworkTXBytesPerSecond: 40,
		UptimeSeconds:           3_600,
		System: domain.SystemInfo{
			Hostname: "canary-report-hostname",
			OS:       "linux",
			Kernel:   "6.12",
			Arch:     "amd64",
		},
	}
}

func TestClientSendsAuthenticatedMetricReport(t *testing.T) {
	wantReport := validReport()
	called := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called++
		if request.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", request.Method)
		}
		if request.URL.String() != "https://monitor.example/api/v1/metrics" {
			t.Errorf("URL = %q", request.URL.String())
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+canaryToken {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("User-Agent = %q, want %q", got, userAgent)
		}
		var report domain.MetricReport
		if err := json.NewDecoder(request.Body).Decode(&report); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if report != wantReport {
			t.Fatalf("report = %#v, want %#v", report, wantReport)
		}
		return response(http.StatusNoContent, io.NopCloser(strings.NewReader(""))), nil
	})

	client, err := newClient("https://monitor.example/api/v1/metrics", canaryToken, 7*time.Second, transport)
	if err != nil {
		t.Fatal(err)
	}
	if client.httpClient.Timeout != 7*time.Second {
		t.Fatalf("timeout = %v", client.httpClient.Timeout)
	}
	if err := client.Send(context.Background(), wantReport); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if called != 1 {
		t.Fatalf("transport calls = %d, want 1", called)
	}
}

func TestClientClassifiesHTTPStatuses(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{204, nil}, {200, ErrPermanent}, {301, ErrPermanent},
		{400, ErrPermanent}, {401, ErrPermanent}, {403, ErrPermanent},
		{404, ErrPermanent}, {405, ErrPermanent}, {408, ErrRetryable},
		{409, ErrRetryable}, {413, ErrPermanent}, {415, ErrPermanent},
		{422, ErrPermanent}, {425, ErrRetryable}, {429, ErrRetryable},
		{500, ErrRetryable}, {503, ErrRetryable},
	}
	for _, test := range tests {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			client, err := newClient("https://monitor.example/api/v1/metrics", canaryToken, time.Second, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(test.status, io.NopCloser(strings.NewReader("response-canary-secret"))), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			err = client.Send(context.Background(), validReport())
			if test.want == nil && err != nil {
				t.Fatalf("Send() error = %v, want nil", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("Send() error = %v, want %v", err, test.want)
			}
			assertNoCanaries(t, err)
		})
	}
}

func TestClientRejectsInvalidReportWithoutRequest(t *testing.T) {
	called := false
	client, err := newClient("https://monitor.example/api/v1/metrics", canaryToken, time.Second, roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected request")
	}))
	if err != nil {
		t.Fatal(err)
	}
	report := validReport()
	report.MemoryTotalBytes = 0
	if err := client.Send(context.Background(), report); err == nil {
		t.Fatal("Send() error = nil, want validation error")
	} else {
		assertNoCanaries(t, err)
	}
	if called {
		t.Fatal("transport was called for invalid report")
	}
}

func TestClientTreatsNetworkErrorsAsRetryable(t *testing.T) {
	client, err := newClient("https://monitor.example/api/v1/metrics", canaryToken, time.Second, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network-response-canary")
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = client.Send(context.Background(), validReport())
	if !errors.Is(err, ErrRetryable) {
		t.Fatalf("Send() error = %v, want ErrRetryable", err)
	}
	assertNoCanaries(t, err)
}

func TestClientPreservesCancellation(t *testing.T) {
	called := false
	client, err := newClient("https://monitor.example/api/v1/metrics", canaryToken, time.Second, roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected request")
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Send(ctx, validReport()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send() error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("transport called for canceled context")
	}
}

func TestClientClosesAndBoundsResponseBody(t *testing.T) {
	body := &trackingBody{reader: strings.NewReader(strings.Repeat("response-canary-secret", 10_000))}
	client, err := newClient("https://monitor.example/api/v1/metrics", canaryToken, time.Second, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusServiceUnavailable, body), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = client.Send(context.Background(), validReport())
	if !errors.Is(err, ErrRetryable) {
		t.Fatalf("Send() error = %v, want ErrRetryable", err)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
	if body.read > maxResponseBodyBytes {
		t.Fatalf("response bytes read = %d, want at most %d", body.read, maxResponseBodyBytes)
	}
	assertNoCanaries(t, err)
}

func TestClientRefusesRedirects(t *testing.T) {
	secondCalls := 0
	second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		secondCalls++
	}))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, second.URL, http.StatusFound)
	}))
	defer first.Close()

	client, err := newClient(first.URL, canaryToken, time.Second, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), validReport()); !errors.Is(err, ErrPermanent) {
		t.Fatalf("Send() error = %v, want ErrPermanent", err)
	}
	if secondCalls != 0 {
		t.Fatalf("redirect target calls = %d, want 0", secondCalls)
	}
}

func TestClientProductionTransportRequiresTLS12(t *testing.T) {
	client, err := New("https://monitor.example/api/v1/metrics", canaryToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.httpClient.Transport)
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS config = %#v, want TLS 1.2 minimum", transport.TLSClientConfig)
	}
}

func TestClientRejectsInvalidConstruction(t *testing.T) {
	for _, test := range []struct {
		name     string
		endpoint string
		token    string
		timeout  time.Duration
	}{
		{name: "empty endpoint", token: canaryToken, timeout: time.Second},
		{name: "empty token", endpoint: "https://monitor.example/api/v1/metrics", timeout: time.Second},
		{name: "zero timeout", endpoint: "https://monitor.example/api/v1/metrics", token: canaryToken},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newClient(test.endpoint, test.token, test.timeout, http.DefaultTransport)
			if err == nil {
				t.Fatal("newClient() error = nil")
			}
			assertNoCanaries(t, err)
		})
	}
}

func response(status int, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: body}
}

func assertNoCanaries(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	text := err.Error()
	for _, forbidden := range []string{canaryToken, "Authorization", "canary-report-hostname", "response-canary-secret", "network-response-canary"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("error leaked %q: %q", forbidden, text)
		}
	}
}
