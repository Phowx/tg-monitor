package serverapp

import (
	"context"
	"errors"
	"time"
)

const telegramUpdateRetention = 7 * 24 * time.Hour

type AccessCleanupRepository interface {
	DeleteExpiredSessions(context.Context, int64) (int64, error)
	DeleteTelegramUpdatesBefore(context.Context, int64) (int64, error)
}

type AccessCleanupResult struct {
	Sessions int64
	Updates  int64
}

func RunAccessCleanupOnce(ctx context.Context, repository AccessCleanupRepository, now time.Time) (AccessCleanupResult, error) {
	var result AccessCleanupResult
	var sessionErr error
	var updateErr error

	result.Sessions, sessionErr = repository.DeleteExpiredSessions(ctx, now.UTC().UnixMilli())
	if sessionErr != nil {
		sessionErr = errors.New("delete expired sessions failed")
	}
	result.Updates, updateErr = repository.DeleteTelegramUpdatesBefore(ctx, now.UTC().Add(-telegramUpdateRetention).UnixMilli())
	if updateErr != nil {
		updateErr = errors.New("delete old Telegram updates failed")
	}
	return result, errors.Join(sessionErr, updateErr)
}
