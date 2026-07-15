package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const serverColumns = `id, name, group_name, sort_order, enabled, token_sha256, created_at_ms, updated_at_ms`

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) CreateServer(ctx context.Context, server domain.Server) (domain.Server, error) {
	if err := validateServerMetadata(server.Name, server.Group); err != nil {
		return domain.Server{}, fmt.Errorf("create server: %w", err)
	}
	if len(server.TokenSHA256) != sha256.Size {
		return domain.Server{}, fmt.Errorf("create server: token hash must be %d bytes", sha256.Size)
	}

	now := s.nowMS()
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO servers(name, group_name, sort_order, enabled, token_sha256, created_at_ms, updated_at_ms)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		RETURNING `+serverColumns,
		server.Name, server.Group, server.SortOrder, server.Enabled, server.TokenSHA256, now, now,
	)
	created, err := scanServer(row)
	if err != nil {
		return domain.Server{}, fmt.Errorf("create server: %w", err)
	}
	return created, nil
}

func (s *Store) ListServers(ctx context.Context) ([]domain.Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+serverColumns+` FROM servers ORDER BY sort_order, name, id`)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	defer rows.Close()

	servers := make([]domain.Server, 0)
	for rows.Next() {
		server, err := scanServer(rows)
		if err != nil {
			return nil, fmt.Errorf("list servers: scan row: %w", err)
		}
		servers = append(servers, server)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	return servers, nil
}

func (s *Store) GetServer(ctx context.Context, id int64) (domain.Server, error) {
	server, err := scanServer(s.db.QueryRowContext(ctx, `SELECT `+serverColumns+` FROM servers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Server{}, fmt.Errorf("get server %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return domain.Server{}, fmt.Errorf("get server %d: %w", id, err)
	}
	return server, nil
}

func (s *Store) UpdateServer(ctx context.Context, server domain.Server) error {
	if err := validateServerMetadata(server.Name, server.Group); err != nil {
		return fmt.Errorf("update server: %w", err)
	}

	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("update server %d: begin transaction: %w", server.ID, err)
	}
	defer transaction.Rollback()

	var currentEnabled bool
	if err := transaction.QueryRowContext(ctx, `SELECT enabled FROM servers WHERE id = ?`, server.ID).Scan(&currentEnabled); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("update server %d: %w", server.ID, ErrNotFound)
	} else if err != nil {
		return fmt.Errorf("update server %d: read current state: %w", server.ID, err)
	}

	nowMS := s.nowMS()
	result, err := transaction.ExecContext(ctx, `
		UPDATE servers
		SET name = ?, group_name = ?, sort_order = ?, enabled = ?, updated_at_ms = ?
		WHERE id = ?`,
		server.Name, server.Group, server.SortOrder, server.Enabled, nowMS, server.ID,
	)
	if err != nil {
		return fmt.Errorf("update server %d: %w", server.ID, err)
	}
	if err := requireAffected(result, fmt.Sprintf("update server %d", server.ID)); err != nil {
		return err
	}

	if currentEnabled != server.Enabled {
		if err := resetAlertState(ctx, transaction, server.ID, nowMS); err != nil {
			return fmt.Errorf("update server %d: %w", server.ID, err)
		}
		if !server.Enabled {
			if err := suppressPendingServerAlerts(ctx, transaction, server.ID, nowMS); err != nil {
				return fmt.Errorf("update server %d: %w", server.ID, err)
			}
		}
	}

	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("update server %d: commit: %w", server.ID, err)
	}
	return nil
}

func (s *Store) UpdateServerTokenHash(ctx context.Context, id int64, hash []byte) error {
	if len(hash) != sha256.Size {
		return fmt.Errorf("update server token hash: token hash must be %d bytes", sha256.Size)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE servers SET token_sha256 = ?, updated_at_ms = ? WHERE id = ?`, hash, s.nowMS(), id)
	if err != nil {
		return fmt.Errorf("update server token hash for server %d: %w", id, err)
	}
	return requireAffected(result, fmt.Sprintf("update server token hash for server %d", id))
}

func (s *Store) DeleteServer(ctx context.Context, id int64) error {
	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("delete server %d: begin transaction: %w", id, err)
	}
	defer transaction.Rollback()

	nowMS := s.nowMS()
	if err := suppressPendingServerAlerts(ctx, transaction, id, nowMS); err != nil {
		return fmt.Errorf("delete server %d: %w", id, err)
	}
	result, err := transaction.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete server %d: %w", id, err)
	}
	if err := requireAffected(result, fmt.Sprintf("delete server %d", id)); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("delete server %d: commit: %w", id, err)
	}
	return nil
}

func scanServer(scanner rowScanner) (domain.Server, error) {
	var server domain.Server
	err := scanner.Scan(
		&server.ID,
		&server.Name,
		&server.Group,
		&server.SortOrder,
		&server.Enabled,
		&server.TokenSHA256,
		&server.CreatedAtMS,
		&server.UpdatedAtMS,
	)
	return server, err
}

func validateServerMetadata(name, group string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	if len(name) > maxStoredAlertFieldBytes || len(group) > maxStoredAlertFieldBytes {
		return fmt.Errorf("name and group must contain at most %d bytes", maxStoredAlertFieldBytes)
	}
	return nil
}

func requireAffected(result sql.Result, operation string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: read affected rows: %w", operation, err)
	}
	if affected == 0 {
		return fmt.Errorf("%s: %w", operation, ErrNotFound)
	}
	return nil
}
