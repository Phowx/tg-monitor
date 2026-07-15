# 简体中文完善与一键安装器设计

**日期：** 2026-07-15
**状态：** 已由用户逐节确认

## 背景与目标

tg-monitor 已具备中心 Server、Linux Agent、Telegram 管理命令、离线与恢复告警、管理员 WebApp、systemd 示例和 Caddy 示例，但用户可见内容仍混有英文，生产部署也依赖手工复制文件和编辑配置。

本阶段完成三个相互配合的交付物：

1. 将 WebApp、Telegram 回复、告警通知、安装器提示和 README 使用说明完善为简体中文。
2. 提供单一交互式 Bash 管理入口，完整覆盖 Server、Agent、同机部署的安装、升级、卸载与彻底清理。
3. 将 WebApp 设置为 Telegram 私聊输入框旁的菜单按钮，使管理员无需先输入 `/app`。

API 字段、环境变量、命令名、结构化日志和内部错误分类保持英文，以维持兼容性和可运维性。CPU、Telegram、WebApp、Server、Agent 等产品或技术名称按中文技术文档的常见写法保留。

## 方案选择

### 采用：单一 Bash 交互向导

新增 `scripts/manage.sh`，无参数运行时展示中文菜单。脚本内部按环境检测、输入校验、产物获取、Server、Agent、Caddy、Telegram 和卸载拆分成独立函数。普通操作全部通过交互选择完成；少量高级参数和测试注入点只用于指定版本、本地产物或自动化验证。

该方案不要求预先安装额外运行时，适合当前以 systemd Linux 和静态 Go 二进制为主的部署模型，也能在 Shell 层直接管理用户、权限、配置文件和服务。

### 未采用：Server、Agent 分散脚本

多个入口更容易产生重复的权限检测、下载、卸载和目录管理逻辑，也会让“安装全部”和共享目录清理变得不一致。

### 未采用：Go 安装器

Go 安装器更适合复杂跨平台安装，但用户必须先取得安装器二进制；对于当前仅支持 systemd Linux 的范围，收益不足以抵消构建和发布成本。

## 简体中文范围

### WebApp

直接完善当前静态 HTML 和 JavaScript 文案，不引入多语言框架。主要变更包括：

- 将 `Operator console`、`Live fleet`、`History`、`Administration`、`Policy` 等装饰性标签改为简体中文。
- 将 `Agent token` 统一写为“Agent 令牌”，相关复制、轮换、一次性展示提示保持术语一致。
- 将 `1m 负载` 改为“1 分钟负载”，其余指标、状态、空数据、表单校验和错误提示统一检查。
- HTML 的 `lang="zh-CN"` 保持不变；可访问性标签和浏览器标题也纳入翻译检查。

### Telegram 管理命令

命令名保持兼容，所有回复改为简体中文：

- `/start`、`/help`：说明仅供管理员使用，并列出中文命令用途。
- `/app`：发送“打开监控面板”按钮作为备用入口。
- `/alerts_on`、`/alerts_off`：确认告警开关状态。
- `/status`：使用“在线、离线、停用、暂无数据、最后上报”等中文状态；时长使用“天、小时、分钟、秒”；超长列表使用中文省略提示。
- 非命令文本和未知命令返回简短中文帮助。

### 告警通知

离线通知包含“服务器已离线、分组、最后上报时间、触发告警用时”；恢复通知包含“服务器已恢复、分组、恢复时间、中断时长”。时间戳继续明确标注 UTC，持续时长使用中文单位。服务器名称与分组仍按纯文本输出并继续执行长度和空白规范化。

### README 与部署资源

README 的项目介绍、构建、配置、安装、Telegram、Agent、Caddy、运维、升级、回滚和卸载说明改为简体中文。环境变量名、Shell 命令和机器可读输出不翻译。systemd 的 `Description` 可改为简洁中文，但 unit 名和路径保持不变。

## Telegram 输入框菜单按钮

`internal/telegramapi` 新增 `SetMenuButton`，调用 Telegram Bot API `setChatMenuButton`，请求默认菜单按钮：

```json
{
  "menu_button": {
    "type": "web_app",
    "text": "打开监控面板",
    "web_app": {"url": "https://monitor.example.com/app/"}
  }
}
```

