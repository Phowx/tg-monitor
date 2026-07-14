package agentapp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/agentclient"
	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/linuxmetrics"
)

type collectorFunc func(context.Context) (linuxmetrics.Snapshot, error)

func (function collectorFunc) Capture(ctx context.Context) (linuxmetrics.Snapshot, error) {
	return function(ctx)
}

type senderFunc func(context.Context, domain.MetricReport) error

func (function senderFunc) Send(ctx context.Context, report domain.MetricReport) error {
	return function(ctx, report)
}

func runnerSnapshot(index int) linuxmetrics.Snapshot {
	return linuxmetrics.Snapshot{
		CapturedAt:         time.Unix(int64(100+index), 0),
		CPU:                linuxmetrics.CPUCounters{Total: uint64(1_000 + index*100), Idle: uint64(400 + index*25)},
		MemoryTotalBytes:   16_000,
		MemoryUsedBytes:    8_000,
		RootDiskTotalBytes: 100_000,
		RootDiskUsedBytes:  40_000,
		Load1:              0.1,
		Load5:              0.2,
		Load15:             0.3,
		Network:            linuxmetrics.NetworkCounters{RXBytes: uint64(1_000 + index*100), TXBytes: uint64(2_000 + index*200)},
		UptimeSeconds:      int64(3_600 + index),
		System:             domain.SystemInfo{Hostname: "body-hostname-canary", OS: "linux", Kernel: "6.12", Arch: "amd64"},
	}
}

func TestRunnerOnceUsesTwoSerialSnapshots(t *testing.T) {
	var order []string
	captures := 0
	runner := New(
		collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
			order = append(order, "capture")
			result := runnerSnapshot(captures)
			captures++
			return result, nil
		}),
		senderFunc(func(context.Context, domain.MetricReport) error {
			order = append(order, "send")
			return nil
		}),
		15*time.Second,
		slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
	)
	runner.wait = func(context.Context, time.Duration) error {
		order = append(order, "wait(15s)")
		return nil
	}
	runner.build = func(previous, current linuxmetrics.Snapshot) (domain.MetricReport, error) {
		order = append(order, "build")
		if previous != runnerSnapshot(0) || current != runnerSnapshot(1) {
			t.Fatalf("build snapshots = %#v / %#v", previous, current)
		}
		return linuxmetrics.BuildReport(previous, current)
	}

	if err := runner.Once(context.Background()); err != nil {
		t.Fatalf("Once() error = %v", err)
	}
	want := []string{"capture", "wait(15s)", "capture", "build", "send"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("call order = %#v, want %#v", order, want)
	}
}

func TestRunnerOncePropagatesStageFailures(t *testing.T) {
	sentinel := errors.New("stage failed")
	for _, stage := range []string{"first capture", "wait", "second capture", "build", "send"} {
		t.Run(stage, func(t *testing.T) {
			captures := 0
			runner := New(
				collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
					captures++
					if stage == "first capture" && captures == 1 || stage == "second capture" && captures == 2 {
						return linuxmetrics.Snapshot{}, sentinel
					}
					return runnerSnapshot(captures - 1), nil
				}),
				senderFunc(func(context.Context, domain.MetricReport) error {
					if stage == "send" {
						return sentinel
					}
					return nil
				}),
				time.Second,
				nil,
			)
			runner.wait = func(context.Context, time.Duration) error {
				if stage == "wait" {
					return sentinel
				}
				return nil
			}
			runner.build = func(previous, current linuxmetrics.Snapshot) (domain.MetricReport, error) {
				if stage == "build" {
					return domain.MetricReport{}, sentinel
				}
				return linuxmetrics.BuildReport(previous, current)
			}
			if err := runner.Once(context.Background()); !errors.Is(err, sentinel) {
				t.Fatalf("Once() error = %v, want sentinel", err)
			}
		})
	}
}

func TestRunnerOnceTreatsSuppliedContextCancellationAsSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := New(collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
		cancel()
		return linuxmetrics.Snapshot{}, context.Canceled
	}), senderFunc(func(context.Context, domain.MetricReport) error { return nil }), time.Second, nil)
	if err := runner.Once(ctx); err != nil {
		t.Fatalf("Once() error = %v, want nil", err)
	}

	runner = New(collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
		return linuxmetrics.Snapshot{}, context.Canceled
	}), senderFunc(func(context.Context, domain.MetricReport) error { return nil }), time.Second, nil)
	if err := runner.Once(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Once() unrelated cancellation = %v", err)
	}
}

