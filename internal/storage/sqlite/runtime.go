package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}
	return nil
}

func (s *Store) GetServerByTokenHash(ctx context.Context, hash []byte) (domain.Server, error) {
	if len(hash) != sha256.Size {
		return domain.Server{}, fmt.Errorf("get server by token hash: token hash must be %d bytes", sha256.Size)
	}

	server, err := scanServer(s.db.QueryRowContext(ctx,
		`SELECT `+serverColumns+` FROM servers WHERE token_sha256 = ?`, hash,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Server{}, fmt.Errorf("get server by token hash: %w", ErrNotFound)
	}
	if err != nil {
		return domain.Server{}, fmt.Errorf("get server by token hash: %w", err)
	}
	return server, nil
}
