# 简体中文完善与一键安装器 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 完成 tg-monitor 的简体中文用户体验、Telegram 输入框 WebApp 菜单按钮，以及可安全安装、升级、卸载 Server 与 Agent 的单一交互式脚本。

**Architecture:** 保持 Go 服务的 API、环境变量和日志兼容性，只在现有 Telegram、告警和 WebApp 展示层替换用户可见文案。Telegram 菜单按钮通过最小 Bot API 客户端和显式 Server CLI 管理；系统部署由一个函数化 Bash 向导完成，并通过临时根目录和模拟系统命令进行无特权测试。GitHub 标签工作流构建四个静态 Linux 二进制及校验清单，为安装器提供可信 Release 资产。

**Tech Stack:** Go 1.26、标准库 `net/http`、SQLite、原生 HTML/CSS/JavaScript、Bash、systemd、Caddy、GitHub Actions。

## Global Constraints

- 用户可见文案使用简体中文；API 字段、环境变量、CLI 命令名、结构化日志和内部安全错误保持英文。
- 安装目标仅为采用 systemd 的 Linux amd64/arm64；不修改云防火墙、DNS 或域名注册商。
- Secret 必须通过隐藏输入和 root:root `0600` 环境文件传递，不得出现在命令参数、日志或脱敏摘要中。
- 普通卸载保留 `/etc/tg-monitor` 和 `/var/lib/tg-monitor`；彻底清理必须二次确认。
- Caddy 只管理 tg-monitor 自己的配置片段和带标记 import，不删除软件包或其他站点。
- 所有行为采用 red-green TDD；每个任务在实现前必须观察到对应测试因缺少行为而失败。

---

## File Map

- `internal/telegrambot/commands.go`: Telegram 管理命令、状态和中文时长展示。
- `internal/telegrambot/commands_test.go`: 管理命令中文契约和 UTF-8 长度边界。
- `internal/alerting/render.go`: 离线/恢复告警中文渲染。
- `internal/alerting/render_test.go`: 告警文案、时间和输入清洗契约。
- `internal/webapp/assets/index.html`: 静态 WebApp 中文标签和可访问性文案。
- `internal/webapp/assets/app.js`: 动态 WebApp 中文状态、校验和反馈。
- `internal/webapp/assets_test.go`: 静态资源中文覆盖断言。
- `scripts/webapp-browser-test.js`: 真实浏览器中文交互断言。
- `internal/telegramapi/client.go`: `setChatMenuButton` 和恢复默认菜单的 Bot API 请求。
- `internal/telegramapi/menu_button_test.go`: 菜单按钮请求形状、URL 校验和安全错误测试。
- `internal/servercmd/run.go`: `set-menu-button` 与 `reset-menu-button` CLI 路由。
- `internal/servercmd/telegram_test.go`: 新 CLI 命令的依赖、输出和失败测试。
- `scripts/manage.sh`: 单一交互式安装、升级和卸载入口。
- `scripts/tests/manage_test.sh`: 临时根目录与模拟 PATH 的无特权脚本测试。
- `internal/deploytest/installer_assets_test.go`: 安装器、Release 和中文文档的仓库级静态契约。
- `.github/workflows/release.yml`: `v*` 标签的测试、交叉构建、摘要和 Release 发布。
- `README.md`: 简体中文安装、使用、升级、卸载和排障说明。

---

### Task 1: Telegram 命令与告警中文化

**Files:**
- Modify: `internal/telegrambot/commands_test.go`
- Modify: `internal/telegrambot/commands.go`
- Modify: `internal/alerting/render_test.go`
- Modify: `internal/alerting/render.go`

**Interfaces:**
- Consumes: 现有 `Commander.Reply`、`Render` 和纯文本 Telegram 输出。
- Produces: 中文 `Reply.Text`、中文状态/时长以及中文离线与恢复消息；函数签名不变。

- [ ] **Step 1: 将命令测试期望改为中文**

在 `commands_test.go` 中将帮助、提示、按钮、告警开关和状态期望改为以下精确契约：

```go
const (
    wantHelp = "tg-monitor 管理员命令：\n/status - 查看服务器状态\n/app - 打开监控面板\n/alerts_on - 开启离线告警\n/alerts_off - 关闭离线告警\n/help - 显示帮助"
    wantPrompt = "请发送 /help 查看可用命令。"
)

want := Reply{
    Text:       "点击下方按钮打开 tg-monitor 监控面板。",
    WebAppURL:  "https://monitor.example.com/app/",
    ButtonText: "打开监控面板",
}
```

