package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const (
	alertPayloadVersion           = 1
	maxStoredAlertPayloadBytes    = 4 << 10
	maxStoredAlertFieldBytes      = 120
	alertSuppressedServerDisabled = "server_disabled"
	alertSuppressedRecipientOff   = "recipient_disabled"
	alertSuppressedRecipientGone  = "recipient_removed"
	alertSuppressedInvalidPayload = "invalid_payload"
	alertRetryFailure             = "telegram_send_failed"
)

type alertStateSnapshot struct {
	Exists         bool
	OfflineSinceMS sql.NullInt64
	AlertSentAtMS  sql.NullInt64
	RecoveredAtMS  sql.NullInt64
	UpdatedAtMS    int64
}

type alertServerSnapshot struct {
	ID          int64
	Name        string
	Group       string
	Enabled     bool
	CreatedAtMS int64
	ReceivedAt  sql.NullInt64
	State       alertStateSnapshot
}

func (s *Store) EvaluateAlerts(ctx context.Context, nowMS int64, adminTelegramIDs []int64) (domain.AlertEvaluationResult, error) {
	if nowMS <= 0 {
		return domain.AlertEvaluationResult{}, errors.New("evaluate alerts: time must be positive")
	}
	adminIDs, err := validatedAlertAdminIDs(adminTelegramIDs)
	if err != nil {
		return domain.AlertEvaluationResult{}, err
	}

	transaction, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AlertEvaluationResult{}, fmt.Errorf("evaluate alerts: begin transaction: %w", err)
	}
	defer transaction.Rollback()

	alertThresholdMS, err := readAlertThresholdMS(ctx, transaction)
	if err != nil {
		return domain.AlertEvaluationResult{}, err
	}
	recipients, err := enabledAlertRecipients(ctx, transaction, adminIDs)
	if err != nil {
		return domain.AlertEvaluationResult{}, err
	}
	servers, err := readAlertServerSnapshots(ctx, transaction)
	if err != nil {
		return domain.AlertEvaluationResult{}, err
	}

	var result domain.AlertEvaluationResult
	for _, server := range servers {
		if !server.Enabled {
			if server.State.Exists && (server.State.OfflineSinceMS.Valid || server.State.AlertSentAtMS.Valid) {
				if err := resetAlertState(ctx, transaction, server.ID, nowMS); err != nil {
					return domain.AlertEvaluationResult{}, err
				}
				if err := suppressPendingServerAlerts(ctx, transaction, server.ID, nowMS); err != nil {
					return domain.AlertEvaluationResult{}, err
				}
			}
			continue
		}

		if server.State.OfflineSinceMS.Valid && server.State.AlertSentAtMS.Valid {
			if server.ReceivedAt.Valid && server.ReceivedAt.Int64 > server.State.OfflineSinceMS.Int64 {
				recoveryRecipients, err := eligibleRecoveryRecipients(ctx, transaction, server.ID, server.State.AlertSentAtMS.Int64, recipients)
				if err != nil {
					return domain.AlertEvaluationResult{}, err
				}
				payloadJSON, err := marshalStoredAlertPayload(domain.AlertPayload{
					Version: alertPayloadVersion, ServerName: server.Name, ServerGroup: server.Group,
					OfflineSinceMS: server.State.OfflineSinceMS.Int64, EventAtMS: nowMS, RecoveredAtMS: nowMS,
				})
				if err != nil {
					return domain.AlertEvaluationResult{}, err
				}
				queued, err := enqueueAlertRows(ctx, transaction, server.ID, recoveryRecipients, domain.AlertRecovery, payloadJSON, nowMS)
				if err != nil {
					return domain.AlertEvaluationResult{}, err
				}
				if err := recoverAlertState(ctx, transaction, server.ID, nowMS); err != nil {
					return domain.AlertEvaluationResult{}, err
				}
				result.Recovery++
				result.Queued += queued
			}
			continue
		}

		baselineMS := server.CreatedAtMS
		if server.ReceivedAt.Valid {
			baselineMS = server.ReceivedAt.Int64
		}
		if server.State.Exists && !server.State.RecoveredAtMS.Valid && server.State.UpdatedAtMS > baselineMS {
			baselineMS = server.State.UpdatedAtMS
		}
		if nowMS < baselineMS || nowMS-baselineMS < alertThresholdMS {
			continue
		}

		payloadJSON, err := marshalStoredAlertPayload(domain.AlertPayload{
			Version: alertPayloadVersion, ServerName: server.Name, ServerGroup: server.Group,
			OfflineSinceMS: baselineMS, EventAtMS: nowMS,
		})
		if err != nil {
			return domain.AlertEvaluationResult{}, err
		}
		queued, err := enqueueAlertRows(ctx, transaction, server.ID, recipients, domain.AlertOffline, payloadJSON, nowMS)
		if err != nil {
			return domain.AlertEvaluationResult{}, err
		}
		if err := offlineAlertState(ctx, transaction, server.ID, baselineMS, nowMS); err != nil {
			return domain.AlertEvaluationResult{}, err
		}
		result.Offline++
		result.Queued += queued
	}

	if err := transaction.Commit(); err != nil {
		return domain.AlertEvaluationResult{}, fmt.Errorf("evaluate alerts: commit: %w", err)
	}
	return result, nil
}