不传 `chat_id`，使该按钮成为 Bot 私聊的默认菜单按钮。WebApp 自身仍执行 Telegram 签名和管理员白名单校验，因此按钮可见性不作为授权边界。

Server CLI 新增：

```text
tg-monitor-server telegram set-menu-button
tg-monitor-server telegram reset-menu-button
```

`set-menu-button` 从现有 Telegram 配置推导 `${TG_MONITOR_PUBLIC_URL}/app/`。`reset-menu-button` 将类型恢复为 `default`。两者输出不含 Bot Token 或 Webhook Secret。现有 `/app` 命令继续保留，避免菜单按钮因客户端版本或 Telegram 配置异常而成为单点入口。

安装器在启用 Telegram 后依次启动 Server、设置 Webhook、设置菜单按钮并读取 Webhook 状态。Telegram 配置失败会显示中文诊断并将本次 Server 安装标为未完成，不会报告虚假成功。卸载 Server 时，如配置仍可读取，向导询问是否删除 Webhook 并恢复默认菜单；远端调用失败只发出警告，不阻止用户继续本地卸载。

## 交互式管理入口

无参数执行 `sudo bash scripts/manage.sh` 时展示：

```text
1. 安装或升级 Server
2. 安装或升级 Agent
3. 同机安装或升级 Server + Agent
4. 卸载 Server
5. 卸载 Agent
6. 卸载全部组件
0. 退出
```

每条路径只询问实际需要的值，提供安全默认值和简短说明。敏感输入使用关闭回显的读取方式。执行系统修改前展示脱敏摘要，要求用户明确输入确认；取消操作不产生修改。

脚本首先验证：

- 操作系统为 Linux，PID 1/服务管理器为 systemd。
- 当前为 root；否则在交互终端中通过 `sudo` 重新执行自身。
- 架构可映射为 `amd64` 或 `arm64`。
- 必需命令存在；下载模式至少需要 `curl` 和 `sha256sum`。
- systemd unit、监听端口和既有安装状态可安全处理。

脚本使用 `set -Eeuo pipefail`，建立临时目录并通过 trap 清理。日志中的配置摘要对 Token 和 Secret 只显示“已设置/未设置”。Secret 不进入命令参数、下载 URL 或进程列表。

## 产物获取与校验

向导提供两个来源：

1. GitHub Release（默认）：选择最新稳定版或输入明确的 `v*` 版本。
2. 本地目录：读取用户指定目录中的构建产物。

Release 资产名称固定为：

```text
tg-monitor-server-linux-amd64
tg-monitor-server-linux-arm64
tg-monitor-agent-linux-amd64
tg-monitor-agent-linux-arm64
SHA256SUMS
```

下载来源固定为 `Phowx/tg-monitor` 的 GitHub Release。脚本下载到临时目录，拒绝重定向到非 HTTPS 最终地址，并使用 `SHA256SUMS` 校验所选资产；缺失校验记录或摘要不匹配时不得安装。本地目录模式要求文件存在且可执行，但将其视为操作者明确提供的可信输入，不要求 Release 清单。

安装二进制前将当前版本保存为同目录 `.previous`；服务验证失败时恢复旧二进制和原配置备份。首次安装失败则停止服务并移除本次新建的 unit 和二进制，但保留诊断所需的配置备份。

## Server 安装与升级

向导收集并验证：

- 本地监听地址，默认 `127.0.0.1:8080`。
- 数据库路径，默认 `/var/lib/tg-monitor/monitor.db`。
- 是否启用 Telegram。
- 启用 Telegram 时的 HTTPS 公网地址、Bot Token、管理员 Telegram ID 列表；Webhook Secret 默认安全随机生成，也允许手动输入。
- 是否自动配置 Caddy；选择后询问域名和 HTTPS 端口，默认 443，可明确选择 8443 等端口。

安装流程为：

