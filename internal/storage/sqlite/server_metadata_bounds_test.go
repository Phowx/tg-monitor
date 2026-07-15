package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestServerRepositoryRejectsMetadataBeyondAlertPayloadBounds(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	store.nowMS = func() int64 { return 1_000 }
	valid := domain.Server{Name: "node", Group: "group", Enabled: true, TokenSHA256: serverHash(0x51)}
	created, err := store.CreateServer(ctx, valid)
	if err != nil {
		t.Fatalf("CreateServer(valid) error = %v", err)
	}

	for name, input := range map[string]domain.Server{
		"long create name":  {Name: strings.Repeat("n", 121), TokenSHA256: serverHash(0x52)},
		"long create group": {Name: "node-2", Group: strings.Repeat("g", 121), TokenSHA256: serverHash(0x53)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.CreateServer(ctx, input); err == nil {
				t.Fatal("CreateServer(oversized metadata) error = nil")
			}
		})
	}

	for name, mutate := range map[string]func(*domain.Server){
		"long update name":  func(server *domain.Server) { server.Name = strings.Repeat("n", 121) },
		"long update group": func(server *domain.Server) { server.Group = strings.Repeat("g", 121) },
	} {
		t.Run(name, func(t *testing.T) {
			input := created
			mutate(&input)
			if err := store.UpdateServer(ctx, input); err == nil {
				t.Fatal("UpdateServer(oversized metadata) error = nil")
			}
			stored, err := store.GetServer(ctx, created.ID)
			if err != nil || stored.Name != valid.Name || stored.Group != valid.Group {
				t.Fatalf("GetServer() = %#v, %v; want unchanged metadata", stored, err)
			}
		})
	}
}
