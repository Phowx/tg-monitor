package domain

type AlertKind string

const (
	AlertOffline  AlertKind = "offline"
	AlertRecovery AlertKind = "recovery"
)

type AlertPayload struct {
	Version        int    `json:"version"`
	ServerName     string `json:"server_name"`
	ServerGroup    string `json:"server_group,omitempty"`
	OfflineSinceMS int64  `json:"offline_since_ms"`
	EventAtMS      int64  `json:"event_at_ms"`
	RecoveredAtMS  int64  `json:"recovered_at_ms,omitempty"`
}

type AlertOutboxItem struct {
	ID              int64
	ServerID        *int64
	TelegramUserID  int64
	Kind            AlertKind
	PayloadJSON     string
	Attempts        int
	NextAttemptAtMS int64
	CreatedAtMS     int64
}

type AlertEvaluationResult struct {
	Offline  int
	Recovery int
	Queued   int
}
