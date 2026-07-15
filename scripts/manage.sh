#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

REPOSITORY="Phowx/tg-monitor"
ROOT_PREFIX="${TG_MONITOR_ROOT:-}"
TEST_MODE="${TG_MONITOR_TEST_MODE:-0}"
WORK_DIR=""
SELECTED_BINARY=""

cleanup() {
  if [[ -n "$WORK_DIR" && -d "$WORK_DIR" ]]; then
    rm -rf "$WORK_DIR"
  fi
}
trap cleanup EXIT

root_path() {
  printf '%s%s' "$ROOT_PREFIX" "$1"
}

say() {
  printf '%s\n' "$*"
}

warn() {
  printf '警告：%s\n' "$*" >&2
}

die() {
  printf '错误：%s\n' "$*" >&2
  return 1
}

show_help() {
  cat <<'HELP'
tg-monitor 一键管理脚本

1. 安装或升级 Server
2. 安装或升级 Agent
3. 同机安装或升级 Server + Agent
4. 卸载 Server
5. 卸载 Agent
6. 卸载全部组件
7. 配置/验证 Cloudflare DNS
0. 退出

普通卸载保留配置和数据库；彻底卸载必须二次确认。
HELP
}

prompt() {
  local message="$1" value
  if [[ "$TEST_MODE" == "1" ]]; then
    IFS= read -r -p "$message" value
  else
    IFS= read -r -p "$message" value </dev/tty
  fi
  printf '%s' "$value"
}

prompt_default() {
  local message="$1" default_value="$2" value
  value="$(prompt "$message [$default_value]：")"
  printf '%s' "${value:-$default_value}"
}

prompt_secret() {
  local message="$1" value
  if [[ "$TEST_MODE" == "1" ]]; then
    IFS= read -r -p "$message" value
  else
    IFS= read -r -s -p "$message" value </dev/tty
    printf '\n' >/dev/tty
  fi
  printf '%s' "$value"
}

confirm() {
  local answer
  answer="$(prompt "$1 [y/N]：")"
  [[ "$answer" == "y" || "$answer" == "Y" || "$answer" == "是" ]]
}

detect_arch() {
  case "${1:-$(uname -m)}" in
    x86_64|amd64) printf 'amd64' ;;
    aarch64|arm64) printf 'arm64' ;;
    *) return 1 ;;
  esac
}

require_supported_host() {
  [[ "$TEST_MODE" == "1" ]] && return 0
  [[ "$(uname -s)" == "Linux" ]] || die "仅支持 Linux。"
  [[ -d /run/systemd/system ]] || die "当前系统未使用 systemd。"
  detect_arch >/dev/null || die "仅支持 amd64 和 arm64。"
  if [[ "$EUID" -ne 0 ]]; then
    [[ -t 0 ]] || die "请使用 sudo 运行此脚本。"
    exec sudo -- bash "$0" "$@"
  fi
}

run_systemctl() {
  [[ "$TEST_MODE" == "1" ]] && return 0
  systemctl "$@"
}

ensure_user() {
  local name="$1" home="$2"
  [[ "$TEST_MODE" == "1" ]] && return 0
  if ! id "$name" >/dev/null 2>&1; then
    useradd --system --home-dir "$home" --shell /usr/sbin/nologin "$name"
  fi
}

remove_user() {
  local name="$1"
  [[ "$TEST_MODE" == "1" ]] && return 0
  if id "$name" >/dev/null 2>&1; then
    userdel "$name"
  fi
}

install_from_stdin() {
  local destination="$1" mode="$2" temporary
  mkdir -p "$(dirname "$destination")"
  temporary="$(mktemp)"
  cat >"$temporary"
  if [[ "$TEST_MODE" == "1" ]]; then
    install -m "$mode" "$temporary" "$destination"
  else
    install -o root -g root -m "$mode" "$temporary" "$destination"
  fi
  rm -f "$temporary"
}

write_assignment() {
  printf '%s=%q\n' "$1" "$2"
}

read_env_value() {
  local env_file="$1" name="$2"
  (
    # shellcheck disable=SC1090
    source "$env_file"
    printf '%s' "${!name:-}"
  )
}

