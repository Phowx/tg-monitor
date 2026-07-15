#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repository_root"

GO="${GO:-go}"
work_dir="$(mktemp -d "$repository_root/.smoke-alerts.XXXXXX")"
server_pid=""
proxy_pid=""

cleanup() {
    if [[ -n "$server_pid" ]]; then
        kill -TERM "$server_pid" 2>/dev/null || true
        wait "$server_pid" 2>/dev/null || true
    fi
    if [[ -n "$proxy_pid" ]]; then
        kill -TERM "$proxy_pid" 2>/dev/null || true
        wait "$proxy_pid" 2>/dev/null || true
    fi
    rm -rf "$work_dir"
}
trap cleanup EXIT
trap 'status=$?; printf "smoke_error_line=%s status=%s\n" "$LINENO" "$status" >&2; exit "$status"' ERR

choose_port() {
    local start port offset
    start=$((20000 + RANDOM % 20000))
    for ((offset = 0; offset < 1000; offset++)); do
        port=$((start + offset))
        if ! (exec 9<>/dev/tcp/127.0.0.1/"$port") 2>/dev/null; then
            printf '%s' "$port"
            return 0
        fi
    done
    return 1
}

wait_for_port() {
    local port="$1" attempt
    for ((attempt = 0; attempt < 200; attempt++)); do
        if (exec 9<>/dev/tcp/127.0.0.1/"$port") 2>/dev/null; then
            return 0
        fi
        sleep 0.05
    done
    return 1
}

wait_for_file() {
    local path="$1" attempt
    for ((attempt = 0; attempt < 200; attempt++)); do
        if [[ -s "$path" ]]; then
            return 0
        fi
        sleep 0.05
    done
    return 1
}

wait_for_line() {
    local path="$1" expected="$2" attempt
    for ((attempt = 0; attempt < 240; attempt++)); do
        if grep -Fxq "$expected" "$path" 2>/dev/null; then
            return 0
        fi
        sleep 0.05
    done
    return 1
}

server_binary="$work_dir/tg-monitor-server"
proxy_binary="$work_dir/fake-telegram-proxy"
helper_binary="$work_dir/alert-smoke-helper"
proxy_source="$work_dir/fake-telegram-proxy.go"
helper_source="$work_dir/alert-smoke-helper.go"
database="$work_dir/monitor.db"
server_log="$work_dir/server.log"
proxy_log="$work_dir/proxy.log"
helper_log="$work_dir/helper.log"
proxy_stats="$work_dir/proxy-stats.txt"
ca_certificate="$work_dir/fake-telegram-ca.pem"
cookie_jar="$work_dir/cookies.txt"
server_port="$(choose_port)"
proxy_port="$(choose_port)"
while [[ "$proxy_port" == "$server_port" ]]; do
    proxy_port="$(choose_port)"
done

bot_token='987654321:SMOKE_ALERT_BOT_TOKEN_CANARY_abcdefghijklmnopqrstuvwxyz'
webhook_secret='SMOKE_ALERT_WEBHOOK_SECRET_CANARY_123456789'
fake_description='SMOKE_ALERT_TELEGRAM_DESCRIPTION_CANARY'
server_name='smoke-alert-host'

cat >"$proxy_source" <<'GO'
package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type recorder struct {
	mu               sync.Mutex
	offlineAttempts  int
	offlineAccepted  int
	recoveryMessages int
	unexpected       int
	statsPath        string
	token            string
	description      string
}

func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	certificate, caPEM, err := testCertificate()
	if err != nil || os.WriteFile(os.Args[2], caPEM, 0o600) != nil {
		os.Exit(2)
	}
	recorder := &recorder{
		statsPath:   os.Getenv("FAKE_STATS_PATH"),
		token:       os.Getenv("FAKE_EXPECTED_BOT_TOKEN"),
		description: os.Getenv("FAKE_RESPONSE_DESCRIPTION"),
	}
	if recorder.statsPath == "" || recorder.token == "" || recorder.description == "" || recorder.writeStats() != nil {
		os.Exit(2)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+os.Args[1])
	if err != nil {
		os.Exit(2)
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go recorder.serveProxy(connection, certificate)
	}
}

