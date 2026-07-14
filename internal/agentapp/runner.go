package agentapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/agentclient"
	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/linuxmetrics"
)

type Collector interface {
	Capture(context.Context) (linuxmetrics.Snapshot, error)
}

type Sender interface {
	Send(context.Context, domain.MetricReport) error
}

type buildFunc func(linuxmetrics.Snapshot, linuxmetrics.Snapshot) (domain.MetricReport, error)
type waitFunc func(context.Context, time.Duration) error

type Runner struct {
	collector Collector
	sender    Sender
	interval  time.Duration
	logger    *slog.Logger
	build     buildFunc
	wait      waitFunc
}

func New(collector Collector, sender Sender, interval time.Duration, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Runner{
		collector: collector,
		sender:    sender,
		interval:  interval,
		logger:    logger,
		build:     linuxmetrics.BuildReport,
		wait:      waitInterval,
	}
}

func (r *Runner) Once(ctx context.Context) error {
	previous, err := r.collector.Capture(ctx)
	if err != nil {
		return canceledAsSuccess(ctx, err)
	}
	if err := r.wait(ctx, r.interval); err != nil {
		return canceledAsSuccess(ctx, err)
	}
	current, err := r.collector.Capture(ctx)
	if err != nil {
		return canceledAsSuccess(ctx, err)
	}
	report, err := r.build(previous, current)
	if err != nil {
		return canceledAsSuccess(ctx, err)
	}
	return canceledAsSuccess(ctx, r.sender.Send(ctx, report))
}

func (r *Runner) Run(ctx context.Context) error {
	var previous *linuxmetrics.Snapshot
	lastFailureClass := ""
	established := false

	logFailure := func(class string) {
		if class == lastFailureClass {
			return
		}
		lastFailureClass = class
		r.logger.Warn("agent reporting failed", "failure_class", class)
	}
	logSuccess := func() {
		if lastFailureClass != "" {
			r.logger.Info("agent reporting recovered")
			lastFailureClass = ""
			established = true
			return
		}
		if !established {
			r.logger.Info("agent reporting established")
			established = true
		}
	}

	for {
		if previous == nil {
			baseline, err := r.collector.Capture(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return canceledAsSuccess(ctx, err)
				}
				logFailure("capture")
				if err := r.wait(ctx, r.interval); err != nil {
					return canceledAsSuccess(ctx, err)
				}
				continue
			}
			previous = &baseline
		}

		if err := r.wait(ctx, r.interval); err != nil {
			return canceledAsSuccess(ctx, err)
		}
		current, err := r.collector.Capture(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return canceledAsSuccess(ctx, err)
			}
			logFailure("capture")
			continue
		}

		report, err := r.build(*previous, current)
		if err != nil {
			if ctx.Err() != nil {
				return canceledAsSuccess(ctx, err)
			}
			if errors.Is(err, linuxmetrics.ErrCounterReset) || errors.Is(err, linuxmetrics.ErrInvalidElapsed) {
				previous = &current
				continue
			}
			logFailure("build")
			continue
		}
		previous = &current

		err = r.sender.Send(ctx, report)
		if err == nil {
			logSuccess()
			continue
		}
		if ctx.Err() != nil {
			return canceledAsSuccess(ctx, err)
		}
		if errors.Is(err, agentclient.ErrRetryable) {
			logFailure("delivery_retryable")
			continue
		}
		if errors.Is(err, agentclient.ErrPermanent) {
			logFailure("delivery_permanent")
			return err
		}
		return err
	}
}

func waitInterval(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func canceledAsSuccess(ctx context.Context, err error) error {
	if err != nil && ctx != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return nil
	}
	return err
}
