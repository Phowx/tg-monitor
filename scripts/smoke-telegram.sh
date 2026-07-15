#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repository_root"

GO="${GO:-go}"
work_dir="$(mktemp -d)"
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
    for ((attempt = 0; attempt < 100; attempt++)); do
        if (exec 9<>/dev/tcp/127.0.0.1/"$port") 2>/dev/null; then
            return 0
        fi
        sleep 0.05
    done
    return 1
}

wait_for_file() {
    local path="$1" attempt
    for ((attempt = 0; attempt < 100; attempt++)); do
        if [[ -s "$path" ]]; then
            return 0
        fi
        sleep 0.05
    done
    return 1
}

server_binary="$work_dir/tg-monitor-server"
server_log="$work_dir/server.log"
proxy_log="$work_dir/fake-telegram-proxy.log"
database="$work_dir/monitor.db"
cookie_jar="$work_dir/cookies.txt"
app_html="$work_dir/app.html"
app_headers="$work_dir/app-headers.txt"
css_asset="$work_dir/app.css"
css_headers="$work_dir/css-headers.txt"
js_asset="$work_dir/app.js"
js_headers="$work_dir/js-headers.txt"
overview_json="$work_dir/overview.json"
history_json="$work_dir/history.json"
rotate_json="$work_dir/rotate.json"
ca_certificate="$work_dir/fake-telegram-ca.pem"
proxy_stats="$work_dir/fake-telegram-stats.txt"
fake_source="$work_dir/fake-telegram-proxy.go"
fake_binary="$work_dir/fake-telegram-proxy"
signer_source="$work_dir/sign-init-data.go"
signer_binary="$work_dir/sign-init-data"
server_port="$(choose_port)"
proxy_port="$(choose_port)"
while [[ "$proxy_port" == "$server_port" ]]; do
    proxy_port="$(choose_port)"
done

bot_token='987654321:SMOKE_BOT_TOKEN_CANARY_abcdefghijklmnopqrstuvwxyz'
webhook_secret='SMOKE_WEBHOOK_SECRET_CANARY_123456789'
message_text='/status SMOKE_TELEGRAM_MESSAGE_CANARY'
fake_description='SMOKE_FAKE_BOT_DESCRIPTION_CANARY'

# The loopback proxy accepts the standard HTTPS_PROXY CONNECT flow for api.telegram.org only.
cat >"$fake_source" <<'GO'
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
	mu          sync.Mutex
	statusCount int
	webAppCount int
	statsPath   string
	token       string
	description string
	webAppURL   string
}

func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	certificate, caPEM, err := testCertificate()
	if err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Args[2], caPEM, 0o600); err != nil {
		os.Exit(2)
	}
	recorder := &recorder{
		statsPath:   os.Getenv("FAKE_STATS_PATH"),
		token:       os.Getenv("FAKE_EXPECTED_BOT_TOKEN"),
		description: os.Getenv("FAKE_RESPONSE_DESCRIPTION"),
		webAppURL:   os.Getenv("FAKE_EXPECTED_WEBAPP_URL"),
	}
	if recorder.token == "" || recorder.statsPath == "" || recorder.description == "" || recorder.webAppURL == "" {
		os.Exit(2)
	}
	if err := recorder.writeStats(); err != nil {
		os.Exit(2)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+os.Args[1])
	if err != nil {
		os.Exit(2)
	}
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodConnect || request.Host != "api.telegram.org:443" {
				http.Error(writer, "not found", http.StatusNotFound)
				return
			}
			hijacker, ok := writer.(http.Hijacker)
			if !ok {
				http.Error(writer, "unavailable", http.StatusServiceUnavailable)
				return
			}
			connection, buffered, err := hijacker.Hijack()
			if err != nil {
				return
			}
			_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			if err := buffered.Flush(); err != nil {
				_ = connection.Close()
				return
			}
			go recorder.serveTLS(connection, certificate)
		}),
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		os.Exit(1)
	}
}

