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

func (s *Store) GetSettings(ctx context.Context) (domain.Settings, error) {
	var settings domain.Settings
	err := s.db.QueryRowContext(ctx, `
		SELECT offline_threshold_seconds, alert_threshold_seconds, history_retention_days
		FROM settings WHERE id = 1`).Scan(
		&settings.OfflineThresholdSeconds,
		&settings.AlertThresholdSeconds,
		&settings.HistoryRetentionDays,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Settings{}, fmt.Errorf("get settings: %w", ErrNotFound)
	}
	if err != nil {
		return domain.Settings{}, fmt.Errorf("get settings: %w", err)
	}
	return settings, nil
}

func (s *Store) UpdateSettings(ctx context.Context, settings domain.Settings) error {
	if settings.OfflineThresholdSeconds <= 0 || settings.AlertThresholdSeconds <= 0 || settings.HistoryRetentionDays <= 0 {
		return errors.New("update settings: thresholds and retention must be positive")
	}
	if settings.AlertThresholdSeconds < settings.OfflineThresholdSeconds {
		return errors.New("update settings: alert threshold must be at least the offline threshold")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE settings SET offline_threshold_seconds = ?, alert_threshold_seconds = ?,
		history_retention_days = ?, updated_at_ms = ? WHERE id = 1`,
		settings.OfflineThresholdSeconds,
		settings.AlertThresholdSeconds,
		settings.HistoryRetentionDays,
		s.nowMS(),
	)
	if err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	return requireAffected(result, "update settings")
}

func (s *Store) CreateSession(ctx context.Context, rawToken string, session domain.Session) error {
	if strings.TrimSpace(rawToken) == "" {
		return errors.New("create session: token is required")
	}
	if session.TelegramUserID <= 0 {
		return errors.New("create session: Telegram user ID must be positive")
	}
	if session.CreatedAtMS <= 0 || session.ExpiresAtMS <= session.CreatedAtMS {
		return errors.New("create session: expiry must be after creation")
	}
	hash := sha256.Sum256([]byte(rawToken))
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions(token_sha256, telegram_user_id, created_at_ms, expires_at_ms)
		VALUES(?, ?, ?, ?)`,
		hash[:], session.TelegramUserID, session.CreatedAtMS, session.ExpiresAtMS,
	); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *Store) GetSession(ctx context.Context, rawToken string, nowMS int64) (domain.Session, error) {
	if rawToken == "" {
		return domain.Session{}, fmt.Errorf("get session: %w", ErrNotFound)
	}
	hash := sha256.Sum256([]byte(rawToken))
	var session domain.Session
	err := s.db.QueryRowContext(ctx, `
		SELECT telegram_user_id, created_at_ms, expires_at_ms
		FROM sessions WHERE token_sha256 = ?`, hash[:]).Scan(
		&session.TelegramUserID,
		&session.CreatedAtMS,
		&session.ExpiresAtMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, fmt.Errorf("get session: %w", ErrNotFound)
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("get session: %w", err)
	}
	if session.ExpiresAtMS <= nowMS {
		return domain.Session{}, fmt.Errorf("get session: %w", ErrSessionExpired)
	}
	return session, nil
}

func (s *Store) DeleteSession(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return fmt.Errorf("delete session: %w", ErrNotFound)
	}
	hash := sha256.Sum256([]byte(rawToken))
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_sha256 = ?`, hash[:])
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return requireAffected(result, "delete session")
}

func (s *Store) GetAlertPreference(ctx context.Context, telegramUserID int64) (bool, error) {
	if telegramUserID <= 0 {
		return false, errors.New("get alert preference: Telegram user ID must be positive")
	}
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT alerts_enabled FROM user_preferences WHERE telegram_user_id = ?`, telegramUserID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get alert preference: %w", err)
	}
	return enabled, nil
}

func (s *Store) SetAlertPreference(ctx context.Context, telegramUserID int64, enabled bool) error {
	if telegramUserID <= 0 {
		return errors.New("set alert preference: Telegram user ID must be positive")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_preferences(telegram_user_id, alerts_enabled, updated_at_ms)
		VALUES(?, ?, ?)
		ON CONFLICT(telegram_user_id) DO UPDATE SET
			alerts_enabled = excluded.alerts_enabled,
			updated_at_ms = excluded.updated_at_ms`,
		telegramUserID, enabled, s.nowMS(),
	)
	if err != nil {
		return fmt.Errorf("set alert preference: %w", err)
	}
	return nil
}
