package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const (
	testPublicURL = "https://monitor.example.com"
	wantHelp      = "tg-monitor 管理员命令：\n/status - 查看服务器状态\n/app - 打开监控面板\n/alerts_on - 开启离线告警\n/alerts_off - 关闭离线告警\n/help - 显示帮助"
	wantPrompt    = "请发送 /help 查看可用命令。"
)

type commandRepositoryStub struct {
	servers           []domain.Server
	serversErr        error
	metrics           []domain.LatestMetrics
	metricsErr        error
	settings          domain.Settings
	settingsErr       error
	preferenceErr     error
	preferenceCalls   int
	preferenceUserID  int64
	preferenceEnabled bool
}

func (r *commandRepositoryStub) ListServers(context.Context) ([]domain.Server, error) {
	return r.servers, r.serversErr
}

func (r *commandRepositoryStub) ListLatestMetrics(context.Context) ([]domain.LatestMetrics, error) {
	return r.metrics, r.metricsErr
}

func (r *commandRepositoryStub) GetSettings(context.Context) (domain.Settings, error) {
	return r.settings, r.settingsErr
}

func (r *commandRepositoryStub) SetAlertPreference(_ context.Context, userID int64, enabled bool) error {
	r.preferenceCalls++
	r.preferenceUserID = userID
	r.preferenceEnabled = enabled
	return r.preferenceErr
}

func newTestCommander(t *testing.T, repository CommandRepository, now func() time.Time) *Commander {
	t.Helper()
	commander, err := NewCommander(repository, testPublicURL, now)
	if err != nil {
		t.Fatalf("NewCommander() error = %v", err)
	}
	return commander
}

func TestCommandHelpRouting(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "start", text: "/start", want: wantHelp},
		{name: "help", text: "/help", want: wantHelp},
		{name: "help with bot suffix", text: "/help@my_bot ignored", want: wantHelp},
		{name: "case insensitive", text: "/HELP@MY_BOT", want: wantHelp},
		{name: "unknown slash command", text: "/unknown secret text", want: wantHelp},
		{name: "non-command", text: "hello /status", want: wantPrompt},
		{name: "empty", text: "  \t\n", want: wantPrompt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commander := newTestCommander(t, &commandRepositoryStub{}, time.Now)
			got, err := commander.Reply(context.Background(), 42, tt.text)
			if err != nil {
				t.Fatalf("Reply() error = %v", err)
			}
			if got.Text != tt.want || got.WebAppURL != "" || got.ButtonText != "" {
				t.Fatalf("Reply() = %#v, want plain text %q", got, tt.want)
			}
		})
	}
}

func TestCommandAlertPreferences(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		wantEnabled bool
		wantReply   string
	}{
		{name: "enable", text: "/alerts_on extra", wantEnabled: true, wantReply: "离线告警已开启。"},
		{name: "disable with suffix", text: "/alerts_off@my_bot", wantEnabled: false, wantReply: "离线告警已关闭。"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository := &commandRepositoryStub{}
			commander := newTestCommander(t, repository, time.Now)
			got, err := commander.Reply(context.Background(), 987654, tt.text)
			if err != nil {
				t.Fatalf("Reply() error = %v", err)
			}
			if got != (Reply{Text: tt.wantReply}) {
				t.Fatalf("Reply() = %#v, want plain text %q", got, tt.wantReply)
			}
			if repository.preferenceCalls != 1 || repository.preferenceUserID != 987654 || repository.preferenceEnabled != tt.wantEnabled {
				t.Fatalf("SetAlertPreference calls = %d, user = %d, enabled = %v", repository.preferenceCalls, repository.preferenceUserID, repository.preferenceEnabled)
			}
		})
	}
}

func TestCommandAlertPreferenceErrorIsSafe(t *testing.T) {
	repository := &commandRepositoryStub{preferenceErr: errors.New("database leaked detail")}
	commander := newTestCommander(t, repository, time.Now)

	_, err := commander.Reply(context.Background(), 987654, "/alerts_on SECRET-TEXT")
	if err == nil {
		t.Fatal("Reply() error = nil")
	}
	if got := err.Error(); got != "telegram command alerts: update preference failed" {
		t.Fatalf("Reply() error = %q, want safe operation error", got)
	}
	for _, secret := range []string{"987654", "SECRET-TEXT", "database leaked detail"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("Reply() error %q contains sensitive value %q", err, secret)
		}
	}
}

func TestStatusReportsOrderedServerStates(t *testing.T) {
	const nowMS int64 = 1_700_000_000_000
	repository := &commandRepositoryStub{
		settings: domain.Settings{OfflineThresholdSeconds: 60},
		servers: []domain.Server{
			{ID: 1, Name: "fresh", Enabled: true},
			{ID: 2, Name: "boundary", Enabled: true},
			{ID: 3, Name: "stale", Enabled: true},
			{ID: 4, Name: "no-data", Enabled: true},
			{ID: 5, Name: "<node>&*_[]", Enabled: false},
		},
		metrics: []domain.LatestMetrics{
			latestMetrics(1, nowMS-5_000, 12.34, 50, 100),
			latestMetrics(2, nowMS-60_000, 34.56, 1, 4),
			latestMetrics(3, nowMS-61_000, 1.26, 3, 4),
			latestMetrics(5, nowMS-1_000, 4.44, 20, 40),
		},
	}
	commander := newTestCommander(t, repository, func() time.Time { return time.UnixMilli(nowMS) })

	reply, err := commander.Reply(context.Background(), 42, "/status@my_bot ignored")
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	want := strings.Join([]string{
		"服务器状态：",
		"【在线】 fresh — CPU 12.3%，内存 50.0%，5 秒前上报",
		"【在线】 boundary — CPU 34.6%，内存 25.0%，1 分钟前上报",
		"【离线】 stale — CPU 1.3%，内存 75.0%，1 分钟 1 秒前上报",
		"【离线】 no-data — 暂无数据",
		"【停用】 <node>&*_[] — CPU 4.4%，内存 50.0%，1 秒前上报",
	}, "\n")
	if reply != (Reply{Text: want}) {
		t.Fatalf("Reply() = %#v, want text:\n%s", reply, want)
	}
}