func TestRunnerRunSuppressesRepeatedInitialCaptureFailureThenRecovers(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	calls := 0
	collector := collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
		calls++
		if calls <= 2 {
			return linuxmetrics.Snapshot{}, errors.New("token-canary capture body-hostname-canary")
		}
		return runnerSnapshot(calls - 3), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	waits := 0
	runner := New(collector, senderFunc(func(context.Context, domain.MetricReport) error { return nil }), time.Second, logger)
	runner.wait = func(context.Context, time.Duration) error {
		waits++
		if waits == 4 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	assertLogCount(t, logs.String(), "agent reporting failed", 1)
	assertLogCount(t, logs.String(), "agent reporting recovered", 1)
	assertLogSafe(t, logs.String())
}

func TestRunnerRunRetainsBaselineAfterCaptureFailure(t *testing.T) {
	snapshots := []linuxmetrics.Snapshot{runnerSnapshot(0), runnerSnapshot(2)}
	calls := 0
	runner := New(collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
		calls++
		if calls == 2 {
			return linuxmetrics.Snapshot{}, errors.New("capture failed")
		}
		result := snapshots[0]
		snapshots = snapshots[1:]
		return result, nil
	}), senderFunc(func(context.Context, domain.MetricReport) error { return nil }), time.Second, nil)
	built := 0
	runner.build = func(previous, current linuxmetrics.Snapshot) (domain.MetricReport, error) {
		built++
		if previous != runnerSnapshot(0) || current != runnerSnapshot(2) {
			t.Fatalf("build snapshots = %#v / %#v", previous, current)
		}
		return linuxmetrics.BuildReport(previous, current)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waits := 0
	runner.wait = func(context.Context, time.Duration) error {
		waits++
		if waits == 3 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if built != 1 {
		t.Fatalf("build calls = %d, want 1", built)
	}
}

func TestRunnerRunRebaselinesCounterAndElapsedErrors(t *testing.T) {
	for _, sentinel := range []error{linuxmetrics.ErrCounterReset, linuxmetrics.ErrInvalidElapsed} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			index := 0
			runner := New(collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
				result := runnerSnapshot(index)
				index++
				return result, nil
			}), senderFunc(func(context.Context, domain.MetricReport) error {
				return nil
			}), time.Second, nil)
			builds := 0
			runner.build = func(previous, current linuxmetrics.Snapshot) (domain.MetricReport, error) {
				builds++
				if builds == 1 {
					return domain.MetricReport{}, sentinel
				}
				if previous != runnerSnapshot(1) || current != runnerSnapshot(2) {
					t.Fatalf("second build snapshots = %#v / %#v", previous, current)
				}
				return linuxmetrics.BuildReport(previous, current)
			}
			ctx, cancel := context.WithCancel(context.Background())
			waits := 0
			runner.wait = func(context.Context, time.Duration) error {
				waits++
				if waits == 3 {
					cancel()
					return context.Canceled
				}
				return nil
			}
			if err := runner.Run(ctx); err != nil {
				t.Fatal(err)
			}
			if builds != 2 {
				t.Fatalf("build calls = %d, want 2", builds)
			}
		})
	}
}

func TestRunnerRunAdvancesBaselineWhenRetryableDeliveryFails(t *testing.T) {
	index := 0
	runner := New(collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
		result := runnerSnapshot(index)
		index++
		return result, nil
	}), senderFunc(func(context.Context, domain.MetricReport) error {
		return agentclient.ErrRetryable
	}), time.Second, nil)
	builds := 0
	runner.build = func(previous, current linuxmetrics.Snapshot) (domain.MetricReport, error) {
		builds++
		if builds == 2 && (previous != runnerSnapshot(1) || current != runnerSnapshot(2)) {
			t.Fatalf("second build snapshots = %#v / %#v", previous, current)
		}
		return linuxmetrics.BuildReport(previous, current)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waits := 0
	runner.wait = func(context.Context, time.Duration) error {
		waits++
		if waits == 3 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Fatalf("build calls = %d, want 2", builds)
	}
}

