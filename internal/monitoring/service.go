package monitoring

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

var ErrOutOfOrder = errors.New("metric report is older than the active minute")

type Repository interface {
	UpsertLatestMetrics(context.Context, domain.LatestMetrics) error
	UpsertMinuteSample(context.Context, domain.MinuteSample) error
}

type Service struct {
	mu         sync.Mutex
	repository Repository
	active     map[int64]*domain.MinuteAccumulator
	bucket     map[int64]int64
}

func NewService(repository Repository) *Service {
	return &Service{
		repository: repository,
		active:     make(map[int64]*domain.MinuteAccumulator),
		bucket:     make(map[int64]int64),
	}
}

func (service *Service) Ingest(ctx context.Context, serverID, receivedAtMS int64, report domain.MetricReport) error {
	if serverID <= 0 {
		return errors.New("ingest metrics: server ID must be positive")
	}
	if receivedAtMS <= 0 {
		return errors.New("ingest metrics: received time must be positive")
	}
	if err := report.Validate(); err != nil {
		return fmt.Errorf("ingest metrics: %w", err)
	}

	reportBucket := time.UnixMilli(report.CapturedAtMS).UTC().Truncate(time.Minute).UnixMilli()
	service.mu.Lock()
	defer service.mu.Unlock()

	active, exists := service.active[serverID]
	activeBucket := service.bucket[serverID]
	if exists && reportBucket < activeBucket {
		return ErrOutOfOrder
	}
	if exists && reportBucket > activeBucket {
		sample, ok := active.Sample(serverID)
		if !ok {
			return errors.New("ingest metrics: active minute has no reports")
		}
		if err := service.repository.UpsertMinuteSample(ctx, sample); err != nil {
			return fmt.Errorf("ingest metrics: flush previous minute: %w", err)
		}
	}

	latest := domain.LatestMetrics{ServerID: serverID, ReceivedAtMS: receivedAtMS, Report: report}
	if err := service.repository.UpsertLatestMetrics(ctx, latest); err != nil {
		return fmt.Errorf("ingest metrics: write latest: %w", err)
	}

	if !exists || reportBucket > activeBucket {
		active = domain.NewMinuteAccumulator(report.CapturedAtMS)
		if err := active.Add(report); err != nil {
			return fmt.Errorf("ingest metrics: add report: %w", err)
		}
		service.active[serverID] = active
		service.bucket[serverID] = reportBucket
		return nil
	}

	if err := active.Add(report); err != nil {
		return fmt.Errorf("ingest metrics: add report: %w", err)
	}
	return nil
}

func (service *Service) Checkpoint(ctx context.Context) error {
	service.mu.Lock()
	defer service.mu.Unlock()

	var checkpointErrors []error
	for serverID, active := range service.active {
		sample, ok := active.Sample(serverID)
		if !ok {
			continue
		}
		if err := service.repository.UpsertMinuteSample(ctx, sample); err != nil {
			checkpointErrors = append(checkpointErrors, fmt.Errorf("checkpoint server %d: %w", serverID, err))
		}
	}
	return errors.Join(checkpointErrors...)
}
