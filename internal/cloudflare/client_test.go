package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/config"
)

const (
	testZoneID = "0123456789abcdef0123456789abcdef"
	testToken  = "CF-HTTP-TOKEN-CANARY"
)

func testRuntime() config.CloudflareRuntimeConfig {
	return config.CloudflareRuntimeConfig{
		APIToken: testToken,
		Zones: []config.CloudflareZoneConfig{{
			ID: testZoneID, Name: "example.com",
		}},
		HTTPTimeout: time.Second,
	}
}

func TestClientVerifyListCreateUpdateAndDelete(t *testing.T) {
	modified := "2026-07-15T00:00:00Z"
	methods := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		methods = append(methods, request.Method+" "+request.URL.Path)
		switch request.URL.Path {
		case "/client/v4/user/tokens/verify":
			writeProviderResult(t, writer, map[string]any{"status": "active"}, nil)
		case "/client/v4/zones/" + testZoneID:
			writeProviderResult(t, writer, Zone{ID: testZoneID, Name: "example.com"}, nil)
		case "/client/v4/zones/" + testZoneID + "/dns_records":
			if request.Method == http.MethodGet {
				if request.URL.Query().Get("per_page") != "100" {
					t.Errorf("per_page = %q", request.URL.Query().Get("per_page"))
				}
				writeProviderResult(t, writer, []any{
					Record{ID: "record-a", Type: "A", Name: "api.example.com", Content: "1.2.3.4", TTL: 1, Proxied: boolPointer(true), Proxiable: true, ModifiedOn: modified},
					Record{ID: "record-mx", Type: "MX", Name: "example.com", Content: "mail.example.com", TTL: 300, ModifiedOn: modified},
				}, map[string]any{"total_pages": 1})
				return
			}
			var payload recordPayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode create: %v", err)
			}
			if payload.Type != "A" || payload.Name != "new.example.com" || payload.Content != "5.6.7.8" || payload.TTL != 1 || payload.Proxied == nil || !*payload.Proxied {
				t.Errorf("create payload = %#v", payload)
			}
			writeProviderResult(t, writer, Record{ID: "record-new", Type: payload.Type, Name: payload.Name, Content: payload.Content, TTL: payload.TTL, Proxied: payload.Proxied, Proxiable: true, ModifiedOn: "created"}, nil)
		case "/client/v4/zones/" + testZoneID + "/dns_records/record-a":
			switch request.Method {
			case http.MethodGet:
				writeProviderResult(t, writer, Record{ID: "record-a", Type: "A", Name: "api.example.com", Content: "1.2.3.4", TTL: 1, Proxied: boolPointer(true), Proxiable: true, ModifiedOn: modified}, nil)
			case http.MethodPatch:
				var payload recordPayload
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Errorf("decode update: %v", err)
				}
				if payload.Type != "A" || payload.Name != "api.example.com" || payload.Content != "8.8.8.8" || payload.TTL != 300 || payload.Proxied == nil || *payload.Proxied {
					t.Errorf("update payload = %#v", payload)
				}
				writeProviderResult(t, writer, Record{ID: "record-a", Type: "A", Name: payload.Name, Content: payload.Content, TTL: payload.TTL, Proxied: payload.Proxied, Proxiable: true, ModifiedOn: "updated"}, nil)
			case http.MethodDelete:
				writeProviderResult(t, writer, map[string]any{"id": "record-a"}, nil)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewForTest(server.URL+"/client/v4", testRuntime(), server.Client())
	if err != nil {
		t.Fatalf("NewForTest() error = %v", err)
	}
	if err := client.Verify(context.Background()); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	page, err := client.ListRecords(context.Background(), testZoneID, 1, 20)
	if err != nil || page.TotalCount != 1 || len(page.Records) != 1 || page.Records[0].Type != "A" {
		t.Fatalf("ListRecords() = %#v, %v", page, err)
	}
	created, err := client.CreateRecord(context.Background(), testZoneID, CreateRecordInput{
		Type: "A", Name: "new", Content: "5.6.7.8", TTL: 300, Proxied: boolPointer(true),
	})
	if err != nil || created.ID != "record-new" || created.TTL != 1 {
		t.Fatalf("CreateRecord() = %#v, %v", created, err)
	}
	updated, err := client.UpdateRecord(context.Background(), testZoneID, "record-a", UpdateRecordInput{
		Name: "api.example.com", Content: "8.8.8.8", TTL: 300,
		Proxied: boolPointer(false), ExpectedModifiedOn: modified,
	})
	if err != nil || updated.Content != "8.8.8.8" {
		t.Fatalf("UpdateRecord() = %#v, %v", updated, err)
	}
	if err := client.DeleteRecord(context.Background(), testZoneID, "record-a", "api.example.com", modified); err != nil {
		t.Fatalf("DeleteRecord() error = %v", err)
	}
	for _, required := range []string{
		"GET /client/v4/user/tokens/verify",
		"GET /client/v4/zones/" + testZoneID,
		"POST /client/v4/zones/" + testZoneID + "/dns_records",
		"PATCH /client/v4/zones/" + testZoneID + "/dns_records/record-a",
		"DELETE /client/v4/zones/" + testZoneID + "/dns_records/record-a",
	} {
		if !contains(methods, required) {
			t.Errorf("calls %#v missing %q", methods, required)
		}
	}
}