状态行使用 `【在线】`、`【离线】`、`【停用】`，“内存”和“前上报”；无数据行为 `【离线】no-data — 暂无数据`；省略行为 `…另有 N 台服务器未显示`。告警开关回复分别为“离线告警已开启。”和“离线告警已关闭。”。

- [ ] **Step 2: 将告警测试期望改为中文并补充时长边界**

```go
assertRendered(t, domain.AlertOffline, offline,
    "🔴 node-1 已离线\n分组：production\n最后上报：2026-07-15 01:46:40 UTC\n告警等待：2 分钟")
assertRendered(t, domain.AlertRecovery, recovery,
    "🟢 node-1 已恢复\n分组：production\n恢复时间：2026-07-15 01:51:10 UTC\n中断时长：4 分钟 30 秒")

func TestFormatDurationChinese(t *testing.T) {
    for _, test := range []struct { milliseconds int64; want string }{
        {0, "0 秒"}, {5_000, "5 秒"}, {65_000, "1 分钟 5 秒"},
        {3_600_000, "1 小时"}, {90_061_000, "1 天 1 小时 1 分钟 1 秒"},
    } {
        if got := formatDuration(test.milliseconds); got != test.want { t.Errorf("formatDuration(%d) = %q, want %q", test.milliseconds, got, test.want) }
    }
}
```

- [ ] **Step 3: 运行聚焦测试并确认 RED**

Run: `go test ./internal/telegrambot ./internal/alerting -count=1`

Expected: FAIL，旧英文回复与新的中文期望不一致。

- [ ] **Step 4: 实现最小中文展示逻辑**

在 `commands.go` 中替换常量和展示字符串，引入状态映射，并令 `formatAge` 返回“天/小时/分钟/秒”。在 `render.go` 中替换告警字段，并用整除拆分实现中文 `formatDuration`：

```go
var statusLabels = map[string]string{"online": "在线", "offline": "离线", "disabled": "停用"}

func omittedServersLine(count int) string {
    return fmt.Sprintf("…另有 %d 台服务器未显示", count)
}

func formatDuration(durationMS int64) string {
    total := durationMS / 1_000
    units := []struct { seconds int64; suffix string }{
        {86_400, "天"}, {3_600, "小时"}, {60, "分钟"}, {1, "秒"},
    }
    parts := make([]string, 0, len(units))
    for _, unit := range units {
        if value := total / unit.seconds; value > 0 || unit.seconds == 1 && len(parts) == 0 {
            parts = append(parts, fmt.Sprintf("%d %s", value, unit.suffix))
        }
        total %= unit.seconds
    }
    return strings.Join(parts, " ")
}
```

- [ ] **Step 5: 验证 GREEN 并提交**

Run: `gofmt -w internal/telegrambot/commands.go internal/telegrambot/commands_test.go internal/alerting/render.go internal/alerting/render_test.go && go test ./internal/telegrambot ./internal/alerting -count=1`

Expected: PASS。

Commit: `git commit -am "feat: localize Telegram operator messages"`

---

### Task 2: WebApp 与 README 简体中文覆盖

**Files:**
- Modify: `internal/webapp/assets_test.go`
- Modify: `internal/webapp/assets/index.html`
- Modify: `internal/webapp/assets/app.js`
- Modify: `scripts/webapp-browser-test.js`
- Modify: `README.md`

**Interfaces:**
- Consumes: 现有 DOM ID、API 路径和浏览器流程。
- Produces: DOM 结构不变的简体中文界面和简体中文主文档。

- [ ] **Step 1: 写中文覆盖静态测试**

在 `assets_test.go` 新增：

```go
func TestAssetsUseSimplifiedChineseOperatorCopy(t *testing.T) {
    combined := readAsset(t, "assets/index.html") + readAsset(t, "assets/app.js")
    for _, required := range []string{"运维控制台", "实时监控", "服务器管理", "监控策略", "Agent 令牌", "1 分钟负载"} {
        if !strings.Contains(combined, required) { t.Errorf("assets missing Chinese copy %q", required) }
    }
    for _, forbidden := range []string{"Operator console", "Live fleet", "Administration", "Policy", "Agent token", "1m 负载"} {
        if strings.Contains(combined, forbidden) { t.Errorf("assets retain user-visible English %q", forbidden) }
    }
}
```

同步把 `webapp-browser-test.js` 中按钮名改为“创建并生成 Agent 令牌”、成功提示改为“Agent 令牌已复制”。