func validatedAlertAdminIDs(ids []int64) ([]int64, error) {
	validated := append([]int64(nil), ids...)
	sort.Slice(validated, func(left, right int) bool { return validated[left] < validated[right] })
	for index, id := range validated {
		if id <= 0 {
			return nil, errors.New("evaluate alerts: administrator IDs must be positive")
		}
		if index > 0 && id == validated[index-1] {
			return nil, errors.New("evaluate alerts: administrator IDs must be unique")
		}
	}
	return validated, nil
}

func readAlertThresholdMS(ctx context.Context, transaction *sql.Tx) (int64, error) {
	var seconds int64
	if err := transaction.QueryRowContext(ctx, `SELECT alert_threshold_seconds FROM settings WHERE id = 1`).Scan(&seconds); err != nil {
		return 0, fmt.Errorf("evaluate alerts: read threshold: %w", err)
	}
	if seconds <= 0 || seconds > math.MaxInt64/1_000 {
		return 0, errors.New("evaluate alerts: invalid threshold")
	}
	return seconds * 1_000, nil
}

func enabledAlertRecipients(ctx context.Context, transaction *sql.Tx, adminIDs []int64) ([]int64, error) {
	recipients := make([]int64, 0, len(adminIDs))
	for _, id := range adminIDs {
		var enabled bool
		err := transaction.QueryRowContext(ctx, `SELECT alerts_enabled FROM user_preferences WHERE telegram_user_id = ?`, id).Scan(&enabled)
		if errors.Is(err, sql.ErrNoRows) {
			recipients = append(recipients, id)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("evaluate alerts: read recipient preference: %w", err)
		}
		if enabled {
			recipients = append(recipients, id)
		}
	}
	return recipients, nil
}

func readAlertServerSnapshots(ctx context.Context, transaction *sql.Tx) ([]alertServerSnapshot, error) {
	rows, err := transaction.QueryContext(ctx, `
		SELECT s.id, s.name, s.group_name, s.enabled, s.created_at_ms, l.received_at_ms,
			a.server_id, a.offline_since_ms, a.alert_sent_at_ms, a.recovered_at_ms, a.updated_at_ms
		FROM servers s
		LEFT JOIN latest_metrics l ON l.server_id = s.id
		LEFT JOIN alert_states a ON a.server_id = s.id
		ORDER BY s.sort_order, s.name, s.id`)
	if err != nil {
		return nil, fmt.Errorf("evaluate alerts: read servers: %w", err)
	}
	defer rows.Close()

	servers := make([]alertServerSnapshot, 0)
	for rows.Next() {
		var server alertServerSnapshot
		var stateServerID sql.NullInt64
		var stateUpdatedAt sql.NullInt64
		if err := rows.Scan(
			&server.ID, &server.Name, &server.Group, &server.Enabled, &server.CreatedAtMS, &server.ReceivedAt,
			&stateServerID, &server.State.OfflineSinceMS, &server.State.AlertSentAtMS, &server.State.RecoveredAtMS, &stateUpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("evaluate alerts: scan server: %w", err)
		}
		server.State.Exists = stateServerID.Valid
		if stateUpdatedAt.Valid {
			server.State.UpdatedAtMS = stateUpdatedAt.Int64
		}
		servers = append(servers, server)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("evaluate alerts: read servers: %w", err)
	}
	return servers, nil
}

