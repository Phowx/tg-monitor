package domain

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func validMetricReport() MetricReport {
	return MetricReport{
		CapturedAtMS:            1_720_000_000_123,
		CPUPct:                  37.5,
		MemoryTotalBytes:        16_000,
		MemoryUsedBytes:         8_000,
		RootDiskTotalBytes:      100_000,
		RootDiskUsedBytes:       40_000,
		Load1:                   0.5,
		Load5:                   0.4,
		Load15:                  0.3,
		NetworkRXTotalBytes:     10_000,
		NetworkTXTotalBytes:     20_000,
		NetworkRXBytesPerSecond: 125.5,
		NetworkTXBytesPerSecond: 75.25,
		UptimeSeconds:           3_600,
		System:                  SystemInfo{Hostname: "host-1", OS: "linux", Kernel: "6.12", Arch: "amd64"},
	}
}

func TestMetricReportValidateAcceptsValidReport(t *testing.T) {
	if err := validMetricReport().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestMetricReportValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*MetricReport)
	}{
		{"zero captured time", func(r *MetricReport) { r.CapturedAtMS = 0 }},
		{"nan cpu", func(r *MetricReport) { r.CPUPct = math.NaN() }},
		{"infinite load", func(r *MetricReport) { r.Load1 = math.Inf(1) }},
		{"negative cpu", func(r *MetricReport) { r.CPUPct = -1 }},
		{"cpu over 100", func(r *MetricReport) { r.CPUPct = 100.01 }},
		{"zero memory total", func(r *MetricReport) { r.MemoryTotalBytes = 0 }},
		{"memory used over total", func(r *MetricReport) { r.MemoryUsedBytes = r.MemoryTotalBytes + 1 }},
		{"negative memory used", func(r *MetricReport) { r.MemoryUsedBytes = -1 }},
		{"zero disk total", func(r *MetricReport) { r.RootDiskTotalBytes = 0 }},
		{"disk used over total", func(r *MetricReport) { r.RootDiskUsedBytes = r.RootDiskTotalBytes + 1 }},
		{"negative load", func(r *MetricReport) { r.Load15 = -0.1 }},
		{"negative rx total", func(r *MetricReport) { r.NetworkRXTotalBytes = -1 }},
		{"negative tx total", func(r *MetricReport) { r.NetworkTXTotalBytes = -1 }},
		{"negative rx rate", func(r *MetricReport) { r.NetworkRXBytesPerSecond = -1 }},
		{"negative tx rate", func(r *MetricReport) { r.NetworkTXBytesPerSecond = -1 }},
		{"negative uptime", func(r *MetricReport) { r.UptimeSeconds = -1 }},
		{"blank hostname", func(r *MetricReport) { r.System.Hostname = "  " }},
		{"blank os", func(r *MetricReport) { r.System.OS = "" }},
		{"blank kernel", func(r *MetricReport) { r.System.Kernel = "" }},
		{"blank arch", func(r *MetricReport) { r.System.Arch = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := validMetricReport()
			tt.mutate(&report)
			if err := report.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want non-nil")
			}
		})
	}
}

func TestServerTokenHashIsNotJSON(t *testing.T) {
	server := Server{
		ID:          1,
		Name:        "primary",
		TokenSHA256: []byte("do-not-serialize-this-token-hash"),
	}

	data, err := json.Marshal(server)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	encoded := string(data)
	if strings.Contains(encoded, "TokenSHA256") || strings.Contains(encoded, "token_sha256") || strings.Contains(encoded, "do-not-serialize") {
		t.Fatalf("json.Marshal() leaked token hash: %s", encoded)
	}
}
