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

type AgentConfig struct {
	EndpointURL string
	Token       string
	Interval    time.Duration
	HTTPTimeout time.Duration
}

func LoadAgentFromEnv() (AgentConfig, error) {
	interval, err := durationFromEnv("TG_MONITOR_AGENT_INTERVAL", 15*time.Second)
	if err != nil {
		return AgentConfig{}, err
	}
	httpTimeout, err := durationFromEnv("TG_MONITOR_AGENT_HTTP_TIMEOUT", 10*time.Second)
	if err != nil {
		return AgentConfig{}, err
	}
	token := strings.TrimSpace(os.Getenv("TG_MONITOR_AGENT_TOKEN"))
	if token == "" {
		return AgentConfig{}, errors.New("TG_MONITOR_AGENT_TOKEN is required")
	}
	endpointURL, err := agentEndpoint(os.Getenv("TG_MONITOR_AGENT_SERVER_URL"))
	if err != nil {
		return AgentConfig{}, err
	}
	return AgentConfig{
		EndpointURL: endpointURL,
		Token:       token,
		Interval:    interval,
		HTTPTimeout: httpTimeout,
	}, nil
}

func agentEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("TG_MONITOR_AGENT_SERVER_URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("TG_MONITOR_AGENT_SERVER_URL must be an absolute origin URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return "", errors.New("TG_MONITOR_AGENT_SERVER_URL must be an absolute origin URL")
	}
	if parsed.User != nil {
		return "", errors.New("TG_MONITOR_AGENT_SERVER_URL must not contain credentials")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", errors.New("TG_MONITOR_AGENT_SERVER_URL must not contain a query")
	}
	if parsed.Fragment != "" {
		return "", errors.New("TG_MONITOR_AGENT_SERVER_URL must not contain a fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("TG_MONITOR_AGENT_SERVER_URL must not contain a path")
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return "", errors.New("TG_MONITOR_AGENT_SERVER_URL must contain a hostname")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		ip := net.ParseIP(hostname)
		if !strings.EqualFold(hostname, "localhost") && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("TG_MONITOR_AGENT_SERVER_URL requires HTTPS for non-loopback hosts")
		}
	default:
		return "", fmt.Errorf("TG_MONITOR_AGENT_SERVER_URL scheme must be HTTPS or loopback HTTP")
	}

	parsed.Path = "/api/v1/metrics"
	parsed.RawPath = ""
	return parsed.String(), nil
}
