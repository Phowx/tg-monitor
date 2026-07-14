package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestSettingsRepositoryPersistsValidatedSettings(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 2_000 }

	got, err := store.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings() error = %v", err)
	}
	wantDefault := domain.Settings{OfflineThresholdSeconds: 60, AlertThresholdSeconds: 120, HistoryRetentionDays: 7}
	if !reflect.DeepEqual(got, wantDefault) {
		t.Fatalf("GetSettings() = %#v, want %#v", got, wantDefault)
	}

	want := domain.Settings{OfflineThresholdSeconds: 90, AlertThresholdSeconds: 180, HistoryRetentionDays: 14}
	if err := store.UpdateSettings(ctx, want); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	got, err = store.GetSettings(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetSettings(updated) = %#v, error %v, want %#v", got, err, want)
	}

	invalid := want
	invalid.AlertThresholdSeconds = 0
	if err := store.UpdateSettings(ctx, invalid); err == nil {
		t.Fatal("UpdateSettings(invalid) error = nil")
	}
	got, _ = store.GetSettings(ctx)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid update changed settings to %#v", got)
	}
}

func TestSessionRepositoryHashesTokensAndRejectsExpiry(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	rawToken := "opaque-session-token-value"
	want := domain.Session{TelegramUserID: 123, CreatedAtMS: 1_000, ExpiresAtMS: 2_000}

	if err := store.CreateSession(ctx, rawToken, want); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	var storedHash []byte
	if err := store.db.QueryRowContext(ctx, `SELECT token_sha256 FROM sessions`).Scan(&storedHash); err != nil {
		t.Fatalf("query stored session hash: %v", err)
	}
	expectedHash := sha256.Sum256([]byte(rawToken))
	if !bytes.Equal(storedHash, expectedHash[:]) {
		t.Fatalf("stored hash = %x, want %x", storedHash, expectedHash)
	}
	if bytes.Contains(storedHash, []byte(rawToken)) {
		t.Fatal("stored session value contains raw token")
	}

	got, err := store.GetSession(ctx, rawToken, 1_999)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetSession() = %#v, error %v, want %#v", got, err, want)
	}
	if _, err := store.GetSession(ctx, rawToken, 2_000); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("GetSession(at expiry) error = %v, want ErrSessionExpired", err)
	}
	if _, err := store.GetSession(ctx, "different-token", 1_000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession(missing) error = %v, want ErrNotFound", err)
	}

	if err := store.DeleteSession(ctx, rawToken); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if _, err := store.GetSession(ctx, rawToken, 1_000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession(deleted) error = %v, want ErrNotFound", err)
	}
}

func TestSessionRepositoryRejectsInvalidInputs(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	valid := domain.Session{TelegramUserID: 123, CreatedAtMS: 1_000, ExpiresAtMS: 2_000}
	if err := store.CreateSession(ctx, "", valid); err == nil {
		t.Fatal("CreateSession(empty token) error = nil")
	}
	invalid := valid
	invalid.ExpiresAtMS = invalid.CreatedAtMS
	if err := store.CreateSession(ctx, "token", invalid); err == nil {
		t.Fatal("CreateSession(invalid expiry) error = nil")
	}
	if err := store.DeleteSession(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteSession(missing) error = %v, want ErrNotFound", err)
	}
}

func TestAlertPreferenceRepositoryDefaultsEnabledAndPersists(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 3_000 }

	enabled, err := store.GetAlertPreference(ctx, 456)
	if err != nil || !enabled {
		t.Fatalf("GetAlertPreference(default) = %v, error %v; want true", enabled, err)
	}
	if err := store.SetAlertPreference(ctx, 456, false); err != nil {
		t.Fatalf("SetAlertPreference(false) error = %v", err)
	}
	enabled, err = store.GetAlertPreference(ctx, 456)
	if err != nil || enabled {
		t.Fatalf("GetAlertPreference(disabled) = %v, error %v; want false", enabled, err)
	}
	if err := store.SetAlertPreference(ctx, 456, true); err != nil {
		t.Fatalf("SetAlertPreference(true) error = %v", err)
	}
	enabled, err = store.GetAlertPreference(ctx, 456)
	if err != nil || !enabled {
		t.Fatalf("GetAlertPreference(re-enabled) = %v, error %v; want true", enabled, err)
	}
	if _, err := store.GetAlertPreference(ctx, 0); err == nil {
		t.Fatal("GetAlertPreference(non-positive user) error = nil")
	}
}
