package auth

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/storage/sqlite"
)

type recordingServerLookup struct {
	server domain.Server
	err    error
	hash   []byte
}

func (lookup *recordingServerLookup) GetServerByTokenHash(_ context.Context, hash []byte) (domain.Server, error) {
	lookup.hash = append([]byte(nil), hash...)
	return lookup.server, lookup.err
}

func TestAuthenticatorAcceptsEnabledServerWithCaseInsensitiveBearer(t *testing.T) {
	lookup := &recordingServerLookup{server: domain.Server{ID: 7, Name: "agent", Enabled: true}}
	authenticator := NewAuthenticator(lookup)

	got, err := authenticator.Authenticate(context.Background(), "bEaReR raw-agent-token")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if got.ID != 7 {
		t.Fatalf("Authenticate() server = %#v, want ID 7", got)
	}
	if !bytes.Equal(lookup.hash, HashToken("raw-agent-token")) {
		t.Fatalf("lookup hash = %x, want %x", lookup.hash, HashToken("raw-agent-token"))
	}
}

func TestAuthenticatorMapsMissingAndUnknownCredentialsToUnauthorized(t *testing.T) {
	tests := []struct {
		name   string
		header string
		lookup *recordingServerLookup
	}{
		{name: "missing header", header: "", lookup: &recordingServerLookup{}},
		{name: "unknown token", header: "Bearer unknown-token", lookup: &recordingServerLookup{err: sqlite.ErrNotFound}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewAuthenticator(tt.lookup).Authenticate(context.Background(), tt.header)
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("Authenticate() error = %v, want ErrUnauthorized", err)
			}
		})
	}
}

func TestAuthenticatorRejectsMalformedAuthorization(t *testing.T) {
	for _, header := range []string{"Bearer", "Basic token", "Bearer token extra"} {
		t.Run(header, func(t *testing.T) {
			_, err := NewAuthenticator(&recordingServerLookup{}).Authenticate(context.Background(), header)
			if !errors.Is(err, ErrMalformedAuthorization) {
				t.Fatalf("Authenticate(%q) error = %v, want ErrMalformedAuthorization", header, err)
			}
		})
	}
}

func TestAuthenticatorRejectsDisabledServer(t *testing.T) {
	lookup := &recordingServerLookup{server: domain.Server{ID: 7, Enabled: false}}
	_, err := NewAuthenticator(lookup).Authenticate(context.Background(), "Bearer disabled-token")
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("Authenticate() error = %v, want ErrDisabled", err)
	}
}

func TestAuthenticatorPropagatesStorageFailureWithoutCredentialLeak(t *testing.T) {
	storageErr := errors.New("database unavailable")
	lookup := &recordingServerLookup{err: storageErr}
	raw := "super-secret-agent-token"
	_, err := NewAuthenticator(lookup).Authenticate(context.Background(), "Bearer "+raw)
	if !errors.Is(err, storageErr) {
		t.Fatalf("Authenticate() error = %v, want wrapped storage error", err)
	}
	hash := HashToken(raw)
	if strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), string(hash)) {
		t.Fatalf("Authenticate() leaked credentials: %v", err)
	}
}
