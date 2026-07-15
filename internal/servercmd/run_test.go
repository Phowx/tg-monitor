package servercmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/config"
	"github.com/tg-monitor/tg-monitor/internal/domain"
)

type commandStoreStub struct {
	created         domain.Server
	createResult    domain.Server
	servers         []domain.Server
	rotatedID       int64
	rotatedHash     []byte
	latestID        int64
	latest          domain.LatestMetrics
	historyServerID int64
	historyFromMS   int64
	historyToMS     int64
	history         []domain.MinuteSample
	closeCalls      int
	createErr       error
	listErr         error
	rotateErr       error
	latestErr       error
	historyErr      error
	closeErr        error
}

func (store *commandStoreStub) Close() error {
	store.closeCalls++
	return store.closeErr
}

func (store *commandStoreStub) CreateServer(_ context.Context, server domain.Server) (domain.Server, error) {
	store.created = server
	if store.createErr != nil {
		return domain.Server{}, store.createErr
	}
	result := store.createResult
	if result.ID == 0 {
		result = server
		result.ID = 10
	}
	return result, nil
}

func (store *commandStoreStub) ListServers(context.Context) ([]domain.Server, error) {
	return store.servers, store.listErr
}

func (store *commandStoreStub) UpdateServerTokenHash(_ context.Context, id int64, hash []byte) error {
	store.rotatedID = id
	store.rotatedHash = append([]byte(nil), hash...)
	return store.rotateErr
}

func (store *commandStoreStub) GetLatestMetrics(_ context.Context, serverID int64) (domain.LatestMetrics, error) {
	store.latestID = serverID
	return store.latest, store.latestErr
}

func (store *commandStoreStub) QueryMinuteSamples(_ context.Context, serverID, fromMS, toMS int64) ([]domain.MinuteSample, error) {
	store.historyServerID = serverID
	store.historyFromMS = fromMS
	store.historyToMS = toMS
	return store.history, store.historyErr
}

type commandHarness struct {
	stdout            bytes.Buffer
	stderr            bytes.Buffer
	store             *commandStoreStub
	openCalls         int
	openedPath        string
	serveCalls        int
	servedConfig      config.ApplicationRuntimeConfig
	serveLogger       *slog.Logger
	serveErr          error
	serverConfig      config.ServerRuntimeConfig
	applicationConfig config.ApplicationRuntimeConfig
}

func newCommandHarness() *commandHarness {
	harness := &commandHarness{
		store: &commandStoreStub{},
		serverConfig: config.ServerRuntimeConfig{
			DatabasePath:       "/tmp/test-monitor.db",
			ListenAddr:         "127.0.0.1:9090",
			CheckpointInterval: 25 * time.Second,
		},
	}
	harness.applicationConfig = config.ApplicationRuntimeConfig{Server: harness.serverConfig}
	return harness
}

func (harness *commandHarness) dependencies(randomByte byte) Dependencies {
	return Dependencies{
		Stdout: harnessOutput{buffer: &harness.stdout},
		Stderr: &harness.stderr,
		Random: bytes.NewReader(bytes.Repeat([]byte{randomByte}, 64)),
		LoadServerConfig: func() (config.ServerRuntimeConfig, error) {
			return harness.serverConfig, nil
		},
		LoadApplicationConfig: func() (config.ApplicationRuntimeConfig, error) {
			return harness.applicationConfig, nil
		},
		OpenStore: func(_ context.Context, path string) (Store, error) {
			harness.openCalls++
			harness.openedPath = path
			return harness.store, nil
		},
		Serve: func(_ context.Context, cfg config.ApplicationRuntimeConfig, logger *slog.Logger) error {
			harness.serveCalls++
			harness.servedConfig = cfg
			harness.serveLogger = logger
			return harness.serveErr
		},
	}
}

type harnessOutput struct {
	buffer *bytes.Buffer
}

