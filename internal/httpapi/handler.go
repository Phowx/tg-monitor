package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/monitoring"
)

const maxMetricBodyBytes int64 = 64 << 10

type Dependencies struct {
	Readiness interface {
		Ping(context.Context) error
	}
	Authenticator interface {
		Authenticate(context.Context, string) (domain.Server, error)
	}
	Ingestor interface {
		Ingest(context.Context, int64, int64, domain.MetricReport) error
	}
	Now    func() time.Time
	Logger *slog.Logger
}

type Handler struct {
	dependencies Dependencies
}

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewHandler(dependencies Dependencies) http.Handler {
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	dependencies.Logger = loggerOrDiscard(dependencies.Logger)
	handler := &Handler{dependencies: dependencies}
	return accessLog(dependencies.Logger, http.HandlerFunc(handler.serveHTTP))
}

func (handler *Handler) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/healthz":
		handler.serveHealth(writer, request)
	case "/readyz":
		handler.serveReady(writer, request)
	case "/api/v1/metrics":
		handler.serveMetrics(writer, request)
	default:
		writeError(writer, http.StatusNotFound, "not_found", "resource was not found")
	}
}

func (handler *Handler) serveHealth(writer http.ResponseWriter, request *http.Request) {
	if !requireMethod(writer, request, http.MethodGet) {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (handler *Handler) serveReady(writer http.ResponseWriter, request *http.Request) {
	if !requireMethod(writer, request, http.MethodGet) {
		return
	}
	if err := handler.dependencies.Readiness.Ping(request.Context()); err != nil {
		writeError(writer, http.StatusServiceUnavailable, "not_ready", "service is not ready")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func (handler *Handler) serveMetrics(writer http.ResponseWriter, request *http.Request) {
	if !requireMethod(writer, request, http.MethodPost) {
		return
	}

	server, err := handler.dependencies.Authenticator.Authenticate(request.Context(), request.Header.Get("Authorization"))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrMalformedAuthorization):
			writeError(writer, http.StatusBadRequest, "malformed_authorization", "authorization header is malformed")
		case errors.Is(err, auth.ErrUnauthorized):
			writeError(writer, http.StatusUnauthorized, "unauthorized", "authentication is required")
		case errors.Is(err, auth.ErrDisabled):
			writeError(writer, http.StatusForbidden, "forbidden", "server is disabled")
		default:
			writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	setAuthenticatedServerID(request.Context(), server.ID)

	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return
	}

	request.Body = http.MaxBytesReader(writer, request.Body, maxMetricBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var report domain.MetricReport
	if err := decoder.Decode(&report); err != nil {
		writeDecodeError(writer, err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeDecodeError(writer, err)
		return
	}
	if err := report.Validate(); err != nil {
		writeError(writer, http.StatusUnprocessableEntity, "invalid_report", "metric report is invalid")
		return
	}

	receivedAtMS := handler.dependencies.Now().UTC().UnixMilli()
	if err := handler.dependencies.Ingestor.Ingest(request.Context(), server.ID, receivedAtMS, report); err != nil {
		if errors.Is(err, monitoring.ErrOutOfOrder) {
			writeError(writer, http.StatusConflict, "out_of_order", "metric report is out of order")
			return
		}
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func requireMethod(writer http.ResponseWriter, request *http.Request, method string) bool {
	if request.Method == method {
		return true
	}
	writer.Header().Set("Allow", method)
	writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
	return false
}

func writeDecodeError(writer http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeError(writer, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 64 KiB")
		return
	}
	writeError(writer, http.StatusBadRequest, "invalid_json", "request body must contain one valid JSON object")
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, errorEnvelope{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
