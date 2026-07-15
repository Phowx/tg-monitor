package websession

import (
	"net/http"
	"testing"
	"time"
)

func TestProductionPolicyUsesHostOnlyPartitionedCookie(t *testing.T) {
	policy, err := NewPolicy("https://monitor.example.com")
	if err != nil {
		t.Fatalf("NewPolicy() error = %v", err)
	}

	expires := time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC)
	cookie := policy.Set("opaque-session", expires, 12*time.Hour)
	if policy.Origin != "https://monitor.example.com" {
		t.Fatalf("Origin = %q", policy.Origin)
	}
	if cookie.Name != "__Host-tg_monitor_session" || cookie.Value != "opaque-session" || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("cookie identity = %#v", cookie)
	}
	if !cookie.HttpOnly || !cookie.Secure || !cookie.Partitioned || cookie.SameSite != http.SameSiteNoneMode {
		t.Fatalf("cookie security attributes = %#v", cookie)
	}
	if !cookie.Expires.Equal(expires) || cookie.MaxAge != int((12*time.Hour).Seconds()) {
		t.Fatalf("cookie lifetime = %#v", cookie)
	}

	cleared := policy.Clear()
	if cleared.Name != cookie.Name || cleared.Path != cookie.Path || cleared.Domain != cookie.Domain {
		t.Fatalf("clear identity differs: set=%#v clear=%#v", cookie, cleared)
	}
	if !cleared.HttpOnly || !cleared.Secure || !cleared.Partitioned || cleared.SameSite != cookie.SameSite || cleared.MaxAge != -1 || cleared.Value != "" {
		t.Fatalf("clear security attributes differ: set=%#v clear=%#v", cookie, cleared)
	}
}

func TestLoopbackPolicyUsesStrictDevelopmentCookie(t *testing.T) {
	policy, err := NewPolicy("http://127.0.0.1:8080/")
	if err != nil {
		t.Fatalf("NewPolicy() error = %v", err)
	}
	cookie := policy.Set("opaque-session", time.Now().Add(time.Hour), time.Hour)
	if policy.Origin != "http://127.0.0.1:8080" || cookie.Name != "tg_monitor_session" {
		t.Fatalf("loopback policy = %#v, cookie = %#v", policy, cookie)
	}
	if cookie.Secure || cookie.Partitioned || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("loopback cookie security attributes = %#v", cookie)
	}
}

func TestPolicyRejectsUnsafeOrNonOriginPublicURLs(t *testing.T) {
	tests := []string{
		"",
		"http://monitor.example.com",
		"https://user@monitor.example.com",
		"https://monitor.example.com/path",
		"https://monitor.example.com?query=1",
		"https://monitor.example.com#fragment",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := NewPolicy(raw); err == nil {
				t.Fatalf("NewPolicy(%q) error = nil", raw)
			}
		})
	}
}
