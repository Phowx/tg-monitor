package adminapi

import (
	"net/http"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

type serverMetadata struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Group     string `json:"group"`
	SortOrder int    `json:"sort_order"`
	Enabled   bool   `json:"enabled"`
}

type overviewServer struct {
	Server serverMetadata        `json:"server"`
	State  string                `json:"state"`
	Latest *domain.LatestMetrics `json:"latest"`
}

type overviewResponse struct {
	NowMS          int64            `json:"now_ms"`
	TelegramUserID int64            `json:"telegram_user_id"`
	AlertsEnabled  bool             `json:"alerts_enabled"`
	Settings       domain.Settings  `json:"settings"`
	Servers        []overviewServer `json:"servers"`
}

func (handler *handler) serveOverview(writer http.ResponseWriter, request *http.Request, session domain.Session) {
	settings, err := handler.repository.GetSettings(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	servers, err := handler.repository.ListServers(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	latestMetrics, err := handler.repository.ListLatestMetrics(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	alertsEnabled, err := handler.repository.GetAlertPreference(request.Context(), session.TelegramUserID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	metricsByServer := make(map[int64]domain.LatestMetrics, len(latestMetrics))
	for _, latest := range latestMetrics {
		metricsByServer[latest.ServerID] = latest
	}
	nowMS := handler.now().UTC().UnixMilli()
	cutoffMS := nowMS - settings.OfflineThresholdSeconds*1_000
	response := overviewResponse{
		NowMS:          nowMS,
		TelegramUserID: session.TelegramUserID,
		AlertsEnabled:  alertsEnabled,
		Settings:       settings,
		Servers:        make([]overviewServer, 0, len(servers)),
	}
	for _, server := range servers {
		entry := overviewServer{
			Server: serverMetadata{
				ID: server.ID, Name: server.Name, Group: server.Group,
				SortOrder: server.SortOrder, Enabled: server.Enabled,
			},
			State: "offline",
		}
		latest, hasLatest := metricsByServer[server.ID]
		if hasLatest {
			entry.Latest = &latest
		}
		if !server.Enabled {
			entry.State = "disabled"
		} else if hasLatest && latest.ReceivedAtMS >= cutoffMS {
			entry.State = "online"
		}
		response.Servers = append(response.Servers, entry)
	}
	writeJSON(writer, http.StatusOK, response)
}
