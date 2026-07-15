package adminapi

import (
	"net/http"
	"strings"

	"github.com/tg-monitor/tg-monitor/internal/auth"
	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const (
	minSortOrder = int64(-2_147_483_648)
	maxSortOrder = int64(2_147_483_647)
)

type serverInput struct {
	Name      string `json:"name"`
	Group     string `json:"group"`
	SortOrder *int64 `json:"sort_order"`
	Enabled   *bool  `json:"enabled"`
}

type createServerResponse struct {
	Server     serverMetadata `json:"server"`
	AgentToken string         `json:"agent_token"`
}

func (handler *handler) serveCreateServer(writer http.ResponseWriter, request *http.Request) {
	var input serverInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	input, ok := validateServerInput(input)
	if !ok {
		writeError(writer, http.StatusUnprocessableEntity, "validation_failed", "request validation failed")
		return
	}
	rawToken, tokenHash, err := auth.GenerateToken(handler.random)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	created, err := handler.repository.CreateServer(request.Context(), domain.Server{
		Name:        input.Name,
		Group:       input.Group,
		SortOrder:   int(*input.SortOrder),
		Enabled:     *input.Enabled,
		TokenSHA256: tokenHash,
	})
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(writer, http.StatusCreated, createServerResponse{
		Server:     metadataFromServer(created),
		AgentToken: rawToken,
	})
}

func validateServerInput(input serverInput) (serverInput, bool) {
	input.Name = strings.TrimSpace(input.Name)
	input.Group = strings.TrimSpace(input.Group)
	valid := input.Name != "" && len([]byte(input.Name)) <= 120 && len([]byte(input.Group)) <= 120 && input.SortOrder != nil && *input.SortOrder >= minSortOrder && *input.SortOrder <= maxSortOrder && input.Enabled != nil
	return input, valid
}

func metadataFromServer(server domain.Server) serverMetadata {
	return serverMetadata{
		ID: server.ID, Name: server.Name, Group: server.Group,
		SortOrder: server.SortOrder, Enabled: server.Enabled,
	}
}
