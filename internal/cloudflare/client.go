package cloudflare

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/tg-monitor/tg-monitor/internal/config"
)

const (
	productionBaseURL = "https://api.cloudflare.com/client/v4"
	maxRequestBytes   = 64 << 10
	maxResponseBytes  = 4 << 20
	providerPageSize  = 100
	maxProviderPages  = 100
)

var (
	ErrUnauthorized = errors.New("Cloudflare authorization failed")
	ErrNotFound     = errors.New("Cloudflare resource was not found")
	ErrConflict     = errors.New("Cloudflare resource changed")
	ErrInvalid      = errors.New("Cloudflare request is invalid")
	ErrUnavailable  = errors.New("Cloudflare is unavailable")
)

var supportedTypes = map[string]struct{}{
	"A": {}, "AAAA": {}, "CNAME": {}, "TXT": {},
}

type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Record struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	TTL        int    `json:"ttl"`
	Proxied    *bool  `json:"proxied"`
	Proxiable  bool   `json:"proxiable"`
	Comment    string `json:"comment,omitempty"`
	ModifiedOn string `json:"modified_on"`
}

type RecordPage struct {
	Zone       Zone     `json:"zone"`
	Records    []Record `json:"records"`
	Page       int      `json:"page"`
	PerPage    int      `json:"per_page"`
	TotalCount int      `json:"total_count"`
	TotalPages int      `json:"total_pages"`
}

type CreateRecordInput struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied *bool  `json:"proxied"`
}

type UpdateRecordInput struct {
	Name               string `json:"name"`
	Content            string `json:"content"`
	TTL                int    `json:"ttl"`
	Proxied            *bool  `json:"proxied"`
	ExpectedModifiedOn string `json:"expected_modified_on"`
}

type Client struct {
	baseURL    *url.URL
	token      string
	zones      []Zone
	zonesByID  map[string]Zone
	httpClient *http.Client
}

func New(runtime config.CloudflareRuntimeConfig) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   runtime.HTTPTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return newClient(productionBaseURL, runtime, httpClient)
}

func NewForTest(baseURL string, runtime config.CloudflareRuntimeConfig, httpClient *http.Client) (*Client, error) {
	if httpClient == nil {
		return nil, errors.New("create Cloudflare client: HTTP client is required")
	}
	copy := *httpClient
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return newClient(baseURL, runtime, &copy)
}

