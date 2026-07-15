package adminapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/cloudflare"
	"github.com/tg-monitor/tg-monitor/internal/domain"
)

const adminDNSZoneID = "0123456789abcdef0123456789abcdef"

type dnsServiceStub struct {
	zones          []cloudflare.Zone
	page           cloudflare.RecordPage
	record         cloudflare.Record
	err            error
	createdZoneID  string
	createdInput   cloudflare.CreateRecordInput
	updatedZoneID  string
	updatedRecord  string
	updatedInput   cloudflare.UpdateRecordInput
	deletedZoneID  string
	deletedRecord  string
	deletedConfirm string
	deletedVersion string
}

func (stub *dnsServiceStub) Zones() []cloudflare.Zone {
	return stub.zones
}

func (stub *dnsServiceStub) ListRecords(context.Context, string, int, int) (cloudflare.RecordPage, error) {
	return stub.page, stub.err
}

func (stub *dnsServiceStub) CreateRecord(_ context.Context, zoneID string, input cloudflare.CreateRecordInput) (cloudflare.Record, error) {
	stub.createdZoneID, stub.createdInput = zoneID, input
	return stub.record, stub.err
}

func (stub *dnsServiceStub) UpdateRecord(_ context.Context, zoneID, recordID string, input cloudflare.UpdateRecordInput) (cloudflare.Record, error) {
	stub.updatedZoneID, stub.updatedRecord, stub.updatedInput = zoneID, recordID, input
	return stub.record, stub.err
}

func (stub *dnsServiceStub) DeleteRecord(_ context.Context, zoneID, recordID, confirmation, version string) error {
	stub.deletedZoneID, stub.deletedRecord = zoneID, recordID
	stub.deletedConfirm, stub.deletedVersion = confirmation, version
	return stub.err
}

func TestDNSZonesExposeConfigurationWithoutSecrets(t *testing.T) {
	repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
	disabled := serveAdmin(mustAdminHandler(t, repository, nil), http.MethodGet, dnsZonesPath, testSessionToken, "", "", "")
	if disabled.Code != http.StatusOK || disabled.Body.String() != `{"enabled":false,"zones":[]}`+"\n" {
		t.Fatalf("disabled response = %d %q", disabled.Code, disabled.Body.String())
	}

	dns := &dnsServiceStub{zones: []cloudflare.Zone{{ID: adminDNSZoneID, Name: "example.com"}}}
	enabled := serveAdmin(mustDNSHandler(t, repository, dns, io.Discard), http.MethodGet, dnsZonesPath, testSessionToken, "", "", "")
	if enabled.Code != http.StatusOK || !strings.Contains(enabled.Body.String(), `"name":"example.com"`) || strings.Contains(enabled.Body.String(), "TOKEN") {
		t.Fatalf("enabled response = %d %q", enabled.Code, enabled.Body.String())
	}
	assertAdminResponsePolicy(t, enabled)
}

func TestDNSMutationsRequireSameOriginAndStrictBodies(t *testing.T) {
	repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
	dns := &dnsServiceStub{record: cloudflare.Record{
		ID: "record-a", Type: "A", Name: "api.example.com", Content: "1.2.3.4",
		TTL: 1, Proxied: dnsBool(true), Proxiable: true, ModifiedOn: "version-1",
	}}
	handler := mustDNSHandler(t, repository, dns, io.Discard)
	path := dnsZonesPath + "/" + adminDNSZoneID + "/records"
	forbidden := serveAdmin(handler, http.MethodPost, path, testSessionToken, "", "application/json", `{"type":"A","name":"api","content":"1.2.3.4","ttl":1,"proxied":true}`)
	if forbidden.Code != http.StatusForbidden || dns.createdZoneID != "" {
		t.Fatalf("forbidden create = %d, call=%q", forbidden.Code, dns.createdZoneID)
	}
	unknown := serveAdmin(handler, http.MethodPost, path, testSessionToken, testAdminOrigin, "application/json", `{"type":"A","name":"api","content":"1.2.3.4","ttl":1,"proxied":true,"extra":1}`)
	if unknown.Code != http.StatusBadRequest || dns.createdZoneID != "" {
		t.Fatalf("unknown-field create = %d, call=%q", unknown.Code, dns.createdZoneID)
	}
	created := serveAdmin(handler, http.MethodPost, path, testSessionToken, testAdminOrigin, "application/json", `{"type":"A","name":"api","content":"1.2.3.4","ttl":1,"proxied":true}`)
	if created.Code != http.StatusCreated || dns.createdZoneID != adminDNSZoneID || dns.createdInput.Content != "1.2.3.4" {
		t.Fatalf("create = %d, zone=%q input=%#v", created.Code, dns.createdZoneID, dns.createdInput)
	}
}

