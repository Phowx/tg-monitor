package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

type storedAlertRow struct {
	ID             int64
	ServerID       sql.NullInt64
	TelegramUserID int64
	Kind           domain.AlertKind
	Payload        domain.AlertPayload
	DeliveredAtMS  sql.NullInt64
	CreatedAtMS    int64
	LastError      string
}

func TestEvaluateAlertsFansOutOnceAndRecoversOnceAcrossRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "monitor.db")
	store := openAlertStoreAt(t, databasePath)
	store.nowMS = func() int64 { return 1_000 }
	server := createAlertServer(t, store, "node-1", "production", true)

	if got, err := store.EvaluateAlerts(ctx, 120_999, []int64{22, 11}); err != nil || got != (domain.AlertEvaluationResult{}) {
		t.Fatalf("EvaluateAlerts(before boundary) = %#v, %v", got, err)
	}
	wantOffline := domain.AlertEvaluationResult{Offline: 1, Queued: 2}
	if got, err := store.EvaluateAlerts(ctx, 121_000, []int64{22, 11}); err != nil || got != wantOffline {
		t.Fatalf("EvaluateAlerts(boundary) = %#v, %v, want %#v", got, err, wantOffline)
	}
	assertAlertState(t, store, server.ID, sql.NullInt64{Int64: 1_000, Valid: true}, sql.NullInt64{Int64: 121_000, Valid: true}, sql.NullInt64{}, 121_000)
	rows := readAlertRows(t, store)
	if got := alertRowSummary(rows); !reflect.DeepEqual(got, []string{"11:offline", "22:offline"}) {
		t.Fatalf("offline rows = %v", got)
	}
	for _, row := range rows {
		if row.CreatedAtMS != 121_000 || row.Payload.Version != 1 || row.Payload.ServerName != "node-1" || row.Payload.ServerGroup != "production" || row.Payload.OfflineSinceMS != 1_000 || row.Payload.EventAtMS != 121_000 || row.Payload.RecoveredAtMS != 0 {
			t.Fatalf("offline row = %#v", row)
		}
	}
	if got, err := store.EvaluateAlerts(ctx, 122_000, []int64{11, 22}); err != nil || got != (domain.AlertEvaluationResult{}) {
		t.Fatalf("EvaluateAlerts(repeat) = %#v, %v", got, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	store = openAlertStoreAt(t, databasePath)
	t.Cleanup(func() { _ = store.Close() })
	if got, err := store.EvaluateAlerts(ctx, 123_000, []int64{11, 22}); err != nil || got != (domain.AlertEvaluationResult{}) {
		t.Fatalf("EvaluateAlerts(after restart) = %#v, %v", got, err)
	}
	upsertAlertLatest(t, store, server.ID, 130_000, 999_999)
	wantRecovery := domain.AlertEvaluationResult{Recovery: 1, Queued: 2}
	if got, err := store.EvaluateAlerts(ctx, 130_000, []int64{22, 11}); err != nil || got != wantRecovery {
		t.Fatalf("EvaluateAlerts(recovery) = %#v, %v, want %#v", got, err, wantRecovery)
	}
	assertAlertState(t, store, server.ID, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{Int64: 130_000, Valid: true}, 130_000)
	rows = readAlertRows(t, store)
	if got := alertRowSummary(rows); !reflect.DeepEqual(got, []string{"11:offline", "22:offline", "11:recovery", "22:recovery"}) {
		t.Fatalf("offline/recovery rows = %v", got)
	}
	for _, row := range rows[2:] {
		if row.Payload.OfflineSinceMS != 1_000 || row.Payload.EventAtMS != 130_000 || row.Payload.RecoveredAtMS != 130_000 {
			t.Fatalf("recovery row = %#v", row)
		}
	}
	if got, err := store.EvaluateAlerts(ctx, 130_001, []int64{11, 22}); err != nil || got != (domain.AlertEvaluationResult{}) {
		t.Fatalf("EvaluateAlerts(recovery repeat) = %#v, %v", got, err)
	}
	if got, err := store.EvaluateAlerts(ctx, 250_000, []int64{11, 22}); err != nil || got != wantOffline {
		t.Fatalf("EvaluateAlerts(second outage boundary) = %#v, %v", got, err)
	}
}

func TestEvaluateAlertsUsesReceiveTimeAndCurrentPreferences(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 1_000 }
	server := createAlertServer(t, store, "receive-time", "", true)
	upsertAlertLatest(t, store, server.ID, 100_000, 999_999)
	store.nowMS = func() int64 { return 110_000 }
	if err := store.SetAlertPreference(ctx, 22, false); err != nil {
		t.Fatalf("SetAlertPreference() error = %v", err)
	}

	if got, err := store.EvaluateAlerts(ctx, 219_999, []int64{22, 11}); err != nil || got != (domain.AlertEvaluationResult{}) {
		t.Fatalf("EvaluateAlerts(before receive boundary) = %#v, %v", got, err)
	}
	want := domain.AlertEvaluationResult{Offline: 1, Queued: 1}
	if got, err := store.EvaluateAlerts(ctx, 220_000, []int64{22, 11}); err != nil || got != want {
		t.Fatalf("EvaluateAlerts(receive boundary) = %#v, %v, want %#v", got, err, want)
	}
	rows := readAlertRows(t, store)
	if len(rows) != 1 || rows[0].TelegramUserID != 11 || rows[0].Payload.OfflineSinceMS != 100_000 {
		t.Fatalf("preference-filtered rows = %#v", rows)
	}
	for name, input := range map[string]struct {
		nowMS int64
		ids   []int64
	}{
		"non-positive now": {nowMS: 0, ids: []int64{11}},
		"non-positive ID":  {nowMS: 1, ids: []int64{0}},
		"duplicate ID":     {nowMS: 1, ids: []int64{11, 11}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.EvaluateAlerts(ctx, input.nowMS, input.ids); err == nil {
				t.Fatal("EvaluateAlerts(invalid input) error = nil")
			}
		})
	}
}

