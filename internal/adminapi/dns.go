package adminapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/tg-monitor/tg-monitor/internal/cloudflare"
	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const dnsZonesPath = "/api/v1/admin/dns/zones"

type dnsZonesResponse struct {
	Enabled bool              `json:"enabled"`
	Zones   []cloudflare.Zone `json:"zones"`
}

type dnsDeleteInput struct {
	ConfirmName        string `json:"confirm_name"`
	ExpectedModifiedOn string `json:"expected_modified_on"`
}

func isDNSPath(path string) bool {
	return path == dnsZonesPath || strings.HasPrefix(path, dnsZonesPath+"/")
}

func dnsRoutePattern(path string) string {
	if path == dnsZonesPath {
		return dnsZonesPath
	}
	_, _, item, ok := parseDNSPath(path)
	if !ok {
		if isDNSPath(path) {
			return "/api/v1/admin/dns/not-found"
		}
		return ""
	}
	if !item {
		return "/api/v1/admin/dns/zones/{zone_id}/records"
	}
	return "/api/v1/admin/dns/zones/{zone_id}/records/{record_id}"
}

func parseDNSPath(path string) (zoneID, recordID string, item bool, ok bool) {
	if !strings.HasPrefix(path, dnsZonesPath+"/") {
		return "", "", false, false
	}
	parts := strings.Split(strings.TrimPrefix(path, dnsZonesPath+"/"), "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] == "records" {
		return parts[0], "", false, true
	}
	if len(parts) == 3 && parts[0] != "" && parts[1] == "records" && parts[2] != "" {
		return parts[0], parts[2], true, true
	}
	return "", "", false, false
}

func (handler *handler) serveDNS(writer http.ResponseWriter, request *http.Request, session domain.Session) {
	if request.URL.Path == dnsZonesPath {
		if !requireMethod(writer, request, http.MethodGet) {
			return
		}
		if handler.dns == nil {
			writeJSON(writer, http.StatusOK, dnsZonesResponse{Enabled: false, Zones: []cloudflare.Zone{}})
			return
		}
		writeJSON(writer, http.StatusOK, dnsZonesResponse{Enabled: true, Zones: handler.dns.Zones()})
		return
	}
	zoneID, recordID, item, ok := parseDNSPath(request.URL.Path)
	if !ok {
		writeError(writer, http.StatusNotFound, "not_found", "resource was not found")
		return
	}
	if handler.dns == nil {
		writeError(writer, http.StatusServiceUnavailable, "dns_not_configured", "Cloudflare DNS is not configured")
		return
	}
	if !item {
		switch request.Method {
		case http.MethodGet:
			handler.serveDNSRecords(writer, request, zoneID)
		case http.MethodPost:
			if !requireOrigin(writer, request, handler.policy.Origin) {
				return
			}
			handler.serveDNSCreate(writer, request, session, zoneID)
		default:
			writer.Header().Set("Allow", "GET, POST")
			writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		}
		return
	}
	switch request.Method {
	case http.MethodPatch:
		if !requireOrigin(writer, request, handler.policy.Origin) {
			return
		}
		handler.serveDNSUpdate(writer, request, session, zoneID, recordID)
	case http.MethodDelete:
		if !requireOrigin(writer, request, handler.policy.Origin) {
			return
		}
		handler.serveDNSDelete(writer, request, session, zoneID, recordID)
	default:
		writer.Header().Set("Allow", "PATCH, DELETE")
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
	}
}

func (handler *handler) serveDNSRecords(writer http.ResponseWriter, request *http.Request, zoneID string) {
	page, ok := dnsPositiveQuery(writer, request, "page", 1, 1_000_000)
	if !ok {
		return
	}
	perPage, ok := dnsPositiveQuery(writer, request, "per_page", 20, 100)
	if !ok {
		return
	}
	result, err := handler.dns.ListRecords(request.Context(), zoneID, page, perPage)
	if err != nil {
		handler.writeDNSError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *handler) serveDNSCreate(writer http.ResponseWriter, request *http.Request, session domain.Session, zoneID string) {
	var input cloudflare.CreateRecordInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	record, err := handler.dns.CreateRecord(request.Context(), zoneID, input)
	if err != nil {
		handler.logDNSMutation(session, "create", zoneID, "", input.Type, "failed")
		handler.writeDNSError(writer, err)
		return
	}
	handler.logDNSMutation(session, "create", zoneID, record.ID, record.Type, "succeeded")
	writeJSON(writer, http.StatusCreated, record)
}

func (handler *handler) serveDNSUpdate(writer http.ResponseWriter, request *http.Request, session domain.Session, zoneID, recordID string) {
	var input cloudflare.UpdateRecordInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	record, err := handler.dns.UpdateRecord(request.Context(), zoneID, recordID, input)
	if err != nil {
		handler.logDNSMutation(session, "update", zoneID, recordID, "", "failed")
		handler.writeDNSError(writer, err)
		return
	}
	handler.logDNSMutation(session, "update", zoneID, record.ID, record.Type, "succeeded")
	writeJSON(writer, http.StatusOK, record)
}

func (handler *handler) serveDNSDelete(writer http.ResponseWriter, request *http.Request, session domain.Session, zoneID, recordID string) {
	var input dnsDeleteInput
	if !decodeJSON(writer, request, &input) {
		return
	}
	err := handler.dns.DeleteRecord(request.Context(), zoneID, recordID, input.ConfirmName, input.ExpectedModifiedOn)
	if err != nil {
		handler.logDNSMutation(session, "delete", zoneID, recordID, "", "failed")
		handler.writeDNSError(writer, err)
		return
	}
	handler.logDNSMutation(session, "delete", zoneID, recordID, "", "succeeded")
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *handler) logDNSMutation(session domain.Session, action, zoneID, recordID, recordType, result string) {
	handler.logger.Info("DNS mutation",
		"admin_id", session.TelegramUserID,
		"action", action,
		"zone_id", zoneID,
		"record_id", recordID,
		"record_type", recordType,
		"result", result,
	)
}

func (handler *handler) writeDNSError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeError(writer, http.StatusGatewayTimeout, "dns_provider_timeout", "Cloudflare request timed out")
	case errors.Is(err, cloudflare.ErrNotFound):
		writeError(writer, http.StatusNotFound, "dns_not_found", "DNS resource was not found")
	case errors.Is(err, cloudflare.ErrConflict):
		writeError(writer, http.StatusConflict, "dns_conflict", "DNS record changed; refresh and try again")
	case errors.Is(err, cloudflare.ErrInvalid):
		writeError(writer, http.StatusUnprocessableEntity, "invalid_dns_record", "DNS record input is invalid")
	default:
		writeError(writer, http.StatusBadGateway, "dns_provider_error", "Cloudflare request failed")
	}
}

func dnsPositiveQuery(writer http.ResponseWriter, request *http.Request, name string, fallback, maximum int) (int, bool) {
	raw := request.URL.Query().Get(name)
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		writeError(writer, http.StatusBadRequest, "invalid_query", "pagination query is invalid")
		return 0, false
	}
	return value, true
}
