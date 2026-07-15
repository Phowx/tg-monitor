package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type ApplicationRuntimeConfig struct {
	Server     ServerRuntimeConfig
	Telegram   *TelegramRuntimeConfig
	Cloudflare *CloudflareRuntimeConfig
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

type CloudflareRuntimeConfig struct {
	APIToken    string
	Zones       []CloudflareZoneConfig
	HTTPTimeout time.Duration
}

type CloudflareZoneConfig struct {
	ID   string
	Name string
}

func LoadApplicationRuntimeFromEnv() (ApplicationRuntimeConfig, error) {
	server, err := LoadServerRuntimeFromEnv()
	if err != nil {
		return ApplicationRuntimeConfig{}, err
	}
	cloudflare, err := LoadCloudflareRuntimeFromEnv()
	if err != nil {
		return ApplicationRuntimeConfig{}, err
	}
	application := ApplicationRuntimeConfig{Server: server, Cloudflare: cloudflare}

	switch strings.TrimSpace(os.Getenv("TG_MONITOR_TELEGRAM_ENABLED")) {
	case "", "false":
		return application, nil
	case "true":
		telegram, err := LoadTelegramRuntimeFromEnv()
		if err != nil {
			return ApplicationRuntimeConfig{}, err
		}
		application.Telegram = &telegram
		return application, nil
	default:
		return ApplicationRuntimeConfig{}, errors.New("TG_MONITOR_TELEGRAM_ENABLED must be true or false")
	}
}

func LoadCloudflareRuntimeFromEnv() (*CloudflareRuntimeConfig, error) {
	switch strings.TrimSpace(os.Getenv("TG_MONITOR_CLOUDFLARE_ENABLED")) {
	case "", "false":
		return nil, nil
	case "true":
	default:
		return nil, errors.New("TG_MONITOR_CLOUDFLARE_ENABLED must be true or false")
	}

	token := strings.TrimSpace(os.Getenv("TG_MONITOR_CLOUDFLARE_API_TOKEN"))
	if token == "" {
		return nil, errors.New("TG_MONITOR_CLOUDFLARE_API_TOKEN is required")
	}
	var configured map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(os.Getenv("TG_MONITOR_CLOUDFLARE_ZONES"))), &configured); err != nil || len(configured) == 0 {
		return nil, errors.New("TG_MONITOR_CLOUDFLARE_ZONES must be a non-empty JSON object")
	}
	zones := make([]CloudflareZoneConfig, 0, len(configured))
	seenIDs := make(map[string]struct{}, len(configured))
	seenNames := make(map[string]struct{}, len(configured))
	for rawName, rawID := range configured {
		name, err := normalizeCloudflareZoneName(rawName)
		if err != nil {
			return nil, err
		}
		if _, exists := seenNames[name]; exists {
			return nil, errors.New("TG_MONITOR_CLOUDFLARE_ZONES contains a duplicate zone name")
		}
		id := strings.TrimSpace(rawID)
		if !isCloudflareZoneID(id) {
			return nil, fmt.Errorf("TG_MONITOR_CLOUDFLARE_ZONES contains an invalid zone ID for %s", name)
		}
		if _, exists := seenIDs[id]; exists {
			return nil, errors.New("TG_MONITOR_CLOUDFLARE_ZONES contains a duplicate zone ID")
		}
		seenIDs[id] = struct{}{}
		seenNames[name] = struct{}{}
		zones = append(zones, CloudflareZoneConfig{ID: id, Name: name})
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
	timeout, err := durationFromEnv("TG_MONITOR_CLOUDFLARE_HTTP_TIMEOUT", 10*time.Second)
	if err != nil {
		return nil, err
	}
	return &CloudflareRuntimeConfig{APIToken: token, Zones: zones, HTTPTimeout: timeout}, nil
}

func normalizeCloudflareZoneName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if len(name) == 0 || len(name) > 253 {
		return "", errors.New("TG_MONITOR_CLOUDFLARE_ZONES contains an invalid zone name")
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("TG_MONITOR_CLOUDFLARE_ZONES contains an invalid zone name %q", name)
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return "", fmt.Errorf("TG_MONITOR_CLOUDFLARE_ZONES contains an invalid zone name %q", name)
		}
	}
	return name, nil
}

func isCloudflareZoneID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F') {
			continue
		}
		return false
	}
	return true
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