write_server_env_file() {
  local destination="$1" database="$2" listen="$3" telegram_enabled="$4"
  local public_url="$5" bot_token="$6" webhook_secret="$7" admin_ids="$8"
  local cloudflare_enabled="${9:-false}" cloudflare_token="${10:-}" cloudflare_zones="${11:-{}}" cloudflare_timeout="${12:-10s}"
  {
    write_assignment TG_MONITOR_DATABASE_PATH "$database"
    write_assignment TG_MONITOR_LISTEN_ADDR "$listen"
    write_assignment TG_MONITOR_CHECKPOINT_INTERVAL "15s"
    write_assignment TG_MONITOR_TELEGRAM_ENABLED "$telegram_enabled"
    if [[ "$telegram_enabled" == "true" ]]; then
      write_assignment TG_MONITOR_PUBLIC_URL "$public_url"
      write_assignment TG_MONITOR_BOT_TOKEN "$bot_token"
      write_assignment TG_MONITOR_WEBHOOK_SECRET "$webhook_secret"
      write_assignment TG_MONITOR_ADMIN_TELEGRAM_IDS "$admin_ids"
      write_assignment TG_MONITOR_SESSION_TTL "12h"
      write_assignment TG_MONITOR_INIT_DATA_MAX_AGE "5m"
      write_assignment TG_MONITOR_TELEGRAM_HTTP_TIMEOUT "10s"
    fi
    write_assignment TG_MONITOR_CLOUDFLARE_ENABLED "$cloudflare_enabled"
    write_assignment TG_MONITOR_CLOUDFLARE_API_TOKEN "$cloudflare_token"
    write_assignment TG_MONITOR_CLOUDFLARE_ZONES "$cloudflare_zones"
    write_assignment TG_MONITOR_CLOUDFLARE_HTTP_TIMEOUT "$cloudflare_timeout"
  } | install_from_stdin "$destination" 0600
}

write_cloudflare_env_file() {
  local destination="$1" enabled="$2" token="$3" zones="$4" timeout="$5"
  {
    awk '
      !/^TG_MONITOR_CLOUDFLARE_ENABLED=/ &&
      !/^TG_MONITOR_CLOUDFLARE_API_TOKEN=/ &&
      !/^TG_MONITOR_CLOUDFLARE_ZONES=/ &&
      !/^TG_MONITOR_CLOUDFLARE_HTTP_TIMEOUT=/
    ' "$destination"
    write_assignment TG_MONITOR_CLOUDFLARE_ENABLED "$enabled"
    write_assignment TG_MONITOR_CLOUDFLARE_API_TOKEN "$token"
    write_assignment TG_MONITOR_CLOUDFLARE_ZONES "$zones"
    write_assignment TG_MONITOR_CLOUDFLARE_HTTP_TIMEOUT "$timeout"
  } | install_from_stdin "$destination" 0600
}