func TestEvaluateAlertsSkipsDisabledShortAndClockRollbackEpisodes(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 10_000 }
	disabled := createAlertServer(t, store, "disabled", "", false)
	enabled := createAlertServer(t, store, "enabled", "", true)

	if got, err := store.EvaluateAlerts(ctx, 500_000, []int64{11}); err != nil || got != (domain.AlertEvaluationResult{Offline: 1, Queued: 1}) {
		t.Fatalf("EvaluateAlerts(mixed servers) = %#v, %v", got, err)
	}
	assertNoAlertState(t, store, disabled.ID)
	upsertAlertLatest(t, store, enabled.ID, 510_000, 1)
	if got, err := store.EvaluateAlerts(ctx, 510_000, []int64{11}); err != nil || got != (domain.AlertEvaluationResult{Recovery: 1, Queued: 1}) {
		t.Fatalf("EvaluateAlerts(recovery) = %#v, %v", got, err)
	}
	if got, err := store.EvaluateAlerts(ctx, 509_000, []int64{11}); err != nil || got != (domain.AlertEvaluationResult{}) {
		t.Fatalf("EvaluateAlerts(clock rollback) = %#v, %v", got, err)
	}
	if got := len(readAlertRows(t, store)); got != 2 {
		t.Fatalf("outbox rows = %d, want one offline and one recovery", got)
	}
}

func TestEvaluateAlertsRecoveryExcludesTerminallySuppressedRecipient(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 1_000 }
	server := createAlertServer(t, store, "node", "", true)
	if _, err := store.EvaluateAlerts(ctx, 121_000, []int64{11, 22}); err != nil {
		t.Fatalf("EvaluateAlerts(offline) error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE alert_outbox SET delivered_at_ms = 122000, last_error = 'recipient_disabled' WHERE telegram_user_id = 22`); err != nil {
		t.Fatalf("suppress recipient: %v", err)
	}
	upsertAlertLatest(t, store, server.ID, 130_000, 129_000)
	want := domain.AlertEvaluationResult{Recovery: 1, Queued: 1}
	if got, err := store.EvaluateAlerts(ctx, 130_000, []int64{11, 22}); err != nil || got != want {
		t.Fatalf("EvaluateAlerts(recovery) = %#v, %v, want %#v", got, err, want)
	}
	rows := readAlertRows(t, store)
	if got := alertRowSummary(rows); !reflect.DeepEqual(got, []string{"11:offline", "22:offline", "11:recovery"}) {
		t.Fatalf("rows = %v", got)
	}
}

