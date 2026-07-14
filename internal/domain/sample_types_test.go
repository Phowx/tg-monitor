package domain

import "testing"

func TestMinuteSampleStoresSizesAsIntegerBytes(t *testing.T) {
	var memoryUsed int64 = MinuteSample{}.MemoryUsedBytes
	var diskUsed int64 = MinuteSample{}.RootDiskUsedBytes
	if memoryUsed != 0 || diskUsed != 0 {
		t.Fatalf("zero-value used bytes = %d/%d", memoryUsed, diskUsed)
	}
}
