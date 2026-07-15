package serverapp

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/config"
	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/storage/sqlite"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func testRuntimeConfig(databasePath string, checkpoint time.Duration) config.ApplicationRuntimeConfig {
	return config.ApplicationRuntimeConfig{
		Server: config.ServerRuntimeConfig{
			DatabasePath:       databasePath,
			ListenAddr:         "127.0.0.1:0",
			CheckpointInterval: checkpoint,
		},
	}
}

func TestNewConfiguresDefensiveHTTPServer(t *testing.T) {
	app, err := New(context.Background(), testRuntimeConfig(filepath.Join(t.TempDir(), "monitor.db"), 10*time.Millisecond), testLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	if app.server.ReadHeaderTimeout != 5*time.Second || app.server.ReadTimeout != 15*time.Second || app.server.WriteTimeout != 15*time.Second || app.server.IdleTimeout != 60*time.Second || app.server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("HTTP server timeouts/limits = read-header %v read %v write %v idle %v headers %d",
			app.server.ReadHeaderTimeout, app.server.ReadTimeout, app.server.WriteTimeout, app.server.IdleTimeout, app.server.MaxHeaderBytes)
	}
}

func TestAppServeExposesHealthAndReadinessAndStopsOnCancellation(t *testing.T) {
	app, err := New(context.Background(), testRuntimeConfig(filepath.Join(t.TempDir(), "monitor.db"), 10*time.Millisecond), testLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx, listener) }()

	client := &http.Client{Timeout: time.Second}
	for _, path := range []string{"/healthz", "/readyz"} {
		url := "http://" + listener.Addr().String() + path
		deadline := time.Now().Add(time.Second)
		for {
			response, requestErr := client.Get(url)
			if requestErr == nil {
				response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("GET %s status = %d, want 200", path, response.StatusCode)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("GET %s did not become ready: %v", path, requestErr)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	for _, path := range []string{
		"/telegram/webhook",
		"/api/v1/auth/telegram",
		"/api/v1/auth/session",
		"/api/v1/auth/logout",
	} {
		response, err := client.Get("http://" + listener.Addr().String() + path)
		if err != nil {
			t.Fatalf("GET core-only %s error = %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("GET core-only %s status = %d, want 404", path, response.StatusCode)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not stop after cancellation")
	}
	if err := app.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestAppServeCanceledContextPersistsFinalCheckpoint(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "monitor.db")
	app, err := New(context.Background(), testRuntimeConfig(databasePath, time.Hour), testLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server, err := app.store.CreateServer(context.Background(), domain.Server{
		Name:        "checkpoint-server",
		Enabled:     true,
		TokenSHA256: make([]byte, 32),
	})
	if err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	report := domain.MetricReport{
		CapturedAtMS:            120_001,
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
		System:                  domain.SystemInfo{Hostname: "host", OS: "linux", Kernel: "6.12", Arch: "amd64"},
	}
	if err := app.monitor.Ingest(context.Background(), server.ID, 130_000, report); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.Serve(ctx, listener); err != nil {
		t.Fatalf("Serve(canceled) error = %v", err)
	}

	reopened, err := sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("sqlite.Open(reopen) error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	samples, err := reopened.QueryMinuteSamples(context.Background(), server.ID, 120_000, 180_000)
	if err != nil {
		t.Fatalf("QueryMinuteSamples() error = %v", err)
	}
	if len(samples) != 1 || samples[0].BucketMS != 120_000 || samples[0].CPUPct != 25 {
		t.Fatalf("final checkpoint samples = %#v", samples)
	}
}
