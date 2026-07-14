package serverapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

type retentionRepositoryStub struct {
	settings    domain.Settings
	settingsErr error
	deleteCount int64
	deleteErr   error
	cutoffMS    int64
	deleteCalls int
}

func (stub *retentionRepositoryStub) GetSettings(context.Context) (domain.Settings, error) {
	return stub.settings, stub.settingsErr
}

func (stub *retentionRepositoryStub) DeleteMetricSamplesBefore(_ context.Context, cutoffMS int64) (int64, error) {
	stub.deleteCalls++
	stub.cutoffMS = cutoffMS
	return stub.deleteCount, stub.deleteErr
}

func TestRunRetentionOnceUsesPersistedDaySetting(t *testing.T) {
	repository := &retentionRepositoryStub{
		settings:    domain.Settings{HistoryRetentionDays: 7},
		deleteCount: 42,
	}
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.FixedZone("test", 8*60*60))
	deleted, err := RunRetentionOnce(context.Background(), repository, now)
	if err != nil {
		t.Fatalf("RunRetentionOnce() error = %v", err)
	}
	if deleted != 42 {
		t.Fatalf("RunRetentionOnce() = %d, want 42", deleted)
	}
	wantCutoff := now.UTC().Add(-7 * 24 * time.Hour).UnixMilli()
	if repository.cutoffMS != wantCutoff {
		t.Fatalf("cutoff = %d, want %d", repository.cutoffMS, wantCutoff)
	}
}

func TestRunRetentionOnceRejectsInvalidSetting(t *testing.T) {
	repository := &retentionRepositoryStub{settings: domain.Settings{HistoryRetentionDays: 0}}
	if _, err := RunRetentionOnce(context.Background(), repository, time.Now()); err == nil {
		t.Fatal("RunRetentionOnce() error = nil, want invalid retention error")
	}
	if repository.deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0", repository.deleteCalls)
	}
}

func TestRunRetentionOncePropagatesRepositoryFailures(t *testing.T) {
	settingsErr := errors.New("settings unavailable")
	if _, err := RunRetentionOnce(context.Background(), &retentionRepositoryStub{settingsErr: settingsErr}, time.Now()); !errors.Is(err, settingsErr) {
		t.Fatalf("settings error = %v, want wrapped %v", err, settingsErr)
	}

	deleteErr := errors.New("delete unavailable")
	repository := &retentionRepositoryStub{settings: domain.Settings{HistoryRetentionDays: 7}, deleteErr: deleteErr}
	if _, err := RunRetentionOnce(context.Background(), repository, time.Now()); !errors.Is(err, deleteErr) {
		t.Fatalf("delete error = %v, want wrapped %v", err, deleteErr)
	}
}