func TestServerAlertLifecycleResetsGraceAndSuppressesPendingRows(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 1_000 }
	server := createAlertServer(t, store, "node", "", true)
	if _, err := store.EvaluateAlerts(ctx, 121_000, []int64{11}); err != nil {
		t.Fatalf("EvaluateAlerts(offline) error = %v", err)
	}

	store.nowMS = func() int64 { return 130_000 }
	server.Enabled = false
	if err := store.UpdateServer(ctx, server); err != nil {
		t.Fatalf("UpdateServer(disable) error = %v", err)
	}
	assertAlertState(t, store, server.ID, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, 130_000)
	rows := readAlertRows(t, store)
	if len(rows) != 1 || rows[0].DeliveredAtMS.Int64 != 130_000 || rows[0].LastError != "server_disabled" {
		t.Fatalf("disabled outbox = %#v", rows)
	}

	store.nowMS = func() int64 { return 200_000 }
	server.Enabled = true
	if err := store.UpdateServer(ctx, server); err != nil {
		t.Fatalf("UpdateServer(re-enable) error = %v", err)
	}
	store.nowMS = func() int64 { return 250_000 }
	server.Name = "renamed"
	if err := store.UpdateServer(ctx, server); err != nil {
		t.Fatalf("UpdateServer(rename) error = %v", err)
	}
	assertAlertState(t, store, server.ID, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, 200_000)
	if got, err := store.EvaluateAlerts(ctx, 319_999, []int64{11}); err != nil || got != (domain.AlertEvaluationResult{}) {
		t.Fatalf("EvaluateAlerts(re-enable before boundary) = %#v, %v", got, err)
	}
	if got, err := store.EvaluateAlerts(ctx, 320_000, []int64{11}); err != nil || got != (domain.AlertEvaluationResult{Offline: 1, Queued: 1}) {
		t.Fatalf("EvaluateAlerts(re-enable boundary) = %#v, %v", got, err)
	}

	store.nowMS = func() int64 { return 330_000 }
	if err := store.DeleteServer(ctx, server.ID); err != nil {
		t.Fatalf("DeleteServer() error = %v", err)
	}
	assertNoAlertState(t, store, server.ID)
	rows = readAlertRows(t, store)
	if len(rows) != 2 || rows[1].ServerID.Valid || rows[1].DeliveredAtMS.Int64 != 330_000 || rows[1].LastError != "server_disabled" {
		t.Fatalf("deleted outbox = %#v", rows)
	}
}