func (output harnessOutput) Write(value []byte) (int, error) {
	return output.buffer.Write(value)
}

func TestServerAddStoresOnlyHashAndPrintsTokenOnce(t *testing.T) {
	harness := newCommandHarness()
	if err := Run(context.Background(), []string{"server", "add", "--name", "node-1", "--group", "prod", "--sort-order", "3"}, harness.dependencies(0x2a)); err != nil {
		t.Fatalf("Run(server add) error = %v", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x2a}, 32))
	if harness.store.created.Name != "node-1" || harness.store.created.Group != "prod" || harness.store.created.SortOrder != 3 || !harness.store.created.Enabled {
		t.Fatalf("created server = %#v", harness.store.created)
	}
	if !bytes.Equal(harness.store.created.TokenSHA256, auth.HashToken(raw)) {
		t.Fatalf("stored hash = %x, want %x", harness.store.created.TokenSHA256, auth.HashToken(raw))
	}
	output := harness.stdout.String()
	if !strings.Contains(output, "server_id=10\n") || !strings.Contains(output, "agent_token="+raw+"\n") || strings.Count(output, raw) != 1 {
		t.Fatalf("stdout = %q, want one server ID and one raw token", output)
	}
	if harness.openedPath != harness.serverConfig.DatabasePath || harness.store.closeCalls != 1 {
		t.Fatalf("store path/close = %q/%d", harness.openedPath, harness.store.closeCalls)
	}
}

func TestServerListEmitsMetadataWithoutTokenMaterial(t *testing.T) {
	harness := newCommandHarness()
	raw := "raw-token-must-not-appear"
	harness.store.servers = []domain.Server{{ID: 1, Name: "node", Enabled: true, TokenSHA256: auth.HashToken(raw), CreatedAtMS: 100, UpdatedAtMS: 200}}
	if err := Run(context.Background(), []string{"server", "list"}, harness.dependencies(0x01)); err != nil {
		t.Fatalf("Run(server list) error = %v", err)
	}
	output := harness.stdout.String()
	for _, required := range []string{`"id": 1`, `"name": "node"`, `"enabled": true`} {
		if !strings.Contains(output, required) {
			t.Fatalf("stdout %q missing %q", output, required)
		}
	}
	for _, forbidden := range []string{raw, "TokenSHA256", "token_sha256"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("stdout leaked %q: %s", forbidden, output)
		}
	}
}

func TestServerRotateTokenUpdatesHashAndPrintsTokenOnce(t *testing.T) {
	harness := newCommandHarness()
	if err := Run(context.Background(), []string{"server", "rotate-token", "--id", "9"}, harness.dependencies(0x4b)); err != nil {
		t.Fatalf("Run(server rotate-token) error = %v", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x4b}, 32))
	if harness.store.rotatedID != 9 || !bytes.Equal(harness.store.rotatedHash, auth.HashToken(raw)) {
		t.Fatalf("rotation = ID %d hash %x", harness.store.rotatedID, harness.store.rotatedHash)
	}
	if output := harness.stdout.String(); strings.Count(output, raw) != 1 || !strings.Contains(output, "server_id=9\n") {
		t.Fatalf("stdout = %q, want token exactly once", output)
	}
}

