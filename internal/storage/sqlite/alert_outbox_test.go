package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestListDueAlertOutboxBlocksLaterRowsPerServerAndRecipient(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 1_000 }
	serverOne := createAlertServer(t, store, "one", "", true)
	serverTwo := createAlertServer(t, store, "two", "", true)
	first := insertAlertOutbox(t, store, &serverOne.ID, 42, domain.AlertOffline, 100, nil, 10, "")
	insertAlertOutbox(t, store, &serverOne.ID, 42, domain.AlertRecovery, 0, nil, 11, "")
	third := insertAlertOutbox(t, store, &serverTwo.ID, 42, domain.AlertOffline, 0, nil, 12, "")
	fourth := insertAlertOutbox(t, store, &serverOne.ID, 43, domain.AlertOffline, 0, nil, 13, "")
	delivered := int64(20)
	insertAlertOutbox(t, store, &serverOne.ID, 44, domain.AlertOffline, 0, &delivered, 14, "")
	nullServer := insertAlertOutbox(t, store, nil, 45, domain.AlertOffline, 0, nil, 15, "")

	got, err := store.ListDueAlertOutbox(ctx, 50, 100)
	if err != nil {
		t.Fatalf("ListDueAlertOutbox() error = %v", err)
	}
	if ids := outboxIDs(got); !reflect.DeepEqual(ids, []int64{third, fourth, nullServer}) {
		t.Fatalf("due IDs = %v, want independent due rows", ids)
	}
	if got[2].ServerID != nil {
		t.Fatalf("nullable server ID = %#v, want nil", got[2].ServerID)
	}
	if err := store.MarkAlertDelivered(ctx, first, 60); err != nil {
		t.Fatalf("MarkAlertDelivered(first) error = %v", err)
	}
	got, err = store.ListDueAlertOutbox(ctx, 60, 1)
	if err != nil {
		t.Fatalf("ListDueAlertOutbox(limit one) error = %v", err)
	}
	if ids := outboxIDs(got); !reflect.DeepEqual(ids, []int64{first + 1}) {
		t.Fatalf("due IDs after unblock = %v, want recovery", ids)
	}

	for name, input := range map[string]struct {
		nowMS int64
		limit int
	}{
		"invalid time":   {nowMS: 0, limit: 1},
		"zero limit":     {nowMS: 1, limit: 0},
		"oversize limit": {nowMS: 1, limit: 101},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.ListDueAlertOutbox(ctx, input.nowMS, input.limit); err == nil {
				t.Fatal("ListDueAlertOutbox(invalid) error = nil")
			}
		})
	}
}

func TestAlertOutboxMutationsAreCompareAndSetAndSafe(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	server := createAlertServer(t, store, "node", "", true)
	failedID := insertAlertOutbox(t, store, &server.ID, 42, domain.AlertOffline, 10, nil, 5, "")

	if err := store.MarkAlertFailed(ctx, failedID, 20, 25, alertRetryFailure); err != nil {
		t.Fatalf("MarkAlertFailed() error = %v", err)
	}
	assertOutboxMutation(t, store, failedID, 1, 25, sql.NullInt64{}, alertRetryFailure)
	if err := store.MarkAlertDelivered(ctx, failedID, 30); err != nil {
		t.Fatalf("MarkAlertDelivered() error = %v", err)
	}
	assertOutboxMutation(t, store, failedID, 1, 25, sql.NullInt64{Int64: 30, Valid: true}, "")
	if err := store.MarkAlertDelivered(ctx, failedID, 31); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkAlertDelivered(stale) error = %v, want ErrNotFound", err)
	}

	suppressedID := insertAlertOutbox(t, store, &server.ID, 43, domain.AlertOffline, 10, nil, 6, "")
	if err := store.MarkAlertSuppressed(ctx, suppressedID, 40, alertSuppressedRecipientOff); err != nil {
		t.Fatalf("MarkAlertSuppressed() error = %v", err)
	}
	assertOutboxMutation(t, store, suppressedID, 0, 10, sql.NullInt64{Int64: 40, Valid: true}, alertSuppressedRecipientOff)

	invalidID := insertAlertOutbox(t, store, &server.ID, 44, domain.AlertOffline, 10, nil, 7, "")
	for name, call := range map[string]func() error{
		"delivered ID":         func() error { return store.MarkAlertDelivered(ctx, 0, 10) },
		"delivered time":       func() error { return store.MarkAlertDelivered(ctx, invalidID, 0) },
		"suppression class":    func() error { return store.MarkAlertSuppressed(ctx, invalidID, 10, alertRetryFailure) },
		"failure class":        func() error { return store.MarkAlertFailed(ctx, invalidID, 10, 20, "raw dependency error") },
		"failure next attempt": func() error { return store.MarkAlertFailed(ctx, invalidID, 20, 20, alertRetryFailure) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("invalid mutation error = nil")
			}
		})
	}
	assertOutboxMutation(t, store, invalidID, 0, 10, sql.NullInt64{}, "")
}

