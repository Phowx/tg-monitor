package alerting

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

type alertMark struct {
	kind            string
	id              int64
	atMS            int64
	nextAttemptAtMS int64
	class           string
}

type alertRepositoryStub struct {
	mu sync.Mutex

	evaluationResults []domain.AlertEvaluationResult
	evaluationErrors  []error
	evaluationCalls   int
	evaluationIDs     [][]int64
	due               []domain.AlertOutboxItem
	listErrors        []error
	listCalls         int
	listNowMS         []int64
	listLimits        []int
	preferences       map[int64]bool
	preferenceErrors  map[int64]error
	marks             []alertMark
	operationCh       chan string
}

func (stub *alertRepositoryStub) EvaluateAlerts(_ context.Context, _ int64, ids []int64) (domain.AlertEvaluationResult, error) {
	stub.notify("evaluate")
	stub.mu.Lock()
	defer stub.mu.Unlock()
	index := stub.evaluationCalls
	stub.evaluationCalls++
	stub.evaluationIDs = append(stub.evaluationIDs, append([]int64(nil), ids...))
	var result domain.AlertEvaluationResult
	if index < len(stub.evaluationResults) {
		result = stub.evaluationResults[index]
	}
	var err error
	if index < len(stub.evaluationErrors) {
		err = stub.evaluationErrors[index]
	}
	return result, err
}

func (stub *alertRepositoryStub) ListDueAlertOutbox(_ context.Context, nowMS int64, limit int) ([]domain.AlertOutboxItem, error) {
	stub.notify("list")
	stub.mu.Lock()
	defer stub.mu.Unlock()
	index := stub.listCalls
	stub.listCalls++
	stub.listNowMS = append(stub.listNowMS, nowMS)
	stub.listLimits = append(stub.listLimits, limit)
	if index < len(stub.listErrors) && stub.listErrors[index] != nil {
		return nil, stub.listErrors[index]
	}
	return append([]domain.AlertOutboxItem(nil), stub.due...), nil
}

func (stub *alertRepositoryStub) GetAlertPreference(_ context.Context, telegramUserID int64) (bool, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if err := stub.preferenceErrors[telegramUserID]; err != nil {
		return false, err
	}
	enabled, exists := stub.preferences[telegramUserID]
	if !exists {
		return true, nil
	}
	return enabled, nil
}

func (stub *alertRepositoryStub) MarkAlertDelivered(_ context.Context, id, deliveredAtMS int64) error {
	stub.recordMark(alertMark{kind: "delivered", id: id, atMS: deliveredAtMS})
	return nil
}

func (stub *alertRepositoryStub) MarkAlertSuppressed(_ context.Context, id, suppressedAtMS int64, class string) error {
	stub.recordMark(alertMark{kind: "suppressed", id: id, atMS: suppressedAtMS, class: class})
	return nil
}

func (stub *alertRepositoryStub) MarkAlertFailed(_ context.Context, id, failedAtMS, nextAttemptAtMS int64, class string) error {
	stub.recordMark(alertMark{kind: "failed", id: id, atMS: failedAtMS, nextAttemptAtMS: nextAttemptAtMS, class: class})
	return nil
}

func (stub *alertRepositoryStub) recordMark(mark alertMark) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.marks = append(stub.marks, mark)
}

func (stub *alertRepositoryStub) notify(operation string) {
	if stub.operationCh == nil {
		return
	}
	select {
	case stub.operationCh <- operation:
	default:
	}
}

type alertSenderStub struct {
	mu       sync.Mutex
	errors   map[int64]error
	calls    []alertSendCall
	sendHook func(context.Context, int64, string) error
}

type alertSendCall struct {
	telegramUserID int64
	text           string
}

func (stub *alertSenderStub) SendMessage(ctx context.Context, telegramUserID int64, text string) error {
	stub.mu.Lock()
	stub.calls = append(stub.calls, alertSendCall{telegramUserID: telegramUserID, text: text})
	hook := stub.sendHook
	err := stub.errors[telegramUserID]
	stub.mu.Unlock()
	if hook != nil {
		return hook(ctx, telegramUserID, text)
	}
	return err
}

