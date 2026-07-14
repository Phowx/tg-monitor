package linuxmetrics

import (
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

type CPUCounters struct {
	Total uint64
	Idle  uint64
}

type NetworkCounters struct {
	RXBytes uint64
	TXBytes uint64
}

type Snapshot struct {
	CapturedAt         time.Time
	CPU                CPUCounters
	MemoryTotalBytes   int64
	MemoryUsedBytes    int64
	RootDiskTotalBytes int64
	RootDiskUsedBytes  int64
	Load1              float64
	Load5              float64
	Load15             float64
	Network            NetworkCounters
	UptimeSeconds      int64
	System             domain.SystemInfo
}
