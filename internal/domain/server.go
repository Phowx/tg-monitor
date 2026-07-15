package domain

import "errors"

var (
	ErrNotFound       = errors.New("not found")
	ErrSessionExpired = errors.New("session expired")
)

type Server struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Group       string `json:"group"`
	SortOrder   int    `json:"sort_order"`
	Enabled     bool   `json:"enabled"`
	TokenSHA256 []byte `json:"-"`
	CreatedAtMS int64  `json:"created_at"`
	UpdatedAtMS int64  `json:"updated_at"`
}

type Settings struct {
	OfflineThresholdSeconds int64 `json:"offline_threshold_seconds"`
	AlertThresholdSeconds   int64 `json:"alert_threshold_seconds"`
	HistoryRetentionDays    int64 `json:"history_retention_days"`
}

type Session struct {
	TelegramUserID int64 `json:"telegram_user_id"`
	CreatedAtMS    int64 `json:"created_at"`
	ExpiresAtMS    int64 `json:"expires_at"`
}
