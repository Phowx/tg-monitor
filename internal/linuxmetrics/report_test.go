package linuxmetrics

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func validSnapshot(at time.Time) Snapshot {
	return Snapshot{
		CapturedAt:         at,
		CPU:                CPUCounters{Total: 1_000, Idle: 400},
		MemoryTotalBytes:   16_000,
		MemoryUsedBytes:    8_000,
		RootDiskTotalBytes: 100_000,
		RootDiskUsedBytes:  40_000,
		Load1:              0.1,
		Load5:              0.2,
		Load15:             0.3,
		Network:            NetworkCounters{RXBytes: 1_000, TXBytes: 2_000},
		UptimeSeconds:      3_600,
		System:             domain.SystemInfo{Hostname: "host", OS: "linux", Kernel: "6.12", Arch: "amd64"},
	}
}

func TestBuildReportCalculatesDeltasAndCopiesCurrentValues(t *testing.T) {
	previous := validSnapshot(time.Unix(100, 0))
	current := validSnapshot(previous.CapturedAt.Add(2 * time.Second))
	current.CPU = CPUCounters{Total: 1_400, Idle: 500}
	current.MemoryTotalBytes = 32_000
	current.MemoryUsedBytes = 12_000
	current.RootDiskTotalBytes = 200_000
	current.RootDiskUsedBytes = 60_000
	current.Load1, current.Load5, current.Load15 = 1.1, 1.2, 1.3
	current.Network = NetworkCounters{RXBytes: 1_400, TXBytes: 2_800}
	current.UptimeSeconds = 7_200
	current.System = domain.SystemInfo{Hostname: "current-host", OS: "linux", Kernel: "6.13", Arch: "arm64"}

	got, err := BuildReport(previous, current)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	want := domain.MetricReport{
		CapturedAtMS:            current.CapturedAt.UTC().UnixMilli(),
		CPUPct:                  75,
		MemoryTotalBytes:        32_000,
		MemoryUsedBytes:         12_000,
		RootDiskTotalBytes:      200_000,
		RootDiskUsedBytes:       60_000,
		Load1:                   1.1,
		Load5:                   1.2,
		Load15:                  1.3,
		NetworkRXTotalBytes:     1_400,
		NetworkTXTotalBytes:     2_800,
		NetworkRXBytesPerSecond: 200,
		NetworkTXBytesPerSecond: 400,
		UptimeSeconds:           7_200,
		System:                  current.System,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildReport() = %#v, want %#v", got, want)
	}
}

func TestBuildReportRejectsCPUCounterReset(t *testing.T) {
	previous := validSnapshot(time.Unix(100, 0))
	tests := []struct {
		name string
		cpu  CPUCounters
	}{
		{name: "zero total delta", cpu: CPUCounters{Total: previous.CPU.Total, Idle: previous.CPU.Idle}},
		{name: "total decrease", cpu: CPUCounters{Total: previous.CPU.Total - 1, Idle: previous.CPU.Idle}},
		{name: "idle decrease", cpu: CPUCounters{Total: previous.CPU.Total + 10, Idle: previous.CPU.Idle - 1}},
		{name: "idle delta exceeds total", cpu: CPUCounters{Total: previous.CPU.Total + 10, Idle: previous.CPU.Idle + 11}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := validSnapshot(previous.CapturedAt.Add(time.Second))
			current.CPU = tt.cpu
			if _, err := BuildReport(previous, current); !errors.Is(err, ErrCounterReset) {
				t.Fatalf("BuildReport() error = %v, want ErrCounterReset", err)
			}
		})
	}
}

func TestBuildReportRejectsInvalidElapsedTime(t *testing.T) {
	previous := validSnapshot(time.Unix(100, 0))
	for _, at := range []time.Time{previous.CapturedAt, previous.CapturedAt.Add(-time.Second)} {
		current := validSnapshot(at)
		current.CPU = CPUCounters{Total: 1_100, Idle: 450}
		if _, err := BuildReport(previous, current); !errors.Is(err, ErrInvalidElapsed) {
			t.Fatalf("BuildReport() error = %v, want ErrInvalidElapsed", err)
		}
	}
}

func TestBuildReportClampsOnlyResetNetworkDirectionToZero(t *testing.T) {
	previous := validSnapshot(time.Unix(100, 0))
	current := validSnapshot(previous.CapturedAt.Add(2 * time.Second))
	current.CPU = CPUCounters{Total: 1_400, Idle: 500}
	current.Network = NetworkCounters{RXBytes: 900, TXBytes: 2_800}

	report, err := BuildReport(previous, current)
	if err != nil {
		t.Fatal(err)
	}
	if report.NetworkRXBytesPerSecond != 0 || report.NetworkTXBytesPerSecond != 400 {
		t.Fatalf("network rates = %v/%v", report.NetworkRXBytesPerSecond, report.NetworkTXBytesPerSecond)
	}

	current.Network = NetworkCounters{RXBytes: 1_400, TXBytes: 1_900}
	report, err = BuildReport(previous, current)
	if err != nil {
		t.Fatal(err)
	}
	if report.NetworkRXBytesPerSecond != 200 || report.NetworkTXBytesPerSecond != 0 {
		t.Fatalf("network rates = %v/%v", report.NetworkRXBytesPerSecond, report.NetworkTXBytesPerSecond)
	}
}

func TestBuildReportRejectsCountersOutsideDomainRange(t *testing.T) {
	previous := validSnapshot(time.Unix(100, 0))
	current := validSnapshot(previous.CapturedAt.Add(time.Second))
	current.CPU = CPUCounters{Total: 1_100, Idle: 450}
	current.Network.RXBytes = uint64(math.MaxInt64) + 1
	if _, err := BuildReport(previous, current); err == nil {
		t.Fatal("BuildReport() error = nil, want network range error")
	}
}

func TestBuildReportRejectsInvalidCurrentSnapshot(t *testing.T) {
	previous := validSnapshot(time.Unix(100, 0))
	current := validSnapshot(previous.CapturedAt.Add(time.Second))
	current.CPU = CPUCounters{Total: 1_100, Idle: 450}
	current.MemoryTotalBytes = 0
	if _, err := BuildReport(previous, current); err == nil {
		t.Fatal("BuildReport() error = nil, want domain validation error")
	}
}

func TestBuildReportCPUPercentageBoundaries(t *testing.T) {
	previous := validSnapshot(time.Unix(100, 0))
	tests := []struct {
		name string
		cpu  CPUCounters
		want float64
	}{
		{name: "zero", cpu: CPUCounters{Total: 1_100, Idle: 500}, want: 0},
		{name: "one hundred", cpu: CPUCounters{Total: 1_100, Idle: 400}, want: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := validSnapshot(previous.CapturedAt.Add(time.Second))
			current.CPU = tt.cpu
			report, err := BuildReport(previous, current)
			if err != nil {
				t.Fatal(err)
			}
			if report.CPUPct != tt.want {
				t.Fatalf("CPUPct = %v, want %v", report.CPUPct, tt.want)
			}
		})
	}
}
