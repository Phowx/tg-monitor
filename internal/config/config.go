package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabasePath         string
	PublicURL            string
	BotToken             string
	WebhookSecret        string
	AdminTelegramIDs     []int64
	ListenAddr           string
	AgentDownloadBaseURL string
	SessionTTL           time.Duration
	InitDataMaxAge       time.Duration
	CheckpointInterval   time.Duration
	HistoryRetention     time.Duration
	OfflineThreshold     time.Duration
	AlertThreshold       time.Duration
}

func LoadFromEnv() (Config, error) {
	config := Config{
		DatabasePath:         envOrDefault("TG_MONITOR_DATABASE_PATH", "/var/lib/tg-monitor/monitor.db"),
		PublicURL:            strings.TrimSpace(os.Getenv("TG_MONITOR_PUBLIC_URL")),
		BotToken:             strings.TrimSpace(os.Getenv("TG_MONITOR_BOT_TOKEN")),
		WebhookSecret:        strings.TrimSpace(os.Getenv("TG_MONITOR_WEBHOOK_SECRET")),
		ListenAddr:           envOrDefault("TG_MONITOR_LISTEN_ADDR", "127.0.0.1:8080"),
		AgentDownloadBaseURL: strings.TrimSpace(os.Getenv("TG_MONITOR_AGENT_DOWNLOAD_BASE_URL")),
		SessionTTL:           12 * time.Hour,
		InitDataMaxAge:       5 * time.Minute,
		CheckpointInterval:   15 * time.Second,
		HistoryRetention:     7 * 24 * time.Hour,
		OfflineThreshold:     60 * time.Second,
		AlertThreshold:       120 * time.Second,
	}

	adminIDs, err := parseAdminIDs(os.Getenv("TG_MONITOR_ADMIN_TELEGRAM_IDS"))
	if err != nil {
		return Config{}, err
	}
	config.AdminTelegramIDs = adminIDs

	durations := []struct {
		name   string
		target *time.Duration
	}{
		{"TG_MONITOR_SESSION_TTL", &config.SessionTTL},
		{"TG_MONITOR_INIT_DATA_MAX_AGE", &config.InitDataMaxAge},
		{"TG_MONITOR_CHECKPOINT_INTERVAL", &config.CheckpointInterval},
		{"TG_MONITOR_HISTORY_RETENTION", &config.HistoryRetention},
		{"TG_MONITOR_OFFLINE_THRESHOLD", &config.OfflineThreshold},
		{"TG_MONITOR_ALERT_THRESHOLD", &config.AlertThreshold},
	}
	for _, duration := range durations {
		value, err := durationFromEnv(duration.name, *duration.target)
		if err != nil {
			return Config{}, err
		}
		*duration.target = value
	}

	if err := validateHTTPURL("TG_MONITOR_PUBLIC_URL", config.PublicURL); err != nil {
		return Config{}, err
	}
	if config.BotToken == "" {
		return Config{}, errors.New("TG_MONITOR_BOT_TOKEN is required")
	}
	if config.WebhookSecret == "" {
		return Config{}, errors.New("TG_MONITOR_WEBHOOK_SECRET is required")
	}
	if err := validateListenAddr(config.ListenAddr); err != nil {
		return Config{}, err
	}
	if err := validateHTTPURL("TG_MONITOR_AGENT_DOWNLOAD_BASE_URL", config.AgentDownloadBaseURL); err != nil {
		return Config{}, err
	}
	return config, nil
}

func envOrDefault(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func durationFromEnv(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil
}

func parseAdminIDs(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("TG_MONITOR_ADMIN_TELEGRAM_IDS is required")
	}

	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("TG_MONITOR_ADMIN_TELEGRAM_IDS contains an empty ID")
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("TG_MONITOR_ADMIN_TELEGRAM_IDS must contain positive integers")
		}
		if _, exists := seen[id]; exists {
			return nil, errors.New("TG_MONITOR_ADMIN_TELEGRAM_IDS contains a duplicate ID")
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func validateHTTPURL(name, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s is required", name)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute HTTP(S) URL", name)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not contain credentials", name)
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain a fragment", name)
	}
	return nil
}

func validateListenAddr(address string) error {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("TG_MONITOR_LISTEN_ADDR must be a host:port address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65_535 {
		return errors.New("TG_MONITOR_LISTEN_ADDR port must be between 1 and 65535")
	}
	return nil
}