func eligibleRecoveryRecipients(ctx context.Context, transaction *sql.Tx, serverID, episodeAtMS int64, currentRecipients []int64) ([]int64, error) {
	current := make(map[int64]struct{}, len(currentRecipients))
	for _, id := range currentRecipients {
		current[id] = struct{}{}
	}
	rows, err := transaction.QueryContext(ctx, `
		SELECT telegram_user_id, last_error FROM alert_outbox
		WHERE server_id = ? AND kind = ? AND created_at_ms = ?
		ORDER BY telegram_user_id, id`, serverID, domain.AlertOffline, episodeAtMS)
	if err != nil {
		return nil, fmt.Errorf("evaluate alerts: read offline recipients: %w", err)
	}
	defer rows.Close()

	recipients := make([]int64, 0, len(currentRecipients))
	seen := make(map[int64]struct{}, len(currentRecipients))
	for rows.Next() {
		var id int64
		var lastError string
		if err := rows.Scan(&id, &lastError); err != nil {
			return nil, fmt.Errorf("evaluate alerts: scan offline recipient: %w", err)
		}
		if _, present := current[id]; !present || terminalAlertClass(lastError) {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		recipients = append(recipients, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("evaluate alerts: read offline recipients: %w", err)
	}
	return recipients, nil
}

func terminalAlertClass(class string) bool {
	switch class {
	case alertSuppressedServerDisabled, alertSuppressedRecipientOff, alertSuppressedRecipientGone, alertSuppressedInvalidPayload:
		return true
	default:
		return false
	}
}

func marshalStoredAlertPayload(payload domain.AlertPayload) (string, error) {
	if strings.TrimSpace(payload.ServerName) == "" || len(payload.ServerName) > maxStoredAlertFieldBytes || len(payload.ServerGroup) > maxStoredAlertFieldBytes {
		return "", errors.New("evaluate alerts: invalid server metadata")
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > maxStoredAlertPayloadBytes {
		return "", errors.New("evaluate alerts: invalid payload")
	}
	return string(encoded), nil
}

func enqueueAlertRows(ctx context.Context, transaction *sql.Tx, serverID int64, recipients []int64, kind domain.AlertKind, payloadJSON string, nowMS int64) (int, error) {
	for _, recipient := range recipients {
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO alert_outbox(
				server_id, telegram_user_id, kind, payload_json, attempts,
				next_attempt_at_ms, delivered_at_ms, created_at_ms, last_error
			) VALUES(?, ?, ?, ?, 0, ?, NULL, ?, '')`,
			serverID, recipient, kind, payloadJSON, nowMS, nowMS,
		); err != nil {
			return 0, fmt.Errorf("evaluate alerts: enqueue %s: %w", kind, err)
		}
	}
	return len(recipients), nil
}

func offlineAlertState(ctx context.Context, transaction *sql.Tx, serverID, offlineSinceMS, nowMS int64) error {
	_, err := transaction.ExecContext(ctx, `
		INSERT INTO alert_states(server_id, offline_since_ms, alert_sent_at_ms, recovered_at_ms, updated_at_ms)
		VALUES(?, ?, ?, NULL, ?)
		ON CONFLICT(server_id) DO UPDATE SET
			offline_since_ms = excluded.offline_since_ms,
			alert_sent_at_ms = excluded.alert_sent_at_ms,
			recovered_at_ms = NULL,
			updated_at_ms = excluded.updated_at_ms`,
		serverID, offlineSinceMS, nowMS, nowMS,
	)
	if err != nil {
		return fmt.Errorf("evaluate alerts: record offline state: %w", err)
	}
	return nil
}

func recoverAlertState(ctx context.Context, transaction *sql.Tx, serverID, nowMS int64) error {
	_, err := transaction.ExecContext(ctx, `
		UPDATE alert_states
		SET offline_since_ms = NULL, alert_sent_at_ms = NULL, recovered_at_ms = ?, updated_at_ms = ?
		WHERE server_id = ?`, nowMS, nowMS, serverID)
	if err != nil {
		return fmt.Errorf("evaluate alerts: record recovery state: %w", err)
	}
	return nil
}

func resetAlertState(ctx context.Context, transaction *sql.Tx, serverID, nowMS int64) error {
	_, err := transaction.ExecContext(ctx, `
		INSERT INTO alert_states(server_id, offline_since_ms, alert_sent_at_ms, recovered_at_ms, updated_at_ms)
		VALUES(?, NULL, NULL, NULL, ?)
		ON CONFLICT(server_id) DO UPDATE SET
			offline_since_ms = NULL,
			alert_sent_at_ms = NULL,
			recovered_at_ms = NULL,
			updated_at_ms = excluded.updated_at_ms`, serverID, nowMS)
	if err != nil {
		return fmt.Errorf("reset alert state: %w", err)
	}
	return nil
}

func suppressPendingServerAlerts(ctx context.Context, transaction *sql.Tx, serverID, nowMS int64) error {
	_, err := transaction.ExecContext(ctx, `
		UPDATE alert_outbox
		SET delivered_at_ms = ?, last_error = ?
		WHERE server_id = ? AND delivered_at_ms IS NULL`,
		nowMS, alertSuppressedServerDisabled, serverID,
	)
	if err != nil {
		return fmt.Errorf("suppress pending server alerts: %w", err)
	}
	return nil
}