func TestDNSListUpdateAndDeleteContracts(t *testing.T) {
	repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
	record := cloudflare.Record{ID: "record-a", Type: "A", Name: "api.example.com", Content: "1.2.3.4", TTL: 1, Proxied: dnsBool(true), Proxiable: true, ModifiedOn: "version-1"}
	dns := &dnsServiceStub{
		page:   cloudflare.RecordPage{Zone: cloudflare.Zone{ID: adminDNSZoneID, Name: "example.com"}, Records: []cloudflare.Record{record}, Page: 2, PerPage: 10, TotalCount: 11, TotalPages: 2},
		record: record,
	}
	handler := mustDNSHandler(t, repository, dns, io.Discard)
	collection := dnsZonesPath + "/" + adminDNSZoneID + "/records"
	listed := serveAdmin(handler, http.MethodGet, collection+"?page=2&per_page=10", testSessionToken, "", "", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"total_count":11`) {
		t.Fatalf("list = %d %q", listed.Code, listed.Body.String())
	}
	item := collection + "/record-a"
	updated := serveAdmin(handler, http.MethodPatch, item, testSessionToken, testAdminOrigin, "application/json", `{"name":"api","content":"8.8.8.8","ttl":300,"proxied":false,"expected_modified_on":"version-1"}`)
	if updated.Code != http.StatusOK || dns.updatedRecord != "record-a" || dns.updatedInput.ExpectedModifiedOn != "version-1" {
		t.Fatalf("update = %d, call=%q input=%#v", updated.Code, dns.updatedRecord, dns.updatedInput)
	}
	deleted := serveAdmin(handler, http.MethodDelete, item, testSessionToken, testAdminOrigin, "application/json", `{"confirm_name":"api.example.com","expected_modified_on":"version-1"}`)
	if deleted.Code != http.StatusNoContent || dns.deletedConfirm != "api.example.com" || dns.deletedVersion != "version-1" {
		t.Fatalf("delete = %d, confirm=%q version=%q", deleted.Code, dns.deletedConfirm, dns.deletedVersion)
	}
}

func TestDNSErrorMappingAndLogsAreSanitized(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid", err: cloudflare.ErrInvalid, status: 422, code: "invalid_dns_record"},
		{name: "not found", err: cloudflare.ErrNotFound, status: 404, code: "dns_not_found"},
		{name: "conflict", err: cloudflare.ErrConflict, status: 409, code: "dns_conflict"},
		{name: "timeout", err: context.DeadlineExceeded, status: 504, code: "dns_provider_timeout"},
		{name: "provider", err: errors.New("PROVIDER-DETAIL-CANARY"), status: 502, code: "dns_provider_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			repository := &repositoryStub{session: domain.Session{TelegramUserID: 42, ExpiresAtMS: testNowMS + 60_000}}
			dns := &dnsServiceStub{err: test.err}
			handler := mustDNSHandler(t, repository, dns, &logs)
			path := dnsZonesPath + "/" + adminDNSZoneID + "/records"
			response := serveAdmin(handler, http.MethodPost, path, testSessionToken, testAdminOrigin, "application/json", `{"type":"TXT","name":"txt","content":"RECORD-VALUE-CANARY","ttl":300,"proxied":null}`)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			combined := response.Body.String() + logs.String()
			for _, forbidden := range []string{"PROVIDER-DETAIL-CANARY", "RECORD-VALUE-CANARY", testSessionToken} {
				if strings.Contains(combined, forbidden) {
					t.Fatalf("output leaked %q: %s", forbidden, combined)
				}
			}
		})
	}
}

func mustDNSHandler(t *testing.T, repository Repository, dns DNSService, loggerOutput io.Writer) http.Handler {
	t.Helper()
	handler, err := NewHandler(Config{PublicURL: testAdminOrigin}, Dependencies{
		Repository: repository,
		DNS:        dns,
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x42}, 4096)),
		Now:        func() time.Time { return time.UnixMilli(testNowMS) },
		Logger: slog.New(slog.NewJSONHandler(loggerOutput, &slog.HandlerOptions{
			ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
				if attribute.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return attribute
			},
		})),
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}

func dnsBool(value bool) *bool {
	return &value
}
