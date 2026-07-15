package adminapi

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/websession"
)

type Repository interface {
	GetSession(context.Context, string, int64) (domain.Session, error)
	ListServers(context.Context) ([]domain.Server, error)
	GetServer(context.Context, int64) (domain.Server, error)
	CreateServer(context.Context, domain.Server) (domain.Server, error)
	UpdateServer(context.Context, domain.Server) error
	DeleteServer(context.Context, int64) error
	UpdateServerTokenHash(context.Context, int64, []byte) error
	ListLatestMetrics(context.Context) ([]domain.LatestMetrics, error)
	QueryMinuteSamples(context.Context, int64, int64, int64) ([]domain.MinuteSample, error)
	GetSettings(context.Context) (domain.Settings, error)
	UpdateSettings(context.Context, domain.Settings) error
	GetAlertPreference(context.Context, int64) (bool, error)
	SetAlertPreference(context.Context, int64, bool) error
}

type Config struct {
	PublicURL string
}

type Dependencies struct {
	Repository Repository
	Random     io.Reader
	Now        func() time.Time
	Logger     *slog.Logger
}

type handler struct {
	repository Repository
	random     io.Reader
	now        func() time.Time
	logger     *slog.Logger
	policy     websession.Policy
}

func NewHandler(config Config, dependencies Dependencies) (http.Handler, error) {
	policy, err := websession.NewPolicy(config.PublicURL)
	if err != nil {
		return nil, errors.New("create admin API handler: invalid public URL")
	}
	if dependencies.Repository == nil {
		return nil, errors.New("create admin API handler: repository is required")
	}
	if dependencies.Random == nil {
		dependencies.Random = rand.Reader
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.Logger == nil {
		dependencies.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &handler{
		repository: dependencies.Repository,
		random:     dependencies.Random,
		now:        dependencies.Now,
		logger:     dependencies.Logger,
		policy:     policy,
	}, nil
}

func (handler *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	logged := &statusWriter{ResponseWriter: writer}
	logged.Header().Set("Cache-Control", "no-store")
	route := routePattern(request)
	var adminID int64
	defer func() {
		status := logged.status
		if status == 0 {
			status = http.StatusOK
		}
		handler.logger.Info(
			"admin API request",
			"method", request.Method,
			"route", route,
			"status", status,
			"duration_ms", time.Since(started).Milliseconds(),
			"admin_id", adminID,
		)
	}()

	cookie, err := request.Cookie(handler.policy.CookieName)
	if err != nil {
		handler.writeUnauthorized(logged)
		return
	}
	session, err := handler.repository.GetSession(request.Context(), cookie.Value, handler.now().UTC().UnixMilli())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrSessionExpired) {
			handler.writeUnauthorized(logged)
			return
		}
		writeError(logged, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	adminID = session.TelegramUserID
	handler.serveAuthorized(logged, request, session)
}

func (handler *handler) serveAuthorized(writer http.ResponseWriter, request *http.Request, session domain.Session) {
	if request.URL.Path == "/api/v1/admin/overview" {
		if !requireMethod(writer, request, http.MethodGet) {
			return
		}
		handler.serveOverview(writer, request, session)
		return
	}
	if isHistoryPath(request.URL.Path) {
		if !requireMethod(writer, request, http.MethodGet) {
			return
		}
		handler.serveHistory(writer, request)
		return
	}
	if request.URL.Path == "/api/v1/admin/servers" {
		if !requireMethod(writer, request, http.MethodPost) {
			return
		}
		if !requireOrigin(writer, request, handler.policy.Origin) {
			return
		}
		handler.serveCreateServer(writer, request)
		return
	}
	if isRotateTokenPath(request.URL.Path) {
		if !requireMethod(writer, request, http.MethodPost) {
			return
		}
		if !requireOrigin(writer, request, handler.policy.Origin) {
			return
		}
		handler.serveRotateToken(writer, request)
		return
	}
	if isServerItemPath(request.URL.Path) {
		if request.Method != http.MethodPut && request.Method != http.MethodDelete {
			writer.Header().Set("Allow", "PUT, DELETE")
			writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
			return
		}
		if !requireOrigin(writer, request, handler.policy.Origin) {
			return
		}
		if request.Method == http.MethodPut {
			handler.serveUpdateServer(writer, request)
		} else {
			handler.serveDeleteServer(writer, request)
		}
		return
	}
	if request.URL.Path == "/api/v1/admin/settings" {
		if request.Method != http.MethodGet && request.Method != http.MethodPut {
			writer.Header().Set("Allow", "GET, PUT")
			writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
			return
		}
		if request.Method == http.MethodPut {
			if !requireOrigin(writer, request, handler.policy.Origin) {
				return
			}
			handler.servePutSettings(writer, request)
		} else {
			handler.serveGetSettings(writer, request)
		}
		return
	}
	if request.URL.Path == "/api/v1/admin/alert-preference" {
		if !requireMethod(writer, request, http.MethodPut) {
			return
		}
		if !requireOrigin(writer, request, handler.policy.Origin) {
			return
		}
		handler.servePutAlertPreference(writer, request, session)
		return
	}
	writeError(writer, http.StatusNotFound, "not_found", "resource was not found")
}

func (handler *handler) writeUnauthorized(writer http.ResponseWriter) {
	http.SetCookie(writer, handler.policy.Clear())
	writeError(writer, http.StatusUnauthorized, "unauthorized", "authentication is required")
}

func routePattern(request *http.Request) string {
	if request.URL.Path == "/api/v1/admin/overview" {
		return "/api/v1/admin/overview"
	}
	if isHistoryPath(request.URL.Path) {
		return "/api/v1/admin/servers/{id}/history"
	}
	if request.URL.Path == "/api/v1/admin/servers" {
		return "/api/v1/admin/servers"
	}
	if isRotateTokenPath(request.URL.Path) {
		return "/api/v1/admin/servers/{id}/rotate-token"
	}
	if isServerItemPath(request.URL.Path) {
		return "/api/v1/admin/servers/{id}"
	}
	if request.URL.Path == "/api/v1/admin/settings" {
		return "/api/v1/admin/settings"
	}
	if request.URL.Path == "/api/v1/admin/alert-preference" {
		return "/api/v1/admin/alert-preference"
	}
	return "not_found"
}
