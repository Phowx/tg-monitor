package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const maxDueAlertOutboxBatch = 100

func (s *Store) ListDueAlertOutbox(ctx context.Context, nowMS int64, limit int) ([]domain.AlertOutboxItem, error) {
	if nowMS <= 0 {
		return nil, errors.New("list due alert outbox: time must be positive")
	}
	if limit < 1 || limit > maxDueAlertOutboxBatch {
		return nil, fmt.Errorf("list due alert outbox: limit must be between 1 and %d", maxDueAlertOutboxBatch)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT current.id, current.server_id, current.telegram_user_id, current.kind,
			current.payload_json, current.attempts, current.next_attempt_at_ms, current.created_at_ms
		FROM alert_outbox current
		WHERE current.delivered_at_ms IS NULL
			AND current.next_attempt_at_ms <= ?
			AND NOT EXISTS (
				SELECT 1 FROM alert_outbox earlier
				WHERE earlier.telegram_user_id = current.telegram_user_id
					AND earlier.server_id IS current.server_id
					AND earlier.delivered_at_ms IS NULL
					AND earlier.id < current.id
			)
		ORDER BY current.id
		LIMIT ?`, nowMS, limit)
	if err != nil {
		return nil, fmt.Errorf("list due alert outbox: %w", err)
	}
	defer rows.Close()

	items := make([]domain.AlertOutboxItem, 0)
	for rows.Next() {
		var item domain.AlertOutboxItem
		var serverID sql.NullInt64
		var kind string
		if err := rows.Scan(
			&item.ID, &serverID, &item.TelegramUserID, &kind,
			&item.PayloadJSON, &item.Attempts, &item.NextAttemptAtMS, &item.CreatedAtMS,
		); err != nil {
			return nil, fmt.Errorf("list due alert outbox: scan row: %w", err)
		}
		if serverID.Valid {
			value := serverID.Int64
			item.ServerID = &value
		}
		item.Kind = domain.AlertKind(kind)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list due alert outbox: %w", err)
	}
	return items, nil
}

func (s *Store) MarkAlertDelivered(ctx context.Context, id, deliveredAtMS int64) error {
	if id <= 0 || deliveredAtMS <= 0 {
		return errors.New("mark alert delivered: ID and time must be positive")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE alert_outbox SET delivered_at_ms = ?, last_error = ''
		WHERE id = ? AND delivered_at_ms IS NULL`, deliveredAtMS, id)
	if err != nil {
		return fmt.Errorf("mark alert delivered %d: %w", id, err)
	}
	return requireAffected(result, fmt.Sprintf("mark alert delivered %d", id))
}

func (s *Store) MarkAlertSuppressed(ctx context.Context, id, suppressedAtMS int64, class string) error {
	if id <= 0 || suppressedAtMS <= 0 {
		return errors.New("mark alert suppressed: ID and time must be positive")
	}
	if !terminalAlertClass(class) {
		return errors.New("mark alert suppressed: invalid class")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE alert_outbox SET delivered_at_ms = ?, last_error = ?
		WHERE id = ? AND delivered_at_ms IS NULL`, suppressedAtMS, class, id)
	if err != nil {
		return fmt.Errorf("mark alert suppressed %d: %w", id, err)
	}
	return requireAffected(result, fmt.Sprintf("mark alert suppressed %d", id))
}

func (s *Store) MarkAlertFailed(ctx context.Context, id, failedAtMS, nextAttemptAtMS int64, class string) error {
	if id <= 0 || failedAtMS <= 0 || nextAttemptAtMS <= failedAtMS {
		return errors.New("mark alert failed: invalid ID or retry time")
	}
	if class != alertRetryFailure {
		return errors.New("mark alert failed: invalid class")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE alert_outbox
		SET attempts = attempts + 1, next_attempt_at_ms = ?, last_error = ?
		WHERE id = ? AND delivered_at_ms IS NULL`, nextAttemptAtMS, class, id)
	if err != nil {
		return fmt.Errorf("mark alert failed %d: %w", id, err)
	}
	return requireAffected(result, fmt.Sprintf("mark alert failed %d", id))
}

func (s *Store) DeleteTerminalAlertOutboxBefore(ctx context.Context, cutoffMS int64) (int64, error) {
	if cutoffMS <= 0 {
		return 0, errors.New("delete terminal alert outbox: cutoff must be positive")
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM alert_outbox
		WHERE delivered_at_ms IS NOT NULL AND delivered_at_ms < ?`, cutoffMS)
	if err != nil {
		return 0, fmt.Errorf("delete terminal alert outbox: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete terminal alert outbox: read affected rows: %w", err)
	}
	return deleted, nil
}
