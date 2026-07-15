package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func openUnmigratedTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "monitor.db")))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, nowMS: func() int64 { return 1_700_000_000_000 }}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func TestMigrateFreshDatabaseAppliesVersionsOneAndTwo(t *testing.T) {
	store := openTestStore(t)
	assertSchemaVersions(t, store, []int{1, 2})
	assertSchemaObject(t, store, "table", "telegram_updates")
	assertSchemaObject(t, store, "index", "telegram_updates_received_idx")

	columns, err := store.db.QueryContext(context.Background(), `PRAGMA table_info(telegram_updates)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info error = %v", err)
	}
	defer columns.Close()
	var names []string
	for columns.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := columns.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		names = append(names, name)
		if name == "update_id" && primaryKey != 1 {
			t.Fatalf("update_id primary key = %d, want 1", primaryKey)
		}
	}
	if err := columns.Err(); err != nil {
		t.Fatalf("iterate table_info: %v", err)
	}
	if !reflect.DeepEqual(names, []string{"update_id", "received_at_ms"}) {
		t.Fatalf("telegram_updates columns = %v", names)
	}
}

func TestMigrateUpgradesVersionOneToTwo(t *testing.T) {
	store := openUnmigratedTestStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at_ms INTEGER NOT NULL CHECK(applied_at_ms > 0)
	)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	if err := store.applyMigrationV1(ctx); err != nil {
		t.Fatalf("applyMigrationV1() error = %v", err)
	}
	assertSchemaVersions(t, store, []int{1})

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	assertSchemaVersions(t, store, []int{1, 2})
	assertSchemaObject(t, store, "table", "telegram_updates")

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	assertSchemaVersions(t, store, []int{1, 2})
}

func assertSchemaVersions(t *testing.T, store *Store, want []int) {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("query schema versions: %v", err)
	}
	defer rows.Close()
	var got []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatalf("scan schema version: %v", err)
		}
		got = append(got, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schema versions: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schema versions = %v, want %v", got, want)
	}
}

func assertSchemaObject(t *testing.T, store *Store, kind, name string) {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(context.Background(), `SELECT count(*) FROM sqlite_master WHERE type = ? AND name = ?`, kind, name).Scan(&count); err != nil {
		t.Fatalf("query schema object %s %s: %v", kind, name, err)
	}
	if count != 1 {
		t.Fatalf("schema object %s %s count = %d, want 1", kind, name, count)
	}
}
