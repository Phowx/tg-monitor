package sessionapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
	"github.com/tg-monitor/tg-monitor/internal/telegramauth"
	"github.com/tg-monitor/tg-monitor/internal/websession"
)

const maxAuthBodyBytes int64 = 64 << 10

type Repository interface {
	CreateSession(context.Context, string, domain.Session) error
	GetSession(context.Context, string, int64) (domain.Session, error)
	DeleteSession(context.Context, string) error
}

type Config struct {
	PublicURL        string
	BotToken         string
	AdminTelegramIDs []int64
	SessionTTL       time.Duration
	InitDataMaxAge   time.Duration
}

type Dependencies struct {
	Repository Repository
	Random     io.Reader
	Now        func() time.Time
	Verify     func(string, string, time.Time, time.Duration, []int64) (telegramauth.User, error)
	Logger     *slog.Logger
}

type Handler struct {
	config     Config
	repository Repository
	random     io.Reader
	now        func() time.Time
	verify     func(string, string, time.Time, time.Duration, []int64) (telegramauth.User, error)
	logger     *slog.Logger
	policy     websession.Policy
}

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewHandler(cfg Config, dependencies Dependencies) (http.Handler, error) {
	policy, err := websession.NewPolicy(cfg.PublicURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.BotToken) == "" || len(cfg.AdminTelegramIDs) == 0 || cfg.SessionTTL <= 0 || cfg.InitDataMaxAge <= 0 {
		return nil, errors.New("create session handler: invalid configuration")
	}
	seenAdminIDs := make(map[int64]struct{}, len(cfg.AdminTelegramIDs))
	for _, id := range cfg.AdminTelegramIDs {
		if id <= 0 {
			return nil, errors.New("create session handler: invalid administrator ID")
		}
		if _, exists := seenAdminIDs[id]; exists {
			return nil, errors.New("create session handler: duplicate administrator ID")
		}
		seenAdminIDs[id] = struct{}{}
	}
	if dependencies.Repository == nil {
		return nil, errors.New("create session handler: repository is required")
	}
	if dependencies.Random == nil {
		dependencies.Random = rand.Reader
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.Verify == nil {
		dependencies.Verify = telegramauth.VerifyInitData
	}
	if dependencies.Logger == nil {
		dependencies.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	cfg.PublicURL = policy.Origin
	cfg.BotToken = strings.TrimSpace(cfg.BotToken)
	handler := &Handler{
		config: cfg, repository: dependencies.Repository, random: dependencies.Random,
		now: dependencies.Now, verify: dependencies.Verify, logger: dependencies.Logger,
		policy: policy,
	}
	return handler.withAccessLog(http.HandlerFunc(handler.serveHTTP)), nil
}

func (handler *Handler) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/api/v1/auth/telegram":
		handler.serveTelegramLogin(writer, request)
	case "/api/v1/auth/session":
		handler.serveSession(writer, request)
	case "/api/v1/auth/logout":
		handler.serveLogout(writer, request)
	default:
		writeError(writer, http.StatusNotFound, "not_found", "resource was not found")
	}
}

func (handler *Handler) serveTelegramLogin(writer http.ResponseWriter, request *http.Request) {
	if !requireMethod(writer, request, http.MethodPost) {
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return
	}
	if !handler.sameOrigin(request.Header.Get("Origin")) {
		writeError(writer, http.StatusForbidden, "forbidden_origin", "request origin is not allowed")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxAuthBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var body struct {
		InitData string `json:"init_data"`
	}
	if err := decoder.Decode(&body); err != nil {
		writeDecodeError(writer, err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeDecodeError(writer, err)
		return
	}
	now := handler.now().UTC()
	user, err := handler.verify(body.InitData, handler.config.BotToken, now, handler.config.InitDataMaxAge, handler.config.AdminTelegramIDs)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, "unauthorized", "authentication is required")
		return
	}
	random := make([]byte, 32)
	if _, err := io.ReadFull(handler.random, random); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	rawToken := base64.RawURLEncoding.EncodeToString(random)
	expires := now.Add(handler.config.SessionTTL)
	if err := handler.repository.CreateSession(request.Context(), rawToken, domain.Session{
		TelegramUserID: user.ID,
		CreatedAtMS:    now.UnixMilli(),
		ExpiresAtMS:    expires.UnixMilli(),
	}); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	http.SetCookie(writer, handler.policy.Set(rawToken, expires, handler.config.SessionTTL))
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) sameOrigin(origin string) bool {
	return origin == "" || origin == handler.config.PublicURL
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

func (handler *Handler) withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		logged := &statusWriter{ResponseWriter: writer}
		next.ServeHTTP(logged, request)
		if logged.status == 0 {
			logged.status = http.StatusOK
		}
		handler.logger.Info("http request", "method", request.Method, "route", request.URL.Path, "status", logged.status, "duration_ms", time.Since(started).Milliseconds())
	})
}