func TestStatusBoundsCompleteUTF8LinesAndReportsOmittedCount(t *testing.T) {
	const nowMS int64 = 1_700_000_000_000
	repository := &commandRepositoryStub{settings: domain.Settings{OfflineThresholdSeconds: 60}}
	wantLines := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		name := strings.Repeat("节点", 40) + fmt.Sprintf("-%03d", i)
		repository.servers = append(repository.servers, domain.Server{ID: int64(i + 1), Name: name, Enabled: true})
		wantLines["【离线】 "+name+" — 暂无数据"] = struct{}{}
	}
	commander := newTestCommander(t, repository, func() time.Time { return time.UnixMilli(nowMS) })

	reply, err := commander.Reply(context.Background(), 42, "/status")
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	got := reply.Text
	if size := len([]byte(got)); size > 3_800 {
		t.Fatalf("Reply() size = %d bytes, want <= 3800", size)
	}
	if !utf8.ValidString(got) {
		t.Fatal("Reply() is not complete UTF-8")
	}

	lines := strings.Split(got, "\n")
	if lines[0] != "服务器状态：" {
		t.Fatalf("Reply() header = %q", lines[0])
	}
	included := 0
	for _, line := range lines[1 : len(lines)-1] {
		if _, ok := wantLines[line]; !ok {
			t.Fatalf("Reply() contains partial or unexpected line %q", line)
		}
		included++
	}
	omittedLine := lines[len(lines)-1]
	omittedText := strings.TrimSuffix(strings.TrimPrefix(omittedLine, "…另有 "), " 台服务器未显示")
	omitted, err := strconv.Atoi(omittedText)
	if err != nil {
		t.Fatalf("Reply() final line = %q, want omitted count", omittedLine)
	}
	if omitted <= 0 || included+omitted != len(repository.servers) {
		t.Fatalf("Reply() included %d and omitted %d of %d servers", included, omitted, len(repository.servers))
	}
}

func TestStatusRepositoryErrorsAreSafe(t *testing.T) {
	tests := []struct {
		name       string
		repository *commandRepositoryStub
		want       string
	}{
		{
			name:       "settings",
			repository: &commandRepositoryStub{settingsErr: errors.New("settings secret")},
			want:       "telegram command status: load settings failed",
		},
		{
			name:       "servers",
			repository: &commandRepositoryStub{settings: domain.Settings{OfflineThresholdSeconds: 60}, serversErr: errors.New("servers secret")},
			want:       "telegram command status: list servers failed",
		},
		{
			name: "metrics",
			repository: &commandRepositoryStub{
				settings:   domain.Settings{OfflineThresholdSeconds: 60},
				servers:    []domain.Server{{ID: 987654, Name: "SECRET-NAME", Enabled: true}},
				metricsErr: errors.New("metrics secret"),
			},
			want: "telegram command status: list metrics failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commander := newTestCommander(t, tt.repository, time.Now)
			_, err := commander.Reply(context.Background(), 42, "/status")
			if err == nil || err.Error() != tt.want {
				t.Fatalf("Reply() error = %v, want %q", err, tt.want)
			}
			for _, secret := range []string{"secret", "SECRET-NAME", "987654"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Reply() error %q contains sensitive value %q", err, secret)
				}
			}
		})
	}
}

func TestCommandAppReturnsStructuredWebAppButton(t *testing.T) {
	commander := newTestCommander(t, &commandRepositoryStub{}, time.Now)
	got, err := commander.Reply(context.Background(), 42, "/app@my_bot")
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	want := Reply{
		Text:       "点击下方按钮打开 tg-monitor 监控面板。",
		WebAppURL:  "https://monitor.example.com/app/",
		ButtonText: "打开监控面板",
	}
	if got != want {
		t.Fatalf("Reply() = %#v, want %#v", got, want)
	}
}

func TestNewCommanderRejectsUnsafePublicURL(t *testing.T) {
	if commander, err := NewCommander(&commandRepositoryStub{}, "http://monitor.example.com", time.Now); err == nil || commander != nil {
		t.Fatalf("NewCommander() = %#v, %v; want nil, error", commander, err)
	}
}

func latestMetrics(serverID, receivedAtMS int64, cpu float64, memoryUsed, memoryTotal int64) domain.LatestMetrics {
	return domain.LatestMetrics{
		ServerID:     serverID,
		ReceivedAtMS: receivedAtMS,
		Report: domain.MetricReport{
			CPUPct:           cpu,
			MemoryUsedBytes:  memoryUsed,
			MemoryTotalBytes: memoryTotal,
		},
	}
}
