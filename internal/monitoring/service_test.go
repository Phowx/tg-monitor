package monitoring

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

type recordingRepository struct {
	latest         []domain.LatestMetrics
	samples        []domain.MinuteSample
	sampleAttempts []domain.MinuteSample
	latestErr      error
	sampleErrors   map[int64]error
}

func (repository *recordingRepository) UpsertLatestMetrics(_ context.Context, latest domain.LatestMetrics) error {
	if repository.latestErr != nil {
		return repository.latestErr
	}
	repository.latest = append(repository.latest, latest)
	return nil
}

func (repository *recordingRepository) UpsertMinuteSample(_ context.Context, sample domain.MinuteSample) error {
	repository.sampleAttempts = append(repository.sampleAttempts, sample)
	if err := repository.sampleErrors[sample.ServerID]; err != nil {
		return err
	}
	repository.samples = append(repository.samples, sample)
	return nil
}

func validReportAt(capturedAtMS int64) domain.MetricReport {
	return domain.MetricReport{
		CapturedAtMS:            capturedAtMS,
		CPUPct:                  20,
		MemoryTotalBytes:        16_000,
		MemoryUsedBytes:         4_000,
		RootDiskTotalBytes:      100_000,
		RootDiskUsedBytes:       25_000,
		Load1:                   0.1,
		Load5:                   0.2,
		Load15:                  0.3,
		NetworkRXTotalBytes:     1_000,
		NetworkTXTotalBytes:     2_000,
		NetworkRXBytesPerSecond: 10,
		NetworkTXBytesPerSecond: 20,
		UptimeSeconds:           3_600,
		System:                  domain.SystemInfo{Hostname: "host", OS: "linux", Kernel: "6.12", Arch: "amd64"},
	}
}

func TestIngestWritesLatestAndCheckpointAveragesActiveMinute(t *testing.T) {
	repository := &recordingRepository{}
	service := NewService(repository)
	first := validReportAt(120_001)
	second := validReportAt(120_002)
	first.CPUPct = 20
	second.CPUPct = 40

	if err := service.Ingest(context.Background(), 7, 200_000, first); err != nil {
		t.Fatalf("Ingest(first) error = %v", err)
	}
	if err := service.Ingest(context.Background(), 7, 200_001, second); err != nil {
		t.Fatalf("Ingest(second) error = %v", err)
	}
	if err := service.Checkpoint(context.Background()); err != nil {
		t.Fatalf("Checkpoint() error = %v", err)
	}

	wantLatest := []domain.LatestMetrics{
		{ServerID: 7, ReceivedAtMS: 200_000, Report: first},
		{ServerID: 7, ReceivedAtMS: 200_001, Report: second},
	}
	if !reflect.DeepEqual(repository.latest, wantLatest) {
		t.Fatalf("latest writes = %#v, want %#v", repository.latest, wantLatest)
	}
	if len(repository.samples) != 1 || repository.samples[0].BucketMS != 120_000 || repository.samples[0].CPUPct != 30 {
		t.Fatalf("checkpoint sample = %#v, want bucket 120000 CPU 30", repository.samples)
	}
}

func TestIngestFlushesPreviousMinuteBeforeStartingNext(t *testing.T) {
	repository := &recordingRepository{}
	service := NewService(repository)

	if err := service.Ingest(context.Background(), 7, 200_000, validReportAt(120_001)); err != nil {
		t.Fatalf("Ingest(first) error = %v", err)
	}
	if err := service.Ingest(context.Background(), 7, 200_001, validReportAt(180_001)); err != nil {
		t.Fatalf("Ingest(next minute) error = %v", err)
	}

	if len(repository.samples) != 1 || repository.samples[0].BucketMS != 120_000 {
		t.Fatalf("rollover samples = %#v, want prior minute", repository.samples)
	}
	if err := service.Checkpoint(context.Background()); err != nil {
		t.Fatalf("Checkpoint() error = %v", err)
	}
	if len(repository.samples) != 2 || repository.samples[1].BucketMS != 180_000 {
		t.Fatalf("checkpoint samples = %#v, want active next minute", repository.samples)
	}
}