func TestDeleteTerminalAlertOutboxBeforePreservesBoundaryAndRetries(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	server := createAlertServer(t, store, "node", "", true)
	deliveredOld := int64(99)
	deliveredBoundary := int64(100)
	insertAlertOutbox(t, store, &server.ID, 41, domain.AlertOffline, 1, &deliveredOld, 1, "")
	insertAlertOutbox(t, store, &server.ID, 42, domain.AlertOffline, 1, &deliveredBoundary, 1, alertSuppressedRecipientOff)
	activeID := insertAlertOutbox(t, store, &server.ID, 43, domain.AlertOffline, 1, nil, 1, alertRetryFailure)

	deleted, err := store.DeleteTerminalAlertOutboxBefore(ctx, 100)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteTerminalAlertOutboxBefore() = %d, %v, want 1", deleted, err)
	}
	rows := readAlertRows(t, store)
	if len(rows) != 2 || rows[1].ID != activeID || rows[1].DeliveredAtMS.Valid {
		t.Fatalf("remaining rows = %#v", rows)
	}
	if _, err := store.DeleteTerminalAlertOutboxBefore(ctx, 0); err == nil {
		t.Fatal("DeleteTerminalAlertOutboxBefore(invalid) error = nil")
	}
}

func insertAlertOutbox(t *testing.T, store *Store, serverID *int64, telegramUserID int64, kind domain.AlertKind, nextAttemptAtMS int64, deliveredAtMS *int64, createdAtMS int64, lastError string) int64 {
	t.Helper()
	result, err := store.db.ExecContext(context.Background(), `
		INSERT INTO alert_outbox(server_id, telegram_user_id, kind, payload_json, attempts, next_attempt_at_ms, delivered_at_ms, created_at_ms, last_error)
		VALUES(?, ?, ?, '{}', 0, ?, ?, ?, ?)`, serverID, telegramUserID, kind, nextAttemptAtMS, deliveredAtMS, createdAtMS, lastError)
	if err != nil {
		t.Fatalf("insert alert outbox: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId() error = %v", err)
	}
	return id
}

func outboxIDs(items []domain.AlertOutboxItem) []int64 {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func assertOutboxMutation(t *testing.T, store *Store, id int64, attempts int, nextAttemptAtMS int64, deliveredAtMS sql.NullInt64, lastError string) {
	t.Helper()
	var gotAttempts int
	var gotNext int64
	var gotDelivered sql.NullInt64
	var gotLastError string
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT attempts, next_attempt_at_ms, delivered_at_ms, last_error
		FROM alert_outbox WHERE id = ?`, id).Scan(&gotAttempts, &gotNext, &gotDelivered, &gotLastError); err != nil {
		t.Fatalf("query outbox mutation: %v", err)
	}
	if gotAttempts != attempts || gotNext != nextAttemptAtMS || gotDelivered != deliveredAtMS || gotLastError != lastError {
		t.Fatalf("outbox mutation = attempts %d next %d delivered %#v error %q", gotAttempts, gotNext, gotDelivered, gotLastError)
	}
}