1. 检查监听端口和现有服务，备份已有二进制与配置。
2. 创建 `tg-monitor` 系统用户和 `/etc/tg-monitor`，以 root:root、`0600` 原子写入 `server.env`。
3. 安装 Server 二进制和仓库内的 hardened systemd unit。
4. 需要时配置并验证 Caddy。
5. `systemctl daemon-reload`，启用并启动 `tg-monitor.service`。
6. 验证本机 `/healthz` 和 `/readyz`；启用 HTTPS 时再验证公网 `/readyz`。
7. 启用 Telegram 时设置 Webhook 与菜单按钮，并检查 Webhook URL。

升级默认保留现有配置。若用户选择修改配置，向导以当前值作为默认值，敏感值只允许保留或重新输入，绝不回显旧值。

## Agent 安装与升级

向导收集中心 Server URL、一次性 Agent Token、采样间隔和 HTTP 超时。URL 必须是 HTTPS；只有字面 loopback IP 或 `localhost` 可使用 HTTP。

安装流程为：

1. 创建 `tg-monitor-agent` 非登录系统用户。
2. 安装 Agent 二进制、hardened systemd unit，并以 root:root、`0600` 原子写入 `agent.env`。
3. 在启用 daemon 前，以 `tg-monitor-agent` 用户执行一次 `tg-monitor-agent once`，完成真实采集与鉴权上报。
4. 首次上报成功后启用并启动 `tg-monitor-agent.service`，检查 active/enabled 状态。

Token 不写入命令参数，而是仅通过 root 可读环境文件传给测试进程。升级失败时恢复旧二进制与配置，并重新启动原服务。

## 同机安装 Server + Agent

组合流程先完成 Server 安装和健康检查，再询问监控节点名称、分组和排序。脚本以 `tg-monitor` 服务用户运行 `server add`，从标准输出严格提取一次性 Agent Token，不在最终摘要回显；Agent URL 使用 `http://127.0.0.1:<本地端口>`，避免同机上报依赖外部 DNS 和 TLS。随后执行完整 Agent 安装与一次真实上报验证。

如果 Server 已存在，用户可选择使用既有节点 Token 或新建节点。脚本不会尝试从数据库恢复旧 Token，因为系统只存储哈希。

## Caddy 管理边界

自动安装 Caddy 仅在 Debian/Ubuntu 且 `apt` 能提供受支持软件包时执行；其他 systemd 发行版继续安装 Server，并给出手工反向代理指引。若用户同时启用了 Telegram，但 HTTPS 入口尚不可用，脚本不得设置 Webhook 或宣告安装完成，而应保留已安装的核心服务并明确列出待完成的反向代理步骤。

脚本将站点配置写入 `/etc/caddy/tg-monitor.caddy`，并在主 Caddyfile 中维护唯一、带明确起止标记的 import 段。修改前保存权限和所有权一致的备份，修改后必须通过 `caddy validate --config /etc/caddy/Caddyfile` 才允许 reload。验证失败立即恢复备份。

新安装时，若目标端口已被非 Caddy 进程占用，脚本停止并要求重新选择；现有 tg-monitor Caddy 配置升级可复用自己的监听端口。配置 Telegram Webhook 时，公网 HTTPS 端口必须属于 Telegram 官方支持的端口集合。脚本不开放云厂商防火墙，只明确提示需要放行 TCP 端口和 HTTP-01 证书签发所需的 80 端口。

卸载只删除 `/etc/caddy/tg-monitor.caddy` 和脚本自己插入的 import 段，随后校验并 reload；不卸载 Caddy 软件包，也不修改其他站点。

## 卸载与彻底清理

默认卸载执行：

- disable/stop 对应 systemd 服务。
- 删除对应 unit 和 `/usr/local/bin` 二进制。
- Server 卸载时删除其 Caddy 片段；Agent 卸载不触碰 Caddy。
- 执行 daemon-reload。
- 保留 `/etc/tg-monitor` 中对应配置、Server 数据库和系统用户，以便恢复或重装。

向导随后明确提供“普通卸载（保留数据）”和“彻底卸载”选择。彻底卸载需第二次输入指定中文确认词，才允许：

- 删除被卸载组件的环境文件。
- Server 清理 `/var/lib/tg-monitor`；执行前再次说明数据库、历史、会话和告警记录不可恢复。
- 仅在没有剩余组件依赖时删除共享目录。
- 删除对应专用用户和组。

