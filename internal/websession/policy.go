package websession

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Policy struct {
	Origin      string
	CookieName  string
	Secure      bool
	SameSite    http.SameSite
	Partitioned bool
}

func NewPolicy(rawPublicURL string) (Policy, error) {
	origin, secure, err := normalizeOrigin(rawPublicURL)
	if err != nil {
		return Policy{}, err
	}
	policy := Policy{
		Origin:     origin,
		CookieName: "tg_monitor_session",
		SameSite:   http.SameSiteStrictMode,
	}
	if secure {
		policy.CookieName = "__Host-tg_monitor_session"
		policy.Secure = true
		policy.SameSite = http.SameSiteNoneMode
		policy.Partitioned = true
	}
	return policy, nil
}

func (policy Policy) Set(value string, expires time.Time, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:        policy.CookieName,
		Value:       value,
		Path:        "/",
		Expires:     expires,
		MaxAge:      int(math.Ceil(ttl.Seconds())),
		HttpOnly:    true,
		Secure:      policy.Secure,
		SameSite:    policy.SameSite,
		Partitioned: policy.Partitioned,
	}
}

func (policy Policy) Clear() *http.Cookie {
	return &http.Cookie{
		Name:        policy.CookieName,
		Value:       "",
		Path:        "/",
		Expires:     time.Unix(1, 0).UTC(),
		MaxAge:      -1,
		HttpOnly:    true,
		Secure:      policy.Secure,
		SameSite:    policy.SameSite,
		Partitioned: policy.Partitioned,
	}
}

func normalizeOrigin(raw string) (string, bool, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", false, errors.New("web session public URL must be an origin")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	secure := parsed.Scheme == "https"
	if !secure && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname())) {
		return "", false, errors.New("web session public URL must use HTTPS")
	}
	return fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host), secure, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