func newClient(baseURL string, runtime config.CloudflareRuntimeConfig, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(runtime.APIToken) == "" || runtime.HTTPTimeout <= 0 || len(runtime.Zones) == 0 {
		return nil, errors.New("create Cloudflare client: invalid configuration")
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("create Cloudflare client: invalid API URL")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopbackHost(parsed.Hostname())) {
		return nil, errors.New("create Cloudflare client: API URL must use HTTPS")
	}
	zones := make([]Zone, 0, len(runtime.Zones))
	zonesByID := make(map[string]Zone, len(runtime.Zones))
	for _, configured := range runtime.Zones {
		zone := Zone{ID: configured.ID, Name: configured.Name}
		if !safeID(zone.ID) || strings.TrimSpace(zone.Name) == "" {
			return nil, errors.New("create Cloudflare client: invalid zone configuration")
		}
		if _, exists := zonesByID[zone.ID]; exists {
			return nil, errors.New("create Cloudflare client: duplicate zone")
		}
		zones = append(zones, zone)
		zonesByID[zone.ID] = zone
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
	return &Client{
		baseURL: parsed, token: strings.TrimSpace(runtime.APIToken),
		zones: zones, zonesByID: zonesByID, httpClient: httpClient,
	}, nil
}

func (client *Client) Zones() []Zone {
	return append([]Zone(nil), client.zones...)
}

func (client *Client) Verify(ctx context.Context) error {
	var token struct {
		Status string `json:"status"`
	}
	if _, err := client.request(ctx, http.MethodGet, "/user/tokens/verify", nil, nil, &token); err != nil {
		return err
	}
	if token.Status != "active" {
		return ErrUnauthorized
	}
	for _, configured := range client.zones {
		var actual Zone
		if _, err := client.request(ctx, http.MethodGet, "/zones/"+configured.ID, nil, nil, &actual); err != nil {
			return err
		}
		if actual.ID != configured.ID || !strings.EqualFold(strings.TrimSuffix(actual.Name, "."), configured.Name) {
			return ErrInvalid
		}
	}
	return nil
}

func (client *Client) ListRecords(ctx context.Context, zoneID string, page, perPage int) (RecordPage, error) {
	zone, err := client.zone(zoneID)
	if err != nil {
		return RecordPage{}, err
	}
	if page < 1 || perPage < 1 || perPage > 100 {
		return RecordPage{}, ErrInvalid
	}
	providerPage := 1
	totalProviderPages := 1
	records := make([]Record, 0)
	for providerPage <= totalProviderPages {
		if providerPage > maxProviderPages {
			return RecordPage{}, ErrUnavailable
		}
		query := url.Values{
			"page":      {strconv.Itoa(providerPage)},
			"per_page":  {strconv.Itoa(providerPageSize)},
			"order":     {"name"},
			"direction": {"asc"},
		}
		var batch []Record
		info, requestErr := client.request(ctx, http.MethodGet, "/zones/"+zone.ID+"/dns_records", query, nil, &batch)
		if requestErr != nil {
			return RecordPage{}, requestErr
		}
		for _, record := range batch {
			if _, supported := supportedTypes[record.Type]; !supported {
				continue
			}
			if err := validateReturnedRecord(record, zone); err != nil {
				return RecordPage{}, err
			}
			records = append(records, record)
		}
		totalProviderPages = info.TotalPages
		if totalProviderPages < 1 {
			totalProviderPages = 1
		}
		providerPage++
	}
	sort.Slice(records, func(i, j int) bool {
		left, right := records[i], records[j]
		if !strings.EqualFold(left.Name, right.Name) {
			return strings.ToLower(left.Name) < strings.ToLower(right.Name)
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		return strings.ToLower(left.Content) < strings.ToLower(right.Content)
	})
	total := len(records)
	totalPages := (total + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * perPage
	end := start + perPage
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	return RecordPage{
		Zone: zone, Records: records[start:end], Page: page, PerPage: perPage,
		TotalCount: total, TotalPages: totalPages,
	}, nil
}

func (client *Client) GetRecord(ctx context.Context, zoneID, recordID string) (Record, error) {
	zone, err := client.zone(zoneID)
	if err != nil || !safeID(recordID) {
		return Record{}, ErrNotFound
	}
	var record Record
	if _, err := client.request(ctx, http.MethodGet, "/zones/"+zone.ID+"/dns_records/"+recordID, nil, nil, &record); err != nil {
		return Record{}, err
	}
	if _, supported := supportedTypes[record.Type]; !supported {
		return Record{}, ErrInvalid
	}
	if err := validateReturnedRecord(record, zone); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (client *Client) CreateRecord(ctx context.Context, zoneID string, input CreateRecordInput) (Record, error) {
	zone, err := client.zone(zoneID)
	if err != nil {
		return Record{}, err
	}
	payload, err := normalizePayload(zone, strings.ToUpper(strings.TrimSpace(input.Type)), input.Name, input.Content, input.TTL, input.Proxied)
	if err != nil {
		return Record{}, err
	}
	var created Record
	if _, err := client.request(ctx, http.MethodPost, "/zones/"+zone.ID+"/dns_records", nil, payload, &created); err != nil {
		return Record{}, err
	}
	if err := validateReturnedRecord(created, zone); err != nil {
		return Record{}, err
	}
	return created, nil
}

func (client *Client) UpdateRecord(ctx context.Context, zoneID, recordID string, input UpdateRecordInput) (Record, error) {
	current, err := client.GetRecord(ctx, zoneID, recordID)
	if err != nil {
		return Record{}, err
	}
	if strings.TrimSpace(input.ExpectedModifiedOn) == "" || input.ExpectedModifiedOn != current.ModifiedOn {
		return Record{}, ErrConflict
	}
	zone, _ := client.zone(zoneID)
	payload, err := normalizePayload(zone, current.Type, input.Name, input.Content, input.TTL, input.Proxied)
	if err != nil {
		return Record{}, err
	}
	var updated Record
	if _, err := client.request(ctx, http.MethodPatch, "/zones/"+zone.ID+"/dns_records/"+recordID, nil, payload, &updated); err != nil {
		return Record{}, err
	}
	if err := validateReturnedRecord(updated, zone); err != nil {
		return Record{}, err
	}
	return updated, nil
}

func (client *Client) DeleteRecord(ctx context.Context, zoneID, recordID, confirmation, expectedModifiedOn string) error {
	current, err := client.GetRecord(ctx, zoneID, recordID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(expectedModifiedOn) == "" || expectedModifiedOn != current.ModifiedOn {
		return ErrConflict
	}
	if strings.TrimSuffix(strings.TrimSpace(confirmation), ".") != current.Name {
		return ErrInvalid
	}
	zone, _ := client.zone(zoneID)
	var result struct {
		ID string `json:"id"`
	}
	if _, err := client.request(ctx, http.MethodDelete, "/zones/"+zone.ID+"/dns_records/"+recordID, nil, nil, &result); err != nil {
		return err
	}
	if result.ID != "" && result.ID != recordID {
		return ErrUnavailable
	}
	return nil
}

func (client *Client) zone(id string) (Zone, error) {
	zone, exists := client.zonesByID[id]
	if !exists {
		return Zone{}, ErrNotFound
	}
	return zone, nil
}

type recordPayload struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied *bool  `json:"proxied,omitempty"`
}

func normalizePayload(zone Zone, recordType, rawName, rawContent string, ttl int, proxied *bool) (recordPayload, error) {
	if _, supported := supportedTypes[recordType]; !supported {
		return recordPayload{}, ErrInvalid
	}
	name, err := normalizeRecordName(rawName, zone.Name)
	if err != nil {
		return recordPayload{}, err
	}
	content := strings.TrimSpace(rawContent)
	if content == "" || (ttl != 1 && (ttl < 60 || ttl > 86400)) {
		return recordPayload{}, ErrInvalid
	}
	switch recordType {
	case "A":
		ip := net.ParseIP(content)
		if ip == nil || ip.To4() == nil {
			return recordPayload{}, ErrInvalid
		}
		content = ip.To4().String()
	case "AAAA":
		ip := net.ParseIP(content)
		if ip == nil || ip.To4() != nil {
			return recordPayload{}, ErrInvalid
		}
		content = ip.String()
	case "CNAME":
		content = strings.ToLower(strings.TrimSuffix(content, "."))
		if !validDomain(content) {
			return recordPayload{}, ErrInvalid
		}
	case "TXT":
		if proxied != nil {
			return recordPayload{}, ErrInvalid
		}
	}
	if recordType != "TXT" {
		value := false
		if proxied != nil {
			value = *proxied
		}
		proxied = &value
		if value {
			ttl = 1
		}
	}
	return recordPayload{Type: recordType, Name: name, Content: content, TTL: ttl, Proxied: proxied}, nil
}

func normalizeRecordName(raw, zoneName string) (string, error) {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if name == "@" {
		return zoneName, nil
	}
	if name == "" {
		return "", ErrInvalid
	}
	if !strings.Contains(name, ".") {
		name += "." + zoneName
	}
	if name != zoneName && !strings.HasSuffix(name, "."+zoneName) {
		return "", ErrInvalid
	}
	if !validDomain(name) {
		return "", ErrInvalid
	}
	return name, nil
}

func validDomain(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
				continue
			}
			return false
		}
	}
	return true
}

func validateReturnedRecord(record Record, zone Zone) error {
	name := strings.ToLower(strings.TrimSuffix(record.Name, "."))
	if !safeID(record.ID) || (name != zone.Name && !strings.HasSuffix(name, "."+zone.Name)) {
		return ErrUnavailable
	}
	return nil
}

type resultInfo struct {
	TotalPages int `json:"total_pages"`
}

type envelope struct {
	Success    bool            `json:"success"`
	Result     json.RawMessage `json:"result"`
	ResultInfo resultInfo      `json:"result_info"`
}

func (client *Client) request(ctx context.Context, method, path string, query url.Values, payload, result any) (resultInfo, error) {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil || len(body) > maxRequestBytes {
			return resultInfo{}, ErrInvalid
		}
	}
	endpoint := *client.baseURL
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + path
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return resultInfo{}, ErrInvalid
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return resultInfo{}, ctx.Err()
		}
		return resultInfo{}, ErrUnavailable
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(encoded) > maxResponseBytes {
		return resultInfo{}, ErrUnavailable
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return resultInfo{}, classifyStatus(response.StatusCode)
	}
	var decoded envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil || !decoded.Success || len(decoded.Result) == 0 {
		return resultInfo{}, ErrUnavailable
	}
	if result != nil {
		if err := json.Unmarshal(decoded.Result, result); err != nil {
			return resultInfo{}, ErrUnavailable
		}
	}
	return decoded.ResultInfo, nil
}

func classifyStatus(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusConflict:
		return ErrConflict
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return ErrInvalid
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return ErrUnavailable
	default:
		if status >= 500 {
			return ErrUnavailable
		}
		return ErrInvalid
	}
}

func safeID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
