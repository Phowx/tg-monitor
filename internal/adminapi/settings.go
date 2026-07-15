package adminapi

import (
	"net/http"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

type alertPreferenceInput struct {
	Enabled *bool `json:"enabled"`
}

type alertPreferenceResponse struct {
	Enabled bool `json:"enabled"`
}

func (handler *handler) serveGetSettings(writer http.ResponseWriter, request *http.Request) {
	settings, err := handler.repository.GetSettings(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(writer, http.StatusOK, settings)
}

func (handler *handler) servePutSettings(writer http.ResponseWriter, request *http.Request) {
	var settings domain.Settings
	if !decodeJSON(writer, request, &settings) {
		return
	}
	if !validSettings(settings) {
		writeError(writer, http.StatusUnprocessableEntity, "validation_failed", "request validation failed")
		return
	}
	if err := handler.repository.UpdateSettings(request.Context(), settings); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(writer, http.StatusOK, settings)
}

func (handler *handler) servePutAlertPreference(writer http.ResponseWriter, request *http.Request, session domain.Session) {
	var input alertPreferenceInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	if input.Enabled == nil {
		writeError(writer, http.StatusUnprocessableEntity, "validation_failed", "request validation failed")
		return
	}
	if err := handler.repository.SetAlertPreference(request.Context(), session.TelegramUserID, *input.Enabled); err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(writer, http.StatusOK, alertPreferenceResponse{Enabled: *input.Enabled})
}

func validSettings(settings domain.Settings) bool {
	return settings.OfflineThresholdSeconds >= 1 && settings.OfflineThresholdSeconds <= 86_400 &&
		settings.AlertThresholdSeconds >= 1 && settings.AlertThresholdSeconds <= 86_400 &&
		settings.HistoryRetentionDays >= 1 && settings.HistoryRetentionDays <= 365
}
