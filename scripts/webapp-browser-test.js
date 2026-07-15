async (page) => {
  const context = page.context();
  const baseURL = page.url().replace(/\/healthz(?:\?.*)?$/, "");
  const telegramSDK = "https://telegram.org/js/telegram-web-app.js";
  const consoleErrors = [];
  const pageErrors = [];
  const failedRequests = [];
  const badResponses = [];
  const requests = [];

  const invariant = (condition, message) => {
    if (!condition) throw new Error(message);
  };
  const waitForText = async (selector, text) => {
    await page.waitForFunction(({ target, expected }) => {
      const element = document.querySelector(target);
      return element && element.textContent.includes(expected);
    }, { target: selector, expected: text });
    const visibility = await page.locator(selector).evaluate((element) => {
      const hiddenAncestor = element.closest("[hidden]");
      const style = getComputedStyle(element);
      const rect = element.getBoundingClientRect();
      return {
        hiddenAncestor: hiddenAncestor ? hiddenAncestor.id || hiddenAncestor.tagName : "",
        display: style.display,
        visibility: style.visibility,
        width: rect.width,
        height: rect.height,
      };
    });
    invariant(
      !visibility.hiddenAncestor && visibility.display !== "none" && visibility.visibility !== "hidden" && visibility.width > 0 && visibility.height > 0,
      `${selector} contains ${text} but is not visible: ${JSON.stringify(visibility)}`,
    );
  };
  const assertNoPageOverflow = async (label) => {
    const dimensions = await page.evaluate(() => ({
      viewport: document.documentElement.clientWidth,
      document: document.documentElement.scrollWidth,
    }));
    invariant(
      dimensions.document <= dimensions.viewport,
      `${label} overflows horizontally: ${dimensions.document} > ${dimensions.viewport}`,
    );
  };
  const assertDialogFits = async (selector, label) => {
    const bounds = await page.locator(selector).evaluate((dialog) => {
      const rect = dialog.getBoundingClientRect();
      return {
        left: rect.left,
        top: rect.top,
        right: rect.right,
        bottom: rect.bottom,
        viewportWidth: window.innerWidth,
        viewportHeight: window.innerHeight,
      };
    });
    invariant(
      bounds.left >= 0 && bounds.top >= 0 && bounds.right <= bounds.viewportWidth && bounds.bottom <= bounds.viewportHeight,
      `${label} exceeds the viewport: ${JSON.stringify(bounds)}`,
    );
  };
  const assertTokenCleared = async (token, label) => {
    const state = await page.evaluate((rawToken) => ({
      visible: document.documentElement.textContent.includes(rawToken),
      html: document.documentElement.outerHTML.includes(rawToken),
      localStorage: Object.values(localStorage).includes(rawToken),
      sessionStorage: Object.values(sessionStorage).includes(rawToken),
    }), token);
    invariant(!state.visible && !state.html && !state.localStorage && !state.sessionStorage, `${label} remained in browser state`);
  };

  page.on("console", (message) => {
    const expectedSessionProbe = message.type() === "error"
      && message.text().includes("Failed to load resource")
      && message.location().url === `${baseURL}/api/v1/auth/session`;
    if (message.type() === "error" && !expectedSessionProbe) consoleErrors.push(message.text());
  });
  page.on("pageerror", (error) => pageErrors.push(error.message));
  page.on("response", (response) => {
    const expectedSessionProbe = response.status() === 401 && response.url() === `${baseURL}/api/v1/auth/session`;
    if (response.status() >= 400 && !expectedSessionProbe) {
      badResponses.push(`${response.status()} ${response.request().method()} ${response.url()}`);
    }
  });
  page.on("requestfailed", (request) => {
    const errorText = request.failure() ? request.failure().errorText : "unknown";
    const expectedNoContentAbort = errorText === "net::ERR_ABORTED" && (
      request.url() === `${baseURL}/api/v1/auth/telegram`
      || request.url() === `${baseURL}/api/v1/auth/logout`
      || (request.method() === "DELETE" && /\/api\/v1\/admin\/servers\/\d+$/.test(request.url()))
    );
    if (!expectedNoContentAbort) failedRequests.push(`${errorText} ${request.method()} ${request.url()}`);
  });
  page.on("request", (request) => requests.push({ url: request.url(), type: request.resourceType() }));

  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: baseURL });
  await page.setViewportSize({ width: 320, height: 800 });
  await page.goto(`${baseURL}/app/`, { waitUntil: "domcontentloaded" });
  await page.locator("#dashboard-view:not([hidden])").waitFor({ state: "visible" });
  await waitForText("#stat-total", "1");
  await waitForText("#stat-online", "1");
  invariant(await page.evaluate(() => Boolean(window.__tgReady && window.__tgExpanded)), "Telegram bridge was not initialized");
  await assertNoPageOverflow("mobile dashboard");

  await page.evaluate(() => document.activeElement && document.activeElement.blur());
  await page.keyboard.press("Tab");
  const focus = await page.evaluate(() => {
    const active = document.activeElement;
    if (!(active instanceof HTMLElement)) return { tag: "", visible: false, outline: "none" };
    const style = getComputedStyle(active);
    const rect = active.getBoundingClientRect();
    return {
      tag: active.tagName,
      visible: rect.width >= 1 && rect.height >= 1,
      outline: style.outlineStyle,
    };
  });
  invariant(focus.tag === "BUTTON" && focus.visible && focus.outline !== "none", `keyboard focus is not visibly styled: ${JSON.stringify(focus)}`);
  await page.screenshot({ path: "output/playwright/webapp/mobile-light.png", fullPage: true });

  const initialCard = page.locator("#server-list .server-card").filter({ hasText: "browser-host" });
  await initialCard.getByRole("button", { name: "查看详情" }).click();
  await page.locator("#history-view:not([hidden])").waitFor({ state: "visible" });
  await page.locator("#history-content:not([hidden])").waitFor({ state: "visible" });
  invariant(await page.locator("#history-rows tr").count() >= 1, "1h history did not render a real sample");
  await page.locator("[data-range='7d']").click();
  await page.locator("[data-range='7d'][aria-pressed='true']").waitFor();
  await page.locator("#history-content:not([hidden])").waitFor({ state: "visible" });
  invariant(await page.locator("#history-rows tr").count() >= 1, "7d history did not render a real sample");
  await assertNoPageOverflow("mobile history");
  await page.locator("#history-back").click();

  await page.getByRole("button", { name: "服务器" }).click();
  await assertNoPageOverflow("mobile administration");
  const createForm = page.locator("#create-server-form");
  await createForm.locator("input[name='name']").fill("browser-created");
  await createForm.locator("input[name='group']").fill("qa");
  await createForm.locator("input[name='sort_order']").fill("7");
  await createForm.getByRole("button", { name: "创建并生成 Agent token" }).click();
  await page.locator("#token-dialog[open]").waitFor({ state: "visible" });
  await assertDialogFits("#token-dialog", "create token dialog");
  const createdToken = (await page.locator("#token-value").textContent()) || "";
  invariant(createdToken.length >= 32, "create did not return a one-time token");
  await page.locator("#token-copy").click();
  await waitForText("#global-message", "Agent token 已复制");
  invariant(await page.evaluate(() => navigator.clipboard.readText()) === createdToken, "created token was not copied to clipboard");
  await page.locator("#token-done").click();
  await page.locator("#token-dialog").waitFor({ state: "hidden" });
  await assertTokenCleared(createdToken, "created token");

  let createdRow = page.locator("#admin-server-list .admin-row").filter({ hasText: "browser-created" });
  await createdRow.waitFor({ state: "visible" });
  await createdRow.getByRole("button", { name: "编辑" }).click();
  const editor = page.locator("#edit-server-form");
  await editor.locator("input[name='name']").fill("browser-edited");
  await editor.locator("input[name='group']").fill("staging");
  await editor.locator("input[name='enabled']").uncheck();
  await editor.getByRole("button", { name: "保存", exact: true }).click();
  await page.locator("#server-dialog").waitFor({ state: "hidden" });

  const editedRow = page.locator("#admin-server-list .admin-row").filter({ hasText: "browser-edited" });
  await editedRow.waitFor({ state: "visible" });
  await waitForText("#admin-server-list", "staging · 停用");
  await editedRow.getByRole("button", { name: "编辑" }).click();
  await page.locator("#server-dialog[open]").waitFor({ state: "visible" });
  await assertDialogFits("#server-dialog", "server editor dialog");
  await page.locator("#rotate-token").click();
  await page.locator("#token-dialog[open]").waitFor({ state: "visible" });
  await assertDialogFits("#token-dialog", "rotate token dialog");
  const rotatedToken = (await page.locator("#token-value").textContent()) || "";
  invariant(rotatedToken.length >= 32 && rotatedToken !== createdToken, "rotate did not replace the one-time token");
  await page.locator("#token-copy").click();
  await waitForText("#global-message", "Agent token 已复制");
  invariant(await page.evaluate(() => navigator.clipboard.readText()) === rotatedToken, "rotated token was not copied to clipboard");
  await page.locator("#token-close").click();
  await page.locator("#token-dialog").waitFor({ state: "hidden" });
  await assertTokenCleared(rotatedToken, "rotated token");

  await page.getByRole("button", { name: "设置" }).click();
  const settings = page.locator("#settings-form");
  await settings.locator("input[name='offline_threshold_seconds']").fill("75");
  await settings.locator("input[name='alert_threshold_seconds']").fill("150");
  await settings.locator("input[name='history_retention_days']").fill("30");
  await settings.locator("input[name='alert_preference']").check();
  await settings.getByRole("button", { name: "保存设置" }).click();
  await waitForText("#global-message", "监控设置已保存");
  invariant(await settings.locator("input[name='alert_preference']").isChecked(), "alert preference did not remain enabled");
  await assertNoPageOverflow("mobile settings");

  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto(`${baseURL}/app/?view=desktop`, { waitUntil: "domcontentloaded" });
  await page.locator("#dashboard-view:not([hidden])").waitFor({ state: "visible" });
  await waitForText("#stat-total", "2");
  await page.getByRole("button", { name: "服务器" }).click();
  await page.locator("#servers-view:not([hidden])").waitFor({ state: "visible" });
  await editedRow.waitFor({ state: "visible" });
  invariant(await page.locator("[data-view='servers']").getAttribute("aria-current") === "page", "desktop administration view did not become active");
  await assertNoPageOverflow("desktop administration");
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.screenshot({ path: "output/playwright/webapp/desktop-admin.png", fullPage: true });

  await page.setViewportSize({ width: 320, height: 800 });
  await page.goto(`${baseURL}/app/?theme=dark#dark`, { waitUntil: "domcontentloaded" });
  await page.locator("#dashboard-view:not([hidden])").waitFor({ state: "visible" });
  await waitForText("#stat-total", "2");
  const themeColor = await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--tg-theme-bg-color").trim());
  invariant(themeColor.toLowerCase() === "#111827", `dark Telegram theme was not applied: ${themeColor}`);
  const darkForegrounds = await page.locator("#dashboard-title, #stat-total, .server-card h3, .metric-grid strong").evaluateAll((elements) => (
    elements.map((element) => ({ text: element.textContent.trim(), color: getComputedStyle(element).color }))
  ));
  const darkThemeState = await page.evaluate(() => ({
    inlineAppText: document.documentElement.style.getPropertyValue("--app-text"),
    computedAppText: getComputedStyle(document.documentElement).getPropertyValue("--app-text").trim(),
    bodyColor: getComputedStyle(document.body).color,
  }));
  invariant(
    darkForegrounds.every(({ color }) => color === "rgb(249, 250, 251)"),
    `dark foreground contrast mismatch: ${JSON.stringify({ darkThemeState, darkForegrounds })}`,
  );
  await assertNoPageOverflow("mobile dark dashboard");
  await page.screenshot({ path: "output/playwright/webapp/mobile-dark.png", fullPage: true });

  await page.getByRole("button", { name: "服务器" }).click();
  const deleteRow = page.locator("#admin-server-list .admin-row").filter({ hasText: "browser-edited" });
  await deleteRow.getByRole("button", { name: "编辑" }).click();
  await page.locator("#server-dialog[open]").waitFor({ state: "visible" });
  await assertDialogFits("#server-dialog", "server delete dialog");
  await assertNoPageOverflow("mobile server dialog");
  await editor.locator("input[name='delete_confirmation']").fill("browser-edited");
  await page.locator("#delete-server").click();
  await page.locator("#server-dialog").waitFor({ state: "hidden" });
  invariant(await page.locator("#admin-server-list .admin-row").filter({ hasText: "browser-edited" }).count() === 0, "deleted server remained in the admin list");

  await page.getByRole("button", { name: "概览" }).click();
  await waitForText("#stat-total", "1");
  await page.locator("#logout-button").click();
  await page.locator("#bootstrap-view:not([hidden])").waitFor({ state: "visible" });
  await waitForText("#bootstrap-message", "已安全退出");

  await page.evaluate(() => navigator.clipboard.writeText(""));
  const storage = await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length }));
  invariant(storage.local === 0 && storage.session === 0, `browser storage is not empty: ${JSON.stringify(storage)}`);
  const unexpectedOrigins = requests.filter(({ url }) => !url.startsWith(baseURL) && url !== telegramSDK);
  const crossOriginData = requests.filter(({ url, type }) => (type === "fetch" || type === "xhr") && !url.startsWith(baseURL));
  invariant(unexpectedOrigins.length === 0, `unexpected request origins: ${JSON.stringify(unexpectedOrigins)}`);
  invariant(crossOriginData.length === 0, `cross-origin data requests: ${JSON.stringify(crossOriginData)}`);
  invariant(consoleErrors.length === 0, `console errors: ${JSON.stringify(consoleErrors)}`);
  invariant(pageErrors.length === 0, `page errors: ${JSON.stringify(pageErrors)}`);
  invariant(badResponses.length === 0, `unexpected HTTP responses: ${JSON.stringify(badResponses)}`);
  invariant(failedRequests.length === 0, `failed requests: ${JSON.stringify(failedRequests)}`);

  return {
    webapp_browser: "ok",
    keyboard: "ok",
    network_origin: "clean",
    screenshots: 3,
  };
}
