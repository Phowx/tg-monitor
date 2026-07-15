package adminapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/domain"
)

type serverResponse struct {
	Server serverMetadata `json:"server"`
}

type rotateTokenResponse struct {
	ServerID   int64  `json:"server_id"`
	AgentToken string `json:"agent_token"`
}

func isServerItemPath(path string) bool {
	if !strings.HasPrefix(path, historyPathPrefix) || isHistoryPath(path) || strings.HasSuffix(path, "/rotate-token") {
		return false
	}
	remainder := strings.TrimPrefix(path, historyPathPrefix)
	return remainder != "" && !strings.Contains(remainder, "/")
}

func isRotateTokenPath(path string) bool {
	if !strings.HasPrefix(path, historyPathPrefix) || !strings.HasSuffix(path, "/rotate-token") {
		return false
	}
	remainder := strings.TrimSuffix(strings.TrimPrefix(path, historyPathPrefix), "/rotate-token")
	return remainder != "" && !strings.Contains(remainder, "/")
}

func parseServerID(path, suffix string) (int64, bool) {
	raw := strings.TrimPrefix(path, historyPathPrefix)
	if suffix != "" {
		raw = strings.TrimSuffix(raw, suffix)
	}
	serverID, err := strconv.ParseInt(raw, 10, 64)
	return serverID, err == nil && serverID > 0
}

func (handler *handler) serveUpdateServer(writer http.ResponseWriter, request *http.Request) {
	serverID, ok := parseServerID(request.URL.Path, "")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	var input serverInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	input, ok = validateServerInput(input)
	if !ok {
		writeError(writer, http.StatusUnprocessableEntity, "validation_failed", "request validation failed")
		return
	}
	server, err := handler.repository.GetServer(request.Context(), serverID)
	if err != nil {
		writeMutationRepositoryError(writer, err)
		return
	}
	server.Name = input.Name
	server.Group = input.Group
	server.SortOrder = int(*input.SortOrder)
	server.Enabled = *input.Enabled
	if err := handler.repository.UpdateServer(request.Context(), server); err != nil {
		writeMutationRepositoryError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, serverResponse{Server: metadataFromServer(server)})
}

func (handler *handler) serveRotateToken(writer http.ResponseWriter, request *http.Request) {
	serverID, ok := parseServerID(request.URL.Path, "/rotate-token")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	var input struct{}
	if !decodeJSON(writer, request, &input) {
		return
	}
	rawToken, tokenHash, err := auth.GenerateToken(handler.random)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if err := handler.repository.UpdateServerTokenHash(request.Context(), serverID, tokenHash); err != nil {
		writeMutationRepositoryError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, rotateTokenResponse{ServerID: serverID, AgentToken: rawToken})
}

func (handler *handler) serveDeleteServer(writer http.ResponseWriter, request *http.Request) {
	serverID, ok := parseServerID(request.URL.Path, "")
	if !ok {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request is invalid")
		return
	}
	if err := handler.repository.DeleteServer(request.Context(), serverID); err != nil {
		writeMutationRepositoryError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func writeMutationRepositoryError(writer http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrNotFound) {
		writeError(writer, http.StatusNotFound, "not_found", "resource was not found")
		return
	}
	writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
}
