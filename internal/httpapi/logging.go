package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type requestLogStateKey struct{}

type requestLogState struct {
	serverID int64
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (writer *statusWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(value)
}

func accessLog(logger *slog.Logger, next http.Handler) http.Handler {
	logger = loggerOrDiscard(logger)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		state := &requestLogState{}
		request = request.WithContext(context.WithValue(request.Context(), requestLogStateKey{}, state))
		loggedWriter := &statusWriter{ResponseWriter: writer}
		next.ServeHTTP(loggedWriter, request)
		if loggedWriter.status == 0 {
			loggedWriter.status = http.StatusOK
		}

		attributes := []any{
			"method", request.Method,
			"route", request.URL.Path,
			"status", loggedWriter.status,
			"duration_ms", time.Since(started).Milliseconds(),
			"remote_addr", request.RemoteAddr,
		}
		if state.serverID > 0 {
			attributes = append(attributes, "server_id", state.serverID)
		}
		logger.Info("http request", attributes...)
	})
}

func setAuthenticatedServerID(ctx context.Context, serverID int64) {
	if state, ok := ctx.Value(requestLogStateKey{}).(*requestLogState); ok {
		state.serverID = serverID
	}
}

func loggerOrDiscard(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