func TestEvaluateAlertsRollsBackStateAndRowsOnOutboxFailure(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 1_000 }
	server := createAlertServer(t, store, "rollback", "", true)
	if _, err := store.db.ExecContext(ctx, `
		CREATE TRIGGER reject_second_alert BEFORE INSERT ON alert_outbox
		WHEN NEW.telegram_user_id = 22 BEGIN SELECT RAISE(ABORT, 'OUTBOX-TRIGGER-CANARY'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if _, err := store.EvaluateAlerts(ctx, 121_000, []int64{11, 22}); err == nil {
		t.Fatal("EvaluateAlerts(trigger failure) error = nil")
	}
	assertNoAlertState(t, store, server.ID)
	if rows := readAlertRows(t, store); len(rows) != 0 {
		t.Fatalf("rolled-back outbox rows = %#v", rows)
	}
}

func openAlertStoreAt(t *testing.T, databasePath string) *Store {
	t.Helper()
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store
}

func createAlertServer(t *testing.T, store *Store, name, group string, enabled bool) domain.Server {
	t.Helper()
	tokenHash := sha256.Sum256([]byte(name + "\x00" + group))
	server, err := store.CreateServer(context.Background(), domain.Server{
		Name: name, Group: group, Enabled: enabled, TokenSHA256: tokenHash[:],
	})
	if err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}
	return server
}

func upsertAlertLatest(t *testing.T, store *Store, serverID, receivedAtMS, capturedAtMS int64) {
	t.Helper()
	report := domain.MetricReport{
		CapturedAtMS: capturedAtMS, CPUPct: 10,
		MemoryTotalBytes: 100, MemoryUsedBytes: 50,
		RootDiskTotalBytes: 200, RootDiskUsedBytes: 75,
		Load1: 0.1, Load5: 0.2, Load15: 0.3,
		NetworkRXTotalBytes: 10, NetworkTXTotalBytes: 20,
		NetworkRXBytesPerSecond: 1, NetworkTXBytesPerSecond: 2,
		UptimeSeconds: 30,
		System:        domain.SystemInfo{Hostname: "host", OS: "linux", Kernel: "kernel", Arch: "amd64"},
	}
	if err := store.UpsertLatestMetrics(context.Background(), domain.LatestMetrics{ServerID: serverID, ReceivedAtMS: receivedAtMS, Report: report}); err != nil {
		t.Fatalf("UpsertLatestMetrics() error = %v", err)
	}
}

func readAlertRows(t *testing.T, store *Store) []storedAlertRow {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `
		SELECT id, server_id, telegram_user_id, kind, payload_json, delivered_at_ms, created_at_ms, last_error
		FROM alert_outbox ORDER BY id`)
	if err != nil {
		t.Fatalf("query alert_outbox: %v", err)
	}
	defer rows.Close()
	result := make([]storedAlertRow, 0)
	for rows.Next() {
		var row storedAlertRow
		var kind string
		var payloadJSON string
		if err := rows.Scan(&row.ID, &row.ServerID, &row.TelegramUserID, &kind, &payloadJSON, &row.DeliveredAtMS, &row.CreatedAtMS, &row.LastError); err != nil {
			t.Fatalf("scan alert_outbox: %v", err)
		}
		row.Kind = domain.AlertKind(kind)
		if err := json.Unmarshal([]byte(payloadJSON), &row.Payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate alert_outbox: %v", err)
	}
	return result
}

func alertRowSummary(rows []storedAlertRow) []string {
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		result = append(result, strconv.FormatInt(row.TelegramUserID, 10)+":"+string(row.Kind))
	}
	return result
}

func assertAlertState(t *testing.T, store *Store, serverID int64, offline, sent, recovered sql.NullInt64, updatedAtMS int64) {
	t.Helper()
	var gotOffline, gotSent, gotRecovered sql.NullInt64
	var gotUpdated int64
	err := store.db.QueryRowContext(context.Background(), `
		SELECT offline_since_ms, alert_sent_at_ms, recovered_at_ms, updated_at_ms
		FROM alert_states WHERE server_id = ?`, serverID).Scan(&gotOffline, &gotSent, &gotRecovered, &gotUpdated)
	if err != nil {
		t.Fatalf("query alert state: %v", err)
	}
	if gotOffline != offline || gotSent != sent || gotRecovered != recovered || gotUpdated != updatedAtMS {
		t.Fatalf("alert state = offline %#v sent %#v recovered %#v updated %d", gotOffline, gotSent, gotRecovered, gotUpdated)
	}
}

func assertNoAlertState(t *testing.T, store *Store, serverID int64) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(), `SELECT count(*) FROM alert_states WHERE server_id = ?`, serverID).Scan(&count); err != nil {
		t.Fatalf("count alert state: %v", err)
	}
	if count != 0 {
		t.Fatalf("alert state count = %d, want 0", count)
	}
}

func TestEvaluateAlertsErrorSupportsContextCancellation(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.EvaluateAlerts(ctx, 1, []int64{11})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EvaluateAlerts(canceled) error = %v, want context.Canceled", err)
	}
}