卸载 Server 不删除 Agent 配置，卸载 Agent 不删除 Server 配置或数据库；卸载全部按 Agent、Server 的顺序执行。不存在的文件和服务按成功处理，使卸载可重复执行。

## Release 自动化

新增 `.github/workflows/release.yml`。推送符合 `v*` 的标签时，工作流：

1. 使用仓库 `go.mod` 对应的 Go 版本。
2. 运行 `go test ./...`。
3. 以 `CGO_ENABLED=0` 构建 Linux amd64/arm64 的 Server 和 Agent。
4. 生成覆盖四个二进制的 `SHA256SUMS`。
5. 使用 GitHub 提供的 token 创建对应 Release 并上传五个资产。

工作流不从分支推送自动发布，也不覆盖已存在的同名 Release。本阶段只提交工作流，不自行创建标签或正式 Release；发布属于单独的外部状态变更，需要用户再次批准。

## 测试策略

所有行为按 red-green TDD 实现。

Go 测试覆盖：

- Telegram `setChatMenuButton` 和恢复默认菜单的精确请求形状、HTTPS/loopback URL 校验、拒绝响应与秘密泄漏扫描。
- Server CLI 新命令的配置加载、输出、失败传播和无效参数。
- Telegram 命令、状态摘要、超长省略、告警时间与中文文案。
- WebApp 静态资源中禁止遗留的用户可见英文短语，并更新真实浏览器断言。

Shell 测试使用 `scripts/tests/manage_test.sh`，以临时根目录、模拟 PATH 和替身命令运行真实脚本函数，不调用宿主 systemd 或修改 `/etc`。覆盖：

- 菜单选择、确认和取消零副作用。
- amd64/arm64 检测、错误平台和缺失依赖。
- Release URL、SHA-256 成功与失败、本地产物模式。
- Server、Agent 和组合安装的文件权限、unit、配置脱敏与调用顺序。
- 重复安装、升级失败回滚、首次 Agent 上报失败。
- Caddy 端口冲突、配置验证失败恢复和只删除自有片段。
- 普通卸载保留数据、彻底卸载确认、单组件与共享目录边界。
- 日志和进程参数中不出现 canary Token/Secret。

验收命令至少包括：

```bash
bash -n scripts/manage.sh scripts/tests/manage_test.sh
bash scripts/tests/manage_test.sh
go test ./... -count=1
go vet ./...
bash scripts/smoke-telegram.sh
bash scripts/smoke-alerts.sh
```

若浏览器运行环境可用，还需通过现有 WebApp 浏览器测试。Release workflow 通过 YAML 静态断言和本地等价构建命令验证。

## 验收标准

- WebApp、Telegram 管理回复、告警和主要使用文档不存在未经确认的用户可见英文文案。
- Telegram 输入框默认显示“打开监控面板”，点击能打开 `${PublicURL}/app/`，未授权用户仍不能建立管理员会话。
- 新机器可通过一次交互运行完成 Server、Agent 或同机组合安装；所有服务 active 且 enabled，健康检查和首次上报成功。
- 普通卸载保留数据，彻底卸载必须二次确认；任何单组件操作不破坏另一组件或其他 Caddy 站点。
- Release 下载资产必须通过 SHA-256 校验；秘密不出现在输出、日志或命令参数。
- 全量测试、静态检查和现有烟雾测试通过，Git 工作区只包含预期变更。

## 明确不在范围内

- 非 systemd Linux、macOS、Windows、容器编排和图形安装器。
- 自动修改 AWS Lightsail、安全组、云防火墙、DNS 或域名注册商配置。
- 自动创建 Bot、获取 Telegram 管理员 ID 或轮换 Bot Token。
- 为 API 字段、环境变量、结构化日志或命令名提供中文别名。
- 自动创建 Git 标签、发布正式 Release 或未经批准直接更新生产服务器。

## 权威参考

- Telegram `setChatMenuButton`：<https://core.telegram.org/bots/api#setchatmenubutton>
- Telegram `MenuButtonWebApp`：<https://core.telegram.org/bots/api#menubuttonwebapp>
- Telegram Webhook 支持端口：<https://core.telegram.org/bots/api#setwebhook>
