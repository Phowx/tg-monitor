# tg-monitor

tg-monitor 是一个面向小型自托管环境的轻量服务器监控系统，由中心 Server、Linux Agent、Telegram Bot 和管理员 WebApp 组成。界面、命令回复、告警及主要运维说明均使用简体中文。

## 功能

- 采集 Linux CPU、内存、根磁盘、负载、网络、运行时间及主机信息。
- 在 WebApp 中查看实时状态、历史趋势和每分钟采样。
- 管理服务器、分组、排序、启停状态及一次性 Agent 令牌。
- 通过 Telegram 接收离线与恢复告警。
- Telegram 输入框旁显示“打开监控面板”，无需先输入 `/app`。
- SQLite WAL 持久化、耐久告警队列、进程重启后继续投递。
- 非 root systemd 服务、严格文件权限和不记录秘密的结构化日志。
- 一份交互脚本完成 Server、Agent、同机部署、升级和卸载。

Server 默认只监听 `127.0.0.1:8080`。不要把明文 HTTP 监听直接暴露到公网；远程 Agent 应通过 Caddy、nginx 等可信 HTTPS 反向代理连接。

## 一键安装与卸载

在已克隆的仓库或 Release 目录中运行：

```bash
sudo bash scripts/manage.sh
```

脚本会显示中文菜单：

1. 安装或升级 Server
2. 安装或升级 Agent
3. 同机安装或升级 Server + Agent
4. 卸载 Server
5. 卸载 Agent
6. 卸载全部组件

安装过程中会逐项说明并要求手动选择，包括二进制来源、监听地址、Server URL、Telegram、Caddy、域名和 HTTPS 端口。Token 使用隐藏输入，执行前只显示脱敏摘要。

二进制来源支持：

- GitHub Release：自动选择 amd64/arm64，下载后强制校验 `SHA256SUMS`。
- 本地文件：适合从源码构建后安装 `dist/` 中的产物。

普通卸载只移除服务、unit 和二进制，保留 `/etc/tg-monitor` 配置及 `/var/lib/tg-monitor` 数据。彻底卸载必须再次输入“彻底删除”，才会永久删除目标组件的配置、数据库和专用用户。

卸载 Server 不会删除 Agent；卸载 Agent 不会删除 Server 数据。Caddy 只删除 tg-monitor 自己管理的配置片段，不改动其他站点，也不卸载 Caddy 软件包。

## HTTPS 与端口

交互安装器可在 Debian/Ubuntu 上安装并配置 Caddy。默认 HTTPS 端口是 443；如果 443 已被其他程序占用，可选择 Telegram Webhook 支持的 8443：

```text
https://monitor.example.com:8443
```

需要在云控制台或系统防火墙中放行对应 TCP 端口。Caddy 使用 HTTP-01 申请证书时还需要公网 80 端口。脚本不会自动修改 AWS Lightsail、安全组、DNS 或域名注册商配置。

示例 Caddy 路由：

```caddyfile
monitor.example.com:8443 {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8080
}
```

## Telegram 配置

使用 BotFather 创建 Bot，并准备：

- Bot Token
- 管理员 Telegram 数字 ID
- 指向 Server 的 HTTPS 公网地址

安装器会生成 Webhook Secret，写入 root:root、`0600` 的 `/etc/tg-monitor/server.env`，启动 Server 后执行：

```bash
tg-monitor-server telegram set-webhook
tg-monitor-server telegram set-menu-button
tg-monitor-server telegram get-webhook
```

菜单按钮文字为“打开监控面板”，直接打开 `${TG_MONITOR_PUBLIC_URL}/app/`。`/app` 命令仍可作为备用入口。

可用管理员命令：

```text
/status       查看服务器状态
/app          打开监控面板
/alerts_on    开启离线告警
/alerts_off   关闭离线告警
/help         显示帮助
```

需要恢复 Telegram 默认命令菜单时：

```bash
tg-monitor-server telegram reset-menu-button
```

