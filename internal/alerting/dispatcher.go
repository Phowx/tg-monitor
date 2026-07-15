package alerting

import (
	"context"
	"errors"
	"math"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const (
	suppressedRecipientRemoved  = "recipient_removed"
	suppressedRecipientDisabled = "recipient_disabled"
	suppressedInvalidPayload    = "invalid_payload"
	telegramSendFailed          = "telegram_send_failed"
)

var errDispatchRepository = errors.New("dispatch alerts: repository operation failed")

type Repository interface {
	EvaluateAlerts(context.Context, int64, []int64) (domain.AlertEvaluationResult, error)
	ListDueAlertOutbox(context.Context, int64, int) ([]domain.AlertOutboxItem, error)
	GetAlertPreference(context.Context, int64) (bool, error)
	MarkAlertDelivered(context.Context, int64, int64) error
	MarkAlertSuppressed(context.Context, int64, int64, string) error
	MarkAlertFailed(context.Context, int64, int64, int64, string) error
}

type Sender interface {
	SendMessage(context.Context, int64, string) error
}

type DispatchResult struct {
	Delivered  int
	Suppressed int
	Retried    int
}

type dispatcher struct {
	repository Repository
	sender     Sender
	allowed    map[int64]struct{}
	batchSize  int
}

func newDispatcher(repository Repository, sender Sender, adminIDs []int64, batchSize int) *dispatcher {
	allowed := make(map[int64]struct{}, len(adminIDs))
	for _, id := range adminIDs {
		allowed[id] = struct{}{}
	}
	return &dispatcher{repository: repository, sender: sender, allowed: allowed, batchSize: batchSize}
}

func (dispatcher *dispatcher) RunOnce(ctx context.Context, nowMS int64) (DispatchResult, error) {
	if err := ctx.Err(); err != nil {
		return DispatchResult{}, err
	}
	items, err := dispatcher.repository.ListDueAlertOutbox(ctx, nowMS, dispatcher.batchSize)
	if err != nil {
		return DispatchResult{}, safeDispatchError(ctx)
	}

	var result DispatchResult
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, allowed := dispatcher.allowed[item.TelegramUserID]; !allowed {
			if err := dispatcher.repository.MarkAlertSuppressed(ctx, item.ID, nowMS, suppressedRecipientRemoved); err != nil {
				return result, safeDispatchError(ctx)
			}
			result.Suppressed++
			continue
		}

		enabled, err := dispatcher.repository.GetAlertPreference(ctx, item.TelegramUserID)
		if err != nil {
			return result, safeDispatchError(ctx)
		}
		if !enabled {
			if err := dispatcher.repository.MarkAlertSuppressed(ctx, item.ID, nowMS, suppressedRecipientDisabled); err != nil {
				return result, safeDispatchError(ctx)
			}
			result.Suppressed++
			continue
		}

		text, err := Render(item.Kind, item.PayloadJSON)
		if err != nil {
			if err := dispatcher.repository.MarkAlertSuppressed(ctx, item.ID, nowMS, suppressedInvalidPayload); err != nil {
				return result, safeDispatchError(ctx)
			}
			result.Suppressed++
			continue
		}

		if err := dispatcher.sender.SendMessage(ctx, item.TelegramUserID, text); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			delayMS := RetryDelay(item.Attempts + 1).Milliseconds()
			if nowMS > math.MaxInt64-delayMS {
				return result, errors.New("dispatch alerts: retry time overflow")
			}
			if err := dispatcher.repository.MarkAlertFailed(ctx, item.ID, nowMS, nowMS+delayMS, telegramSendFailed); err != nil {
				return result, safeDispatchError(ctx)
			}
			result.Retried++
			continue
		}
		if err := dispatcher.repository.MarkAlertDelivered(ctx, item.ID, nowMS); err != nil {
			return result, safeDispatchError(ctx)
		}
		result.Delivered++
	}
	return result, nil
}

func safeDispatchError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errDispatchRepository
}
