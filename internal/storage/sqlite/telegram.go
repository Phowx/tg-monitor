package sqlite

import (
	"context"
	"errors"
	"fmt"
)

func (s *Store) RecordTelegramUpdate(ctx context.Context, updateID, receivedAtMS int64) (bool, error) {
	if updateID <= 0 {
		return false, errors.New("record Telegram update: update ID must be positive")
	}
	if receivedAtMS <= 0 {
		return false, errors.New("record Telegram update: received time must be positive")
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_updates(update_id, received_at_ms)
		VALUES(?, ?)
		ON CONFLICT(update_id) DO NOTHING`,
		updateID,
		receivedAtMS,
	)
	if err != nil {
		return false, fmt.Errorf("record Telegram update: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("record Telegram update: read affected rows: %w", err)
	}
	return affected == 1, nil
}

func (s *Store) DeleteTelegramUpdatesBefore(ctx context.Context, cutoffMS int64) (int64, error) {
	if cutoffMS <= 0 {
		return 0, errors.New("delete Telegram updates: cutoff must be positive")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM telegram_updates WHERE received_at_ms < ?`, cutoffMS)
	if err != nil {
		return 0, fmt.Errorf("delete Telegram updates: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete Telegram updates: read affected rows: %w", err)
	}
	return deleted, nil
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, nowMS int64) (int64, error) {
	if nowMS <= 0 {
		return 0, errors.New("delete expired sessions: current time must be positive")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at_ms <= ?`, nowMS)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: read affected rows: %w", err)
	}
	return deleted, nil
}
