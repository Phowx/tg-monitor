package serverapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type accessCleanupRepositoryStub struct {
	sessionCount int64
	sessionErr   error
	sessionNowMS int64
	sessionCalls int
	updateCount  int64
	updateErr    error
	updateCutoff int64
	updateCalls  int
	outboxCount  int64
	outboxErr    error
	outboxCutoff int64
	outboxCalls  int
}

func (stub *accessCleanupRepositoryStub) DeleteExpiredSessions(_ context.Context, nowMS int64) (int64, error) {
	stub.sessionCalls++
	stub.sessionNowMS = nowMS
	return stub.sessionCount, stub.sessionErr
}

func (stub *accessCleanupRepositoryStub) DeleteTelegramUpdatesBefore(_ context.Context, cutoffMS int64) (int64, error) {
	stub.updateCalls++
	stub.updateCutoff = cutoffMS
	return stub.updateCount, stub.updateErr
}

func (stub *accessCleanupRepositoryStub) DeleteTerminalAlertOutboxBefore(_ context.Context, cutoffMS int64) (int64, error) {
	stub.outboxCalls++
	stub.outboxCutoff = cutoffMS
	return stub.outboxCount, stub.outboxErr
}

func TestRunAccessCleanupUsesExpectedCutoffsAndReturnsCounts(t *testing.T) {
	now := time.Date(2026, time.July, 15, 9, 30, 0, 123_000_000, time.UTC)
	repository := &accessCleanupRepositoryStub{sessionCount: 3, updateCount: 7, outboxCount: 11}

	result, err := RunAccessCleanupOnce(context.Background(), repository, now)
	if err != nil {
		t.Fatalf("RunAccessCleanupOnce() error = %v", err)
	}
	want := AccessCleanupResult{Sessions: 3, Updates: 7, Outbox: 11}
	if result != want {
		t.Fatalf("RunAccessCleanupOnce() = %#v, want %#v", result, want)
	}
	if repository.sessionCalls != 1 || repository.sessionNowMS != now.UnixMilli() {
		t.Fatalf("DeleteExpiredSessions calls = %d, now = %d, want %d", repository.sessionCalls, repository.sessionNowMS, now.UnixMilli())
	}
	wantCutoff := now.Add(-7 * 24 * time.Hour).UnixMilli()
	if repository.updateCalls != 1 || repository.updateCutoff != wantCutoff {
		t.Fatalf("DeleteTelegramUpdatesBefore calls = %d, cutoff = %d, want %d", repository.updateCalls, repository.updateCutoff, wantCutoff)
	}
	wantOutboxCutoff := now.Add(-30 * 24 * time.Hour).UnixMilli()
	if repository.outboxCalls != 1 || repository.outboxCutoff != wantOutboxCutoff {
		t.Fatalf("DeleteTerminalAlertOutboxBefore calls = %d, cutoff = %d, want %d", repository.outboxCalls, repository.outboxCutoff, wantOutboxCutoff)
	}
}

func TestRunAccessCleanupAttemptsEveryDeleteAndJoinsSafeOperationErrors(t *testing.T) {
	repository := &accessCleanupRepositoryStub{
		sessionErr: errors.New("SESSION-STORE-CANARY"),
		updateErr:  errors.New("UPDATE-STORE-CANARY"),
		outboxErr:  errors.New("OUTBOX-STORE-CANARY"),
	}

	result, err := RunAccessCleanupOnce(context.Background(), repository, time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("RunAccessCleanupOnce() error = nil")
	}
	if result != (AccessCleanupResult{}) {
		t.Fatalf("RunAccessCleanupOnce() result = %#v, want zero counts on failures", result)
	}
	if repository.sessionCalls != 1 || repository.updateCalls != 1 || repository.outboxCalls != 1 {
		t.Fatalf("delete calls: sessions=%d updates=%d outbox=%d, want every delete attempted", repository.sessionCalls, repository.updateCalls, repository.outboxCalls)
	}
	for _, operation := range []string{"delete expired sessions failed", "delete old Telegram updates failed", "delete old alert outbox failed"} {
		if !strings.Contains(err.Error(), operation) {
			t.Fatalf("error %q missing operation class %q", err, operation)
		}
	}
	for _, canary := range []string{"SESSION-STORE-CANARY", "UPDATE-STORE-CANARY", "OUTBOX-STORE-CANARY"} {
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("error %q leaked dependency detail %q", err, canary)
		}
	}
}
