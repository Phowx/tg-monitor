package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

type SystemInfo struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Kernel   string `json:"kernel"`
	Arch     string `json:"arch"`
}

type MetricReport struct {
	CapturedAtMS            int64      `json:"captured_at"`
	CPUPct                  float64    `json:"cpu_pct"`
	MemoryTotalBytes        int64      `json:"memory_total_bytes"`
	MemoryUsedBytes         int64      `json:"memory_used_bytes"`
	RootDiskTotalBytes      int64      `json:"root_disk_total_bytes"`
	RootDiskUsedBytes       int64      `json:"root_disk_used_bytes"`
	Load1                   float64    `json:"load_1"`
	Load5                   float64    `json:"load_5"`
	Load15                  float64    `json:"load_15"`
	NetworkRXTotalBytes     int64      `json:"network_rx_total_bytes"`
	NetworkTXTotalBytes     int64      `json:"network_tx_total_bytes"`
	NetworkRXBytesPerSecond float64    `json:"network_rx_bytes_per_second"`
	NetworkTXBytesPerSecond float64    `json:"network_tx_bytes_per_second"`
	UptimeSeconds           int64      `json:"uptime_seconds"`
	System                  SystemInfo `json:"system"`
}

func (r MetricReport) Validate() error {
	finiteFields := []struct {
		name  string
		value float64
	}{
		{"cpu_pct", r.CPUPct},
		{"load_1", r.Load1},
		{"load_5", r.Load5},
		{"load_15", r.Load15},
		{"network_rx_bytes_per_second", r.NetworkRXBytesPerSecond},
		{"network_tx_bytes_per_second", r.NetworkTXBytesPerSecond},
	}
	for _, field := range finiteFields {
		if math.IsNaN(field.value) || math.IsInf(field.value, 0) {
			return fmt.Errorf("%s must be finite", field.name)
		}
	}

	if r.CapturedAtMS <= 0 {
		return errors.New("captured_at must be positive")
	}
	if r.CPUPct < 0 || r.CPUPct > 100 {
		return errors.New("cpu_pct must be between 0 and 100")
	}
	if r.MemoryTotalBytes <= 0 {
		return errors.New("memory_total_bytes must be positive")
	}
	if r.MemoryUsedBytes < 0 || r.MemoryUsedBytes > r.MemoryTotalBytes {
		return errors.New("memory_used_bytes must be between zero and memory_total_bytes")
	}
	if r.RootDiskTotalBytes <= 0 {
		return errors.New("root_disk_total_bytes must be positive")
	}
	if r.RootDiskUsedBytes < 0 || r.RootDiskUsedBytes > r.RootDiskTotalBytes {
		return errors.New("root_disk_used_bytes must be between zero and root_disk_total_bytes")
	}
	if r.Load1 < 0 || r.Load5 < 0 || r.Load15 < 0 {
		return errors.New("loads must be non-negative")
	}
	if r.NetworkRXTotalBytes < 0 || r.NetworkTXTotalBytes < 0 {
		return errors.New("network total bytes must be non-negative")
	}
	if r.NetworkRXBytesPerSecond < 0 || r.NetworkTXBytesPerSecond < 0 {
		return errors.New("network rates must be non-negative")
	}
	if r.UptimeSeconds < 0 {
		return errors.New("uptime_seconds must be non-negative")
	}
	if strings.TrimSpace(r.System.Hostname) == "" {
		return errors.New("system.hostname must be non-empty")
	}
	if strings.TrimSpace(r.System.OS) == "" {
		return errors.New("system.os must be non-empty")
	}
	if strings.TrimSpace(r.System.Kernel) == "" {
		return errors.New("system.kernel must be non-empty")
	}
	if strings.TrimSpace(r.System.Arch) == "" {
		return errors.New("system.arch must be non-empty")
	}
	return nil
}

type LatestMetrics struct {
	ServerID     int64        `json:"server_id"`
	ReceivedAtMS int64        `json:"received_at"`
	Report       MetricReport `json:"report"`
}

type MinuteSample struct {
	ServerID                int64      `json:"server_id"`
	BucketMS                int64      `json:"bucket"`
	CPUPct                  float64    `json:"cpu_pct"`
	MemoryTotalBytes        int64      `json:"memory_total_bytes"`
	MemoryUsedBytes         int64      `json:"memory_used_bytes"`
	RootDiskTotalBytes      int64      `json:"root_disk_total_bytes"`
	RootDiskUsedBytes       int64      `json:"root_disk_used_bytes"`
	Load1                   float64    `json:"load_1"`
	Load5                   float64    `json:"load_5"`
	Load15                  float64    `json:"load_15"`
	NetworkRXTotalBytes     int64      `json:"network_rx_total_bytes"`
	NetworkTXTotalBytes     int64      `json:"network_tx_total_bytes"`
	NetworkRXBytesPerSecond float64    `json:"network_rx_bytes_per_second"`
	NetworkTXBytesPerSecond float64    `json:"network_tx_bytes_per_second"`
	UptimeSeconds           int64      `json:"uptime_seconds"`
	System                  SystemInfo `json:"system"`
}
