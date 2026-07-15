package alerting

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const (
	alertPayloadVersion  = 1
	maxAlertPayloadBytes = 4 << 10
	maxAlertFieldBytes   = 120
	alertTimestampLayout = "2006-01-02 15:04:05 UTC"
	maxRetryDelay        = 15 * time.Minute
)

var errInvalidAlert = errors.New("render alert: invalid payload")

func Render(kind domain.AlertKind, payloadJSON string) (string, error) {
	payload, err := decodePayload(payloadJSON)
	if err != nil || !validPayload(kind, payload) {
		return "", errInvalidAlert
	}

	serverName := displayField(payload.ServerName)
	serverGroup := displayField(payload.ServerGroup)
	var message strings.Builder
	switch kind {
	case domain.AlertOffline:
		fmt.Fprintf(&message, "🔴 %s 已离线\n", serverName)
		writeGroup(&message, serverGroup)
		fmt.Fprintf(&message, "最后上报：%s\n告警等待：%s",
			formatTimestamp(payload.OfflineSinceMS),
			formatDuration(payload.EventAtMS-payload.OfflineSinceMS),
		)
	case domain.AlertRecovery:
		fmt.Fprintf(&message, "🟢 %s 已恢复\n", serverName)
		writeGroup(&message, serverGroup)
		fmt.Fprintf(&message, "恢复时间：%s\n中断时长：%s",
			formatTimestamp(payload.RecoveredAtMS),
			formatDuration(payload.RecoveredAtMS-payload.OfflineSinceMS),
		)
	default:
		return "", errInvalidAlert
	}
	return message.String(), nil
}

func RetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 5 * time.Second
	}
	shift := min(attempt-1, 8)
	delay := 5 * time.Second * time.Duration(1<<shift)
	return min(delay, maxRetryDelay)
}

func decodePayload(raw string) (domain.AlertPayload, error) {
	if len(raw) == 0 || len(raw) > maxAlertPayloadBytes {
		return domain.AlertPayload{}, errInvalidAlert
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var payload domain.AlertPayload
	if err := decoder.Decode(&payload); err != nil {
		return domain.AlertPayload{}, errInvalidAlert
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return domain.AlertPayload{}, errInvalidAlert
	}
	return payload, nil
}

func validPayload(kind domain.AlertKind, payload domain.AlertPayload) bool {
	name := displayField(payload.ServerName)
	if payload.Version != alertPayloadVersion || name == "" || len(payload.ServerName) > maxAlertFieldBytes || len(payload.ServerGroup) > maxAlertFieldBytes {
		return false
	}
	if payload.OfflineSinceMS <= 0 || payload.EventAtMS < payload.OfflineSinceMS || !validDurationMS(payload.EventAtMS-payload.OfflineSinceMS) {
		return false
	}
	switch kind {
	case domain.AlertOffline:
		return payload.RecoveredAtMS == 0
	case domain.AlertRecovery:
		return payload.RecoveredAtMS >= payload.OfflineSinceMS && validDurationMS(payload.RecoveredAtMS-payload.OfflineSinceMS)
	default:
		return false
	}
}

func validDurationMS(durationMS int64) bool {
	return durationMS >= 0 && durationMS <= math.MaxInt64/int64(time.Millisecond)
}

func displayField(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func writeGroup(message *strings.Builder, group string) {
	if group != "" {
		fmt.Fprintf(message, "分组：%s\n", group)
	}
}

func formatTimestamp(timestampMS int64) string {
	return time.UnixMilli(timestampMS).UTC().Format(alertTimestampLayout)
}

func formatDuration(durationMS int64) string {
	total := durationMS / 1_000
	units := []struct {
		seconds int64
		suffix  string
	}{
		{seconds: 86_400, suffix: "天"},
		{seconds: 3_600, suffix: "小时"},
		{seconds: 60, suffix: "分钟"},
		{seconds: 1, suffix: "秒"},
	}
	parts := make([]string, 0, len(units))
	for _, unit := range units {
		value := total / unit.seconds
		if value > 0 || unit.seconds == 1 && len(parts) == 0 {
			parts = append(parts, fmt.Sprintf("%d %s", value, unit.suffix))
		}
		total %= unit.seconds
	}
	return strings.Join(parts, " ")
}
