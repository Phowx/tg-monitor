package serverapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

type RetentionRepository interface {
	GetSettings(context.Context) (domain.Settings, error)
	DeleteMetricSamplesBefore(context.Context, int64) (int64, error)
}

func RunRetentionOnce(ctx context.Context, repository RetentionRepository, now time.Time) (int64, error) {
	settings, err := repository.GetSettings(ctx)
	if err != nil {
		return 0, fmt.Errorf("load retention settings: %w", err)
	}
	if settings.HistoryRetentionDays <= 0 {
		return 0, errors.New("history retention days must be positive")
	}
	cutoffMS := now.UTC().Add(-time.Duration(settings.HistoryRetentionDays) * 24 * time.Hour).UnixMilli()
	deleted, err := repository.DeleteMetricSamplesBefore(ctx, cutoffMS)
	if err != nil {
		return 0, fmt.Errorf("delete expired metric samples: %w", err)
	}
	return deleted, nil
}