func (recorder *recorder) serveProxy(connection net.Conn, certificate tls.Certificate) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "CONNECT api.telegram.org:443 ") {
		return
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			return
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	tlsConnection := tls.Server(connection, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	})
	if err := tlsConnection.Handshake(); err != nil {
		return
	}
	tlsReader := bufio.NewReader(tlsConnection)
	tlsWriter := bufio.NewWriter(tlsConnection)
	for {
		request, err := http.ReadRequest(tlsReader)
		if err != nil {
			return
		}
		body, readErr := io.ReadAll(io.LimitReader(request.Body, 64<<10))
		_ = request.Body.Close()
		accepted := recorder.record(request, body, readErr)
		response, _ := json.Marshal(struct {
			OK          bool   `json:"ok"`
			Result      bool   `json:"result"`
			Description string `json:"description,omitempty"`
		}{OK: true, Result: accepted, Description: recorder.description})
		_, _ = fmt.Fprintf(tlsWriter, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: keep-alive\r\n\r\n", len(response))
		if _, err := tlsWriter.Write(response); err != nil || tlsWriter.Flush() != nil {
			return
		}
	}
}

func (recorder *recorder) record(request *http.Request, body []byte, readErr error) bool {
	var payload struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	valid := readErr == nil && request.Method == http.MethodPost &&
		request.URL.Path == "/bot"+recorder.token+"/sendMessage" &&
		json.Unmarshal(body, &payload) == nil && payload.ChatID == 42
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	accepted := false
	switch {
	case valid && strings.Contains(payload.Text, " is offline"):
		recorder.offlineAttempts++
		accepted = recorder.offlineAttempts > 1
		if accepted {
			recorder.offlineAccepted++
		}
	case valid && strings.Contains(payload.Text, " recovered"):
		recorder.recoveryMessages++
		accepted = true
	default:
		recorder.unexpected++
	}
	if recorder.writeStatsLocked() != nil {
		return false
	}
	return accepted
}

func (recorder *recorder) writeStats() error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.writeStatsLocked()
}

func (recorder *recorder) writeStatsLocked() error {
	content := fmt.Sprintf("offlineAttempts=%d\nofflineAccepted=%d\nrecoveryMessages=%d\nunexpected=%d\n",
		recorder.offlineAttempts, recorder.offlineAccepted, recorder.recoveryMessages, recorder.unexpected)
	temporary := recorder.statsPath + ".tmp"
	if err := os.WriteFile(temporary, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, recorder.statsPath)
}

func testCertificate() (tls.Certificate, []byte, error) {
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "tg-monitor alert smoke CA"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "api.telegram.org"},
		DNSNames:     []string{"api.telegram.org"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	leafKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER})
	certificate, err := tls.X509KeyPair(leafPEM, leafKeyPEM)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return certificate, caPEM, err
}
GO

cat >"$helper_source" <<'GO'
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"

	"github.com/tg-monitor/tg-monitor/internal/storage/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "sign":
		sign()
	case "inspect":
		if len(os.Args) != 3 {
			os.Exit(2)
		}
		inspect(os.Args[2])
	case "cleanup":
		if len(os.Args) != 4 {
			os.Exit(2)
		}
		cutoffMS, err := strconv.ParseInt(os.Args[3], 10, 64)
		if err != nil {
			os.Exit(2)
		}
		cleanup(os.Args[2], cutoffMS)
	default:
		os.Exit(2)
	}
}

func sign() {
	botToken := os.Getenv("SIGN_BOT_TOKEN")
	authDate := os.Getenv("SIGN_AUTH_DATE")
	user := os.Getenv("SIGN_USER_JSON")
	if botToken == "" || authDate == "" || user == "" {
		os.Exit(2)
	}
	checkString := "auth_date=" + authDate + "\nuser=" + user
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(botToken))
	check := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = check.Write([]byte(checkString))
	values := url.Values{"auth_date": {authDate}, "user": {user}}
	values.Set("hash", hex.EncodeToString(check.Sum(nil)))
	fmt.Print(values.Encode())
}

func inspect(path string) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		fail()
	}
	defer database.Close()
	var total, delivered, pending, attempts int
	err = database.QueryRow(`
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN delivered_at_ms IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN delivered_at_ms IS NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(attempts), 0)
		FROM alert_outbox`).Scan(&total, &delivered, &pending, &attempts)
	if err != nil {
		fail()
	}
	fmt.Printf("total=%d delivered=%d pending=%d attempts=%d\n", total, delivered, pending, attempts)
}

func cleanup(path string, cutoffMS int64) {
	store, err := sqlite.Open(context.Background(), path)
	if err != nil {
		fail()
	}
	deleted, cleanupErr := store.DeleteTerminalAlertOutboxBefore(context.Background(), cutoffMS)
	closeErr := store.Close()
	if cleanupErr != nil || closeErr != nil {
		fail()
	}
	fmt.Printf("cleanup=%d\n", deleted)
}

func fail() {
	os.Exit(1)
}
GO

