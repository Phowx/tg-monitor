package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/websession"
)

const (
	helpText            = "tg-monitor 管理员命令：\n/status - 查看服务器状态\n/app - 打开监控面板\n/alerts_on - 开启离线告警\n/alerts_off - 关闭离线告警\n/help - 显示帮助"
	promptText          = "请发送 /help 查看可用命令。"
	maxStatusReplyBytes = 3_800
)

type CommandRepository interface {
	ListServers(context.Context) ([]domain.Server, error)
	ListLatestMetrics(context.Context) ([]domain.LatestMetrics, error)
	GetSettings(context.Context) (domain.Settings, error)
	SetAlertPreference(context.Context, int64, bool) error
}

type Reply struct {
	Text       string
	WebAppURL  string
	ButtonText string
}

type Commander struct {
	repository CommandRepository
	now        func() time.Time
	webAppURL  string
}

func NewCommander(repository CommandRepository, publicURL string, now func() time.Time) (*Commander, error) {
	policy, err := websession.NewPolicy(publicURL)
	if err != nil {
		return nil, errors.New("create Telegram commander: invalid public URL")
	}
	if now == nil {
		now = time.Now
	}
	return &Commander{
		repository: repository,
		now:        now,
		webAppURL:  strings.TrimSuffix(policy.Origin, "/") + "/app/",
	}, nil
}

func (c *Commander) Reply(ctx context.Context, telegramUserID int64, text string) (Reply, error) {
	command, ok := parseCommand(text)
	if !ok {
		return Reply{Text: promptText}, nil
	}

	switch command {
	case "/start", "/help":
		return Reply{Text: helpText}, nil
	case "/app":
		return Reply{
			Text:       "点击下方按钮打开 tg-monitor 监控面板。",
			WebAppURL:  c.webAppURL,
			ButtonText: "打开监控面板",
		}, nil
	case "/alerts_on":
		text, err := c.setAlertPreference(ctx, telegramUserID, true)
		return Reply{Text: text}, err
	case "/alerts_off":
		text, err := c.setAlertPreference(ctx, telegramUserID, false)
		return Reply{Text: text}, err
	case "/status":
		text, err := c.status(ctx)
		return Reply{Text: text}, err
	default:
		return Reply{Text: helpText}, nil
	}
}

func parseCommand(text string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", false
	}
	command := strings.ToLower(fields[0])
	if suffix := strings.IndexByte(command, '@'); suffix >= 0 {
		command = command[:suffix]
	}
	return command, true
}

func (c *Commander) setAlertPreference(ctx context.Context, telegramUserID int64, enabled bool) (string, error) {
	if c == nil || c.repository == nil {
		return "", errors.New("telegram command alerts: update preference failed")
	}
	if err := c.repository.SetAlertPreference(ctx, telegramUserID, enabled); err != nil {
		return "", errors.New("telegram command alerts: update preference failed")
	}
	if enabled {
		return "离线告警已开启。", nil
	}
	return "离线告警已关闭。", nil
}

func (c *Commander) status(ctx context.Context) (string, error) {
	if c == nil || c.repository == nil {
		return "", errors.New("telegram command status: load settings failed")
	}
	settings, err := c.repository.GetSettings(ctx)
	if err != nil {
		return "", errors.New("telegram command status: load settings failed")
	}
	servers, err := c.repository.ListServers(ctx)
	if err != nil {
		return "", errors.New("telegram command status: list servers failed")
	}
	metrics, err := c.repository.ListLatestMetrics(ctx)
	if err != nil {
		return "", errors.New("telegram command status: list metrics failed")
	}

	metricsByServer := make(map[int64]domain.LatestMetrics, len(metrics))
	for _, latest := range metrics {
		metricsByServer[latest.ServerID] = latest
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	cutoffMS := now.UnixMilli() - settings.OfflineThresholdSeconds*1_000

	lines := make([]string, 0, len(servers))
	for _, server := range servers {
		latest, hasMetrics := metricsByServer[server.ID]
		state := "离线"
		if !server.Enabled {
			state = "停用"
		} else if hasMetrics && latest.ReceivedAtMS >= cutoffMS {
			state = "在线"
		}

		if !hasMetrics {
			lines = append(lines, fmt.Sprintf("【%s】 %s — 暂无数据", state, server.Name))
			continue
		}
		memoryPct := 100 * float64(latest.Report.MemoryUsedBytes) / float64(latest.Report.MemoryTotalBytes)
		age := formatAge(now.UnixMilli() - latest.ReceivedAtMS)
		lines = append(lines, fmt.Sprintf(
			"【%s】 %s — CPU %.1f%%，内存 %.1f%%，%s前上报",
			state,
			server.Name,
			latest.Report.CPUPct,
			memoryPct,
			age,
		))
	}
	return boundedStatusReply(lines), nil
}

func formatAge(ageMS int64) string {
	if ageMS < 0 {
		ageMS = 0
	}
	seconds := ageMS / 1_000
	days := seconds / (24 * 60 * 60)
	seconds %= 24 * 60 * 60
	hours := seconds / (60 * 60)
	seconds %= 60 * 60
	minutes := seconds / 60
	seconds %= 60

	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	case days > 0:
		return fmt.Sprintf("%d 天", days)
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%d 小时", hours)
	case minutes > 0 && seconds > 0:
		return fmt.Sprintf("%d 分钟 %d 秒", minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf("%d 分钟", minutes)
	default:
		return fmt.Sprintf("%d 秒", seconds)
	}
}

func boundedStatusReply(lines []string) string {
	reply := "服务器状态："
	for i, line := range lines {
		candidate := reply + "\n" + line
		remaining := len(lines) - i - 1
		if remaining > 0 {
			candidate += "\n" + omittedServersLine(remaining)
		}
		if len([]byte(candidate)) > maxStatusReplyBytes {
			return reply + "\n" + omittedServersLine(len(lines)-i)
		}
		reply += "\n" + line
	}
	return reply
}

func omittedServersLine(count int) string {
	return fmt.Sprintf("…另有 %d 台服务器未显示", count)
}