func (recorder *recorder) serveTLS(connection net.Conn, certificate tls.Certificate) {
	defer connection.Close()
	tlsConnection := tls.Server(connection, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	})
	if err := tlsConnection.Handshake(); err != nil {
		return
	}
	reader := bufio.NewReader(tlsConnection)
	writer := bufio.NewWriter(tlsConnection)
	for {
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
		request.Body.Close()
		var payload struct {
			ChatID     int64  `json:"chat_id"`
			Text       string `json:"text"`
			ReplyMarkup struct {
				InlineKeyboard [][]struct {
					Text   string `json:"text"`
					WebApp struct {
						URL string `json:"url"`
					} `json:"web_app"`
				} `json:"inline_keyboard"`
			} `json:"reply_markup"`
		}
		baseValid := err == nil && request.Method == http.MethodPost && request.URL.Path == "/bot"+recorder.token+"/sendMessage"
		decodeErr := json.Unmarshal(body, &payload)
		statusValid := baseValid && decodeErr == nil && payload.ChatID == 4242 && strings.Contains(payload.Text, "smoke-host")
		webAppValid := false
		if baseValid && decodeErr == nil && payload.ChatID == 4242 && len(payload.ReplyMarkup.InlineKeyboard) == 1 && len(payload.ReplyMarkup.InlineKeyboard[0]) == 1 {
			button := payload.ReplyMarkup.InlineKeyboard[0][0]
			webAppValid = payload.Text == "Open the tg-monitor operator app." && button.Text == "Open tg-monitor" && button.WebApp.URL == recorder.webAppURL
		}
		valid := statusValid || webAppValid
		if valid {
			recorder.mu.Lock()
			if statusValid {
				recorder.statusCount++
			}
			if webAppValid {
				recorder.webAppCount++
			}
			_ = recorder.writeStatsLocked()
			recorder.mu.Unlock()
		}
		response, _ := json.Marshal(map[string]any{
			"ok": valid, "result": valid, "description": recorder.description,
		})
		_, _ = fmt.Fprintf(writer,
			"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: keep-alive\r\n\r\n%s",
			len(response), response,
		)
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func (recorder *recorder) writeStats() error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.writeStatsLocked()
}

func (recorder *recorder) writeStatsLocked() error {
	contents := fmt.Sprintf("statusMessage=%d\nwebAppMessage=%d\n", recorder.statusCount, recorder.webAppCount)
	return os.WriteFile(recorder.statsPath, []byte(contents), 0o600)
}

func testCertificate() (tls.Certificate, []byte, error) {
	now := time.Now().UTC()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tg-monitor smoke CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
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
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "api.telegram.org"},
		DNSNames: []string{"api.telegram.org"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER}),
	)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return certificate, caPEM, err
}
GO

cat >"$signer_source" <<'GO'
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
)

func main() {
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
GO

CGO_ENABLED=0 "$GO" build -o "$server_binary" ./cmd/tg-monitor-server >/dev/null
CGO_ENABLED=0 "$GO" build -o "$fake_binary" "$fake_source" >/dev/null
CGO_ENABLED=0 "$GO" build -o "$signer_binary" "$signer_source" >/dev/null

FAKE_STATS_PATH="$proxy_stats" \
FAKE_EXPECTED_BOT_TOKEN="$bot_token" \
FAKE_EXPECTED_WEBAPP_URL="http://127.0.0.1:$server_port/app/" \
FAKE_RESPONSE_DESCRIPTION="$fake_description" \
    "$fake_binary" "$proxy_port" "$ca_certificate" >"$proxy_log" 2>&1 &
proxy_pid=$!
wait_for_port "$proxy_port"
wait_for_file "$ca_certificate"
wait_for_file "$proxy_stats"

registration="$(TG_MONITOR_DATABASE_PATH="$database" "$server_binary" server add --name smoke-host --group smoke 2>>"$server_log")"
server_id="$(printf '%s\n' "$registration" | sed -n 's/^server_id=//p')"
agent_token="$(printf '%s\n' "$registration" | sed -n 's/^agent_token=//p')"
unset registration
if [[ -z "$server_id" || -z "$agent_token" ]]; then
    exit 1
fi

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
    "$server_binary" serve >"$server_log" 2>&1 &
server_pid=$!
wait_for_port "$server_port"

curl --fail --silent --show-error --dump-header "$app_headers" --output "$app_html" \
    "http://127.0.0.1:$server_port/app/"
grep -Fq 'href="/app/app.css"' "$app_html"
grep -Fq 'src="/app/app.js"' "$app_html"
grep -iq '^Content-Security-Policy:' "$app_headers"
grep -iq '^Cache-Control: no-store' "$app_headers"
curl --fail --silent --show-error --dump-header "$css_headers" --output "$css_asset" \
    "http://127.0.0.1:$server_port/app/app.css"
grep -iq '^Content-Type: text/css; charset=utf-8' "$css_headers"
grep -iq '^Cache-Control: public, max-age=31536000, immutable' "$css_headers"
curl --fail --silent --show-error --dump-header "$js_headers" --output "$js_asset" \
    "http://127.0.0.1:$server_port/app/app.js"
grep -iq '^Content-Type: text/javascript; charset=utf-8' "$js_headers"
grep -iq '^Cache-Control: public, max-age=31536000, immutable' "$js_headers"

# Send the same JSON "update_id":9001 twice to prove record-first deduplication.
webhook_body="{\"update_id\":9001,\"message\":{\"from\":{\"id\":42},\"chat\":{\"id\":4242,\"type\":\"private\"},\"text\":\"$message_text\"}}"
for _ in 1 2; do
    status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
        --request POST "http://127.0.0.1:$server_port/telegram/webhook" \
        --header "X-Telegram-Bot-Api-Secret-Token: $webhook_secret" \
        --header 'Content-Type: application/json' \
        --data "$webhook_body")"
    if [[ "$status" != 204 ]]; then
        exit 1
    fi