func TestMetricsLatestAndHistoryEmitRepositoryResults(t *testing.T) {
	t.Run("latest", func(t *testing.T) {
		harness := newCommandHarness()
		harness.store.latest = domain.LatestMetrics{ServerID: 7, ReceivedAtMS: 200, Report: commandMetricReport(120_001)}
		if err := Run(context.Background(), []string{"metrics", "latest", "--server-id", "7"}, harness.dependencies(0x01)); err != nil {
			t.Fatalf("Run(metrics latest) error = %v", err)
		}
		if harness.store.latestID != 7 || !strings.Contains(harness.stdout.String(), `"server_id": 7`) {
			t.Fatalf("latest ID/output = %d/%q", harness.store.latestID, harness.stdout.String())
		}
	})

	t.Run("history", func(t *testing.T) {
		harness := newCommandHarness()
		harness.store.history = []domain.MinuteSample{{ServerID: 7, BucketMS: 120_000}, {ServerID: 7, BucketMS: 180_000}}
		args := []string{"metrics", "history", "--server-id", "7", "--from-ms", "120000", "--to-ms", "240000"}
		if err := Run(context.Background(), args, harness.dependencies(0x01)); err != nil {
			t.Fatalf("Run(metrics history) error = %v", err)
		}
		if harness.store.historyServerID != 7 || harness.store.historyFromMS != 120_000 || harness.store.historyToMS != 240_000 {
			t.Fatalf("history bounds = server %d [%d,%d)", harness.store.historyServerID, harness.store.historyFromMS, harness.store.historyToMS)
		}
		output := harness.stdout.String()
		if strings.Index(output, `"bucket": 120000`) > strings.Index(output, `"bucket": 180000`) {
			t.Fatalf("history output is not ascending: %s", output)
		}
	})
}

func TestServeLoadsFocusedConfigWithoutOpeningCommandStore(t *testing.T) {
	harness := newCommandHarness()
	if err := Run(context.Background(), []string{"serve"}, harness.dependencies(0x01)); err != nil {
		t.Fatalf("Run(serve) error = %v", err)
	}
	if harness.serveCalls != 1 || harness.servedConfig != harness.applicationConfig || harness.serveLogger == nil {
		t.Fatalf("serve calls/config/logger = %d/%#v/%v", harness.serveCalls, harness.servedConfig, harness.serveLogger)
	}
	if harness.openCalls != 0 {
		t.Fatalf("OpenStore() calls = %d, want 0", harness.openCalls)
	}
}

func TestInvalidCommandsFailBeforeOpeningStore(t *testing.T) {
	tests := [][]string{
		nil,
		{"unknown"},
		{"server"},
		{"server", "add"},
		{"server", "list", "extra"},
		{"server", "rotate-token", "--id", "0"},
		{"metrics", "latest", "--server-id", "0"},
		{"metrics", "history", "--server-id", "1", "--from-ms", "200", "--to-ms", "100"},
	}
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			harness := newCommandHarness()
			if err := Run(context.Background(), args, harness.dependencies(0x01)); err == nil {
				t.Fatalf("Run(%v) error = nil", args)
			}
			if harness.openCalls != 0 || harness.serveCalls != 0 {
				t.Fatalf("invalid command invoked dependencies: open=%d serve=%d", harness.openCalls, harness.serveCalls)
			}
		})
	}
}

func TestRunPropagatesOperationAndCloseErrors(t *testing.T) {
	harness := newCommandHarness()
	operationErr := errors.New("list failed")
	closeErr := errors.New("close failed")
	harness.store.listErr = operationErr
	harness.store.closeErr = closeErr
	err := Run(context.Background(), []string{"server", "list"}, harness.dependencies(0x01))
	if !errors.Is(err, operationErr) || !errors.Is(err, closeErr) {
		t.Fatalf("Run() error = %v, want operation and close errors", err)
	}
}

func commandMetricReport(capturedAtMS int64) domain.MetricReport {
	return domain.MetricReport{
		CapturedAtMS: capturedAtMS, CPUPct: 20,
		MemoryTotalBytes: 16_000, MemoryUsedBytes: 8_000,
		RootDiskTotalBytes: 100_000, RootDiskUsedBytes: 40_000,
		Load1: 0.1, Load5: 0.2, Load15: 0.3,
		NetworkRXTotalBytes: 1_000, NetworkTXTotalBytes: 2_000,
		NetworkRXBytesPerSecond: 10, NetworkTXBytesPerSecond: 20,
		UptimeSeconds: 3_600,
		System:        domain.SystemInfo{Hostname: "host", OS: "linux", Kernel: "6.12", Arch: "amd64"},
	}
}
