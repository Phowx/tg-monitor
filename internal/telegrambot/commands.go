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
	helpText            = "tg-monitor administrator commands:\n/status - show server status\n/app - open operator app\n/alerts_on - enable alerts\n/alerts_off - disable alerts\n/help - show this help"
	promptText          = "Use /help to list available commands."
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
			Text:       "Open the tg-monitor operator app.",
			WebAppURL:  c.webAppURL,
			ButtonText: "Open tg-monitor",
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
		return "Alerts enabled.", nil
	}
	return "Alerts disabled.", nil
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
		state := "offline"
		if !server.Enabled {
			state = "disabled"
		} else if hasMetrics && latest.ReceivedAtMS >= cutoffMS {
			state = "online"
		}

		if !hasMetrics {
			lines = append(lines, fmt.Sprintf("[%s] %s — no data", state, server.Name))
			continue
		}
		memoryPct := 100 * float64(latest.Report.MemoryUsedBytes) / float64(latest.Report.MemoryTotalBytes)
		age := formatAge(now.UnixMilli() - latest.ReceivedAtMS)
		lines = append(lines, fmt.Sprintf(
			"[%s] %s — CPU %.1f%%, memory %.1f%%, seen %s ago",
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
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	case minutes > 0 && seconds > 0:
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf("%dm", minutes)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

func boundedStatusReply(lines []string) string {
	reply := "Server status:"
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
	return fmt.Sprintf("… and %d more server(s)", count)
}