CGO_ENABLED=0 "$GO" build -o "$server_binary" ./cmd/tg-monitor-server >/dev/null
CGO_ENABLED=0 "$GO" build -o "$proxy_binary" "$proxy_source" >/dev/null
CGO_ENABLED=0 "$GO" build -o "$helper_binary" "$helper_source" >/dev/null

FAKE_STATS_PATH="$proxy_stats" \
FAKE_EXPECTED_BOT_TOKEN="$bot_token" \
FAKE_RESPONSE_DESCRIPTION="$fake_description" \
    "$proxy_binary" "$proxy_port" "$ca_certificate" >"$proxy_log" 2>&1 &
proxy_pid=$!
wait_for_port "$proxy_port"
wait_for_file "$ca_certificate"
wait_for_file "$proxy_stats"

registration="$(TG_MONITOR_DATABASE_PATH="$database" "$server_binary" server add --name "$server_name" --group smoke 2>>"$server_log")"
server_id="$(printf '%s\n' "$registration" | sed -n 's/^server_id=//p')"
agent_token="$(printf '%s\n' "$registration" | sed -n 's/^agent_token=//p')"
unset registration
if [[ -z "$server_id" || -z "$agent_token" ]]; then
    exit 1
fi

start_server() {
    HTTPS_PROXY="http://127.0.0.1:$proxy_port" \
    https_proxy="http://127.0.0.1:$proxy_port" \
    NO_PROXY="127.0.0.1,localhost" \
    no_proxy="127.0.0.1,localhost" \
    SSL_CERT_FILE="$ca_certificate" \
    TG_MONITOR_DATABASE_PATH="$database" \
    TG_MONITOR_LISTEN_ADDR="127.0.0.1:$server_port" \
    TG_MONITOR_CHECKPOINT_INTERVAL=100ms \
    TG_MONITOR_TELEGRAM_ENABLED=true \
    TG_MONITOR_PUBLIC_URL="http://127.0.0.1:$server_port" \
    TG_MONITOR_BOT_TOKEN="$bot_token" \
    TG_MONITOR_WEBHOOK_SECRET="$webhook_secret" \
    TG_MONITOR_ADMIN_TELEGRAM_IDS=42 \
    TG_MONITOR_SESSION_TTL=12h \
    TG_MONITOR_INIT_DATA_MAX_AGE=5m \
    TG_MONITOR_TELEGRAM_HTTP_TIMEOUT=2s \
        "$server_binary" serve >>"$server_log" 2>&1 &
    server_pid=$!
    wait_for_port "$server_port"
}

stop_server() {
    kill -TERM "$server_pid"
    wait "$server_pid"
    server_pid=""
}

inspect_outbox() {
    local output
    output="$("$helper_binary" inspect "$database")"
    printf '%s\n' "$output" >>"$helper_log"
    printf '%s' "$output"
}

wait_for_outbox() {
    local expected="$1" attempt output
    for ((attempt = 0; attempt < 200; attempt++)); do
        output="$(inspect_outbox)"
        if [[ "$output" == "$expected" ]]; then
            return 0
        fi
        sleep 0.05
    done
    return 1
}

start_server

auth_date="$(date +%s)"
user_json='{"id":42,"first_name":"Smoke"}'
init_data="$(SIGN_BOT_TOKEN="$bot_token" SIGN_AUTH_DATE="$auth_date" SIGN_USER_JSON="$user_json" "$helper_binary" sign)"
login_payload="{\"init_data\":\"$init_data\"}"
login_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --request POST "http://127.0.0.1:$server_port/api/v1/auth/telegram" \
    --header 'Content-Type: application/json' \
    --cookie-jar "$cookie_jar" \
    --data "$login_payload")"
if [[ "$login_status" != 204 ]]; then
    exit 1
fi
session_cookie="$(awk '$6 == "tg_monitor_session" {print $7}' "$cookie_jar")"
if [[ -z "$session_cookie" ]]; then
    exit 1
fi

settings_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --request PUT --cookie "$cookie_jar" \
    --header "Origin: http://127.0.0.1:$server_port" \
    --header 'Content-Type: application/json' \
    --data '{"offline_threshold_seconds":1,"alert_threshold_seconds":1,"history_retention_days":7}' \
    "http://127.0.0.1:$server_port/api/v1/admin/settings")"
if [[ "$settings_status" != 200 ]]; then
    exit 1
fi

