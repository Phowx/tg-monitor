package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestSettingsRejectAlertThresholdBeforeOfflineThreshold(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	wantStored := domain.Settings{OfflineThresholdSeconds: 60, AlertThresholdSeconds: 120, HistoryRetentionDays: 7}

	err := store.UpdateSettings(ctx, domain.Settings{
		OfflineThresholdSeconds: 120,
		AlertThresholdSeconds:   60,
		HistoryRetentionDays:    7,
	})
	if err == nil || !strings.Contains(err.Error(), "alert threshold") {
		t.Fatalf("UpdateSettings() error = %v, want safe alert threshold error", err)
	}
	got, getErr := store.GetSettings(ctx)
	if getErr != nil || got != wantStored {
		t.Fatalf("GetSettings() = %#v, error %v, want unchanged %#v", got, getErr, wantStored)
	}
}