func TestClientRejectsInvalidInputAndStaleMutation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeProviderResult(t, writer, Record{
			ID: "record-a", Type: "A", Name: "api.example.com", Content: "1.2.3.4",
			TTL: 1, Proxied: boolPointer(true), Proxiable: true, ModifiedOn: "current",
		}, nil)
	}))
	defer server.Close()
	client, err := NewForTest(server.URL, testRuntime(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	invalid := []CreateRecordInput{
		{Type: "MX", Name: "mail", Content: "mail.example.com", TTL: 300},
		{Type: "A", Name: "outside.example.net", Content: "1.2.3.4", TTL: 300},
		{Type: "A", Name: "api", Content: "not-an-ip", TTL: 300},
		{Type: "AAAA", Name: "api", Content: "1.2.3.4", TTL: 300},
		{Type: "CNAME", Name: "www", Content: "bad target", TTL: 300},
		{Type: "TXT", Name: "txt", Content: "value", TTL: 300, Proxied: boolPointer(false)},
		{Type: "TXT", Name: "txt", Content: "", TTL: 300},
		{Type: "TXT", Name: "txt", Content: "value", TTL: 2},
	}
	for _, input := range invalid {
		if _, err := client.CreateRecord(context.Background(), testZoneID, input); !errors.Is(err, ErrInvalid) {
			t.Errorf("CreateRecord(%#v) error = %v, want ErrInvalid", input, err)
		}
	}
	if _, err := client.UpdateRecord(context.Background(), testZoneID, "record-a", UpdateRecordInput{
		Name: "api", Content: "1.2.3.4", TTL: 1, Proxied: boolPointer(true), ExpectedModifiedOn: "stale",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale UpdateRecord() error = %v", err)
	}
	if err := client.DeleteRecord(context.Background(), testZoneID, "record-a", "wrong.example.com", "current"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("DeleteRecord confirmation error = %v", err)
	}
}

func TestClientSanitizesProviderFailuresAndTimeouts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/timeout/zones/"+testZoneID+"/dns_records" {
			<-request.Context().Done()
			return
		}
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"success":false,"errors":[{"message":"PROVIDER-DETAIL-CANARY"}]}`))
	}))
	defer server.Close()

	client, err := NewForTest(server.URL+"/denied", testRuntime(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListRecords(context.Background(), testZoneID, 1, 20); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("forbidden error = %v", err)
	} else if strings.Contains(err.Error(), "PROVIDER-DETAIL-CANARY") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("provider failure leaked details: %v", err)
	}

	timeoutHTTPClient := server.Client()
	timeoutHTTPClient.Timeout = 20 * time.Millisecond
	timeoutClient, err := NewForTest(server.URL+"/timeout", testRuntime(), timeoutHTTPClient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := timeoutClient.ListRecords(context.Background(), testZoneID, 1, 20); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("timeout error = %v, want ErrUnavailable", err)
	}
}

func writeProviderResult(t *testing.T, writer http.ResponseWriter, result any, info map[string]any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(map[string]any{
		"success": true, "result": result, "result_info": info,
	}); err != nil {
		t.Errorf("encode provider response: %v", err)
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
