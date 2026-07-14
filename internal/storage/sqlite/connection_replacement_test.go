package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

func TestOpenPreservesConnectionPragmasAfterReplacement(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	connection, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() error = %v", err)
	}
	if err := connection.Raw(func(any) error { return driver.ErrBadConn }); err != driver.ErrBadConn {
		t.Fatalf("Raw() error = %v, want driver.ErrBadConn", err)
	}
	if err := connection.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("connection.Close() error = %v", err)
	}

	for pragma, want := range map[string]int{"foreign_keys": 1, "busy_timeout": 5000} {
		var got int
		if err := store.db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatalf("query %s after replacement: %v", pragma, err)
		}
		if got != want {
			t.Errorf("%s after replacement = %d, want %d", pragma, got, want)
		}
	}
}
