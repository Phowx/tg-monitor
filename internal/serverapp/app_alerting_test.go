package serverapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/telegrambot"
)

type appAlertSend struct {
	telegramUserID int64
	text           string
}

type lifecycleAlertSender struct {
	started  chan appAlertSend
	canceled chan struct{}
	once     sync.Once
}

func (sender *lifecycleAlertSender) SendMessage(ctx context.Context, telegramUserID int64, text string) error {
	select {
	case sender.started <- appAlertSend{telegramUserID: telegramUserID, text: text}:
	case <-ctx.Done():
		return ctx.Err()
	}
	<-ctx.Done()
	sender.once.Do(func() { close(sender.canceled) })
	return ctx.Err()
}

func (*lifecycleAlertSender) SendWebAppButton(context.Context, int64, string, string, string) error {
	return errors.New("unexpected Web App send")
}

func TestAppServeRunsAlertWorkerAndWaitsForCancellation(t *testing.T) {
	now := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	sender := &lifecycleAlertSender{
		started:  make(chan appAlertSend, 1),
		canceled: make(chan struct{}),
	}
	cfg := telegramApplicationConfig(filepath.Join(t.TempDir(), "monitor.db"))
	app, err := newWithDependencies(context.Background(), cfg, testLogger(), appDependencies{
		newTelegramSender: func(string, time.Duration) (telegrambot.Sender, error) { return sender, nil },
		random:            bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)),
		now:               func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("newWithDependencies() error = %v", err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	if err := app.store.UpdateSettings(context.Background(), domain.Settings{
		OfflineThresholdSeconds: 1,
		AlertThresholdSeconds:   1,
		HistoryRetentionDays:    30,
	}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	server, err := app.store.CreateServer(context.Background(), domain.Server{
		Name:        "alert-lifecycle-node",
		Group:       "integration",
		Enabled:     true,
		TokenSHA256: make([]byte, sha256.Size),
	})
	if err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	stale := now.Add(-2 * time.Second)
	if err := app.monitor.Ingest(context.Background(), server.ID, stale.UnixMilli(), validApplicationMetricReport(stale.UnixMilli())); err != nil {
		t.Fatalf("Ingest(stale) error = %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
	})

	select {
	case sent := <-sender.started:
		if sent.telegramUserID != 42 || !strings.Contains(sent.text, "alert-lifecycle-node 已离线") {
			t.Fatalf("alert send = %#v", sent)
		}
	case <-time.After(time.Second):
		t.Fatal("alert worker did not dispatch the startup alert")
	}

	cancel()
	select {
	case <-sender.canceled:
	case <-time.After(time.Second):
		t.Fatal("alert sender did not observe worker cancellation")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not wait for alert worker shutdown")
	}
}