done
if ! grep -Fxq 'statusMessage=1' "$proxy_stats"; then
    exit 1
fi
printf '%s\n' 'telegram_webhook=ok duplicate=ok'

app_webhook_body='{"update_id":9002,"message":{"from":{"id":42},"chat":{"id":4242,"type":"private"},"text":"/app"}}'
app_webhook_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --request POST "http://127.0.0.1:$server_port/telegram/webhook" \
    --header "X-Telegram-Bot-Api-Secret-Token: $webhook_secret" \
    --header 'Content-Type: application/json' \
    --data "$app_webhook_body")"
if [[ "$app_webhook_status" != 204 ]]; then
    exit 1
fi
if ! grep -Fxq 'statusMessage=1' "$proxy_stats" || ! grep -Fxq 'webAppMessage=1' "$proxy_stats"; then
    exit 1
fi

auth_date="$(date +%s)"
user_json='{"id":42,"first_name":"Smoke"}'
init_data="$(SIGN_BOT_TOKEN="$bot_token" SIGN_AUTH_DATE="$auth_date" SIGN_USER_JSON="$user_json" "$signer_binary")"
init_hash="$(printf '%s' "$init_data" | sed -n 's/.*hash=\([^&]*\).*/\1/p')"
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
session_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --cookie "$cookie_jar" "http://127.0.0.1:$server_port/api/v1/auth/session")"
if [[ "$session_status" != 200 ]]; then
    exit 1
fi

curl --fail --silent --show-error --cookie "$cookie_jar" --output "$overview_json" \
    "http://127.0.0.1:$server_port/api/v1/admin/overview"
if ! grep -Fq '"name":"smoke-host"' "$overview_json"; then
    exit 1
fi
now_ms="$(( $(date +%s) * 1000 ))"
from_ms="$((now_ms - 3600000))"
to_ms="$((now_ms + 1000))"
curl --fail --silent --show-error --cookie "$cookie_jar" --output "$history_json" \
    "http://127.0.0.1:$server_port/api/v1/admin/servers/$server_id/history?from_ms=$from_ms&to_ms=$to_ms"
if ! grep -Fq '"name":"smoke-host"' "$history_json"; then
    exit 1
fi
curl --fail --silent --show-error --request POST --cookie "$cookie_jar" --output "$rotate_json" \
    --header "Origin: http://127.0.0.1:$server_port" \
    --header 'Content-Type: application/json' \
    --data '{}' \
    "http://127.0.0.1:$server_port/api/v1/admin/servers/$server_id/rotate-token"
rotated_token="$(sed -n 's/.*"agent_token":"\([^"]*\)".*/\1/p' "$rotate_json")"
if [[ -z "$rotated_token" ]]; then
    exit 1
fi
rm -f "$rotate_json"
printf '%s\n' 'telegram_webapp=ok'

logout_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --request POST --cookie "$cookie_jar" --cookie-jar "$cookie_jar" \
    "http://127.0.0.1:$server_port/api/v1/auth/logout")"
if [[ "$logout_status" != 204 ]]; then
    exit 1
fi
cleared_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --cookie "$cookie_jar" "http://127.0.0.1:$server_port/api/v1/auth/session")"
if [[ "$cleared_status" != 401 ]]; then
    exit 1
fi
printf '%s\n' 'telegram_session=ok logout=ok'

kill -TERM "$server_pid"
wait "$server_pid"
server_pid=""
kill -TERM "$proxy_pid" 2>/dev/null || true
wait "$proxy_pid" 2>/dev/null || true
proxy_pid=""

canaries=(
    "$bot_token"
    "$webhook_secret"
    "$agent_token"
	"$rotated_token"
    "$init_data"
    "$init_hash"
    "$session_cookie"
    "$message_text"
    "$fake_description"
)
rm -f "$cookie_jar" "$fake_source" "$fake_binary" "$signer_source" "$signer_binary"
for canary in "${canaries[@]}"; do
    if grep -R -F -- "$canary" "$work_dir" >/dev/null 2>&1; then
        exit 1
    fi
done

printf '%s\n' 'telegram_sigterm=clean secret_log_scan=clean'
