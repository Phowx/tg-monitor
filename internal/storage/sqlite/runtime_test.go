package sqlite

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestStorePing(t *testing.T) {
	store := openTestStore(t)
	if err := store.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

func TestGetServerByTokenHash(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	hash := bytes.Repeat([]byte{0x7a}, 32)
	created, err := store.CreateServer(ctx, domain.Server{
		Name:        "test-server",
		Enabled:     false,
		TokenSHA256: hash,
	})
	if err != nil {
		t.Fatalf("CreateServer() error = %v", err)
	}

	got, err := store.GetServerByTokenHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetServerByTokenHash() error = %v", err)
	}
	if got.ID != created.ID || got.Name != created.Name || got.Enabled || !bytes.Equal(got.TokenSHA256, hash) {
		t.Fatalf("GetServerByTokenHash() = %#v, want server ID %d", got, created.ID)
	}
}

func TestGetServerByTokenHashRejectsInvalidAndMissingHashesWithoutLeak(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, err := store.GetServerByTokenHash(ctx, bytes.Repeat([]byte{1}, 31)); err == nil {
		t.Fatal("GetServerByTokenHash(short hash) error = nil")
	}

	missing := bytes.Repeat([]byte{0xab}, 32)
	_, err := store.GetServerByTokenHash(ctx, missing)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetServerByTokenHash(missing) error = %v, want ErrNotFound", err)
	}
	if strings.Contains(err.Error(), string(missing)) || strings.Contains(err.Error(), "abababab") {
		t.Fatalf("GetServerByTokenHash(missing) leaked hash: %v", err)
	}
}
