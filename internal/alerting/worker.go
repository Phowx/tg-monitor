package alerting

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const (
	defaultAlertInterval = 5 * time.Second
	defaultAlertBatch    = 100
)

type Config struct {
	AdminTelegramIDs []int64
	Interval         time.Duration
	BatchSize        int
}

type tickerFactory func(time.Duration) (<-chan time.Time, func())

type Worker struct {
	repository Repository
	dispatcher *dispatcher
	adminIDs   []int64
	interval   time.Duration
	batchSize  int
	now        func() time.Time
	logger     *slog.Logger
	newTicker  tickerFactory

	evaluationFailed bool
	deliveryFailed   bool
}

func New(config Config, repository Repository, sender Sender, now func() time.Time, logger *slog.Logger) (*Worker, error) {
	if repository == nil {
		return nil, errors.New("create alert worker: repository is required")
	}
	if sender == nil {
		return nil, errors.New("create alert worker: sender is required")
	}
	adminIDs, err := validatedAdminIDs(config.AdminTelegramIDs)
	if err != nil {
		return nil, err
	}
	interval := config.Interval
	if interval == 0 {
		interval = defaultAlertInterval
	}
	if interval < 0 {
		return nil, errors.New("create alert worker: interval must be positive")
	}
	batchSize := config.BatchSize
	if batchSize == 0 {
		batchSize = defaultAlertBatch
	}
	if batchSize < 1 || batchSize > defaultAlertBatch {
		return nil, errors.New("create alert worker: batch size must be between 1 and 100")
	}
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	worker := &Worker{
		repository: repository,
		adminIDs:   adminIDs,
		interval:   interval,
		batchSize:  batchSize,
		now:        now,
		logger:     logger,
		newTicker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
	}
	worker.dispatcher = newDispatcher(repository, sender, adminIDs, batchSize)
	return worker, nil
}

func (worker *Worker) Run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	worker.runOnce(ctx)
	if ctx.Err() != nil {
		return
	}
	ticks, stop := worker.newTicker(worker.interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			worker.runOnce(ctx)
		}
	}
}

func (worker *Worker) runOnce(ctx context.Context) {
	nowMS := worker.now().UTC().UnixMilli()
	evaluation, evaluationErr := worker.repository.EvaluateAlerts(ctx, nowMS, worker.adminIDs)
	worker.logEvaluation(ctx, evaluation, evaluationErr)

	delivery, deliveryErr := worker.dispatcher.RunOnce(ctx, nowMS)
	worker.logDelivery(ctx, delivery, deliveryErr)
}

func (worker *Worker) logEvaluation(ctx context.Context, result domain.AlertEvaluationResult, err error) {
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return
	}
	if err != nil {
		if !worker.evaluationFailed {
			worker.logger.Error("alert evaluation failed", "operation", "evaluate")
		}
		worker.evaluationFailed = true
		return
	}
	if worker.evaluationFailed {
		worker.logger.Info("alert evaluation recovered", "operation", "evaluate")
		worker.evaluationFailed = false
	}
	if result.Offline > 0 || result.Recovery > 0 || result.Queued > 0 {
		worker.logger.Info("alert evaluation completed", "offline", result.Offline, "recovery", result.Recovery, "queued", result.Queued)
	}
}

func (worker *Worker) logDelivery(ctx context.Context, result DispatchResult, err error) {
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		return
	}
	failed := err != nil || result.Retried > 0
	if failed {
		if !worker.deliveryFailed {
			worker.logger.Error("alert delivery failed", "operation", "deliver", "retried", result.Retried)
		}
		worker.deliveryFailed = true
	} else if worker.deliveryFailed {
		worker.logger.Info("alert delivery recovered", "operation", "deliver")
		worker.deliveryFailed = false
	}
	if result.Delivered > 0 || result.Suppressed > 0 {
		worker.logger.Info("alert delivery completed", "delivered", result.Delivered, "suppressed", result.Suppressed)
	}
}

func validatedAdminIDs(ids []int64) ([]int64, error) {
	validated := append([]int64(nil), ids...)
	sort.Slice(validated, func(left, right int) bool { return validated[left] < validated[right] })
	for index, id := range validated {
		if id <= 0 {
			return nil, errors.New("create alert worker: administrator IDs must be positive")
		}
		if index > 0 && id == validated[index-1] {
			return nil, errors.New("create alert worker: administrator IDs must be unique")
		}
	}
	return validated, nil
}