validate_cloudflare_zone_name() {
  local value="$1" label
  value="${value%.}"
  [[ -n "$value" && ${#value} -le 253 && "$value" != *".."* ]] || return 1
  IFS='.' read -r -a labels <<<"$value"
  for label in "${labels[@]}"; do
    [[ "$label" =~ ^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$ ]] || return 1
  done
}

validate_cloudflare_zone_id() {
  [[ "$1" =~ ^[0-9A-Fa-f]{32}$ ]]
}

collect_cloudflare_zones() {
  local json="{" separator="" name id seen_names="|" seen_ids="|" count=0
  while true; do
    name="$(prompt "Zone 域名（留空结束）：")"
    [[ -n "$name" ]] || break
    name="${name%.}"
    name="${name,,}"
    if ! validate_cloudflare_zone_name "$name"; then
      warn "Zone 域名格式无效，请重新输入。"
      continue
    fi
    if [[ "$seen_names" == *"|$name|"* ]]; then
      warn "Zone 域名重复，请重新输入。"
      continue
    fi
    id="$(prompt "Zone ID（32 位十六进制）：")"
    if ! validate_cloudflare_zone_id "$id"; then
      warn "Zone ID 格式无效，请重新输入。"
      continue
    fi
    id="${id,,}"
    if [[ "$seen_ids" == *"|$id|"* ]]; then
      warn "Zone ID 重复，请重新输入。"
      continue
    fi
    json+="${separator}\"${name}\":\"${id}\""
    separator=","
    seen_names+="$name|"
    seen_ids+="$id|"
    ((count += 1))
    confirm "是否继续添加 Zone" || break
  done
  ((count > 0)) || die "至少需要配置一个 Zone。"
  printf '%s}' "$json"
}

render_cloudflare_summary() {
  local enabled="$1" token="$2" zones="$3"
  printf 'Cloudflare DNS：%s\n' "$enabled"
  if [[ "$enabled" == "true" ]]; then
    printf 'API Token：%s\nZone 白名单：%s\n' "$([[ -n "$token" ]] && printf '已设置' || printf '未设置')" "$([[ "$zones" != "{}" ]] && printf '已设置' || printf '未设置')"
  fi
}

write_agent_env_file() {
  local destination="$1" server_url="$2" token="$3" interval="$4" timeout="$5"
  {
    write_assignment TG_MONITOR_AGENT_SERVER_URL "$server_url"
    write_assignment TG_MONITOR_AGENT_TOKEN "$token"
    write_assignment TG_MONITOR_AGENT_INTERVAL "$interval"
    write_assignment TG_MONITOR_AGENT_HTTP_TIMEOUT "$timeout"
  } | install_from_stdin "$destination" 0600
}

write_server_unit() {
  install_from_stdin "$(root_path /etc/systemd/system/tg-monitor.service)" 0644 <<'UNIT'
[Unit]
Description=tg-monitor 中心服务
Documentation=https://github.com/Phowx/tg-monitor
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=tg-monitor
Group=tg-monitor
EnvironmentFile=/etc/tg-monitor/server.env
StateDirectory=tg-monitor
StateDirectoryMode=0750
ExecStart=/usr/local/bin/tg-monitor-server serve
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
CapabilityBoundingSet=
AmbientCapabilities=
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
UNIT
}

write_agent_unit() {
  install_from_stdin "$(root_path /etc/systemd/system/tg-monitor-agent.service)" 0644 <<'UNIT'
[Unit]
Description=tg-monitor Linux Agent
Documentation=https://github.com/Phowx/tg-monitor
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=tg-monitor-agent
Group=tg-monitor-agent
EnvironmentFile=/etc/tg-monitor/agent.env
ExecStart=/usr/local/bin/tg-monitor-agent run
Restart=on-failure
RestartSec=30s
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectProc=invisible
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
UNIT
}

render_server_summary() {
  local listen="$1" telegram_enabled="$2" public_url="$3" bot_token="$4" webhook_secret="$5"
  printf '监听地址：%s\nTelegram：%s\n' "$listen" "$telegram_enabled"
  if [[ "$telegram_enabled" == "true" ]]; then
    printf '公网地址：%s\nBot Token：%s\nWebhook Secret：%s\n' \
      "$public_url" "$([[ -n "$bot_token" ]] && printf '已设置' || printf '未设置')" \
      "$([[ -n "$webhook_secret" ]] && printf '已设置' || printf '未设置')"
  fi
}

ensure_work_dir() {
  if [[ -z "$WORK_DIR" ]]; then
    WORK_DIR="$(mktemp -d)"
  fi
}

resolve_release_version() {
  local requested="$1" effective version
  if [[ "$requested" != "latest" ]]; then
    [[ "$requested" =~ ^v[0-9][0-9A-Za-z._-]*$ ]] || die "版本号必须以 v 开头。"
    printf '%s' "$requested"
    return 0
  fi
  effective="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${REPOSITORY}/releases/latest")"
  [[ "$effective" == https://github.com/${REPOSITORY}/releases/tag/v* ]] || die "无法解析最新稳定版本。"
  version="${effective##*/}"
  [[ "$version" =~ ^v[0-9][0-9A-Za-z._-]*$ ]] || die "Release 版本无效。"
  printf '%s' "$version"
}

download_binary() {
  local component="$1" requested_version="$2" arch version asset base checksum_line effective
  arch="$(detect_arch)" || die "不支持当前 CPU 架构。"
  version="$(resolve_release_version "$requested_version")" || return 1
  asset="tg-monitor-${component}-linux-${arch}"
  base="https://github.com/${REPOSITORY}/releases/download/${version}"
  ensure_work_dir
  effective="$(curl --proto '=https' --tlsv1.2 -fsSL -w '%{url_effective}' -o "$WORK_DIR/$asset" "$base/$asset")"
  [[ "$effective" == https://* ]] || die "二进制下载未保持 HTTPS。"
  effective="$(curl --proto '=https' --tlsv1.2 -fsSL -w '%{url_effective}' -o "$WORK_DIR/SHA256SUMS" "$base/SHA256SUMS")"
  [[ "$effective" == https://* ]] || die "校验清单下载未保持 HTTPS。"
  checksum_line="$(awk -v name="$asset" '$2 == name || $2 == "*" name {print; count++} END {if (count != 1) exit 1}' "$WORK_DIR/SHA256SUMS")" || die "校验清单缺少唯一资产记录。"
  (cd "$WORK_DIR" && printf '%s\n' "$checksum_line" | sha256sum -c - >/dev/null) || die "SHA-256 校验失败。"
  chmod 0755 "$WORK_DIR/$asset"
  SELECTED_BINARY="$WORK_DIR/$asset"
}

choose_binary() {
  local component="$1" source version path
  say "选择 ${component} 二进制来源：1) GitHub Release  2) 本地文件"
  source="$(prompt_default "请输入选项" "1")"
  case "$source" in
    1)
      command -v curl >/dev/null || die "下载模式需要 curl。"
      command -v sha256sum >/dev/null || die "下载模式需要 sha256sum。"
      version="$(prompt_default "版本（latest 或 v*）" "latest")"
      download_binary "$component" "$version" || return 1
      ;;
    2)
      path="$(prompt "请输入本地二进制绝对路径：")"
      [[ -f "$path" && -r "$path" ]] || die "本地二进制不存在或不可读。"
      SELECTED_BINARY="$path"
      ;;
    *) die "无效的来源选项。" ;;
  esac
}

install_binary() {
  local source="$1" destination="$2"
  mkdir -p "$(dirname "$destination")"
  if [[ -f "$destination" ]]; then
    cp -p "$destination" "${destination}.previous"
  fi
  if [[ "$TEST_MODE" == "1" ]]; then
    install -m 0755 "$source" "$destination"
  else
    install -o root -g root -m 0755 "$source" "$destination"
  fi
}

rollback_binary() {
  local destination="$1"
  if [[ -f "${destination}.previous" ]]; then
    mv -f "${destination}.previous" "$destination"
  fi
}

validate_listen() {
  local value="$1" port
  [[ "$value" =~ ^(127\.0\.0\.1|localhost|\[::1\]):([0-9]{1,5})$ ]] || return 1
  port="${BASH_REMATCH[2]}"
  ((port >= 1 && port <= 65535))
}

validate_server_url() {
  local value="$1"
  [[ "$value" =~ ^https://[^[:space:]/]+(:[0-9]{1,5})?$ || "$value" =~ ^http://(127\.0\.0\.1|localhost|\[::1\])(:[0-9]{1,5})?$ ]]
}

webhook_port_supported() {
  case "$1" in 80|88|443|8443) return 0 ;; *) return 1 ;; esac
}

run_server_command() {
  local env_file
  env_file="$(root_path /etc/tg-monitor/server.env)"
  [[ "$TEST_MODE" == "1" ]] && return 0
  (
    set -a
    # shellcheck disable=SC1090
    source "$env_file"
    set +a
    exec runuser --preserve-environment -u tg-monitor -- /usr/local/bin/tg-monitor-server "$@"
  )
}

verify_server_local() {
  local listen="$1"
  [[ "$TEST_MODE" == "1" ]] && return 0
  curl --fail --silent --show-error "http://${listen}/healthz" >/dev/null
  curl --fail --silent --show-error "http://${listen}/readyz" >/dev/null
}

verify_server_public() {
  local public_url="$1"
  [[ "$TEST_MODE" == "1" ]] && return 0
  curl --fail --silent --show-error "${public_url%/}/readyz" >/dev/null
}

configure_telegram() {
  run_server_command telegram set-webhook
  run_server_command telegram set-menu-button
  run_server_command telegram get-webhook >/dev/null
}

configure_cloudflare_interactive() {
  local env_file current_enabled current_token current_zones current_timeout action enabled token zones timeout listen backup
  env_file="$(root_path /etc/tg-monitor/server.env)"
  [[ -f "$env_file" ]] || die "未找到 Server 配置，请先安装 Server。"
  [[ -x "$(root_path /usr/local/bin/tg-monitor-server)" ]] || die "未找到 Server 程序，请先安装 Server。"
  current_enabled="$(read_env_value "$env_file" TG_MONITOR_CLOUDFLARE_ENABLED)"
  current_token="$(read_env_value "$env_file" TG_MONITOR_CLOUDFLARE_API_TOKEN)"
  current_zones="$(read_env_value "$env_file" TG_MONITOR_CLOUDFLARE_ZONES)"
  current_timeout="$(read_env_value "$env_file" TG_MONITOR_CLOUDFLARE_HTTP_TIMEOUT)"
  listen="$(read_env_value "$env_file" TG_MONITOR_LISTEN_ADDR)"
  current_enabled="${current_enabled:-false}"
  current_zones="${current_zones:-{}}"
  current_timeout="${current_timeout:-10s}"
  listen="${listen:-127.0.0.1:8080}"

  if [[ "$current_enabled" == "true" ]]; then
    say "1) 更新并验证配置  2) 停用 Cloudflare DNS  0) 取消"
    action="$(prompt_default "请选择" "1")"
  else
    say "1) 启用并配置 Cloudflare DNS  0) 取消"
    action="$(prompt_default "请选择" "1")"
  fi
  case "$action" in
    0) say "已取消。"; return 0 ;;
    2)
      [[ "$current_enabled" == "true" ]] || die "Cloudflare DNS 当前未启用。"
      enabled="false"
      token=""
      zones="{}"
      timeout="$current_timeout"
      ;;
    1)
      enabled="true"
      token="$(prompt_secret "Cloudflare API Token（留空保留现有值）：")"
      token="${token:-$current_token}"
      [[ -n "$token" ]] || die "Cloudflare API Token 不能为空。"
      if [[ "$current_enabled" == "true" && "$current_zones" != "{}" ]] && confirm "是否保留现有 Zone 白名单"; then
        zones="$current_zones"
      else
        say "依次输入允许管理的 Zone；Token 权限也应限制到这些 Zone。"
        zones="$(collect_cloudflare_zones)" || return 1
      fi
      timeout="$(prompt_default "Cloudflare HTTP 超时" "$current_timeout")"
      [[ "$timeout" =~ ^[1-9][0-9]*(ms|s|m)$ ]] || die "HTTP 超时格式无效，例如 10s。"
      ;;
    *) die "无效的选项。" ;;
  esac

  say "$(render_cloudflare_summary "$enabled" "$token" "$zones")"
  confirm "确认写入并验证 Cloudflare DNS 配置" || { say "已取消。"; return 0; }
  backup="$(mktemp)"
  cp -p "$env_file" "$backup"
  write_cloudflare_env_file "$env_file" "$enabled" "$token" "$zones" "$timeout"
  if [[ "$enabled" == "true" ]] && ! run_server_command dns verify; then
    install_from_stdin "$env_file" 0600 <"$backup"
    rm -f "$backup"
    die "Cloudflare 验证失败，已恢复旧配置。"
    return 1
  fi
  if ! run_systemctl restart tg-monitor || ! verify_server_local "$listen"; then
    install_from_stdin "$env_file" 0600 <"$backup"
    run_systemctl restart tg-monitor || true
    rm -f "$backup"
    die "Server 重启或健康检查失败，已恢复旧配置。"
    return 1
  fi
  rm -f "$backup"
  say "Cloudflare DNS 配置已更新并生效。"
}

ensure_caddy() {
  command -v caddy >/dev/null && return 0
  [[ "$TEST_MODE" == "1" ]] && return 0
  # shellcheck disable=SC1091
  source /etc/os-release
  [[ "${ID:-}" == "debian" || "${ID:-}" == "ubuntu" ]] || die "当前发行版需手工安装 Caddy。"
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y caddy
}

configure_caddy() {
  local domain="$1" https_port="$2" upstream_port="$3" main snippet backup temporary
  ensure_caddy || return 1
  main="$(root_path /etc/caddy/Caddyfile)"
  snippet="$(root_path /etc/caddy/tg-monitor.caddy)"
  mkdir -p "$(dirname "$main")"
  [[ -f "$main" ]] || : >"$main"
  if [[ ! -f "$snippet" && "$TEST_MODE" != "1" ]] && ss -ltnH | awk '{print $4}' | grep -Eq "(^|:)$https_port$"; then
    die "TCP ${https_port} 已被占用，请重新选择。"
  fi
  backup="${main}.tg-monitor.backup"
  cp -p "$main" "$backup"
  install_from_stdin "$snippet" 0644 <<CADDY
${domain}:${https_port} {
    encode zstd gzip
    reverse_proxy 127.0.0.1:${upstream_port}
}
CADDY
  if ! grep -q '^# BEGIN tg-monitor managed import$' "$main"; then
    temporary="$(mktemp)"
    {
      cat "$main"
      printf '\n# BEGIN tg-monitor managed import\nimport /etc/caddy/tg-monitor.caddy\n# END tg-monitor managed import\n'
    } >"$temporary"
    install_from_stdin "$main" 0644 <"$temporary"
    rm -f "$temporary"
  fi
  if [[ "$TEST_MODE" != "1" ]]; then
    if ! caddy validate --config /etc/caddy/Caddyfile; then
      cp -p "$backup" "$main"
      rm -f "$snippet"
      die "Caddy 配置校验失败，已恢复。"
    fi
    run_systemctl reload caddy
  fi
  rm -f "$backup"
}

remove_caddy_config() {
  local main snippet temporary backup
  main="$(root_path /etc/caddy/Caddyfile)"
  snippet="$(root_path /etc/caddy/tg-monitor.caddy)"
  rm -f "$snippet"
  [[ -f "$main" ]] || return 0
  backup="${main}.tg-monitor.backup"
  cp -p "$main" "$backup"
  temporary="$(mktemp)"
  awk '
    /^# BEGIN tg-monitor managed import$/ {skip=1; next}
    /^# END tg-monitor managed import$/ {skip=0; next}
    !skip {print}
  ' "$main" >"$temporary"
  install_from_stdin "$main" 0644 <"$temporary"
  rm -f "$temporary"
  if [[ "$TEST_MODE" != "1" ]] && command -v caddy >/dev/null; then
    if ! caddy validate --config /etc/caddy/Caddyfile; then
      cp -p "$backup" "$main"
      die "移除 Caddy 配置后校验失败，已恢复。"
    fi
    run_systemctl reload caddy
  fi
  rm -f "$backup"
}

install_server_interactive() {
  local env_file listen database telegram_enabled="false" public_url="" bot_token="" webhook_secret="" admin_ids=""
  local cloudflare_enabled="false" cloudflare_token="" cloudflare_zones="{}" cloudflare_timeout="10s"
  local configure_proxy="false" domain="" https_port="443" upstream_port preserve="false"
  env_file="$(root_path /etc/tg-monitor/server.env)"
  choose_binary server || return 1
  if [[ -f "$env_file" ]] && confirm "检测到现有 Server 配置，是否保留并仅升级程序"; then
    preserve="true"
    listen="127.0.0.1:8080"
  else
    listen="$(prompt_default "本地监听地址" "127.0.0.1:8080")"
    validate_listen "$listen" || die "监听地址必须是 loopback:port。"
    database="$(prompt_default "数据库路径" "/var/lib/tg-monitor/monitor.db")"
    if confirm "是否启用 Telegram 管理和告警"; then
      telegram_enabled="true"
      public_url="$(prompt "HTTPS 公网地址（含非 443 端口）：")"
      validate_server_url "$public_url" && [[ "$public_url" == https://* ]] || die "Telegram 公网地址必须是 HTTPS origin。"
      bot_token="$(prompt_secret "Bot Token：")"
      [[ -n "$bot_token" ]] || die "Bot Token 不能为空。"
      admin_ids="$(prompt "管理员 Telegram ID，多个用逗号分隔：")"
      [[ "$admin_ids" =~ ^[0-9]+(,[0-9]+)*$ ]] || die "管理员 ID 格式无效。"
      webhook_secret="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
      if confirm "是否启用 Cloudflare DNS 管理"; then
        cloudflare_enabled="true"
        cloudflare_token="$(prompt_secret "Cloudflare API Token：")"
        [[ -n "$cloudflare_token" ]] || die "Cloudflare API Token 不能为空。"
        say "依次输入允许管理的 Zone；Token 权限也应限制到这些 Zone。"
        cloudflare_zones="$(collect_cloudflare_zones)" || return 1
      fi
    fi
    say "$(render_server_summary "$listen" "$telegram_enabled" "$public_url" "$bot_token" "$webhook_secret")"
    say "$(render_cloudflare_summary "$cloudflare_enabled" "$cloudflare_token" "$cloudflare_zones")"
    confirm "确认安装 Server" || { say "已取消。"; return 0; }
  fi

  ensure_user tg-monitor /var/lib/tg-monitor
  install_binary "$SELECTED_BINARY" "$(root_path /usr/local/bin/tg-monitor-server)"
  write_server_unit
  if [[ "$preserve" != "true" ]]; then
    write_server_env_file "$env_file" "$database" "$listen" "$telegram_enabled" "$public_url" "$bot_token" "$webhook_secret" "$admin_ids" "$cloudflare_enabled" "$cloudflare_token" "$cloudflare_zones" "$cloudflare_timeout"
  fi
  run_systemctl daemon-reload
  run_systemctl enable --now tg-monitor
  if ! verify_server_local "$listen"; then
    rollback_binary "$(root_path /usr/local/bin/tg-monitor-server)"
    run_systemctl restart tg-monitor || true
    die "Server 健康检查失败，已恢复旧程序。"
  fi

  if [[ "$preserve" != "true" ]] && confirm "是否自动配置 Caddy HTTPS"; then
    configure_proxy="true"
    domain="$(prompt "域名：")"
    https_port="$(prompt_default "HTTPS 端口" "443")"
    [[ "$https_port" =~ ^[0-9]+$ ]] && ((https_port >= 1 && https_port <= 65535)) || die "HTTPS 端口无效。"
    upstream_port="${listen##*:}"
    configure_caddy "$domain" "$https_port" "$upstream_port" || return 1
  fi
  if [[ "$telegram_enabled" == "true" ]]; then
    https_port="443"
    [[ "$public_url" =~ :([0-9]+)$ ]] && https_port="${BASH_REMATCH[1]}"
    webhook_port_supported "$https_port" || die "Telegram Webhook 不支持端口 ${https_port}。"
    verify_server_public "$public_url" || die "公网 HTTPS 未就绪；核心服务已安装，请完成反向代理后重试。"
    configure_telegram || die "Telegram Webhook 或菜单按钮配置失败。"
  elif [[ "$configure_proxy" == "true" ]]; then
    verify_server_public "https://${domain}${https_port:+:${https_port}}" || warn "公网 HTTPS 检查失败，请检查 DNS 和防火墙。"
  fi
  if [[ "$cloudflare_enabled" == "true" ]] && ! run_server_command dns verify; then
    write_cloudflare_env_file "$env_file" "false" "" "{}" "$cloudflare_timeout"
    run_systemctl restart tg-monitor || true
    die "Cloudflare 验证失败，已自动停用 DNS 管理；可通过主菜单重新配置。"
  fi
  say "Server 安装或升级完成。"
}

install_agent_values() {
  local binary="$1" server_url="$2" token="$3" interval="$4" timeout="$5"
  ensure_user tg-monitor-agent /nonexistent
  install_binary "$binary" "$(root_path /usr/local/bin/tg-monitor-agent)"
  write_agent_unit
  write_agent_env_file "$(root_path /etc/tg-monitor/agent.env)" "$server_url" "$token" "$interval" "$timeout"
  if [[ "$TEST_MODE" != "1" ]]; then
    if ! (
      set -a
      # shellcheck disable=SC1090
      source /etc/tg-monitor/agent.env
      set +a
      exec runuser --preserve-environment -u tg-monitor-agent -- /usr/local/bin/tg-monitor-agent once
    ); then
      rollback_binary /usr/local/bin/tg-monitor-agent
      die "Agent 首次上报失败，未启用服务。"
    fi
  fi
  run_systemctl daemon-reload
  run_systemctl enable --now tg-monitor-agent
}

install_agent_interactive() {
  local preset_url="${1:-}" preset_token="${2:-}" env_file server_url token interval timeout
  env_file="$(root_path /etc/tg-monitor/agent.env)"
  choose_binary agent || return 1
  if [[ -z "$preset_url" && -f "$env_file" ]] && confirm "检测到现有 Agent 配置，是否保留并仅升级程序"; then
    ensure_user tg-monitor-agent /nonexistent
    install_binary "$SELECTED_BINARY" "$(root_path /usr/local/bin/tg-monitor-agent)"
    write_agent_unit
    run_systemctl daemon-reload
    run_systemctl enable --now tg-monitor-agent
    say "Agent 升级完成。"
    return 0
  fi
  server_url="${preset_url:-$(prompt "Server URL：")}"
  validate_server_url "$server_url" || die "Server URL 必须为 HTTPS；仅 loopback 可用 HTTP。"
  token="${preset_token:-$(prompt_secret "一次性 Agent Token：")}"
  [[ -n "$token" ]] || die "Agent Token 不能为空。"
  interval="$(prompt_default "采样间隔" "15s")"
  timeout="$(prompt_default "HTTP 超时" "10s")"
  say "Server URL：${server_url}\nAgent Token：已设置\n采样间隔：${interval}"
  confirm "确认安装 Agent" || { say "已取消。"; return 0; }
  install_agent_values "$SELECTED_BINARY" "$server_url" "$token" "$interval" "$timeout"
  say "Agent 安装或升级完成。"
}

install_all_same_host() {
  local output token_count token listen node_name group
  install_server_interactive || return 1
  choose_binary agent || return 1
  node_name="$(prompt_default "本机监控名称" "$(hostname)")"
  group="$(prompt_default "分组" "central")"
  if [[ "$TEST_MODE" == "1" ]]; then
    output=$'server_id=1\nagent_token=TEST-TOKEN'
  else
    output="$(run_server_command server add --name "$node_name" --group "$group")" || die "创建监控节点失败。"
  fi
  token_count="$(printf '%s\n' "$output" | grep -c '^agent_token=' || true)"
  [[ "$token_count" == "1" ]] || die "无法安全提取一次性 Agent Token。"
  token="$(printf '%s\n' "$output" | sed -n 's/^agent_token=//p')"
  [[ -n "$token" && "$token" != *$'\n'* ]] || die "一次性 Agent Token 响应无效。"
  listen="127.0.0.1:8080"
  install_agent_values "$SELECTED_BINARY" "http://${listen}" "$token" "15s" "10s"
  say "Server 与 Agent 同机安装完成。"
}

uninstall_server_files() {
  local mode="$1"
  run_systemctl disable --now tg-monitor >/dev/null 2>&1 || true
  rm -f "$(root_path /etc/systemd/system/tg-monitor.service)" "$(root_path /usr/local/bin/tg-monitor-server)" "$(root_path /usr/local/bin/tg-monitor-server.previous)"
  remove_caddy_config || true
  run_systemctl daemon-reload
  if [[ "$mode" == "purge" ]]; then
    rm -f "$(root_path /etc/tg-monitor/server.env)"
    rm -rf "$(root_path /var/lib/tg-monitor)"
    remove_user tg-monitor
  fi
}

uninstall_agent_files() {
  local mode="$1"
  run_systemctl disable --now tg-monitor-agent >/dev/null 2>&1 || true
  rm -f "$(root_path /etc/systemd/system/tg-monitor-agent.service)" "$(root_path /usr/local/bin/tg-monitor-agent)" "$(root_path /usr/local/bin/tg-monitor-agent.previous)"
  run_systemctl daemon-reload
  if [[ "$mode" == "purge" ]]; then
    rm -f "$(root_path /etc/tg-monitor/agent.env)"
    remove_user tg-monitor-agent
  fi
  rmdir "$(root_path /etc/tg-monitor)" 2>/dev/null || true
}

choose_uninstall_mode() {
  local choice confirmation
  say "1) 普通卸载（保留配置和数据库）  2) 彻底卸载"
  choice="$(prompt_default "请选择" "1")"
  if [[ "$choice" == "2" ]]; then
    say "彻底卸载会永久删除目标组件的配置和数据，无法恢复。"
    confirmation="$(prompt "请输入“彻底删除”确认：")"
    [[ "$confirmation" == "彻底删除" ]] || die "确认词不匹配，已取消。"
    printf 'purge'
  else
    printf 'keep'
  fi
}

uninstall_server_interactive() {
  local mode
  if [[ -f "$(root_path /etc/tg-monitor/server.env)" ]] && confirm "卸载前是否删除 Telegram Webhook 并恢复默认菜单"; then
    run_server_command telegram delete-webhook || warn "删除 Webhook 失败。"
    run_server_command telegram reset-menu-button || warn "恢复菜单按钮失败。"
  fi
  mode="$(choose_uninstall_mode)" || return 1
  uninstall_server_files "$mode"
  say "Server 已卸载。"
}

uninstall_agent_interactive() {
  local mode
  mode="$(choose_uninstall_mode)" || return 1
  uninstall_agent_files "$mode"
  say "Agent 已卸载。"
}

uninstall_all_interactive() {
  local mode
  mode="$(choose_uninstall_mode)" || return 1
  uninstall_agent_files "$mode"
  uninstall_server_files "$mode"
  rmdir "$(root_path /etc/tg-monitor)" 2>/dev/null || true
  say "全部组件已卸载。"
}

main_menu() {
  local choice
  while true; do
    show_help
    choice="$(prompt "请选择操作：")"
    case "$choice" in
      1) install_server_interactive ;;
      2) install_agent_interactive ;;
      3) install_all_same_host ;;
      4) uninstall_server_interactive ;;
      5) uninstall_agent_interactive ;;
      6) uninstall_all_interactive ;;
      7) configure_cloudflare_interactive ;;
      0) say "已退出。"; return 0 ;;
      *) warn "无效选项，请重新输入。" ;;
    esac
  done
}

main() {
  case "${1:-}" in
    -h|--help) show_help; return 0 ;;
    "") ;;
    *) die "不支持命令行操作；请直接运行交互向导。" ;;
  esac
  require_supported_host "$@"
  main_menu
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
