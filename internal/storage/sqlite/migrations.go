package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

const latestSchemaVersion = 2

var migrationV1 = []string{
	`CREATE TABLE servers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL CHECK(length(trim(name)) > 0),
		group_name TEXT NOT NULL DEFAULT '',
		sort_order INTEGER NOT NULL DEFAULT 0,
		enabled INTEGER NOT NULL CHECK(enabled IN (0, 1)),
		token_sha256 BLOB NOT NULL UNIQUE CHECK(length(token_sha256) = 32),
		created_at_ms INTEGER NOT NULL CHECK(created_at_ms > 0),
		updated_at_ms INTEGER NOT NULL CHECK(updated_at_ms > 0)
	)`,
	`CREATE TABLE latest_metrics (
		server_id INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
		received_at_ms INTEGER NOT NULL CHECK(received_at_ms > 0),
		captured_at_ms INTEGER NOT NULL CHECK(captured_at_ms > 0),
		cpu_pct REAL NOT NULL CHECK(cpu_pct >= 0 AND cpu_pct <= 100),
		memory_total_bytes INTEGER NOT NULL CHECK(memory_total_bytes > 0),
		memory_used_bytes INTEGER NOT NULL CHECK(memory_used_bytes >= 0 AND memory_used_bytes <= memory_total_bytes),
		root_disk_total_bytes INTEGER NOT NULL CHECK(root_disk_total_bytes > 0),
		root_disk_used_bytes INTEGER NOT NULL CHECK(root_disk_used_bytes >= 0 AND root_disk_used_bytes <= root_disk_total_bytes),
		load_1 REAL NOT NULL CHECK(load_1 >= 0),
		load_5 REAL NOT NULL CHECK(load_5 >= 0),
		load_15 REAL NOT NULL CHECK(load_15 >= 0),
		network_rx_total_bytes INTEGER NOT NULL CHECK(network_rx_total_bytes >= 0),
		network_tx_total_bytes INTEGER NOT NULL CHECK(network_tx_total_bytes >= 0),
		network_rx_bytes_per_second REAL NOT NULL CHECK(network_rx_bytes_per_second >= 0),
		network_tx_bytes_per_second REAL NOT NULL CHECK(network_tx_bytes_per_second >= 0),
		uptime_seconds INTEGER NOT NULL CHECK(uptime_seconds >= 0),
		hostname TEXT NOT NULL CHECK(length(trim(hostname)) > 0),
		os TEXT NOT NULL CHECK(length(trim(os)) > 0),
		kernel TEXT NOT NULL CHECK(length(trim(kernel)) > 0),
		arch TEXT NOT NULL CHECK(length(trim(arch)) > 0)
	)`,
	`CREATE TABLE metric_samples (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
		bucket_ms INTEGER NOT NULL,
		cpu_pct REAL NOT NULL CHECK(cpu_pct >= 0 AND cpu_pct <= 100),
		memory_total_bytes INTEGER NOT NULL CHECK(memory_total_bytes > 0),
		memory_used_bytes INTEGER NOT NULL CHECK(memory_used_bytes >= 0 AND memory_used_bytes <= memory_total_bytes),
		root_disk_total_bytes INTEGER NOT NULL CHECK(root_disk_total_bytes > 0),
		root_disk_used_bytes INTEGER NOT NULL CHECK(root_disk_used_bytes >= 0 AND root_disk_used_bytes <= root_disk_total_bytes),
		load_1 REAL NOT NULL CHECK(load_1 >= 0),
		load_5 REAL NOT NULL CHECK(load_5 >= 0),
		load_15 REAL NOT NULL CHECK(load_15 >= 0),
		network_rx_total_bytes INTEGER NOT NULL CHECK(network_rx_total_bytes >= 0),
		network_tx_total_bytes INTEGER NOT NULL CHECK(network_tx_total_bytes >= 0),
		network_rx_bytes_per_second REAL NOT NULL CHECK(network_rx_bytes_per_second >= 0),
		network_tx_bytes_per_second REAL NOT NULL CHECK(network_tx_bytes_per_second >= 0),
		uptime_seconds INTEGER NOT NULL CHECK(uptime_seconds >= 0),
		hostname TEXT NOT NULL CHECK(length(trim(hostname)) > 0),
		os TEXT NOT NULL CHECK(length(trim(os)) > 0),
		kernel TEXT NOT NULL CHECK(length(trim(kernel)) > 0),
		arch TEXT NOT NULL CHECK(length(trim(arch)) > 0),
		UNIQUE(server_id, bucket_ms)
	)`,
	`CREATE INDEX metric_samples_server_bucket_idx ON metric_samples(server_id, bucket_ms)`,
	`CREATE TABLE sessions (
		token_sha256 BLOB PRIMARY KEY CHECK(length(token_sha256) = 32),
		telegram_user_id INTEGER NOT NULL CHECK(telegram_user_id > 0),
		created_at_ms INTEGER NOT NULL CHECK(created_at_ms > 0),
		expires_at_ms INTEGER NOT NULL CHECK(expires_at_ms > created_at_ms)
	)`,
	`CREATE INDEX sessions_expiry_idx ON sessions(expires_at_ms)`,
	`CREATE TABLE user_preferences (
		telegram_user_id INTEGER PRIMARY KEY CHECK(telegram_user_id > 0),
		alerts_enabled INTEGER NOT NULL CHECK(alerts_enabled IN (0, 1)),
		updated_at_ms INTEGER NOT NULL CHECK(updated_at_ms > 0)
	)`,
	`CREATE TABLE settings (
		id INTEGER PRIMARY KEY CHECK(id = 1),
		offline_threshold_seconds INTEGER NOT NULL CHECK(offline_threshold_seconds > 0),
		alert_threshold_seconds INTEGER NOT NULL CHECK(alert_threshold_seconds > 0),
		history_retention_days INTEGER NOT NULL CHECK(history_retention_days > 0),
		updated_at_ms INTEGER NOT NULL CHECK(updated_at_ms > 0)
	)`,
	`CREATE TABLE alert_states (
		server_id INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
		offline_since_ms INTEGER,
		alert_sent_at_ms INTEGER,
		recovered_at_ms INTEGER,
		updated_at_ms INTEGER NOT NULL CHECK(updated_at_ms > 0)
	)`,
	`CREATE TABLE alert_outbox (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		server_id INTEGER REFERENCES servers(id) ON DELETE SET NULL,
		telegram_user_id INTEGER NOT NULL CHECK(telegram_user_id > 0),
		kind TEXT NOT NULL CHECK(length(trim(kind)) > 0),
		payload_json TEXT NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
		next_attempt_at_ms INTEGER NOT NULL,
		delivered_at_ms INTEGER,
		created_at_ms INTEGER NOT NULL CHECK(created_at_ms > 0),
		last_error TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX alert_outbox_delivery_idx ON alert_outbox(delivered_at_ms, next_attempt_at_ms)`,
}