## 手工 Server 部署

构建后安装 amd64 产物；arm64 主机替换相应文件名：

```bash
sudo useradd --system --home-dir /var/lib/tg-monitor --shell /usr/sbin/nologin tg-monitor 2>/dev/null || true
sudo install -o root -g root -m 0755 dist/tg-monitor-server-linux-amd64 /usr/local/bin/tg-monitor-server
sudo install -d -o root -g root -m 0755 /etc/tg-monitor
sudo install -o root -g root -m 0600 deploy/systemd/server.env.example /etc/tg-monitor/server.env
sudo install -o root -g root -m 0644 deploy/systemd/tg-monitor.service /etc/systemd/system/tg-monitor.service
sudo systemctl daemon-reload
sudo systemctl enable --now tg-monitor
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/readyz
```

生产安全默认值：

```dotenv
TG_MONITOR_DATABASE_PATH=/var/lib/tg-monitor/monitor.db
TG_MONITOR_LISTEN_ADDR=127.0.0.1:8080
TG_MONITOR_CHECKPOINT_INTERVAL=15s
TG_MONITOR_TELEGRAM_ENABLED=false
```

Telegram 启用后，离线阈值、告警阈值和历史保留天数在 WebApp 中管理。

## 手工 Agent 部署

先在 WebApp 创建服务器并立即保存一次性 Agent 令牌，然后在被监控主机安装：

```bash
sudo useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin tg-monitor-agent 2>/dev/null || true
sudo install -o root -g root -m 0755 dist/tg-monitor-agent-linux-amd64 /usr/local/bin/tg-monitor-agent
sudo install -d -o root -g root -m 0755 /etc/tg-monitor
sudo install -o root -g root -m 0600 deploy/systemd/agent.env.example /etc/tg-monitor/agent.env
sudo install -o root -g root -m 0644 deploy/systemd/tg-monitor-agent.service /etc/systemd/system/tg-monitor-agent.service
sudoedit /etc/tg-monitor/agent.env
```

配置示例：

```dotenv
TG_MONITOR_AGENT_SERVER_URL=https://monitor.example.com:8443
TG_MONITOR_AGENT_TOKEN=replace-with-one-time-token
TG_MONITOR_AGENT_INTERVAL=15s
TG_MONITOR_AGENT_HTTP_TIMEOUT=10s
```

先执行一次真实上报，再启用服务：

```bash
sudo sh -c 'set -a; . /etc/tg-monitor/agent.env; set +a; exec runuser --preserve-environment -u tg-monitor-agent -- /usr/local/bin/tg-monitor-agent once'
sudo systemctl daemon-reload
sudo systemctl enable --now tg-monitor-agent
```

跨主机连接必须使用 HTTPS；只有字面 loopback 地址和 localhost 可使用 HTTP。

## 构建与测试

需要 Go 1.26 或兼容工具链：

```bash
go test ./...
go vet ./...
bash scripts/build-server.sh
bash scripts/build-agent.sh
bash scripts/tests/manage_test.sh
bash scripts/smoke-agent.sh
bash scripts/smoke-telegram.sh
bash scripts/smoke-alerts.sh
```

构建产物：

```text
dist/tg-monitor-server-linux-amd64
dist/tg-monitor-server-linux-arm64
dist/tg-monitor-agent-linux-amd64
dist/tg-monitor-agent-linux-arm64
```

推送 `v*` Git 标签会运行测试、构建以上四个静态二进制、生成 `SHA256SUMS` 并创建 GitHub Release。工作流不会覆盖已存在的同名 Release。

## 日常检查与排障

```bash
sudo systemctl status --no-pager tg-monitor tg-monitor-agent caddy
sudo journalctl -u tg-monitor --since '10 minutes ago' --no-pager
sudo journalctl -u tg-monitor-agent --since '10 minutes ago' --no-pager
curl --fail http://127.0.0.1:8080/readyz
```

