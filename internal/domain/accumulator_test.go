package domain

import (
	"reflect"
	"testing"
)

func TestMinuteAccumulatorReturnsNoSampleBeforeAdd(t *testing.T) {
	accumulator := NewMinuteAccumulator(1_710_000_059_123)
	if _, ok := accumulator.Sample(1); ok {
		t.Fatal("Sample() ok = true before Add(), want false")
	}
}

func TestMinuteAccumulatorAveragesGaugesAndKeepsLatestCounters(t *testing.T) {
	accumulator := NewMinuteAccumulator(1_710_000_059_123)

	first := validMetricReport()
	first.CPUPct = 20
	first.MemoryUsedBytes = 4_000
	first.RootDiskUsedBytes = 30_000
	first.Load1, first.Load5, first.Load15 = 1, 2, 3
	first.NetworkRXBytesPerSecond = 100
	first.NetworkTXBytesPerSecond = 200

	last := validMetricReport()
	last.CapturedAtMS++
	last.CPUPct = 40
	last.MemoryTotalBytes = 32_000
	last.MemoryUsedBytes = 8_000
	last.RootDiskTotalBytes = 200_000
	last.RootDiskUsedBytes = 50_000
	last.Load1, last.Load5, last.Load15 = 3, 4, 5
	last.NetworkRXTotalBytes = 90_000
	last.NetworkTXTotalBytes = 100_000
	last.NetworkRXBytesPerSecond = 300
	last.NetworkTXBytesPerSecond = 400
	last.UptimeSeconds = 7_200
	last.System = SystemInfo{Hostname: "host-2", OS: "linux-next", Kernel: "6.13", Arch: "arm64"}

	if err := accumulator.Add(first); err != nil {
		t.Fatalf("Add(first) error = %v", err)
	}
	if err := accumulator.Add(last); err != nil {
		t.Fatalf("Add(last) error = %v", err)
	}

	got, ok := accumulator.Sample(7)
	if !ok {
		t.Fatal("Sample() ok = false, want true")
	}
	want := MinuteSample{
		ServerID:                7,
		BucketMS:                1_710_000_000_000,
		CPUPct:                  30,
		MemoryTotalBytes:        32_000,
		MemoryUsedBytes:         6_000,
		RootDiskTotalBytes:      200_000,
		RootDiskUsedBytes:       40_000,
		Load1:                   2,
		Load5:                   3,
		Load15:                  4,
		NetworkRXTotalBytes:     90_000,
		NetworkTXTotalBytes:     100_000,
		NetworkRXBytesPerSecond: 200,
		NetworkTXBytesPerSecond: 300,
		UptimeSeconds:           7_200,
		System:                  last.System,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Sample() = %#v, want %#v", got, want)
	}
}

func TestMinuteAccumulatorRejectsInvalidReportWithoutChangingSample(t *testing.T) {
	accumulator := NewMinuteAccumulator(1_710_000_000_000)
	valid := validMetricReport()
	if err := accumulator.Add(valid); err != nil {
		t.Fatalf("Add(valid) error = %v", err)
	}
	want, _ := accumulator.Sample(1)

	invalid := valid
	invalid.CPUPct = 101
	if err := accumulator.Add(invalid); err == nil {
		t.Fatal("Add(invalid) error = nil, want non-nil")
	}
	got, _ := accumulator.Sample(1)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Sample() changed after rejected Add(): got %#v, want %#v", got, want)
	}
}
