package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/storage/sqlite"
)

var (
	ErrMalformedAuthorization = errors.New("malformed authorization")
	ErrUnauthorized           = errors.New("unauthorized")
	ErrDisabled               = errors.New("server disabled")
)

type ServerLookup interface {
	GetServerByTokenHash(context.Context, []byte) (domain.Server, error)
}

type Authenticator struct {
	lookup ServerLookup
}

func NewAuthenticator(lookup ServerLookup) *Authenticator {
	return &Authenticator{lookup: lookup}
}

func (authenticator *Authenticator) Authenticate(ctx context.Context, authorization string) (domain.Server, error) {
	if strings.TrimSpace(authorization) == "" {
		return domain.Server{}, ErrUnauthorized
	}

	fields := strings.Fields(authorization)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return domain.Server{}, ErrMalformedAuthorization
	}

	server, err := authenticator.lookup.GetServerByTokenHash(ctx, HashToken(fields[1]))
	if errors.Is(err, sqlite.ErrNotFound) {
		return domain.Server{}, ErrUnauthorized
	}
	if err != nil {
		return domain.Server{}, fmt.Errorf("authenticate agent: %w", err)
	}
	if !server.Enabled {
		return domain.Server{}, ErrDisabled
	}
	return server, nil
}
