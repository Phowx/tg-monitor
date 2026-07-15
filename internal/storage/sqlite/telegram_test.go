package sqlite

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestTelegramUpdateRepositoryRecordsFirstAndDeduplicates(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	inserted, err := store.RecordTelegramUpdate(ctx, 9_001, 10_000)
	if err != nil || !inserted {
		t.Fatalf("RecordTelegramUpdate(first) = %v, %v; want true, nil", inserted, err)
	}
	inserted, err = store.RecordTelegramUpdate(ctx, 9_001, 20_000)
	if err != nil || inserted {
		t.Fatalf("RecordTelegramUpdate(duplicate) = %v, %v; want false, nil", inserted, err)
	}

	var count int
	var receivedAtMS int64
	if err := store.db.QueryRowContext(ctx, `SELECT count(*), min(received_at_ms) FROM telegram_updates WHERE update_id = ?`, 9_001).Scan(&count, &receivedAtMS); err != nil {
		t.Fatalf("query telegram update: %v", err)
	}
	if count != 1 || receivedAtMS != 10_000 {
		t.Fatalf("stored update = count %d received %d, want 1/10000", count, receivedAtMS)
	}
}

func TestTelegramUpdateRepositoryRejectsInvalidInput(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	for _, input := range []struct {
		updateID, receivedAtMS int64
	}{
		{updateID: 0, receivedAtMS: 1},
		{updateID: -1, receivedAtMS: 1},
		{updateID: 1, receivedAtMS: 0},
		{updateID: 1, receivedAtMS: -1},
	} {
		if _, err := store.RecordTelegramUpdate(ctx, input.updateID, input.receivedAtMS); err == nil {
			t.Fatalf("RecordTelegramUpdate(%d, %d) error = nil", input.updateID, input.receivedAtMS)
		}
	}
}

func TestTelegramUpdateRepositoryConcurrentDedupeHasOneWinner(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	const attempts = 32
	results := make(chan bool, attempts)
	errors := make(chan error, attempts)
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(attempts)
	for index := 0; index < attempts; index++ {
		go func(index int) {
			defer workers.Done()
			<-start
			inserted, err := store.RecordTelegramUpdate(ctx, 7_777, int64(10_000+index))
			if err != nil {
				errors <- err
				return
			}
			results <- inserted
		}(index)
	}
	close(start)
	workers.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatalf("RecordTelegramUpdate(concurrent) error = %v", err)
	}
	winners := 0
	for inserted := range results {
		if inserted {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent insert winners = %d, want 1", winners)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM telegram_updates WHERE update_id = 7777`).Scan(&count); err != nil {
		t.Fatalf("query concurrent update: %v", err)
	}
	if count != 1 {
		t.Fatalf("concurrent update row count = %d, want 1", count)
	}
}

func TestExpiredSessionAndTelegramUpdateCleanupUsesExactBoundaries(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	for index, expiry := range []int64{999, 1_000, 1_001} {
		rawToken := fmt.Sprintf("session-%d", index)
		if err := store.CreateSession(ctx, rawToken, domain.Session{
			TelegramUserID: int64(100 + index),
			CreatedAtMS:    500,
			ExpiresAtMS:    expiry,
		}); err != nil {
			t.Fatalf("CreateSession(%d) error = %v", expiry, err)
		}
	}
	for _, receivedAtMS := range []int64{999, 1_000, 1_001} {
		if inserted, err := store.RecordTelegramUpdate(ctx, receivedAtMS, receivedAtMS); err != nil || !inserted {
			t.Fatalf("RecordTelegramUpdate(%d) = %v, %v", receivedAtMS, inserted, err)
		}
	}

	sessions, err := store.DeleteExpiredSessions(ctx, 1_000)
	if err != nil || sessions != 2 {
		t.Fatalf("DeleteExpiredSessions() = %d, %v; want 2, nil", sessions, err)
	}
	updates, err := store.DeleteTelegramUpdatesBefore(ctx, 1_000)
	if err != nil || updates != 1 {
		t.Fatalf("DeleteTelegramUpdatesBefore() = %d, %v; want 1, nil", updates, err)
	}

	var sessionCount, updateCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM telegram_updates`).Scan(&updateCount); err != nil {
		t.Fatalf("count updates: %v", err)
	}
	if sessionCount != 1 || updateCount != 2 {
		t.Fatalf("remaining sessions/updates = %d/%d, want 1/2", sessionCount, updateCount)
	}
}

func TestTelegramCleanupRejectsNonPositiveCutoffs(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	for _, cutoff := range []int64{0, -1} {
		if _, err := store.DeleteExpiredSessions(ctx, cutoff); err == nil {
			t.Fatalf("DeleteExpiredSessions(%d) error = nil", cutoff)
		}
		if _, err := store.DeleteTelegramUpdatesBefore(ctx, cutoff); err == nil {
			t.Fatalf("DeleteTelegramUpdatesBefore(%d) error = nil", cutoff)
		}
	}
}
