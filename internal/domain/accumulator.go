package domain

import "time"

type MinuteAccumulator struct {
	bucketMS int64
	count    int64
	cpu      float64
	memory   int64
	disk     int64
	load1    float64
	load5    float64
	load15   float64
	rxPerSec float64
	txPerSec float64
	last     MetricReport
}

func NewMinuteAccumulator(atMS int64) *MinuteAccumulator {
	return &MinuteAccumulator{
		bucketMS: time.UnixMilli(atMS).UTC().Truncate(time.Minute).UnixMilli(),
	}
}

func (a *MinuteAccumulator) Add(report MetricReport) error {
	if err := report.Validate(); err != nil {
		return err
	}

	a.count++
	a.cpu += report.CPUPct
	a.memory += report.MemoryUsedBytes
	a.disk += report.RootDiskUsedBytes
	a.load1 += report.Load1
	a.load5 += report.Load5
	a.load15 += report.Load15
	a.rxPerSec += report.NetworkRXBytesPerSecond
	a.txPerSec += report.NetworkTXBytesPerSecond
	a.last = report
	return nil
}

func (a *MinuteAccumulator) Sample(serverID int64) (MinuteSample, bool) {
	if a.count == 0 {
		return MinuteSample{}, false
	}

	count := float64(a.count)
	return MinuteSample{
		ServerID:                serverID,
		BucketMS:                a.bucketMS,
		CPUPct:                  a.cpu / count,
		MemoryTotalBytes:        a.last.MemoryTotalBytes,
		MemoryUsedBytes:         a.memory / a.count,
		RootDiskTotalBytes:      a.last.RootDiskTotalBytes,
		RootDiskUsedBytes:       a.disk / a.count,
		Load1:                   a.load1 / count,
		Load5:                   a.load5 / count,
		Load15:                  a.load15 / count,
		NetworkRXTotalBytes:     a.last.NetworkRXTotalBytes,
		NetworkTXTotalBytes:     a.last.NetworkTXTotalBytes,
		NetworkRXBytesPerSecond: a.rxPerSec / count,
		NetworkTXBytesPerSecond: a.txPerSec / count,
		UptimeSeconds:           a.last.UptimeSeconds,
		System:                  a.last.System,
	}, true
}
