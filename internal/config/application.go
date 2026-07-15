package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

type ApplicationRuntimeConfig struct {
	Server   ServerRuntimeConfig
	Telegram *TelegramRuntimeConfig
}

type TelegramRuntimeConfig struct {
	PublicURL        string
	BotToken         string
	WebhookSecret    string
	AdminTelegramIDs []int64
	SessionTTL       time.Duration
	InitDataMaxAge   time.Duration
	HTTPTimeout      time.Duration
}

func LoadApplicationRuntimeFromEnv() (ApplicationRuntimeConfig, error) {
	server, err := LoadServerRuntimeFromEnv()
	if err != nil {
		return ApplicationRuntimeConfig{}, err
	}

	switch strings.TrimSpace(os.Getenv("TG_MONITOR_TELEGRAM_ENABLED")) {
	case "", "false":
		return ApplicationRuntimeConfig{Server: server}, nil
	case "true":
		telegram, err := LoadTelegramRuntimeFromEnv()
		if err != nil {
			return ApplicationRuntimeConfig{}, err
		}
		return ApplicationRuntimeConfig{Server: server, Telegram: &telegram}, nil
	default:
		return ApplicationRuntimeConfig{}, errors.New("TG_MONITOR_TELEGRAM_ENABLED must be true or false")
	}
}

func LoadTelegramRuntimeFromEnv() (TelegramRuntimeConfig, error) {
	publicURL, err := telegramOrigin(os.Getenv("TG_MONITOR_PUBLIC_URL"))
	if err != nil {
		return TelegramRuntimeConfig{}, err
	}

	botToken := strings.TrimSpace(os.Getenv("TG_MONITOR_BOT_TOKEN"))
	if botToken == "" {
		return TelegramRuntimeConfig{}, errors.New("TG_MONITOR_BOT_TOKEN is required")
	}
	webhookSecret := strings.TrimSpace(os.Getenv("TG_MONITOR_WEBHOOK_SECRET"))
	if err := validateWebhookSecret(webhookSecret); err != nil {
		return TelegramRuntimeConfig{}, err
	}
	adminIDs, err := parseAdminIDs(os.Getenv("TG_MONITOR_ADMIN_TELEGRAM_IDS"))
	if err != nil {
		return TelegramRuntimeConfig{}, err
	}

	sessionTTL, err := durationFromEnv("TG_MONITOR_SESSION_TTL", 12*time.Hour)
	if err != nil {
		return TelegramRuntimeConfig{}, err
	}
	initDataMaxAge, err := durationFromEnv("TG_MONITOR_INIT_DATA_MAX_AGE", 5*time.Minute)
	if err != nil {
		return TelegramRuntimeConfig{}, err
	}
	httpTimeout, err := durationFromEnv("TG_MONITOR_TELEGRAM_HTTP_TIMEOUT", 10*time.Second)
	if err != nil {
		return TelegramRuntimeConfig{}, err
	}

	return TelegramRuntimeConfig{
		PublicURL:        publicURL,
		BotToken:         botToken,
		WebhookSecret:    webhookSecret,
		AdminTelegramIDs: adminIDs,
		SessionTTL:       sessionTTL,
		InitDataMaxAge:   initDataMaxAge,
		HTTPTimeout:      httpTimeout,
	}, nil
}

func ValidateWebhookPublicURL(raw string) error {
	origin, err := telegramOrigin(raw)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" {
		return errors.New("TG_MONITOR_PUBLIC_URL must use HTTPS for webhook registration")
	}
	return nil
}

func telegramOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" {
		return "", errors.New("TG_MONITOR_PUBLIC_URL must be an absolute HTTP(S) origin")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("TG_MONITOR_PUBLIC_URL must be an absolute HTTP(S) origin")
	}
	if parsed.User != nil {
		return "", errors.New("TG_MONITOR_PUBLIC_URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", errors.New("TG_MONITOR_PUBLIC_URL must not contain a query")
	}
	if parsed.Fragment != "" {
		return "", errors.New("TG_MONITOR_PUBLIC_URL must not contain a fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("TG_MONITOR_PUBLIC_URL must not contain a non-root path")
	}
	if scheme == "http" && !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("TG_MONITOR_PUBLIC_URL must use HTTPS except on loopback")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("TG_MONITOR_PUBLIC_URL must include a host")
	}
	if port := parsed.Port(); port != "" {
		if _, err := net.LookupPort("tcp", port); err != nil {
			return "", errors.New("TG_MONITOR_PUBLIC_URL contains an invalid port")
		}
	}
	return fmt.Sprintf("%s://%s", scheme, parsed.Host), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateWebhookSecret(secret string) error {
	if len(secret) < 1 || len(secret) > 256 {
		return errors.New("TG_MONITOR_WEBHOOK_SECRET must contain between 1 and 256 allowed characters")
	}
	for _, character := range secret {
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '_' || character == '-' {
			continue
		}
		return errors.New("TG_MONITOR_WEBHOOK_SECRET contains a disallowed character")
	}
	return nil
}