- [ ] **Step 2: 运行 WebApp 测试并确认 RED**

Run: `go test ./internal/webapp -count=1`

Expected: FAIL，仍存在英文标签和旧 Agent token 文案。

- [ ] **Step 3: 替换静态与动态用户可见文案**

保留所有 DOM ID、name、API 路径和状态键，只替换 text node、`aria-label`、`title` 与动态字符串。统一词汇：运维控制台、实时监控、历史数据、服务器管理、监控策略、Agent 令牌、1 分钟负载。

- [ ] **Step 4: 将 README 改为简体中文运行手册**

保留原有命令和章节覆盖，中文标题至少包含：项目能力、构建与验证、Server 部署、Telegram 配置、Agent 部署、Caddy HTTPS、日常管理、升级与回滚、一键安装与卸载。新增入口：

```bash
sudo bash scripts/manage.sh
```

明确普通卸载保留数据、彻底卸载二次确认，以及 Telegram 菜单按钮由安装器配置。

- [ ] **Step 5: 验证 GREEN 并提交**

Run: `go test ./internal/webapp -count=1 && go test ./internal/deploytest -count=1`

Expected: PASS。

Commit: `git add internal/webapp README.md scripts/webapp-browser-test.js && git commit -m "feat: complete Simplified Chinese operator copy"`

---

### Task 3: Telegram WebApp 默认菜单按钮

**Files:**
- Create: `internal/telegramapi/menu_button_test.go`
- Modify: `internal/telegramapi/client.go`
- Modify: `internal/servercmd/telegram_test.go`
- Modify: `internal/servercmd/run.go`
- Modify: `scripts/smoke-telegram.sh`

**Interfaces:**
- Produces: `Client.SetMenuButton(context.Context, string, string) error`、`Client.ResetMenuButton(context.Context) error`。
- Produces: CLI `telegram set-menu-button`、`telegram reset-menu-button`。

- [ ] **Step 1: 写 Bot API 请求形状测试**

`menu_button_test.go` 捕获两个请求并断言：

```go
wantSet := map[string]any{"menu_button": map[string]any{
    "type": "web_app", "text": "打开监控面板",
    "web_app": map[string]any{"url": "https://monitor.example.com/app/"},
}}
wantReset := map[string]any{"menu_button": map[string]any{"type": "default"}}
```

同时测试空按钮文字、远程 HTTP、userinfo、fragment 和超大输入在 transport 前失败；Telegram 返回 `result:false` 时返回安全错误。

- [ ] **Step 2: 写 CLI 路由测试**

扩展 `telegramWebhookClientStub` 记录 `setMenuCalls/menuText/menuURL/resetMenuCalls`，接口新增：

```go
SetMenuButton(context.Context, string, string) error
ResetMenuButton(context.Context) error
```

断言 `set-menu-button` 使用“打开监控面板”和 `${PublicURL}/app/`，输出 `menu_button=web_app`；重置输出 `menu_button=default`；extra args 在加载秘密前失败。

- [ ] **Step 3: 运行聚焦测试并确认 RED**

Run: `go test ./internal/telegramapi ./internal/servercmd -count=1`

Expected: 编译失败，因为菜单接口和 CLI 尚不存在。

- [ ] **Step 4: 实现菜单请求和 CLI**

在 `client.go` 添加私有请求类型，复用 WebApp URL 安全校验，Bot API 成功结果必须为 `true`。在 `run.go` 扩展 usage、switch 和 `TelegramWebhookClient`，从 `PublicURL` 派生 `/app/`，不得输出 URL 或 Token。

- [ ] **Step 5: 扩展 Telegram 烟雾测试**

Fake Bot API 接受 `setChatMenuButton`，验证一次 `web_app` 和一次 `default` 请求；最终输出增加 `menu_button=ok`，并继续执行 canary secret 日志扫描。

- [ ] **Step 6: 验证 GREEN 并提交**

Run: `gofmt -w internal/telegramapi/client.go internal/telegramapi/menu_button_test.go internal/servercmd/run.go internal/servercmd/telegram_test.go && go test ./internal/telegramapi ./internal/servercmd -count=1 && bash scripts/smoke-telegram.sh`

Expected: PASS，烟雾输出包含 `menu_button=ok` 和 `secret_log_scan=clean`。

Commit: `git add internal/telegramapi internal/servercmd scripts/smoke-telegram.sh && git commit -m "feat: manage Telegram WebApp menu button"`

---

### Task 4: 安装器基础、交互和可信产物获取

