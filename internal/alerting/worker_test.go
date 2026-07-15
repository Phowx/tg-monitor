package alerting

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewWorkerValidatesAndCopiesConfiguration(t *testing.T) {
	repository := &alertRepositoryStub{}
	sender := &alertSenderStub{}
	worker, err := New(Config{AdminTelegramIDs: []int64{22, 11}}, repository, sender, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if worker.interval != 5*time.Second || worker.batchSize != 100 || !reflect.DeepEqual(worker.adminIDs, []int64{11, 22}) {
		t.Fatalf("worker config = interval %v batch %d IDs %v", worker.interval, worker.batchSize, worker.adminIDs)
	}

	for name, build := range map[string]func() (*Worker, error){
		"nil repository": func() (*Worker, error) { return New(Config{AdminTelegramIDs: []int64{1}}, nil, sender, time.Now, nil) },
		"nil sender": func() (*Worker, error) {
			return New(Config{AdminTelegramIDs: []int64{1}}, repository, nil, time.Now, nil)
		},
		"duplicate ID": func() (*Worker, error) {
			return New(Config{AdminTelegramIDs: []int64{1, 1}}, repository, sender, time.Now, nil)
		},
		"invalid ID": func() (*Worker, error) {
			return New(Config{AdminTelegramIDs: []int64{0}}, repository, sender, time.Now, nil)
		},
		"interval": func() (*Worker, error) {
			return New(Config{AdminTelegramIDs: []int64{1}, Interval: -1}, repository, sender, time.Now, nil)
		},
		"batch": func() (*Worker, error) {
			return New(Config{AdminTelegramIDs: []int64{1}, BatchSize: 101}, repository, sender, time.Now, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := build(); err == nil {
				t.Fatal("New(invalid) error = nil")
			}
		})
	}
}

func TestWorkerRunsImmediatelyContinuesAcrossFailuresAndLogsTransitionsSafely(t *testing.T) {
	var log bytes.Buffer
	repository := &alertRepositoryStub{
		evaluationErrors: []error{errors.New("EVALUATION-DETAIL-CANARY"), nil},
		listErrors:       []error{errors.New("DELIVERY-DETAIL-CANARY"), nil},
		operationCh:      make(chan string, 8),
	}
	logger := slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
		if attr.Key == slog.TimeKey {
			return slog.Attr{}
		}
		return attr
	}}))
	worker, err := New(Config{AdminTelegramIDs: []int64{42}, Interval: time.Hour, BatchSize: 7}, repository, &alertSenderStub{}, func() time.Time {
		return time.UnixMilli(100_000)
	}, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ticks := make(chan time.Time, 1)
	worker.newTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	waitOperations(t, repository.operationCh, "evaluate", "list")
	ticks <- time.Now()
	waitOperations(t, repository.operationCh, "evaluate", "list")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Worker.Run() did not stop after cancellation")
	}

	repository.mu.Lock()
	if repository.evaluationCalls != 2 || repository.listCalls != 2 || !reflect.DeepEqual(repository.evaluationIDs, [][]int64{{42}, {42}}) || !reflect.DeepEqual(repository.listLimits, []int{7, 7}) {
		t.Fatalf("worker calls = evaluations %d lists %d IDs %#v limits %v", repository.evaluationCalls, repository.listCalls, repository.evaluationIDs, repository.listLimits)
	}
	repository.mu.Unlock()
	logged := log.String()
	for _, required := range []string{"alert evaluation failed", "alert delivery failed", "alert evaluation recovered", "alert delivery recovered"} {
		if !strings.Contains(logged, required) {
			t.Errorf("logs missing %q: %s", required, logged)
		}
	}
	for _, forbidden := range []string{"EVALUATION-DETAIL-CANARY", "DELIVERY-DETAIL-CANARY", "42", "payload_json", "telegram_user_id"} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("logs leaked %q: %s", forbidden, logged)
		}
	}
}

func TestWorkerCanceledContextDoesNoWork(t *testing.T) {
	repository := &alertRepositoryStub{}
	worker, err := New(Config{AdminTelegramIDs: []int64{42}}, repository, &alertSenderStub{}, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	worker.Run(ctx)
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.evaluationCalls != 0 || repository.listCalls != 0 {
		t.Fatalf("canceled worker calls = evaluations %d lists %d", repository.evaluationCalls, repository.listCalls)
	}
}

func waitOperations(t *testing.T, operations <-chan string, want ...string) {
	t.Helper()
	for _, expected := range want {
		select {
		case got := <-operations:
			if got != expected {
				t.Fatalf("operation = %q, want %q", got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for operation %q", expected)
		}
	}
}
