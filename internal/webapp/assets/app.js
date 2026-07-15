"use strict";

(() => {
  const state = {
    authenticated: false,
    loginAttempted: false,
    overview: null,
    selectedServer: null,
    historyServer: null,
    historyRange: "1h",
    historyController: null,
    token: null,
    refreshing: false,
  };

  const views = ["bootstrap", "dashboard", "history", "servers", "settings"];
  const rangeMilliseconds = {
    "1h": 60 * 60 * 1000,
    "6h": 6 * 60 * 60 * 1000,
    "24h": 24 * 60 * 60 * 1000,
    "7d": 7 * 24 * 60 * 60 * 1000,
  };
  const stateLabels = { online: "在线", offline: "离线", disabled: "停用" };

  class APIError extends Error {
    constructor(status, code) {
      super(code);
      this.name = "APIError";
      this.status = status;
      this.code = code;
    }
  }

  function byID(id) {
    return document.getElementById(id);
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = String(text);
    return node;
  }

  function applyTelegramTheme() {
    if (!window.Telegram || !window.Telegram.WebApp) return;
    try {
      window.Telegram.WebApp.ready();
      window.Telegram.WebApp.expand();
    } catch (_) {
      // The bridge is optional in a regular browser.
    }
    const theme = window.Telegram.WebApp.themeParams || {};
    const variables = {
      bg_color: ["--tg-theme-bg-color", "--app-bg"],
      secondary_bg_color: ["--tg-theme-secondary-bg-color", "--app-surface"],
      text_color: ["--tg-theme-text-color", "--app-text"],
      hint_color: ["--tg-theme-hint-color", "--app-muted"],
      button_color: ["--tg-theme-button-color", "--app-accent"],
      button_text_color: ["--tg-theme-button-text-color", "--app-accent-text"],
    };
    Object.entries(variables).forEach(([name, variableNames]) => {
      const value = theme[name];
      if (typeof value === "string" && /^#[0-9a-f]{6}$/i.test(value)) {
        variableNames.forEach((variable) => document.documentElement.style.setProperty(variable, value));
      }
    });
  }

  async function request(path, options = {}) {
    const response = await fetch(path, {
      ...options,
      credentials: "same-origin",
      headers: options.body ? { "Content-Type": "application/json", ...(options.headers || {}) } : options.headers,
    });
    if (!response.ok) {
      let code = "request_failed";
      try {
        const payload = await response.json();
        if (payload && payload.error && typeof payload.error.code === "string") code = payload.error.code;
      } catch (_) {
        // Public behavior depends on status, not an untrusted response body.
      }
      throw new APIError(response.status, code);
    }
    if (response.status === 204) return null;
    return response.json();
  }

  function showView(name) {
    views.forEach((view) => {
      byID(`${view}-view`).hidden = view !== name;
    });
    const authenticatedView = name !== "bootstrap";
    byID("primary-nav").hidden = !authenticatedView;
    byID("logout-button").hidden = !authenticatedView;
    document.querySelectorAll("[data-view]").forEach((button) => {
      const active = button.dataset.view === name || (name === "history" && button.dataset.view === "dashboard");
      if (active) button.setAttribute("aria-current", "page");
      else button.removeAttribute("aria-current");
    });
  }

  function setMessage(message) {
    byID("global-message").textContent = message || "";
  }

  function clearError() {
    const target = byID("global-error");
    target.textContent = "";
    target.hidden = true;
  }

  function showError(error, validationMessage) {
    if (error instanceof APIError && error.status === 401) {
      showAuthenticationRequired("会话已失效，请从配置的 Telegram Bot 重新打开 Mini App。");
      return;
    }
    let message = "操作暂时失败，请稍后重试。";
    if (error instanceof APIError && (error.status === 400 || error.status === 422)) {
      message = validationMessage || "请检查输入内容后重试。";
    } else if (error instanceof APIError && error.status === 404) {
      message = "该资源已发生变化，列表已刷新。";
      void loadOverview({ quiet: true });
    } else if (error instanceof APIError && error.status === 409) {
      message = "数据已被其他操作更新，请刷新后重试。";
    }
    const target = byID("global-error");
    target.textContent = message;
    target.hidden = false;
  }

  function telegramInitData() {
    if (!window.Telegram || !window.Telegram.WebApp) return "";
    return typeof window.Telegram.WebApp.initData === "string" ? window.Telegram.WebApp.initData : "";
  }

  async function bootstrap() {
    clearError();
    showView("bootstrap");
    byID("bootstrap-retry").hidden = true;
    byID("bootstrap-message").textContent = "请稍候，正在验证 Telegram 管理员身份。";
    try {
      await request("/api/v1/auth/session");
      await enterAuthenticatedApp();
      return;
    } catch (error) {
      if (!(error instanceof APIError) || error.status !== 401) {
        showBootstrapFailure();
        return;
      }
    }

    const initData = telegramInitData();
    if (!initData || state.loginAttempted) {
      showAuthenticationRequired("请从配置的 Telegram Bot 打开此 Mini App。");
      return;
    }

    state.loginAttempted = true;
    byID("bootstrap-message").textContent = "正在验证 Telegram 签名并创建会话。";
    try {
      await request("/api/v1/auth/telegram", {
        method: "POST",
        body: JSON.stringify({ init_data: initData }),
      });
      await request("/api/v1/auth/session");
      await enterAuthenticatedApp();
    } catch (_) {
      showAuthenticationRequired("无法建立管理员会话，请从配置的 Telegram Bot 重新打开 Mini App。");
    }
  }

  function showBootstrapFailure() {
    state.authenticated = false;
    showView("bootstrap");
    byID("bootstrap-message").textContent = "服务暂时不可用，请稍后重试。";
    byID("bootstrap-retry").hidden = false;
  }

  function showAuthenticationRequired(message) {
    state.authenticated = false;
    state.overview = null;
    clearToken();
    showView("bootstrap");
    byID("bootstrap-message").textContent = message;
    byID("bootstrap-retry").hidden = telegramInitData() === "";
  }

  async function enterAuthenticatedApp() {
    state.authenticated = true;
    showView("dashboard");
    await loadOverview();
  }

  async function loadOverview({ quiet = false } = {}) {
    if (!state.authenticated || state.refreshing) return;
    state.refreshing = true;
    if (!quiet) setMessage("正在更新监控数据…");
    try {
      const overview = await request("/api/v1/admin/overview");
      state.overview = overview;
      renderOverview();
      renderAdminServers();
      syncSettingsForm();
      clearError();
      setMessage(quiet ? "" : "监控数据已更新。");
    } catch (error) {
      showError(error);
    } finally {
      state.refreshing = false;
    }
  }

  function renderOverview() {
    if (!state.overview) return;
    const counts = { total: state.overview.servers.length, online: 0, offline: 0, disabled: 0 };
    state.overview.servers.forEach((entry) => {
      if (Object.hasOwn(counts, entry.state)) counts[entry.state] += 1;
    });
    Object.entries(counts).forEach(([name, count]) => {
      byID(`stat-${name}`).textContent = String(count);
    });
    byID("last-refresh").textContent = `更新于 ${formatTime(state.overview.now_ms)}`;

    const list = byID("server-list");
    list.replaceChildren();
    byID("server-empty").hidden = state.overview.servers.length !== 0;
    state.overview.servers.forEach((entry) => list.append(createServerCard(entry)));
  }

  function createServerCard(entry) {
    const card = element("article", "server-card");
    const heading = element("div", "server-card-heading");
    const identity = element("div");
    identity.append(element("h3", "", entry.server.name));
    identity.append(element("p", "group", entry.server.group || "未分组"));
    heading.append(identity);
    heading.append(element("span", `state-badge state-${entry.state}`, stateLabels[entry.state] || entry.state));
    card.append(heading);

    const metrics = element("div", "metric-grid");
    if (entry.latest) {
      const report = entry.latest.report;
      appendMetric(metrics, "CPU", formatPercent(report.cpu_pct));
      appendMetric(metrics, "内存", formatPercent(ratioPercent(report.memory_used_bytes, report.memory_total_bytes)));
      appendMetric(metrics, "根磁盘", formatPercent(ratioPercent(report.root_disk_used_bytes, report.root_disk_total_bytes)));
      appendMetric(metrics, "1 分钟负载", formatNumber(report.load_1));
      appendMetric(metrics, "最后上报", formatAge(entry.latest.received_at, state.overview.now_ms));
      appendMetric(metrics, "主机", report.system && report.system.hostname ? report.system.hostname : "未知");
    } else {
      ["CPU", "内存", "根磁盘", "1 分钟负载", "最后上报", "主机"].forEach((label) => appendMetric(metrics, label, "暂无数据"));
    }
    card.append(metrics);
    const details = element("button", "button button-secondary", "查看详情");
    details.type = "button";
    details.addEventListener("click", () => openHistory(entry.server, state.historyRange));
    card.append(details);
    return card;
  }

  function appendMetric(container, label, value) {
    const item = element("div");
    item.append(element("span", "", label));
    item.append(element("strong", "", value));
    container.append(item);
  }

  function renderAdminServers() {
    const list = byID("admin-server-list");
    list.replaceChildren();
    if (!state.overview) return;
    state.overview.servers.forEach((entry) => {
      const row = element("article", "admin-row");
      const identity = element("div");
      identity.append(element("strong", "", entry.server.name));
      identity.append(element("span", "muted", `${entry.server.group || "未分组"} · ${stateLabels[entry.state]}`));
      const edit = element("button", "button button-secondary", "编辑");
      edit.type = "button";
      edit.addEventListener("click", () => openServerEditor(entry.server));
      row.append(identity, edit);
      list.append(row);
    });
  }

  function syncSettingsForm() {
    if (!state.overview) return;
    const form = byID("settings-form");
    setField(form, "offline_threshold_seconds", state.overview.settings.offline_threshold_seconds);
    setField(form, "alert_threshold_seconds", state.overview.settings.alert_threshold_seconds);
    setField(form, "history_retention_days", state.overview.settings.history_retention_days);
    field(form, "alert_preference").checked = Boolean(state.overview.alerts_enabled);
    syncThresholdConstraint();
  }

  function syncThresholdConstraint() {
    const form = byID("settings-form");
    const offline = field(form, "offline_threshold_seconds");
    const alert = field(form, "alert_threshold_seconds");
    alert.min = offline.value || "1";
    alert.setCustomValidity(Number(alert.value) < Number(offline.value)
      ? "告警阈值不能小于离线判定。"
      : "");
  }

  function field(form, name) {
    return form.elements.namedItem(name);
  }

  function setField(form, name, value) {
    field(form, name).value = String(value);
  }

  function serverPayload(form) {
    return {
      name: field(form, "name").value.trim(),
      group: field(form, "group").value.trim(),
      sort_order: Number(field(form, "sort_order").value),
      enabled: field(form, "enabled").checked,
    };
  }

  function setFormBusy(form, busy) {
    Array.from(form.elements).forEach((control) => {
      control.disabled = busy;
    });
    form.setAttribute("aria-busy", busy ? "true" : "false");
  }

  async function createServer(event) {
    event.preventDefault();
    const form = event.currentTarget;
    if (!form.reportValidity()) return;
    setFormBusy(form, true);
    clearError();
    try {
      const response = await request("/api/v1/admin/servers", {
        method: "POST",
        body: JSON.stringify(serverPayload(form)),
      });
      form.reset();
      setField(form, "sort_order", 0);
      field(form, "enabled").checked = true;
      await loadOverview({ quiet: true });
      showToken(response.agent_token);
    } catch (error) {
      showError(error, "名称不能为空，名称和分组最多 120 字节，排序必须在 int32 范围内。");
    } finally {
      setFormBusy(form, false);
    }
  }

  function openServerEditor(server) {
    state.selectedServer = { ...server };
    const form = byID("edit-server-form");
    setField(form, "name", server.name);
    setField(form, "group", server.group);
    setField(form, "sort_order", server.sort_order);
    field(form, "enabled").checked = Boolean(server.enabled);
    setField(form, "delete_confirmation", "");
    byID("server-dialog").showModal();
  }

  function closeServerEditor() {
    const dialog = byID("server-dialog");
    if (dialog.open) dialog.close();
    state.selectedServer = null;
    setField(byID("edit-server-form"), "delete_confirmation", "");
  }

  async function updateServer(event) {
    event.preventDefault();
    if (!state.selectedServer) return;
    const form = event.currentTarget;
    if (!form.reportValidity()) return;
    setFormBusy(form, true);
    clearError();
    try {
      await request(`/api/v1/admin/servers/${state.selectedServer.id}`, {
        method: "PUT",
        body: JSON.stringify(serverPayload(form)),
      });
      closeServerEditor();
      await loadOverview({ quiet: true });
      setMessage("服务器信息已保存。");
    } catch (error) {
      showError(error, "请检查服务器名称、分组与排序值。");
    } finally {
      setFormBusy(form, false);
    }
  }

  async function rotateToken() {
    if (!state.selectedServer) return;
    const form = byID("edit-server-form");
    setFormBusy(form, true);
    clearError();
    try {
      const response = await request(`/api/v1/admin/servers/${state.selectedServer.id}/rotate-token`, {
        method: "POST",
        body: JSON.stringify({}),
      });
      closeServerEditor();
      showToken(response.agent_token);
    } catch (error) {
      showError(error);
    } finally {
      setFormBusy(form, false);
    }
  }

  async function deleteServer() {
    if (!state.selectedServer) return;
    const form = byID("edit-server-form");
    const confirmation = field(form, "delete_confirmation");
    confirmation.setCustomValidity(confirmation.value === state.selectedServer.name ? "" : "请输入完整服务器名称");
    if (!confirmation.reportValidity()) return;
    setFormBusy(form, true);
    clearError();
    try {
      await request(`/api/v1/admin/servers/${state.selectedServer.id}`, { method: "DELETE" });
      closeServerEditor();
      await loadOverview({ quiet: true });
      setMessage("服务器已删除。");
    } catch (error) {
      showError(error);
    } finally {
      confirmation.setCustomValidity("");
      setFormBusy(form, false);
    }
  }

  async function saveSettings(event) {
    event.preventDefault();
    const form = event.currentTarget;
    syncThresholdConstraint();
    if (!form.reportValidity()) return;
    setFormBusy(form, true);
    clearError();
    try {
      await request("/api/v1/admin/settings", {
        method: "PUT",
        body: JSON.stringify({
          offline_threshold_seconds: Number(field(form, "offline_threshold_seconds").value),
          alert_threshold_seconds: Number(field(form, "alert_threshold_seconds").value),
          history_retention_days: Number(field(form, "history_retention_days").value),
        }),
      });
      await request("/api/v1/admin/alert-preference", {
        method: "PUT",
        body: JSON.stringify({ enabled: field(form, "alert_preference").checked }),
      });
      await loadOverview({ quiet: true });
      setMessage("监控设置已保存。");
    } catch (error) {
      showError(error, "阈值应为 1–86400 秒，历史保留应为 1–365 天。");
    } finally {
      setFormBusy(form, false);
    }
  }

  function showToken(token) {
    state.token = typeof token === "string" ? token : "";
    byID("token-value").textContent = state.token;
    byID("token-dialog").showModal();
  }

  function clearToken() {
    state.token = null;
    const value = byID("token-value");
    if (value) value.textContent = "";
  }

  function dismissToken() {
    clearToken();
    const dialog = byID("token-dialog");
    if (dialog.open) dialog.close();
  }

  async function copyToken() {
    if (!state.token) return;
    try {
      await navigator.clipboard.writeText(state.token);
      setMessage("Agent 令牌已复制。");
    } catch (_) {
      setMessage("无法自动复制，请手动选择令牌。");
    }
  }

  async function openHistory(server, range) {
    state.historyServer = { ...server };
    state.historyRange = range;
    showView("history");
    byID("history-title").textContent = server.name;
    byID("history-subtitle").textContent = server.group || "未分组";
    document.querySelectorAll("[data-range]").forEach((button) => {
      button.setAttribute("aria-pressed", button.dataset.range === range ? "true" : "false");
    });
    if (state.historyController) state.historyController.abort();
    const controller = new AbortController();
    state.historyController = controller;
    byID("history-loading").hidden = false;
    byID("history-content").hidden = true;
    byID("history-empty").hidden = true;
    clearError();
    const toMS = Date.now();
    const fromMS = toMS - rangeMilliseconds[range];
    try {
      const history = await request(`/api/v1/admin/servers/${server.id}/history?from_ms=${fromMS}&to_ms=${toMS}`, {
        signal: controller.signal,
      });
      if (state.historyController !== controller) return;
      renderHistory(history.samples || []);
    } catch (error) {
      if (error && error.name === "AbortError") return;
      showError(error);
    } finally {
      if (state.historyController === controller) {
        byID("history-loading").hidden = true;
        state.historyController = null;
      }
    }
  }

  function renderHistory(samples) {
    const empty = samples.length === 0;
    byID("history-empty").hidden = !empty;
    byID("history-content").hidden = empty;
    const rows = byID("history-rows");
    rows.replaceChildren();
    buildChart(byID("cpu-chart"), samples, (sample) => sample.cpu_pct, byID("cpu-summary"));
    buildChart(byID("memory-chart"), samples, (sample) => ratioPercent(sample.memory_used_bytes, sample.memory_total_bytes), byID("memory-summary"));
    samples.forEach((sample) => {
      const row = element("tr");
      const memory = formatPercent(ratioPercent(sample.memory_used_bytes, sample.memory_total_bytes));
      const disk = formatPercent(ratioPercent(sample.root_disk_used_bytes, sample.root_disk_total_bytes));
      const network = `↓${formatRate(sample.network_rx_bytes_per_second)} ↑${formatRate(sample.network_tx_bytes_per_second)}`;
      const host = sample.system ? `${sample.system.hostname} · ${sample.system.os} · ${sample.system.arch}` : "未知";
      [
        formatTime(sample.bucket), formatPercent(sample.cpu_pct), memory, disk,
        `${formatNumber(sample.load_1)} / ${formatNumber(sample.load_5)} / ${formatNumber(sample.load_15)}`,
        network, formatUptime(sample.uptime_seconds), host,
      ].forEach((value) => row.append(element("td", "", value)));
      rows.append(row);
    });
  }

  function buildChart(svg, samples, valueForSample, summary) {
    svg.replaceChildren();
    const baseline = document.createElementNS("http://www.w3.org/2000/svg", "path");
    baseline.setAttribute("class", "chart-baseline");
    baseline.setAttribute("d", "M 12 168 L 628 168");
    svg.append(baseline);
    if (samples.length === 0) {
      summary.textContent = "";
      return;
    }
    const values = samples.map((sample) => clamp(Number(valueForSample(sample)), 0, 100));
    const denominator = Math.max(1, values.length - 1);
    const commands = values.map((value, index) => {
      const x = 12 + (index / denominator) * 616;
      const y = 168 - (value / 100) * 156;
      return `${index === 0 ? "M" : "L"} ${x.toFixed(2)} ${y.toFixed(2)}`;
    });
    const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
    path.setAttribute("d", commands.join(" "));
    svg.append(path);
    summary.textContent = `${formatPercent(Math.min(...values))}–${formatPercent(Math.max(...values))}`;
  }

  function clamp(value, minimum, maximum) {
    if (!Number.isFinite(value)) return minimum;
    return Math.min(maximum, Math.max(minimum, value));
  }

  function ratioPercent(used, total) {
    const denominator = Number(total);
    return denominator > 0 ? (Number(used) / denominator) * 100 : 0;
  }

  function formatPercent(value) {
    return `${clamp(Number(value), 0, 100).toFixed(1)}%`;
  }

  function formatNumber(value) {
    const number = Number(value);
    return Number.isFinite(number) ? number.toFixed(2) : "—";
  }

  function formatTime(value) {
    const date = new Date(Number(value));
    return Number.isFinite(date.getTime()) ? date.toLocaleString() : "未知";
  }

  function formatAge(receivedAt, now) {
    const seconds = Math.max(0, Math.floor((Number(now) - Number(receivedAt)) / 1000));
    if (seconds < 60) return `${seconds} 秒前`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`;
    return `${Math.floor(seconds / 86400)} 天前`;
  }

  function formatRate(bytes) {
    const value = Number(bytes);
    if (!Number.isFinite(value) || value < 0) return "—";
    if (value < 1024) return `${value.toFixed(0)} B/s`;
    if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB/s`;
    return `${(value / (1024 * 1024)).toFixed(1)} MiB/s`;
  }

  function formatUptime(seconds) {
    const value = Math.max(0, Number(seconds));
    if (!Number.isFinite(value)) return "—";
    const days = Math.floor(value / 86400);
    const hours = Math.floor((value % 86400) / 3600);
    return days > 0 ? `${days} 天 ${hours} 小时` : `${hours} 小时`;
  }

  async function logout() {
    clearError();
    try {
      await request("/api/v1/auth/logout", { method: "POST" });
      state.loginAttempted = true;
      showAuthenticationRequired("已安全退出。请从配置的 Telegram Bot 重新打开 Mini App。");
    } catch (error) {
      showError(error);
    }
  }

  function wireEvents() {
    document.querySelectorAll("[data-view]").forEach((button) => {
      button.addEventListener("click", () => {
        const view = button.dataset.view;
        showView(view);
        if (view === "servers") renderAdminServers();
        if (view === "settings") syncSettingsForm();
      });
    });
    document.querySelectorAll("[data-range]").forEach((button) => {
      button.addEventListener("click", () => {
        if (state.historyServer) void openHistory(state.historyServer, button.dataset.range);
      });
    });
    byID("history-back").addEventListener("click", () => showView("dashboard"));
    byID("create-server-form").addEventListener("submit", createServer);
    byID("edit-server-form").addEventListener("submit", updateServer);
    byID("server-dialog-close").addEventListener("click", closeServerEditor);
    byID("server-dialog").addEventListener("close", () => { state.selectedServer = null; });
    byID("rotate-token").addEventListener("click", rotateToken);
    byID("delete-server").addEventListener("click", deleteServer);
    const settingsForm = byID("settings-form");
    settingsForm.addEventListener("submit", saveSettings);
    const offline = byID("offline-threshold");
    const alert = byID("alert-threshold");
    offline.addEventListener("input", syncThresholdConstraint);
    alert.addEventListener("input", syncThresholdConstraint);
    byID("token-copy").addEventListener("click", copyToken);
    byID("token-close").addEventListener("click", dismissToken);
    byID("token-done").addEventListener("click", dismissToken);
    byID("token-dialog").addEventListener("close", clearToken);
    byID("logout-button").addEventListener("click", logout);
    byID("bootstrap-retry").addEventListener("click", () => {
      state.loginAttempted = false;
      void bootstrap();
    });
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible" && state.authenticated) void loadOverview({ quiet: true });
    });
    window.addEventListener("pagehide", () => {
      if (state.historyController) state.historyController.abort();
      clearToken();
    });
  }

  function start() {
    applyTelegramTheme();
    wireEvents();
    void bootstrap();
    window.setInterval(() => {
      if (document.visibilityState === "visible" && state.authenticated) void loadOverview({ quiet: true });
    }, 15000);
  }

  start();
})();
