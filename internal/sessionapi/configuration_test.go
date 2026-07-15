package sessionapi

import (
	"strings"
	"testing"
	"time"
)

func TestNewHandlerRejectsInvalidConfigurationWithoutSecretLeak(t *testing.T) {
	valid := Config{
		PublicURL:        "https://monitor.example.com",
		BotToken:         "123456:constructor-canary-token",
		AdminTelegramIDs: []int64{101},
		SessionTTL:       time.Hour,
		InitDataMaxAge:   time.Minute,
	}
	tests := []struct {
		name        string
		mutate      func(*Config)
		missingRepo bool
	}{
		{name: "forced empty query", mutate: func(cfg *Config) { cfg.PublicURL = "https://monitor.example.com?" }},
		{name: "duplicate administrator", mutate: func(cfg *Config) { cfg.AdminTelegramIDs = []int64{101, 101} }},
		{name: "remote HTTP", mutate: func(cfg *Config) { cfg.PublicURL = "http://monitor.example.com" }},
		{name: "missing bot token", mutate: func(cfg *Config) { cfg.BotToken = "" }},
		{name: "missing administrators", mutate: func(cfg *Config) { cfg.AdminTelegramIDs = nil }},
		{name: "invalid administrator", mutate: func(cfg *Config) { cfg.AdminTelegramIDs = []int64{0} }},
		{name: "invalid session TTL", mutate: func(cfg *Config) { cfg.SessionTTL = 0 }},
		{name: "invalid init-data age", mutate: func(cfg *Config) { cfg.InitDataMaxAge = 0 }},
		{name: "missing repository", mutate: func(*Config) {}, missingRepo: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			cfg.AdminTelegramIDs = append([]int64(nil), valid.AdminTelegramIDs...)
			tt.mutate(&cfg)
			dependencies := Dependencies{}
			if !tt.missingRepo {
				dependencies.Repository = &sessionRepositoryStub{}
			}
			_, err := NewHandler(cfg, dependencies)
			if err == nil {
				t.Fatal("NewHandler() error = nil")
			}
			if strings.Contains(err.Error(), valid.BotToken) {
				t.Fatalf("error leaked bot token: %v", err)
			}
		})
	}
}