Agent 日志不会记录令牌或完整上报正文。Server 日志不会记录 Bot Token、Webhook Secret、Telegram 消息正文、会话 Token 或 Cookie。

## 升级与回滚

再次运行 `sudo bash scripts/manage.sh`，选择对应安装项即可升级。脚本检测到现有配置时允许保留配置，仅替换二进制；旧二进制保存为 `.previous`，健康检查或首次 Agent 上报失败时自动恢复。

SQLite migration 是向前、幂等的。跨数据库 schema 版本回滚时，应同时恢复升级前数据库备份和匹配的旧 Server 二进制。

## 仓库结构

- `cmd/tg-monitor-server`：中心服务和管理 CLI。
- `cmd/tg-monitor-agent`：Linux 采集 Agent。
- `internal/webapp`：管理员 WebApp。
- `internal/telegramapi`、`internal/telegrambot`：Bot API 与管理员命令。
- `internal/alerting`：离线、恢复和耐久告警投递。
- `internal/storage/sqlite`：SQLite migration 与数据访问。
- `deploy`：systemd 与 Caddy 示例。
- `scripts/manage.sh`：交互式安装、升级和卸载入口。

项目使用 MIT License，详情见 `LICENSE` 与 `NOTICE`。

## Telegram 安全验证与回滚清单

在 `@BotFather` 中创建 Bot，并在 Mini App 设置中确认 `/setmenubutton` 对应的 WebApp 域名。Webhook Secret 只能使用 `[A-Za-z0-9_-]` 字符。编辑环境文件后再次固定权限：

```bash
sudo chmod 0600 /etc/tg-monitor/server.env
sudo sh -c 'set -a; . /etc/tg-monitor/server.env; set +a; exec runuser --preserve-environment -u tg-monitor -- /usr/local/bin/tg-monitor-server telegram set-webhook'
sudo sh -c 'set -a; . /etc/tg-monitor/server.env; set +a; exec runuser --preserve-environment -u tg-monitor -- /usr/local/bin/tg-monitor-server telegram set-menu-button'
sudo sh -c 'set -a; . /etc/tg-monitor/server.env; set +a; exec runuser --preserve-environment -u tg-monitor -- /usr/local/bin/tg-monitor-server telegram get-webhook'
```

随后从允许的管理员私聊中 send `/status`，并确认菜单按钮打开 `${TG_MONITOR_PUBLIC_URL}/app/`。WebApp 启动后应能访问 `/api/v1/admin/overview`；响应必须包含 `Content-Security-Policy`，静态资源不得缓存秘密。

本地 Telegram 实进程验证：

```bash
bash scripts/smoke-telegram.sh
```

成功输出顺序必须为：

```text
telegram_webhook=ok duplicate=ok
telegram_webapp=ok
telegram_session=ok logout=ok
telegram_sigterm=clean secret_log_scan=clean
```

`Exact canary to scan for` 是测试生成的精确秘密标记；任何日志命中都必须视为失败。

只回滚 Telegram 时，将 `TG_MONITOR_TELEGRAM_ENABLED=false`，必要时在禁用前执行：

```bash
tg-monitor-server telegram delete-webhook
tg-monitor-server telegram reset-menu-button
```

Schema version 2 is additive；禁用 Telegram 不要求删除数据库表，核心采集仍可运行。

## WebApp 浏览器验证

API 与静态资源 smoke：

```bash
bash scripts/smoke-webapp.sh
```

真实浏览器流程需要开发机安装 `playwright-cli` 及 Chromium。测试产物写入 `output/playwright/webapp`，包括：

```text
mobile-light.png
mobile-dark.png
desktop-admin.png
```

完整成功标记：

```text
webapp_browser=ok screenshots=3
```

该流程验证移动端明暗主题、桌面管理操作、令牌清理、控制台错误、失败网络请求和秘密扫描。Node/Playwright 仅为开发测试依赖，生产 Server 与 Agent 不需要 Node.js。