**Files:**
- Create: `scripts/tests/manage_test.sh`
- Create: `scripts/manage.sh`

**Interfaces:**
- Produces: `main_menu`、`detect_arch`、`root_path`、`prompt_choice`、`prompt_secret`、`confirm_summary`、`resolve_release_version`、`acquire_binary`。
- Test injection: `TG_MONITOR_ROOT` 重定向绝对路径；`TG_MONITOR_TEST_MODE=1` 禁止 sudo 重启和真实网络；`TG_MONITOR_RELEASE_BASE_URL` 仅供 fake curl 测试。

- [ ] **Step 1: 建立无特权 Shell 测试框架**

测试脚本创建临时目录和 `fake-bin`，设置清理 trap，提供：

```bash
assert_eq() { [[ "$1" == "$2" ]] || fail "got [$1], want [$2]"; }
assert_file_mode() { assert_eq "$(stat -c '%a' "$1")" "$2"; }
run_case() { printf 'case=%s\n' "$1"; "$2"; }
```

source `scripts/manage.sh` 后测试 `detect_arch x86_64=amd64`、`aarch64=arm64`、其他架构失败；菜单 0 不写文件；错误选择重新提示；摘要中 canary Token 只显示“已设置”。

- [ ] **Step 2: 运行测试并确认 RED**

Run: `bash scripts/tests/manage_test.sh`

Expected: FAIL，`scripts/manage.sh` 不存在。

- [ ] **Step 3: 实现安全 Shell 骨架和菜单**

`manage.sh` 从以下结构开始：

```bash
#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT_PREFIX="${TG_MONITOR_ROOT:-}"
root_path() { printf '%s%s' "$ROOT_PREFIX" "$1"; }
detect_arch() {
  case "${1:-$(uname -m)}" in x86_64|amd64) printf amd64;; aarch64|arm64) printf arm64;; *) return 1;; esac
}
main() { require_supported_host; main_menu; }
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then main "$@"; fi
```

无 root 时仅在 TTY 中 `exec sudo -- bash "$0" "$@"`；非 TTY 直接中文报错。所有提示函数从 `/dev/tty` 读取，测试模式改用 stdin。

- [ ] **Step 4: 写 Release 和校验失败测试**

Fake curl 记录 URL 并复制 fixtures。测试资产名严格为 `tg-monitor-{server|agent}-linux-{amd64|arm64}`；摘要匹配成功，缺失条目、错误摘要、非 HTTPS 最终 URL 均失败且目标二进制不存在。本地目录模式只接受普通可读文件。

- [ ] **Step 5: 实现下载和 SHA-256 校验**

使用 GitHub `releases/latest` 的 HTTPS 最终 URL 解析标签，明确版本必须匹配 `^v[0-9][0-9A-Za-z._-]*$`。下载资产及 `SHA256SUMS` 到 `mktemp -d`，只提取与资产 basename 完全匹配的一行交给 `sha256sum -c`；校验后才安装。

- [ ] **Step 6: 验证 GREEN 并提交**

Run: `bash -n scripts/manage.sh scripts/tests/manage_test.sh && bash scripts/tests/manage_test.sh`

Expected: PASS，并输出各测试 case 和 `manage_tests=ok`。

Commit: `git add scripts/manage.sh scripts/tests/manage_test.sh && git commit -m "feat: add interactive installer foundation"`

---

### Task 5: Server、Caddy 与 Telegram 安装升级

**Files:**
- Modify: `scripts/tests/manage_test.sh`
- Modify: `scripts/manage.sh`

**Interfaces:**
- Produces: `install_server`、`write_server_env`、`install_server_unit`、`configure_caddy`、`configure_telegram`、`verify_server`、`rollback_server`。

- [ ] **Step 1: 写 Server 安装失败测试**

使用 fake `useradd/install/systemctl/curl/caddy/ss/runuser`，向 `install_server` 传入关联数组配置。断言：

- 二进制为 `0755`，`server.env` 为 `0600`，unit 为 `0644`。
- 环境文件包含本地地址、数据库、Telegram 开关和输入值，但捕获的 systemctl 参数与测试输出不含 Token/Secret。
- 调用顺序为 daemon-reload、enable/start、healthz、readyz、set-webhook、set-menu-button、get-webhook。
- 已有配置默认保留；选择更新时原文件备份。
- 健康检查失败恢复 `.previous` 和配置备份。

- [ ] **Step 2: 运行 Server 测试并确认 RED**

Run: `bash scripts/tests/manage_test.sh server`

