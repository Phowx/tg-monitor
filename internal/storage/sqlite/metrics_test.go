package sqlite

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func createTestServer(t *testing.T, store *Store, name string, sortOrder int, hashByte byte) domain.Server {
	t.Helper()
	server, err := store.CreateServer(context.Background(), domain.Server{
		Name: name, SortOrder: sortOrder, Enabled: true, TokenSHA256: serverHash(hashByte),
	})
	if err != nil {
		t.Fatalf("CreateServer(%q) error = %v", name, err)
	}
	return server
}

func testMetricReport(seed int64) domain.MetricReport {
	return domain.MetricReport{
		CapturedAtMS:            1_700_000_000_000 + seed,
		CPUPct:                  20 + float64(seed),
		MemoryTotalBytes:        10_000 + seed,
		MemoryUsedBytes:         4_000 + seed,
		RootDiskTotalBytes:      20_000 + seed,
		RootDiskUsedBytes:       8_000 + seed,
		Load1:                   0.1 + float64(seed),
		Load5:                   0.2 + float64(seed),
		Load15:                  0.3 + float64(seed),
		NetworkRXTotalBytes:     30_000 + seed,
		NetworkTXTotalBytes:     40_000 + seed,
		NetworkRXBytesPerSecond: 50 + float64(seed),
		NetworkTXBytesPerSecond: 60 + float64(seed),
		UptimeSeconds:           7_000 + seed,
		System:                  domain.SystemInfo{Hostname: "host", OS: "linux", Kernel: "6.12", Arch: "amd64"},
	}
}

func TestLatestMetricsRepositoryRoundTripUpsertAndOrder(t *testing.T) {
	store := openTestStore(t)
	store.nowMS = func() int64 { return 1_000 }
	ctx := context.Background()
	beta := createTestServer(t, store, "beta", 2, 1)
	alpha := createTestServer(t, store, "alpha", 1, 2)

	for _, latest := range []domain.LatestMetrics{
		{ServerID: beta.ID, ReceivedAtMS: 1_700_000_000_500, Report: testMetricReport(1)},
		{ServerID: alpha.ID, ReceivedAtMS: 1_700_000_000_600, Report: testMetricReport(2)},
	} {
		if err := store.UpsertLatestMetrics(ctx, latest); err != nil {
			t.Fatalf("UpsertLatestMetrics() error = %v", err)
		}
	}

	got, err := store.GetLatestMetrics(ctx, beta.ID)
	if err != nil {
		t.Fatalf("GetLatestMetrics() error = %v", err)
	}
	want := domain.LatestMetrics{ServerID: beta.ID, ReceivedAtMS: 1_700_000_000_500, Report: testMetricReport(1)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetLatestMetrics() = %#v, want %#v", got, want)
	}

	want.ReceivedAtMS++
	want.Report = testMetricReport(3)
	if err := store.UpsertLatestMetrics(ctx, want); err != nil {
		t.Fatalf("UpsertLatestMetrics(replace) error = %v", err)
	}
	got, err = store.GetLatestMetrics(ctx, beta.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetLatestMetrics(replaced) = %#v, error %v, want %#v", got, err, want)
	}

	listed, err := store.ListLatestMetrics(ctx)
	if err != nil {
		t.Fatalf("ListLatestMetrics() error = %v", err)
	}
	if len(listed) != 2 || listed[0].ServerID != alpha.ID || listed[1].ServerID != beta.ID {
		t.Fatalf("ListLatestMetrics() order = %#v", listed)
	}
}

func TestLatestMetricsRepositoryRejectsInvalidAndMissing(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	server := createTestServer(t, store, "server", 0, 1)
	invalid := domain.LatestMetrics{ServerID: server.ID, ReceivedAtMS: 1, Report: testMetricReport(0)}
	invalid.Report.CPUPct = math.NaN()
	if err := store.UpsertLatestMetrics(ctx, invalid); err == nil {
		t.Fatal("UpsertLatestMetrics(invalid) error = nil")
	}
	if _, err := store.GetLatestMetrics(ctx, server.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetLatestMetrics(missing) error = %v, want ErrNotFound", err)
	}
}

func testMinuteSample(serverID, bucketMS int64, seed float64) domain.MinuteSample {
	return domain.MinuteSample{
		ServerID:                serverID,
		BucketMS:                bucketMS,
		CPUPct:                  10 + seed,
		MemoryTotalBytes:        10_000,
		MemoryUsedBytes:         4_000 + int64(seed),
		RootDiskTotalBytes:      20_000,
		RootDiskUsedBytes:       8_000 + int64(seed),
		Load1:                   0.1 + seed,
		Load5:                   0.2 + seed,
		Load15:                  0.3 + seed,
		NetworkRXTotalBytes:     30_000 + int64(seed),
		NetworkTXTotalBytes:     40_000 + int64(seed),
		NetworkRXBytesPerSecond: 50 + seed,
		NetworkTXBytesPerSecond: 60 + seed,
		UptimeSeconds:           7_000 + int64(seed),
		System:                  domain.SystemInfo{Hostname: "host", OS: "linux", Kernel: "6.12", Arch: "amd64"},
	}
}

func TestMinuteSampleRepositoryUpsertRangeAndCleanup(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	server := createTestServer(t, store, "server", 0, 1)

	for i, bucket := range []int64{60_000, 120_000, 180_000} {
		if err := store.UpsertMinuteSample(ctx, testMinuteSample(server.ID, bucket, float64(i))); err != nil {
			t.Fatalf("UpsertMinuteSample(%d) error = %v", bucket, err)
		}
	}
	replacement := testMinuteSample(server.ID, 120_000, 9)
	if err := store.UpsertMinuteSample(ctx, replacement); err != nil {
		t.Fatalf("UpsertMinuteSample(replacement) error = %v", err)
	}

	got, err := store.QueryMinuteSamples(ctx, server.ID, 60_000, 180_000)
	if err != nil {
		t.Fatalf("QueryMinuteSamples() error = %v", err)
	}
	want := []domain.MinuteSample{testMinuteSample(server.ID, 60_000, 0), replacement}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("QueryMinuteSamples() = %#v, want %#v", got, want)
	}

	deleted, err := store.DeleteMetricSamplesBefore(ctx, 120_000)
	if err != nil {
		t.Fatalf("DeleteMetricSamplesBefore() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("DeleteMetricSamplesBefore() = %d, want 1", deleted)
	}
	remaining, err := store.QueryMinuteSamples(ctx, server.ID, 0, 240_000)
	if err != nil {
		t.Fatalf("QueryMinuteSamples(remaining) error = %v", err)
	}
	if len(remaining) != 2 || remaining[0].BucketMS != 120_000 || remaining[1].BucketMS != 180_000 {
		t.Fatalf("remaining samples = %#v", remaining)
	}
}

func TestMinuteSampleRepositoryRejectsInvalidSample(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	server := createTestServer(t, store, "server", 0, 1)
	invalid := testMinuteSample(server.ID, 60_001, 0)
	if err := store.UpsertMinuteSample(ctx, invalid); err == nil {
		t.Fatal("UpsertMinuteSample(non-minute bucket) error = nil")
	}
}
