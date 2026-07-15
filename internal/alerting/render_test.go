package alerting

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func TestRenderOfflineAndRecoveryPlainText(t *testing.T) {
	offline := domain.AlertPayload{
		Version:        1,
		ServerName:     "node-1",
		ServerGroup:    "production",
		OfflineSinceMS: 1_784_080_000_000,
		EventAtMS:      1_784_080_120_000,
	}
	recovery := offline
	recovery.EventAtMS = 1_784_080_270_000
	recovery.RecoveredAtMS = 1_784_080_270_000

	assertRendered(t, domain.AlertOffline, offline, "🔴 node-1 已离线\n分组：production\n最后上报：2026-07-15 01:46:40 UTC\n告警等待：2 分钟")
	assertRendered(t, domain.AlertRecovery, recovery, "🟢 node-1 已恢复\n分组：production\n恢复时间：2026-07-15 01:51:10 UTC\n中断时长：4 分钟 30 秒")
}

func TestFormatDurationChinese(t *testing.T) {
	tests := []struct {
		milliseconds int64
		want         string
	}{
		{0, "0 秒"},
		{5_000, "5 秒"},
		{65_000, "1 分钟 5 秒"},
		{3_600_000, "1 小时"},
		{90_061_000, "1 天 1 小时 1 分钟 1 秒"},
	}
	for _, test := range tests {
		if got := formatDuration(test.milliseconds); got != test.want {
			t.Errorf("formatDuration(%d) = %q, want %q", test.milliseconds, got, test.want)
		}
	}
}

func TestRenderFlattensUntrustedDisplayWhitespaceAndOmitsEmptyGroup(t *testing.T) {
	payload := domain.AlertPayload{
		Version:        1,
		ServerName:     "  node\n\tone  ",
		ServerGroup:    " \r\n ",
		OfflineSinceMS: 1_784_080_000_000,
		EventAtMS:      1_784_080_120_000,
	}
	encoded := encodePayload(t, payload)
	got, err := Render(domain.AlertOffline, encoded)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.HasPrefix(got, "🔴 node one 已离线\n最后上报：") || strings.Contains(got, "分组：") {
		t.Fatalf("Render() = %q, want flattened name and no group", got)
	}
}

func TestRenderRejectsMalformedPayloadWithoutLeakingIt(t *testing.T) {
	valid := domain.AlertPayload{
		Version:        1,
		ServerName:     "node",
		OfflineSinceMS: 1_784_080_000_000,
		EventAtMS:      1_784_080_120_000,
	}
	tests := []struct {
		name    string
		kind    domain.AlertKind
		payload string
	}{
		{name: "unknown kind", kind: domain.AlertKind("other"), payload: encodePayload(t, valid)},
		{name: "unknown version", kind: domain.AlertOffline, payload: mutatePayload(t, valid, func(value *domain.AlertPayload) { value.Version = 2 })},
		{name: "empty name", kind: domain.AlertOffline, payload: mutatePayload(t, valid, func(value *domain.AlertPayload) { value.ServerName = " \n " })},
		{name: "long name", kind: domain.AlertOffline, payload: mutatePayload(t, valid, func(value *domain.AlertPayload) { value.ServerName = strings.Repeat("n", 121) })},
		{name: "long group", kind: domain.AlertOffline, payload: mutatePayload(t, valid, func(value *domain.AlertPayload) { value.ServerGroup = strings.Repeat("g", 121) })},
		{name: "missing offline time", kind: domain.AlertOffline, payload: mutatePayload(t, valid, func(value *domain.AlertPayload) { value.OfflineSinceMS = 0 })},
		{name: "event before offline", kind: domain.AlertOffline, payload: mutatePayload(t, valid, func(value *domain.AlertPayload) { value.EventAtMS = value.OfflineSinceMS - 1 })},
		{name: "offline with recovery time", kind: domain.AlertOffline, payload: mutatePayload(t, valid, func(value *domain.AlertPayload) { value.RecoveredAtMS = value.EventAtMS })},
		{name: "recovery without recovery time", kind: domain.AlertRecovery, payload: encodePayload(t, valid)},
		{name: "recovery before offline", kind: domain.AlertRecovery, payload: mutatePayload(t, valid, func(value *domain.AlertPayload) { value.RecoveredAtMS = value.OfflineSinceMS - 1 })},
		{name: "unknown field", kind: domain.AlertOffline, payload: `{"version":1,"server_name":"node","offline_since_ms":1784080000000,"event_at_ms":1784080120000,"TG_MONITOR_BOT_TOKEN":"SECRET-CANARY"}`},
		{name: "trailing JSON", kind: domain.AlertOffline, payload: encodePayload(t, valid) + ` {"secret":"SECRET-CANARY"}`},
		{name: "over payload limit", kind: domain.AlertOffline, payload: strings.Repeat("SECRET-CANARY", 400)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Render(test.kind, test.payload)
			if err == nil || got != "" {
				t.Fatalf("Render() = %q, %v; want empty safe failure", got, err)
			}
			if strings.Contains(err.Error(), "SECRET-CANARY") || strings.Contains(err.Error(), test.payload) {
				t.Fatalf("Render() error leaked payload detail: %q", err)
			}
		})
	}
}

func TestRetryDelayIsExponentialAndCapped(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: -1, want: 5 * time.Second},
		{attempt: 0, want: 5 * time.Second},
		{attempt: 1, want: 5 * time.Second},
		{attempt: 2, want: 10 * time.Second},
		{attempt: 3, want: 20 * time.Second},
		{attempt: 8, want: 10*time.Minute + 40*time.Second},
		{attempt: 9, want: 15 * time.Minute},
		{attempt: 99, want: 15 * time.Minute},
	}
	for _, test := range tests {
		if got := RetryDelay(test.attempt); got != test.want {
			t.Errorf("RetryDelay(%d) = %v, want %v", test.attempt, got, test.want)
		}
	}
}

func assertRendered(t *testing.T, kind domain.AlertKind, payload domain.AlertPayload, want string) {
	t.Helper()
	got, err := Render(kind, encodePayload(t, payload))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
}

func encodePayload(t *testing.T, payload domain.AlertPayload) string {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(encoded)
}

func mutatePayload(t *testing.T, payload domain.AlertPayload, mutate func(*domain.AlertPayload)) string {
	t.Helper()
	mutate(&payload)
	return encodePayload(t, payload)
}
