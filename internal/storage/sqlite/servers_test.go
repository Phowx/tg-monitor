package sqlite

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func serverHash(value byte) []byte {
	return bytes.Repeat([]byte{value}, 32)
}

func TestServerRepositoryCRUDAndOrdering(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 1_000 }

	inputs := []domain.Server{
		{Name: "beta", Group: "west", SortOrder: 2, Enabled: true, TokenSHA256: serverHash(1)},
		{Name: "zeta", Group: "east", SortOrder: 1, Enabled: false, TokenSHA256: serverHash(2)},
		{Name: "alpha", Group: "east", SortOrder: 1, Enabled: true, TokenSHA256: serverHash(3)},
	}
	created := make([]domain.Server, 0, len(inputs))
	for _, input := range inputs {
		server, err := store.CreateServer(ctx, input)
		if err != nil {
			t.Fatalf("CreateServer(%q) error = %v", input.Name, err)
		}
		if server.ID <= 0 || server.CreatedAtMS != 1_000 || server.UpdatedAtMS != 1_000 {
			t.Fatalf("created server metadata = %#v", server)
		}
		created = append(created, server)
	}

	listed, err := store.ListServers(ctx)
	if err != nil {
		t.Fatalf("ListServers() error = %v", err)
	}
	gotNames := []string{listed[0].Name, listed[1].Name, listed[2].Name}
	if !reflect.DeepEqual(gotNames, []string{"alpha", "zeta", "beta"}) {
		t.Fatalf("ListServers() names = %v", gotNames)
	}

	got, err := store.GetServer(ctx, created[0].ID)
	if err != nil {
		t.Fatalf("GetServer() error = %v", err)
	}
	if !reflect.DeepEqual(got, created[0]) {
		t.Fatalf("GetServer() = %#v, want %#v", got, created[0])
	}

	store.nowMS = func() int64 { return 2_000 }
	got.Name = "primary"
	got.Group = "core"
	got.SortOrder = -1
	got.Enabled = false
	got.TokenSHA256 = serverHash(9)
	if err := store.UpdateServer(ctx, got); err != nil {
		t.Fatalf("UpdateServer() error = %v", err)
	}
	updated, err := store.GetServer(ctx, got.ID)
	if err != nil {
		t.Fatalf("GetServer(updated) error = %v", err)
	}
	if updated.Name != "primary" || updated.Group != "core" || updated.SortOrder != -1 || updated.Enabled || updated.UpdatedAtMS != 2_000 {
		t.Fatalf("updated server = %#v", updated)
	}
	if !bytes.Equal(updated.TokenSHA256, inputs[0].TokenSHA256) {
		t.Fatal("UpdateServer() changed token hash")
	}

	store.nowMS = func() int64 { return 3_000 }
	newHash := serverHash(8)
	if err := store.UpdateServerTokenHash(ctx, got.ID, newHash); err != nil {
		t.Fatalf("UpdateServerTokenHash() error = %v", err)
	}
	updated, err = store.GetServer(ctx, got.ID)
	if err != nil {
		t.Fatalf("GetServer(token updated) error = %v", err)
	}
	if !bytes.Equal(updated.TokenSHA256, newHash) || updated.UpdatedAtMS != 3_000 {
		t.Fatalf("token-updated server = %#v", updated)
	}

	if err := store.DeleteServer(ctx, got.ID); err != nil {
		t.Fatalf("DeleteServer() error = %v", err)
	}
	if _, err := store.GetServer(ctx, got.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetServer(deleted) error = %v, want ErrNotFound", err)
	}
}

func TestServerRepositoryRejectsInvalidHashAndMissingRows(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, err := store.CreateServer(ctx, domain.Server{Name: "bad", TokenSHA256: []byte("short")}); err == nil {
		t.Fatal("CreateServer(short hash) error = nil")
	}
	if err := store.UpdateServerTokenHash(ctx, 999, []byte("short")); err == nil {
		t.Fatal("UpdateServerTokenHash(short hash) error = nil")
	}
	if err := store.UpdateServer(ctx, domain.Server{ID: 999, Name: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateServer(missing) error = %v, want ErrNotFound", err)
	}
	if err := store.UpdateServerTokenHash(ctx, 999, serverHash(7)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateServerTokenHash(missing) error = %v, want ErrNotFound", err)
	}
	if err := store.DeleteServer(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteServer(missing) error = %v, want ErrNotFound", err)
	}
}
