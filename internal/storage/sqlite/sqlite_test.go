package sqlite

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func TestOpenConfiguresSQLitePragmas(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	var journalMode string
	if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	for pragma, want := range map[string]int{"foreign_keys": 1, "busy_timeout": 5000} {
		var got int
		if err := store.db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatalf("query %s: %v", pragma, err)
		}
		if got != want {
			t.Errorf("%s = %d, want %d", pragma, got, want)
		}
	}
}

func TestMigrateIsIdempotentAndSeedsSettings(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	tables := []string{
		"schema_migrations", "servers", "latest_metrics", "metric_samples",
		"sessions", "user_preferences", "settings", "alert_states", "alert_outbox",
	}
	for _, table := range tables {
		var count int
		err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count)
		if err != nil || count != 1 {
			t.Errorf("table %s count = %d, error = %v", table, count, err)
		}
	}
	for _, index := range []string{"metric_samples_server_bucket_idx", "alert_outbox_delivery_idx"} {
		var count int
		err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count)
		if err != nil || count != 1 {
			t.Errorf("index %s count = %d, error = %v", index, count, err)
		}
	}

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	var migrationCount, version int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*), max(version) FROM schema_migrations`).Scan(&migrationCount, &version); err != nil {
		t.Fatalf("query migrations: %v", err)
	}
	if migrationCount != 1 || version != 1 {
		t.Fatalf("migrations = count %d, max %d; want count 1, max 1", migrationCount, version)
	}

	var offline, alert, retention int64
	if err := store.db.QueryRowContext(ctx, `SELECT offline_threshold_seconds, alert_threshold_seconds, history_retention_days FROM settings WHERE id=1`).Scan(&offline, &alert, &retention); err != nil {
		t.Fatalf("query settings: %v", err)
	}
	if offline != 60 || alert != 120 || retention != 7 {
		t.Fatalf("seeded settings = %d/%d/%d, want 60/120/7", offline, alert, retention)
	}
}

func TestForeignKeyCascadeDeletesServerOwnedRows(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	hash := bytes.Repeat([]byte{0x5a}, 32)
	result, err := store.db.ExecContext(ctx, `INSERT INTO servers(name,group_name,sort_order,enabled,token_sha256,created_at_ms,updated_at_ms) VALUES('server','',0,1,?,1,1)`, hash)
	if err != nil {
		t.Fatalf("insert server: %v", err)
	}
	serverID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId(): %v", err)
	}

	latestValues := []any{serverID, 2, 1, 10.0, 100, 50, 200, 75, 0.1, 0.2, 0.3, 10, 20, 1.0, 2.0, 3, "host", "linux", "kernel", "amd64"}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO latest_metrics(server_id,received_at_ms,captured_at_ms,cpu_pct,memory_total_bytes,memory_used_bytes,root_disk_total_bytes,root_disk_used_bytes,load_1,load_5,load_15,network_rx_total_bytes,network_tx_total_bytes,network_rx_bytes_per_second,network_tx_bytes_per_second,uptime_seconds,hostname,os,kernel,arch) VALUES(`+placeholders(len(latestValues))+`)`, latestValues...); err != nil {
		t.Fatalf("insert latest metrics: %v", err)
	}
	sampleValues := []any{serverID, 60_000, 10.0, 100, 50.0, 200, 75.0, 0.1, 0.2, 0.3, 10, 20, 1.0, 2.0, 3, "host", "linux", "kernel", "amd64"}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO metric_samples(server_id,bucket_ms,cpu_pct,memory_total_bytes,memory_used_bytes,root_disk_total_bytes,root_disk_used_bytes,load_1,load_5,load_15,network_rx_total_bytes,network_tx_total_bytes,network_rx_bytes_per_second,network_tx_bytes_per_second,uptime_seconds,hostname,os,kernel,arch) VALUES(`+placeholders(len(sampleValues))+`)`, sampleValues...); err != nil {
		t.Fatalf("insert metric sample: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO alert_states(server_id,updated_at_ms) VALUES(?,1)`, serverID); err != nil {
		t.Fatalf("insert alert state: %v", err)
	}

	if _, err := store.db.ExecContext(ctx, `DELETE FROM servers WHERE id=?`, serverID); err != nil {
		t.Fatalf("delete server: %v", err)
	}
	for _, table := range []string{"latest_metrics", "metric_samples", "alert_states"} {
		var count int
		if err := store.db.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s count after server delete = %d, want 0", table, count)
		}
	}
}

func placeholders(count int) string {
	result := "?"
	for i := 1; i < count; i++ {
		result += ",?"
	}
	return result
}