captured_at_ms="$(( $(date +%s) * 1000 ))"
metric_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --request POST "http://127.0.0.1:$server_port/api/v1/metrics" \
    --header "Authorization: Bearer $agent_token" \
    --header 'Content-Type: application/json' \
    --data "{\"captured_at\":$captured_at_ms,\"cpu_pct\":12.5,\"memory_total_bytes\":16000,\"memory_used_bytes\":8000,\"root_disk_total_bytes\":100000,\"root_disk_used_bytes\":40000,\"load_1\":0.1,\"load_5\":0.2,\"load_15\":0.3,\"network_rx_total_bytes\":1000,\"network_tx_total_bytes\":2000,\"network_rx_bytes_per_second\":10,\"network_tx_bytes_per_second\":20,\"uptime_seconds\":3600,\"system\":{\"hostname\":\"$server_name\",\"os\":\"linux\",\"kernel\":\"6.12\",\"arch\":\"amd64\"}}")"
if [[ "$metric_status" != 204 ]]; then
    exit 1
fi

wait_for_line "$proxy_stats" 'offlineAttempts=1'
wait_for_outbox 'total=1 delivered=0 pending=1 attempts=1'
stop_server

future_cutoff_ms="$(( $(date +%s) * 1000 + 40 * 24 * 60 * 60 * 1000 ))"
active_cleanup="$("$helper_binary" cleanup "$database" "$future_cutoff_ms")"
printf '%s\n' "$active_cleanup" >>"$helper_log"
if [[ "$active_cleanup" != 'cleanup=0' ]]; then
    exit 1
fi

start_server
wait_for_line "$proxy_stats" 'offlineAttempts=2'
wait_for_line "$proxy_stats" 'offlineAccepted=1'
wait_for_outbox 'total=1 delivered=1 pending=0 attempts=1'

recovered_at_ms="$(( $(date +%s) * 1000 ))"
recovery_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --request POST "http://127.0.0.1:$server_port/api/v1/metrics" \
    --header "Authorization: Bearer $agent_token" \
    --header 'Content-Type: application/json' \
    --data "{\"captured_at\":$recovered_at_ms,\"cpu_pct\":10,\"memory_total_bytes\":16000,\"memory_used_bytes\":7000,\"root_disk_total_bytes\":100000,\"root_disk_used_bytes\":39000,\"load_1\":0.1,\"load_5\":0.2,\"load_15\":0.3,\"network_rx_total_bytes\":1100,\"network_tx_total_bytes\":2100,\"network_rx_bytes_per_second\":11,\"network_tx_bytes_per_second\":21,\"uptime_seconds\":3610,\"system\":{\"hostname\":\"$server_name\",\"os\":\"linux\",\"kernel\":\"6.12\",\"arch\":\"amd64\"}}")"
if [[ "$recovery_status" != 204 ]]; then
    exit 1
fi
wait_for_line "$proxy_stats" 'recoveryMessages=1'
wait_for_outbox 'total=2 delivered=2 pending=0 attempts=1'
stable_settings_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --request PUT --cookie "$cookie_jar" \
    --header "Origin: http://127.0.0.1:$server_port" \
    --header 'Content-Type: application/json' \
    --data '{"offline_threshold_seconds":86400,"alert_threshold_seconds":86400,"history_retention_days":7}' \
    "http://127.0.0.1:$server_port/api/v1/admin/settings")"
if [[ "$stable_settings_status" != 200 ]]; then
    exit 1
fi

terminal_cleanup="$("$helper_binary" cleanup "$database" "$future_cutoff_ms")"
printf '%s\n' "$terminal_cleanup" >>"$helper_log"
if [[ "$terminal_cleanup" != 'cleanup=2' ]]; then
    exit 1
fi
wait_for_outbox 'total=0 delivered=0 pending=0 attempts=0'

sleep 5.2
grep -Fxq 'offlineAttempts=2' "$proxy_stats"
grep -Fxq 'offlineAccepted=1' "$proxy_stats"
grep -Fxq 'recoveryMessages=1' "$proxy_stats"
grep -Fxq 'unexpected=0' "$proxy_stats"

stop_server
kill -TERM "$proxy_pid" 2>/dev/null || true
wait "$proxy_pid" 2>/dev/null || true
proxy_pid=""

canaries=(
    "$bot_token"
    "$webhook_secret"
    "$agent_token"
    "$session_cookie"
    "$init_data"
    "$fake_description"
    "$server_name is offline"
    "\"server_name\":\"$server_name\""
)
for canary in "${canaries[@]}"; do
    if grep -F -- "$canary" "$server_log" "$proxy_log" "$helper_log" >/dev/null 2>&1; then
        exit 1
    fi
done

printf '%s\n' 'alert_offline=ok retry_restart=ok recovery=ok cleanup=ok'
printf '%s\n' 'alert_sigterm=clean secret_log_scan=clean'