func TestRunnerRunLogsDeliveryTransitions(t *testing.T) {
	var logs bytes.Buffer
	index := 0
	sends := 0
	runner := New(collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
		result := runnerSnapshot(index)
		index++
		return result, nil
	}), senderFunc(func(context.Context, domain.MetricReport) error {
		sends++
		if sends <= 2 {
			return agentclient.ErrRetryable
		}
		return nil
	}), time.Second, slog.New(slog.NewJSONHandler(&logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	waits := 0
	runner.wait = func(context.Context, time.Duration) error {
		waits++
		if waits == 5 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	assertLogCount(t, logs.String(), "agent reporting failed", 1)
	assertLogCount(t, logs.String(), "agent reporting recovered", 1)
	assertLogCount(t, logs.String(), "agent reporting established", 0)
	assertLogSafe(t, logs.String())
}

func TestRunnerRunLogsOnlyFirstEstablishedSuccess(t *testing.T) {
	var logs bytes.Buffer
	index := 0
	runner := New(collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
		result := runnerSnapshot(index)
		index++
		return result, nil
	}), senderFunc(func(context.Context, domain.MetricReport) error { return nil }), time.Second, slog.New(slog.NewJSONHandler(&logs, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	waits := 0
	runner.wait = func(context.Context, time.Duration) error {
		waits++
		if waits == 3 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	assertLogCount(t, logs.String(), "agent reporting established", 1)
	assertLogCount(t, logs.String(), "agent reporting recovered", 0)
}

func TestRunnerRunStopsOnPermanentDeliveryFailure(t *testing.T) {
	index := 0
	runner := New(collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
		result := runnerSnapshot(index)
		index++
		return result, nil
	}), senderFunc(func(context.Context, domain.MetricReport) error { return agentclient.ErrPermanent }), time.Second, nil)
	runner.wait = func(context.Context, time.Duration) error { return nil }
	if err := runner.Run(context.Background()); !errors.Is(err, agentclient.ErrPermanent) {
		t.Fatalf("Run() error = %v, want ErrPermanent", err)
	}
}

func TestRunnerRunNeverOverlapsStages(t *testing.T) {
	var inFlight atomic.Int32
	var maximum atomic.Int32
	enter := func() func() {
		current := inFlight.Add(1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		return func() { inFlight.Add(-1) }
	}
	index := 0
	collector := collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
		leave := enter()
		defer leave()
		result := runnerSnapshot(index)
		index++
		return result, nil
	})
	runner := New(collector, senderFunc(func(context.Context, domain.MetricReport) error {
		leave := enter()
		defer leave()
		return nil
	}), time.Second, nil)
	runner.build = func(previous, current linuxmetrics.Snapshot) (domain.MetricReport, error) {
		leave := enter()
		defer leave()
		return linuxmetrics.BuildReport(previous, current)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waits := 0
	runner.wait = func(context.Context, time.Duration) error {
		leave := enter()
		defer leave()
		waits++
		if waits == 2 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	if err := runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum in-flight stages = %d, want 1", maximum.Load())
	}
}

func TestRunnerRunTreatsSenderCancellationFromContextAsSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	index := 0
	runner := New(collectorFunc(func(context.Context) (linuxmetrics.Snapshot, error) {
		result := runnerSnapshot(index)
		index++
		return result, nil
	}), senderFunc(func(context.Context, domain.MetricReport) error {
		cancel()
		return context.Canceled
	}), time.Second, nil)
	runner.wait = func(context.Context, time.Duration) error { return nil }
	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
}

func assertLogCount(t *testing.T, logs, message string, want int) {
	t.Helper()
	if got := strings.Count(logs, `"msg":"`+message+`"`); got != want {
		t.Fatalf("log count for %q = %d, want %d; logs=%s", message, got, want, logs)
	}
}

func assertLogSafe(t *testing.T, logs string) {
	t.Helper()
	for _, forbidden := range []string{"token-canary", "body-hostname-canary", "Authorization"} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("logs leaked %q: %s", forbidden, logs)
		}
	}
}