Expected: FAIL，Server 安装函数不存在。

- [ ] **Step 3: 实现 Server 文件和服务事务**

嵌入与 `deploy/systemd/tg-monitor.service` 等价的 hardened unit 模板。用临时文件写 env，`install -o root -g root -m 0600` 原子替换。现有二进制复制为 `.previous`；保存服务原 active 状态，失败时恢复并重启原版本。

- [ ] **Step 4: 写 Caddy 边界测试**

测试目标端口被非 Caddy 占用时零修改；主 Caddyfile 只新增一次：

```text
# BEGIN tg-monitor managed import
import /etc/caddy/tg-monitor.caddy
# END tg-monitor managed import
```

站点片段必须包含 `<domain>:<port>` 和 `reverse_proxy 127.0.0.1:<server-port>`。`caddy validate` 失败恢复原文件；重复配置不重复 import；其他站点内容逐字保留。

- [ ] **Step 5: 实现 Caddy 与 Telegram 收尾**

Debian/Ubuntu 缺少 Caddy 时使用 apt 非交互安装；其他发行版显示手工步骤。Telegram 开启时公网 URL 必须 HTTPS，显式端口属于官方 Webhook 支持集合，并在公网 ready 成功后才运行 CLI 设置 Webhook 和菜单按钮。

- [ ] **Step 6: 验证 GREEN 并提交**

Run: `bash -n scripts/manage.sh scripts/tests/manage_test.sh && bash scripts/tests/manage_test.sh server && bash scripts/tests/manage_test.sh caddy`

Expected: PASS，canary 扫描为 clean。

Commit: `git add scripts/manage.sh scripts/tests/manage_test.sh && git commit -m "feat: install and upgrade central server"`

---

### Task 6: Agent 与同机组合安装

**Files:**
- Modify: `scripts/tests/manage_test.sh`
- Modify: `scripts/manage.sh`

**Interfaces:**
- Produces: `install_agent`、`write_agent_env`、`verify_agent_once`、`rollback_agent`、`install_all_same_host`、`register_local_server`。

- [ ] **Step 1: 写 Agent RED 测试**

断言 Agent 用户、二进制、`agent.env`、hardened unit 权限正确；`runuser ... tg-monitor-agent once` 必须发生在 `systemctl enable --now tg-monitor-agent` 之前；Token 只存在于 `0600` env。`once` 失败时服务未启用并恢复旧版本。

- [ ] **Step 2: 写同机组合 RED 测试**

Fake `tg-monitor-server server add` 返回：

```text
server_id=7
agent_token=TEST-ONE-TIME-TOKEN
```

断言严格提取两行、Agent URL 为 `http://127.0.0.1:<本地端口>`、最终摘要不含 Token；输出缺行、重复行或额外 credential 行时失败。

- [ ] **Step 3: 运行并确认 RED**

Run: `bash scripts/tests/manage_test.sh agent && bash scripts/tests/manage_test.sh combined`

Expected: FAIL，Agent 和组合函数不存在。

- [ ] **Step 4: 实现 Agent 事务和组合流程**

嵌入现有 hardened Agent unit。环境文件原子写入，使用 `runuser -u tg-monitor-agent -- /usr/local/bin/tg-monitor-agent once`，环境仅从 root 可读文件加载。组合流程 Server 成功后注册节点，再调用同一 Agent 安装函数，不复制安装逻辑。

- [ ] **Step 5: 验证 GREEN 并提交**

Run: `bash -n scripts/manage.sh scripts/tests/manage_test.sh && bash scripts/tests/manage_test.sh agent && bash scripts/tests/manage_test.sh combined && bash scripts/smoke-agent.sh`

Expected: PASS，烟雾输出包含 `ingestion=ok` 和 `token_log_scan=clean`。

Commit: `git add scripts/manage.sh scripts/tests/manage_test.sh && git commit -m "feat: install Agent and same-host monitoring"`

---

### Task 7: 安全卸载与彻底清理

**Files:**
- Modify: `scripts/tests/manage_test.sh`
- Modify: `scripts/manage.sh`

**Interfaces:**
- Produces: `uninstall_server`、`uninstall_agent`、`uninstall_all`、`purge_server_data`、`purge_agent_data`、`remove_caddy_config`。

- [ ] **Step 1: 写普通卸载 RED 测试**

构造同时安装两个组件的临时根目录。卸载 Server 后断言 Server unit/二进制删除，`server.env`、数据库、Agent unit/二进制/env 保留；卸载 Agent 对 Server 同理；缺失文件重复卸载仍成功。