func TestDispatcherRechecksRecipientsRendersAndRetriesSafely(t *testing.T) {
	nowMS := int64(100_000)
	payload := encodePayload(t, domain.AlertPayload{
		Version: 1, ServerName: "node", OfflineSinceMS: 1_000, EventAtMS: nowMS,
	})
	repository := &alertRepositoryStub{
		due: []domain.AlertOutboxItem{
			{ID: 1, TelegramUserID: 1, Kind: domain.AlertOffline, PayloadJSON: payload},
			{ID: 2, TelegramUserID: 2, Kind: domain.AlertOffline, PayloadJSON: payload},
			{ID: 3, TelegramUserID: 3, Kind: domain.AlertOffline, PayloadJSON: `{"bad":true}`},
			{ID: 4, TelegramUserID: 4, Kind: domain.AlertOffline, PayloadJSON: payload},
			{ID: 5, TelegramUserID: 5, Kind: domain.AlertOffline, PayloadJSON: payload},
		},
		preferences: map[int64]bool{2: false, 3: true, 4: true, 5: true},
	}
	sender := &alertSenderStub{errors: map[int64]error{4: errors.New("TELEGRAM-TRANSPORT-CANARY")}}
	dispatcher := newDispatcher(repository, sender, []int64{2, 3, 4, 5}, 100)

	got, err := dispatcher.RunOnce(context.Background(), nowMS)
	want := DispatchResult{Delivered: 1, Suppressed: 3, Retried: 1}
	if err != nil || got != want {
		t.Fatalf("RunOnce() = %#v, %v, want %#v", got, err, want)
	}
	repository.mu.Lock()
	marks := append([]alertMark(nil), repository.marks...)
	listNow := append([]int64(nil), repository.listNowMS...)
	listLimits := append([]int(nil), repository.listLimits...)
	repository.mu.Unlock()
	wantMarks := []alertMark{
		{kind: "suppressed", id: 1, atMS: nowMS, class: "recipient_removed"},
		{kind: "suppressed", id: 2, atMS: nowMS, class: "recipient_disabled"},
		{kind: "suppressed", id: 3, atMS: nowMS, class: "invalid_payload"},
		{kind: "failed", id: 4, atMS: nowMS, nextAttemptAtMS: nowMS + int64(5*time.Second/time.Millisecond), class: "telegram_send_failed"},
		{kind: "delivered", id: 5, atMS: nowMS},
	}
	if !reflect.DeepEqual(marks, wantMarks) || !reflect.DeepEqual(listNow, []int64{nowMS}) || !reflect.DeepEqual(listLimits, []int{100}) {
		t.Fatalf("marks=%#v listNow=%v limits=%v", marks, listNow, listLimits)
	}
	sender.mu.Lock()
	calls := append([]alertSendCall(nil), sender.calls...)
	sender.mu.Unlock()
	if len(calls) != 2 || calls[0].telegramUserID != 4 || calls[1].telegramUserID != 5 || calls[0].text == "" || calls[1].text == "" {
		t.Fatalf("send calls = %#v", calls)
	}
}

func TestDispatcherReturnsSafeRepositoryFailure(t *testing.T) {
	repository := &alertRepositoryStub{listErrors: []error{errors.New("REPOSITORY-DETAIL-CANARY")}}
	dispatcher := newDispatcher(repository, &alertSenderStub{}, []int64{42}, 100)
	_, err := dispatcher.RunOnce(context.Background(), 1)
	if err == nil || errors.Is(err, repository.listErrors[0]) || containsAny(err.Error(), "REPOSITORY-DETAIL-CANARY", "42") {
		t.Fatalf("RunOnce() error = %v, want safe operation error", err)
	}
}

func TestDispatcherCancellationLeavesInflightRowPending(t *testing.T) {
	payload := encodePayload(t, domain.AlertPayload{Version: 1, ServerName: "node", OfflineSinceMS: 1, EventAtMS: 2})
	repository := &alertRepositoryStub{due: []domain.AlertOutboxItem{{ID: 1, TelegramUserID: 42, Kind: domain.AlertOffline, PayloadJSON: payload}}}
	ctx, cancel := context.WithCancel(context.Background())
	sender := &alertSenderStub{sendHook: func(context.Context, int64, string) error {
		cancel()
		return context.Canceled
	}}
	dispatcher := newDispatcher(repository, sender, []int64{42}, 100)
	_, err := dispatcher.RunOnce(ctx, 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce() error = %v, want context.Canceled", err)
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if len(repository.marks) != 0 {
		t.Fatalf("canceled row marks = %#v, want pending", repository.marks)
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if candidate != "" && strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
