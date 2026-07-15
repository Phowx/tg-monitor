package sessionapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

func (handler *Handler) serveSession(writer http.ResponseWriter, request *http.Request) {
	if !requireMethod(writer, request, http.MethodGet) {
		return
	}
	cookie, err := request.Cookie(handler.cookieName)
	if err != nil {
		handler.writeUnauthorizedSession(writer)
		return
	}
	session, err := handler.repository.GetSession(request.Context(), cookie.Value, handler.now().UTC().UnixMilli())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrSessionExpired) {
			handler.writeUnauthorizedSession(writer)
			return
		}
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		TelegramUserID int64 `json:"telegram_user_id"`
		ExpiresAtMS    int64 `json:"expires_at"`
	}{TelegramUserID: session.TelegramUserID, ExpiresAtMS: session.ExpiresAtMS})
}

func (handler *Handler) serveLogout(writer http.ResponseWriter, request *http.Request) {
	if !requireMethod(writer, request, http.MethodPost) {
		return
	}
	if !handler.sameOrigin(request.Header.Get("Origin")) {
		writeError(writer, http.StatusForbidden, "forbidden_origin", "request origin is not allowed")
		return
	}
	handler.clearCookie(writer)
	cookie, err := request.Cookie(handler.cookieName)
	if err != nil {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	err = handler.repository.DeleteSession(request.Context(), cookie.Value)
	if err != nil && !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrSessionExpired) {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *Handler) writeUnauthorizedSession(writer http.ResponseWriter) {
	handler.clearCookie(writer)
	writeError(writer, http.StatusUnauthorized, "unauthorized", "authentication is required")
}

func (handler *Handler) clearCookie(writer http.ResponseWriter) {
	http.SetCookie(writer, &http.Cookie{
		Name: handler.cookieName, Value: "", Path: "/", Expires: time.Unix(1, 0).UTC(),
		MaxAge: -1, HttpOnly: true, Secure: handler.secure, SameSite: http.SameSiteStrictMode,
	})
}
