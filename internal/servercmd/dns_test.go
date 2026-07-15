package servercmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/cloudflare"
	"github.com/tg-monitor/tg-monitor/internal/config"
)

type dnsVerifierStub struct {
	verifyCalls int
	verifyErr   error
	zones       []cloudflare.Zone
}

func (stub *dnsVerifierStub) Verify(context.Context) error {
	stub.verifyCalls++
	return stub.verifyErr
}

func (stub *dnsVerifierStub) Zones() []cloudflare.Zone {
	return stub.zones
}

func TestDNSVerifyUsesFocusedConfigurationAndSafeOutput(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runtime := &config.CloudflareRuntimeConfig{
		APIToken: "CLI-TOKEN-CANARY",
		Zones: []config.CloudflareZoneConfig{{
			ID: "0123456789abcdef0123456789abcdef", Name: "example.com",
		}},
		HTTPTimeout: 9 * time.Second,
	}
	verifier := &dnsVerifierStub{zones: []cloudflare.Zone{{ID: runtime.Zones[0].ID, Name: "example.com"}}}
	loadCalls, createCalls := 0, 0
	dependencies := Dependencies{
		Stdout: &stdout,
		Stderr: &stderr,
		LoadCloudflareConfig: func() (*config.CloudflareRuntimeConfig, error) {
			loadCalls++
			return runtime, nil
		},
		NewCloudflareClient: func(got config.CloudflareRuntimeConfig) (CloudflareVerifier, error) {
			createCalls++
			if got.APIToken != runtime.APIToken || got.HTTPTimeout != runtime.HTTPTimeout {
				t.Fatalf("runtime = %#v", got)
			}
			return verifier, nil
		},
	}
	if err := Run(context.Background(), []string{"dns", "verify"}, dependencies); err != nil {
		t.Fatalf("Run(dns verify) error = %v", err)
	}
	if loadCalls != 1 || createCalls != 1 || verifier.verifyCalls != 1 {
		t.Fatalf("loads/create/verify = %d/%d/%d", loadCalls, createCalls, verifier.verifyCalls)
	}
	if got := stdout.String(); got != "cloudflare=verified\nzone=example.com status=ok\n" {
		t.Fatalf("stdout = %q", got)
	}
	if strings.Contains(stdout.String()+stderr.String(), runtime.APIToken) || strings.Contains(stdout.String(), runtime.Zones[0].ID) {
		t.Fatal("dns verify output leaked token or zone ID")
	}
}

func TestDNSVerifyRejectsDisabledInvalidAndFailedConfigurations(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		err := Run(context.Background(), []string{"dns", "verify"}, Dependencies{
			LoadCloudflareConfig: func() (*config.CloudflareRuntimeConfig, error) { return nil, nil },
		})
		if err == nil || !strings.Contains(err.Error(), "not enabled") {
			t.Fatalf("disabled error = %v", err)
		}
	})
	t.Run("invalid command", func(t *testing.T) {
		called := false
		err := Run(context.Background(), []string{"dns", "unknown"}, Dependencies{
			LoadCloudflareConfig: func() (*config.CloudflareRuntimeConfig, error) {
				called = true
				return nil, nil
			},
		})
		if err == nil || called {
			t.Fatalf("invalid command error/called = %v/%v", err, called)
		}
	})
	t.Run("verify failure", func(t *testing.T) {
		var stdout bytes.Buffer
		runtime := &config.CloudflareRuntimeConfig{APIToken: "secret", HTTPTimeout: time.Second}
		verifier := &dnsVerifierStub{verifyErr: errors.New("safe verification failure")}
		err := Run(context.Background(), []string{"dns", "verify"}, Dependencies{
			Stdout:               &stdout,
			LoadCloudflareConfig: func() (*config.CloudflareRuntimeConfig, error) { return runtime, nil },
			NewCloudflareClient: func(config.CloudflareRuntimeConfig) (CloudflareVerifier, error) {
				return verifier, nil
			},
		})
		if err == nil || stdout.Len() != 0 || verifier.verifyCalls != 1 {
			t.Fatalf("verify failure = err %v output %q calls %d", err, stdout.String(), verifier.verifyCalls)
		}
	})
}
