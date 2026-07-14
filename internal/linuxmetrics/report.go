package linuxmetrics

import (
	"errors"
	"fmt"
	"math"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

var (
	ErrCounterReset   = errors.New("metric counter reset")
	ErrInvalidElapsed = errors.New("invalid snapshot elapsed time")
)

func BuildReport(previous, current Snapshot) (domain.MetricReport, error) {
	elapsed := current.CapturedAt.Sub(previous.CapturedAt).Seconds()
	if elapsed <= 0 || math.IsNaN(elapsed) || math.IsInf(elapsed, 0) {
		return domain.MetricReport{}, ErrInvalidElapsed
	}

	if current.CPU.Total <= previous.CPU.Total || current.CPU.Idle < previous.CPU.Idle {
		return domain.MetricReport{}, ErrCounterReset
	}
	totalDelta := current.CPU.Total - previous.CPU.Total
	idleDelta := current.CPU.Idle - previous.CPU.Idle
	if idleDelta > totalDelta {
		return domain.MetricReport{}, ErrCounterReset
	}

	if current.Network.RXBytes > math.MaxInt64 || current.Network.TXBytes > math.MaxInt64 {
		return domain.MetricReport{}, fmt.Errorf("network counter exceeds report range")
	}

	cpuPct := 100 * float64(totalDelta-idleDelta) / float64(totalDelta)
	cpuPct = math.Max(0, math.Min(100, cpuPct))
	report := domain.MetricReport{
		CapturedAtMS:            current.CapturedAt.UTC().UnixMilli(),
		CPUPct:                  cpuPct,
		MemoryTotalBytes:        current.MemoryTotalBytes,
		MemoryUsedBytes:         current.MemoryUsedBytes,
		RootDiskTotalBytes:      current.RootDiskTotalBytes,
		RootDiskUsedBytes:       current.RootDiskUsedBytes,
		Load1:                   current.Load1,
		Load5:                   current.Load5,
		Load15:                  current.Load15,
		NetworkRXTotalBytes:     int64(current.Network.RXBytes),
		NetworkTXTotalBytes:     int64(current.Network.TXBytes),
		NetworkRXBytesPerSecond: counterRate(previous.Network.RXBytes, current.Network.RXBytes, elapsed),
		NetworkTXBytesPerSecond: counterRate(previous.Network.TXBytes, current.Network.TXBytes, elapsed),
		UptimeSeconds:           current.UptimeSeconds,
		System:                  current.System,
	}
	if err := report.Validate(); err != nil {
		return domain.MetricReport{}, fmt.Errorf("validate metric report: %w", err)
	}
	return report, nil
}

func counterRate(previous, current uint64, elapsed float64) float64 {
	if current < previous {
		return 0
	}
	return float64(current-previous) / elapsed
}
