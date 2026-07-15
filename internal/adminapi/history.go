package adminapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const (
	historyPathPrefix = "/api/v1/admin/servers/"
	historyPathSuffix = "/history"
	maxHistoryRangeMS = int64(7 * 24 * 60 * 60 * 1_000)
)

type historyResponse struct {
	Server  serverMetadata        `json:"server"`
	FromMS  int64                 `json:"from_ms"`
	ToMS    int64                 `json:"to_ms"`
	Samples []domain.MinuteSample `json:"samples"`
}

func isHistoryPath(path string) bool {
	return strings.HasPrefix(path, historyPathPrefix) && strings.HasSuffix(path, historyPathSuffix)
}

func (handler *handler) serveHistory(writer http.ResponseWriter, request *http.Request) {
	serverID, fromMS, toMS, ok := parseHistoryRequest(request)
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	server, err := handler.repository.GetServer(request.Context(), serverID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			writeError(writer, http.StatusNotFound, "not_found", "resource was not found")
			return
		}
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	samples, err := handler.repository.QueryMinuteSamples(request.Context(), serverID, fromMS, toMS)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if samples == nil {
		samples = make([]domain.MinuteSample, 0)
	}
	writeJSON(writer, http.StatusOK, historyResponse{
		Server: serverMetadata{
			ID: server.ID, Name: server.Name, Group: server.Group,
			SortOrder: server.SortOrder, Enabled: server.Enabled,
		},
		FromMS:  fromMS,
		ToMS:    toMS,
		Samples: samples,
	})
}

func parseHistoryRequest(request *http.Request) (int64, int64, int64, bool) {
	rawServerID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, historyPathPrefix), historyPathSuffix)
	if rawServerID == "" || strings.Contains(rawServerID, "/") {
		return 0, 0, 0, false
	}
	serverID, err := strconv.ParseInt(rawServerID, 10, 64)
	if err != nil || serverID <= 0 {
		return 0, 0, 0, false
	}
	query := request.URL.Query()
	fromValues, hasFrom := query["from_ms"]
	toValues, hasTo := query["to_ms"]
	if len(query) != 2 || !hasFrom || !hasTo || len(fromValues) != 1 || len(toValues) != 1 {
		return 0, 0, 0, false
	}
	fromMS, fromErr := strconv.ParseInt(fromValues[0], 10, 64)
	toMS, toErr := strconv.ParseInt(toValues[0], 10, 64)
	if fromErr != nil || toErr != nil || fromMS < 0 || toMS <= fromMS || toMS-fromMS > maxHistoryRangeMS {
		return 0, 0, 0, false
	}
	return serverID, fromMS, toMS, true
}