func TestIngestRejectsOlderMinuteAndInvalidInputWithoutWrites(t *testing.T) {
	repository := &recordingRepository{}
	service := NewService(repository)
	if err := service.Ingest(context.Background(), 7, 200_000, validReportAt(180_001)); err != nil {
		t.Fatalf("Ingest(first) error = %v", err)
	}
	wantLatest := append([]domain.LatestMetrics(nil), repository.latest...)

	if err := service.Ingest(context.Background(), 7, 200_001, validReportAt(120_001)); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("Ingest(older) error = %v, want ErrOutOfOrder", err)
	}
	invalid := validReportAt(180_002)
	invalid.CPUPct = 101
	if err := service.Ingest(context.Background(), 7, 200_002, invalid); err == nil {
		t.Fatal("Ingest(invalid report) error = nil")
	}
	if err := service.Ingest(context.Background(), 0, 200_002, validReportAt(180_002)); err == nil {
		t.Fatal("Ingest(invalid server ID) error = nil")
	}
	if err := service.Ingest(context.Background(), 7, 0, validReportAt(180_002)); err == nil {
		t.Fatal("Ingest(invalid receive time) error = nil")
	}
	if !reflect.DeepEqual(repository.latest, wantLatest) || len(repository.samples) != 0 {
		t.Fatalf("rejected input mutated repository: latest=%#v samples=%#v", repository.latest, repository.samples)
	}
}

func TestIngestRepositoryFailuresPreserveRetryableState(t *testing.T) {
	repository := &recordingRepository{}
	service := NewService(repository)
	first := validReportAt(120_001)
	if err := service.Ingest(context.Background(), 7, 200_000, first); err != nil {
		t.Fatalf("Ingest(first) error = %v", err)
	}

	latestErr := errors.New("latest unavailable")
	repository.latestErr = latestErr
	if err := service.Ingest(context.Background(), 7, 200_001, validReportAt(120_002)); !errors.Is(err, latestErr) {
		t.Fatalf("Ingest(latest failure) error = %v, want wrapped latest failure", err)
	}
	repository.latestErr = nil
	if err := service.Checkpoint(context.Background()); err != nil {
		t.Fatalf("Checkpoint() error = %v", err)
	}
	if len(repository.samples) != 1 || repository.samples[0].CPUPct != first.CPUPct {
		t.Fatalf("failed latest write mutated active sample: %#v", repository.samples)
	}

	sampleErr := errors.New("sample unavailable")
	repository.sampleErrors = map[int64]error{7: sampleErr}
	if err := service.Ingest(context.Background(), 7, 200_002, validReportAt(180_001)); !errors.Is(err, sampleErr) {
		t.Fatalf("Ingest(rollover failure) error = %v, want wrapped sample failure", err)
	}
	repository.sampleErrors = nil
	if err := service.Ingest(context.Background(), 7, 200_003, validReportAt(180_001)); err != nil {
		t.Fatalf("Ingest(rollover retry) error = %v", err)
	}
	if got := repository.samples[len(repository.samples)-1].BucketMS; got != 120_000 {
		t.Fatalf("retried rollover bucket = %d, want 120000", got)
	}
}

func TestCheckpointAttemptsEveryServerAndJoinsErrors(t *testing.T) {
	firstErr := errors.New("server one unavailable")
	secondErr := errors.New("server two unavailable")
	repository := &recordingRepository{}
	service := NewService(repository)
	if err := service.Ingest(context.Background(), 1, 200_000, validReportAt(120_001)); err != nil {
		t.Fatal(err)
	}
	if err := service.Ingest(context.Background(), 2, 200_000, validReportAt(120_001)); err != nil {
		t.Fatal(err)
	}
	repository.sampleErrors = map[int64]error{1: firstErr, 2: secondErr}

	err := service.Checkpoint(context.Background())
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Checkpoint() error = %v, want both repository errors", err)
	}
	if len(repository.sampleAttempts) != 2 {
		t.Fatalf("Checkpoint() attempts = %d, want 2", len(repository.sampleAttempts))
	}
}