- [ ] **Step 2: 写 purge 和 Caddy RED 测试**

错误确认词不得删除任何配置或数据。正确确认后只删除目标组件；卸载全部按 Agent 后 Server 顺序，最后删除无剩余依赖的共享目录和对应用户。Caddy 移除只删除自有片段和标记 import，校验失败恢复配置。Telegram reset/delete 失败只警告并继续本地卸载。

- [ ] **Step 3: 运行并确认 RED**

Run: `bash scripts/tests/manage_test.sh uninstall`

Expected: FAIL，卸载函数不存在。

- [ ] **Step 4: 实现幂等卸载**

每个删除动作使用固定路径，不接受用户输入拼接删除目标。普通卸载执行 disable/stop、删除 unit/二进制、daemon-reload；purge 单独删除 env、Server 数据目录和无依赖用户。禁止 glob 扩大删除范围。

- [ ] **Step 5: 验证 GREEN 并提交**

Run: `bash -n scripts/manage.sh scripts/tests/manage_test.sh && bash scripts/tests/manage_test.sh uninstall`

Expected: PASS，普通卸载/彻底清理/共享边界均通过。

Commit: `git add scripts/manage.sh scripts/tests/manage_test.sh && git commit -m "feat: safely uninstall tg-monitor components"`

---

### Task 8: Release 工作流、部署契约与全量验收

**Files:**
- Create: `internal/deploytest/installer_assets_test.go`
- Create: `.github/workflows/release.yml`
- Modify: `README.md`
- Modify: `scripts/build-server.sh`
- Modify: `scripts/build-agent.sh`

**Interfaces:**
- Produces: 标签 `v*` 对应四个二进制和 `SHA256SUMS`。

- [ ] **Step 1: 写仓库级 Release/安装器契约测试**

`installer_assets_test.go` 读取脚本、工作流和 README，断言菜单六项、`0600`、普通卸载保留、purge 确认、四个资产名、`SHA256SUMS`、`go test ./...`、`CGO_ENABLED=0`、amd64/arm64 和 `scripts/manage.sh` 使用说明存在；禁止脚本包含用户提供 Token 的命令行展开模式。

- [ ] **Step 2: 运行并确认 RED**

Run: `go test ./internal/deploytest -count=1`

Expected: FAIL，Release workflow 尚不存在且部署契约不完整。

- [ ] **Step 3: 新增最小权限 Release workflow**

Workflow 仅触发：

```yaml
on:
  push:
    tags: ['v*']
permissions:
  contents: write
```

使用 `actions/checkout`、`actions/setup-go`，运行测试和两个现有构建脚本，在 `dist` 内执行 `sha256sum tg-monitor-*-linux-* > SHA256SUMS`，最后用 `gh release create "$GITHUB_REF_NAME" dist/tg-monitor-*-linux-* dist/SHA256SUMS --verify-tag`。已存在同名 Release 时失败，不覆盖。

- [ ] **Step 4: 对齐构建脚本和 README**

确保两个构建脚本统一 `-trimpath -ldflags='-s -w'` 和固定资产名；README 补充 Release 标签发布、校验、交互安装器、菜单按钮设置/恢复和卸载矩阵。

- [ ] **Step 5: 执行全量验证**

Run:

```bash
bash -n scripts/manage.sh scripts/tests/manage_test.sh
bash scripts/tests/manage_test.sh
go test ./... -count=1
go vet ./...
bash scripts/smoke-agent.sh
bash scripts/smoke-telegram.sh
bash scripts/smoke-alerts.sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/tg-monitor-server ./cmd/tg-monitor-agent
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/tg-monitor-server ./cmd/tg-monitor-agent
git diff --check
```

Expected: 所有命令退出 0；烟雾测试包含 `token_log_scan=clean`、`secret_log_scan=clean`；工作区仅有计划内文件。

- [ ] **Step 6: 可用时运行真实浏览器测试**

Run: `bash scripts/smoke-webapp.sh`

Expected: API/WebApp smoke PASS；若 Playwright CLI 已安装，再运行仓库文档中的真实浏览器命令并确认中文按钮流程 PASS。

- [ ] **Step 7: 提交最终交付**

Commit: `git add .github/workflows/release.yml internal/deploytest/installer_assets_test.go README.md scripts && git commit -m "feat: publish verified one-click installer assets"`

发布标签和生产服务器升级不在此提交步骤内；必须在全部验收通过后另行获得用户批准。
