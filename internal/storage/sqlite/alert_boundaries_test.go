package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestEvaluateAlertsRecordsTransitionsWithoutRecipients(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 1_000 }
	server := createAlertServer(t, store, "no-recipients", "", true)
	if got, err := store.EvaluateAlerts(ctx, 121_000, nil); err != nil || got != (domain.AlertEvaluationResult{Offline: 1}) {
		t.Fatalf("EvaluateAlerts(offline) = %#v, %v", got, err)
	}
	assertAlertState(t, store, server.ID, sql.NullInt64{Int64: 1_000, Valid: true}, sql.NullInt64{Int64: 121_000, Valid: true}, sql.NullInt64{}, 121_000)
	upsertAlertLatest(t, store, server.ID, 130_000, 130_000)
	if got, err := store.EvaluateAlerts(ctx, 130_000, nil); err != nil || got != (domain.AlertEvaluationResult{Recovery: 1}) {
		t.Fatalf("EvaluateAlerts(recovery) = %#v, %v", got, err)
	}
	if rows := readAlertRows(t, store); len(rows) != 0 {
		t.Fatalf("outbox rows = %#v, want none", rows)
	}
}

func TestEvaluateAlertsSilentRecoveryBeforeThresholdAndImmediateLowering(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 1_000 }
	server := createAlertServer(t, store, "threshold", "", true)
	if got, err := store.EvaluateAlerts(ctx, 60_999, []int64{11}); err != nil || got != (domain.AlertEvaluationResult{}) {
		t.Fatalf("EvaluateAlerts(before original threshold) = %#v, %v", got, err)
	}
	upsertAlertLatest(t, store, server.ID, 61_000, 61_000)
	if got, err := store.EvaluateAlerts(ctx, 61_000, []int64{11}); err != nil || got != (domain.AlertEvaluationResult{}) {
		t.Fatalf("EvaluateAlerts(silent recovery) = %#v, %v", got, err)
	}
	if rows := readAlertRows(t, store); len(rows) != 0 {
		t.Fatalf("silent recovery outbox rows = %#v", rows)
	}

	if err := store.UpdateSettings(ctx, domain.Settings{OfflineThresholdSeconds: 60, AlertThresholdSeconds: 60, HistoryRetentionDays: 7}); err != nil {
		t.Fatalf("UpdateSettings(lower threshold) error = %v", err)
	}
	if got, err := store.EvaluateAlerts(ctx, 121_000, []int64{11}); err != nil || got != (domain.AlertEvaluationResult{Offline: 1, Queued: 1}) {
		t.Fatalf("EvaluateAlerts(lowered boundary) = %#v, %v", got, err)
	}
}