var migrationV2 = []string{
	`CREATE TABLE telegram_updates (
		update_id INTEGER PRIMARY KEY CHECK(update_id > 0),
		received_at_ms INTEGER NOT NULL CHECK(received_at_ms > 0)
	)`,
	`CREATE INDEX telegram_updates_received_idx ON telegram_updates(received_at_ms)`,
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at_ms INTEGER NOT NULL CHECK(applied_at_ms > 0)
	)`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > latestSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, latestSchemaVersion)
	}
	for version < latestSchemaVersion {
		switch version + 1 {
		case 1:
			if err := s.applyMigrationV1(ctx); err != nil {
				return err
			}
		case 2:
			if err := s.applyMigration(ctx, 2, migrationV2); err != nil {
				return err
			}
		}
		version++
	}
	return nil
}

func (s *Store) applyMigrationV1(ctx context.Context) error {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin schema migration 1: %w", err)
	}
	defer transaction.Rollback()

	for _, statement := range migrationV1 {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply schema migration 1: %w", err)
		}
	}
	now := s.nowMS()
	if _, err := transaction.ExecContext(ctx, `INSERT INTO settings(id, offline_threshold_seconds, alert_threshold_seconds, history_retention_days, updated_at_ms) VALUES(1, 60, 120, 7, ?)`, now); err != nil {
		return fmt.Errorf("seed settings in schema migration 1: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at_ms) VALUES(1, ?)`, now); err != nil {
		return fmt.Errorf("record schema migration 1: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit schema migration 1: %w", err)
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, version int, statements []string) error {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin schema migration %d: %w", version, err)
	}
	defer transaction.Rollback()

	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply schema migration %d: %w", version, err)
		}
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, applied_at_ms) VALUES(?, ?)`,
		version,
		s.nowMS(),
	); err != nil {
		return fmt.Errorf("record schema migration %d: %w", version, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit schema migration %d: %w", version, err)
	}
	return nil
}
