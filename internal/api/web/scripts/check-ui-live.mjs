import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { existsSync } from "node:fs";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { randomBytes } from "node:crypto";
import http from "node:http";
import { chromium, expect } from "@playwright/test";
import { auditPanelSpacing, verifyQualityViews, verifySettingsCategories } from "./check-quality-ui.mjs";

const binary = process.env.PRISM_TEST_BACKEND || process.env.PRISMX_TEST_BACKEND || fileURLToPath(new URL("../../../../bin/prism", import.meta.url));
if (!existsSync(binary))
  throw new Error("Build bin/prism first, or set PRISM_TEST_BACKEND to a backend binary.");
const executablePath =
  process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE ||
  (existsSync(
    "/home/ermit/.cache/ms-playwright/chromium-1234/chrome-linux64/chrome",
  )
    ? "/home/ermit/.cache/ms-playwright/chromium-1234/chrome-linux64/chrome"
    : undefined);
const root = await mkdtemp(join(tmpdir(), "prism-live-test-"));
const adminToken = randomBytes(32).toString("hex");
const proxyToken = randomBytes(32).toString("hex");
const reservation = http.createServer();
reservation.listen(0, "127.0.0.1");
await once(reservation, "listening");
const backendPort = reservation.address().port;
await new Promise((resolve) => reservation.close(resolve));
// Prism is single-port: the panel is served from the main listener under /ui/,
// so the UI assertions target the same port as the API. (A separate UI port
// belonged to the pre-WP03 entrypoint.)
const panelPort = backendPort;
const backend = spawn(binary, [], {
  cwd: root,
  env: {
    ...process.env,
    RESIN_ADMIN_TOKEN: adminToken,
    RESIN_PROXY_TOKEN: proxyToken,
    RESIN_STATE_DIR: join(root, "state"),
    RESIN_CACHE_DIR: join(root, "cache"),
    RESIN_LOG_DIR: join(root, "logs"),
    RESIN_LISTEN_ADDRESS: "127.0.0.1",
    RESIN_PORT: String(backendPort),
    PRISM_UI_HOST: "127.0.0.1",
    PRISM_UI_PORT: String(panelPort),
    RESIN_PROBE_CONCURRENCY: "2",
    RESIN_RESOURCE_FETCH_TIMEOUT: "2s",
    PRISM_QUALITY_ENABLED: "true",
    PRISM_QUALITY_API_KEY: "",
    PRISM_ABUSEIPDB_API_KEY: "",
    PRISM_QUALITY_DAILY_LIMIT: "10",
    PRISM_QUALITY_WORKERS: "1",
    PRISM_QUALITY_QUEUE_SIZE: "16",
  },
  stdio: ["ignore", "ignore", "pipe"],
});
let backendError = "";
backend.stderr.on("data", (data) => {
  backendError = (backendError + data).slice(-4000);
});
const backendExit = once(backend, "exit");
let browser;
let failurePage;
const screenshots = fileURLToPath(
  new URL("../test-results/screenshots/", import.meta.url),
);
await mkdir(screenshots, { recursive: true });
const output = [];
try {
  let ready = false;
  for (let i = 0; i < 100; i++) {
    if (backend.exitCode !== null)
      throw new Error("Test backend exited: " + backendError);
    try {
      ready = (
        await fetch(`http://127.0.0.1:${backendPort}/api/v1/system/info`, {
          headers: { Authorization: "Bearer " + adminToken },
          signal: AbortSignal.timeout(500),
        })
      ).ok;
      if (ready) break;
    } catch {
      /* Wait for the child to start listening. */
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  assert(ready, "Isolated backend must start before UI assertions");
  const origin = `http://127.0.0.1:${panelPort}`;
  assert.equal((await fetch(origin + "/healthz")).status, 200);
  assert.equal((await fetch(origin + "/api/v1/system/info")).status, 401);
  browser = await chromium.launch({
    headless: true,
    executablePath,
    args: ["--no-sandbox"],
  });
  const context = await browser.newContext({
    locale: "zh-CN",
    viewport: { width: 1440, height: 1000 },
    reducedMotion: "reduce",
  });
  await context.addInitScript(() => {
    localStorage.setItem("prism.locale", "zh-CN");
  });
  const page = await context.newPage();
  failurePage = page;
  const errors = [];
  const apiErrors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("response", (response) => { if (response.url().includes("/api/") && response.status() >= 500) apiErrors.push(`${response.status()} ${new URL(response.url()).pathname}`); });
  await page.goto(origin + "/ui/login");
  await expect(page.locator("#token")).toBeVisible();
  await page.screenshot({ path: join(screenshots, "login-light.png"), fullPage: true });
  await page.locator("#token").fill(adminToken);
  await page.getByRole("button", { name: "进入工作台" }).click();
  await expect(page).toHaveURL(/\/ui\/dashboard$/);
  await expect(page.getByText("服务已连接", { exact: true })).toBeVisible();
  assert.equal(
    await page.evaluate(() => localStorage.getItem("prism.admin-session")),
    null,
  );
  assert.equal(await page.evaluate(() => sessionStorage.getItem("prismx.admin-session")), null);

  const migrationContext = await browser.newContext({ locale: "zh-CN" });
  await migrationContext.addInitScript(({ adminToken, proxyToken }) => {
    if (sessionStorage.getItem("prism.test.seeded")) return;
    sessionStorage.setItem("prism.test.seeded", "1");
    sessionStorage.setItem("prismx.admin-session", adminToken);
    sessionStorage.setItem("prismx.proxy-session-token", proxyToken);
    localStorage.setItem("prismx.theme", "dark");
    localStorage.setItem("resin.webui.locale", "zh-CN");
  }, { adminToken, proxyToken });
  const migrationPage = await migrationContext.newPage();
  await migrationPage.goto(origin + "/ui/dashboard");
  await expect(migrationPage.getByText("服务已连接", { exact: true })).toBeVisible();
  await expect(migrationPage.locator("html")).toHaveAttribute("data-theme", "dark");
  assert(await migrationPage.evaluate((token) => sessionStorage.getItem("prism.admin-session") === token, adminToken));
  assert.equal(await migrationPage.evaluate(() => sessionStorage.getItem("prismx.admin-session")), null);
  assert.equal(await migrationPage.evaluate(() => localStorage.getItem("prism.theme")), "dark");
  assert.equal(await migrationPage.evaluate(() => localStorage.getItem("prism.locale")), "zh-CN");
  await migrationPage.getByRole("button", { name: "退出登录", exact: true }).click();
  await expect(migrationPage.locator("#token")).toBeVisible();
  assert(await migrationPage.evaluate(() => ["prism.admin-session", "prismx.admin-session", "prism.proxy-session-token", "prismx.proxy-session-token"].every((key) => sessionStorage.getItem(key) === null)));
  await migrationPage.reload();
  await expect(migrationPage.locator("#token")).toBeVisible();
  await migrationContext.close();

  const privateContext = await browser.newContext({ locale: "zh-CN" });
  await privateContext.addInitScript(() => {
    for (const key of ["localStorage", "sessionStorage"]) {
      Object.defineProperty(window, key, { get() { throw new DOMException("Storage disabled", "SecurityError"); } });
    }
  });
  const privatePage = await privateContext.newPage();
  const privateErrors = [];
  privatePage.on("pageerror", (error) => privateErrors.push(error.message));
  await privatePage.goto(origin + "/ui/login");
  await privatePage.locator("#token").fill(adminToken);
  await privatePage.getByRole("button", { name: "进入工作台", exact: true }).click();
  await expect(privatePage.getByText("服务已连接", { exact: true })).toBeVisible();
  assert.deepEqual(privateErrors, [], "Storage restrictions must not break startup or sign-in");
  await privateContext.close();
  output.push("legacy preferences and tokens migrate, logout clears credentials, restricted storage supports sign-in");

  // Create actual local inventory in the isolated backend.
  await page.getByRole("link", { name: "添加订阅", exact: true }).click();
  await expect(page).toHaveURL(/\/ui\/subscriptions\?create=1$/, { timeout: 15000 });
  const create = page.getByRole("dialog");
  await expect(create).toBeVisible({ timeout: 15000 });
  await create.locator("#create-sub-name").fill("UI_Local");
  await create.getByRole("tab", { name: "本地", exact: true }).click();
  await create.locator("#create-sub-content").fill(
    JSON.stringify({
      outbounds: [
        {
          type: "http",
          tag: "Local Alpha",
          server: "127.0.0.1",
          server_port: 9,
        },
        {
          type: "http",
          tag: "Local Beta",
          server: "127.0.0.1",
          server_port: 10,
        },
      ],
    }),
  );
  await create.getByRole("button", { name: "确认创建", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.getByText("UI_Local", { exact: true }).first(),
  ).toBeVisible();
  await page.getByRole("row").filter({ hasText: "UI_Local" }).getByRole("button", { name: "刷新", exact: true }).click();
  await expect(page.getByText("订阅 UI_Local 已手动刷新", { exact: true })).toBeVisible();
  output.push("login and local subscription creation");

  await page.goto(origin + "/ui/nodes");
  await expect(page.locator(".node-name")).toHaveCount(2, { timeout: 15000 });
  await page
    .getByRole("textbox", { name: "搜索节点", exact: true })
    .fill("Alpha");
  await expect(page.locator(".node-name")).toHaveCount(1);
  await page.locator(".node-name").first().click();
  await expect(
    page.getByRole("dialog", { name: "节点详情", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("dialog").getByText("纯净度与风险", { exact: true }),
  ).toBeVisible();
  await page.screenshot({ path: join(screenshots, "node-detail-light.png"), fullPage: true });
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "搜索节点", exact: true }),
  ).toHaveValue("Alpha");
  await page.screenshot({
    path: join(screenshots, "nodes-light.png"),
    fullPage: true,
  });
  output.push("node search, detail, unknown quality and return");

  await expect(page.getByRole("columnheader", { name: /纯净度 prism-purity-v2/ })).toBeVisible();
  await expect(page.getByRole("combobox", { name: "IP 类型", exact: true })).toBeVisible();
  await page.goto(origin + "/ui/quality?ip=8.8.8.8&q=fixture&page=1");
  await expect(page).toHaveURL(/\/ui\/nodes\?view=exits/);
  assert.equal(new URL(page.url()).searchParams.get("quality_ip"), "8.8.8.8");
  assert.equal(new URL(page.url()).searchParams.get("quality_q"), "fixture");
  assert.equal(new URL(page.url()).searchParams.get("quality_page"), "1");
  await expect(page.getByRole("dialog", { name: "IP 质量详情", exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  // WP09 moved the data sources out of the system-config "quality" stub category
  // and into the dedicated intel settings page, so assert them where they live.
  await page.goto(origin + "/ui/intel-settings");
  await expect(page.getByText(/proxycheck/i).first()).toBeVisible();
  await expect(page.getByText(/abuseipdb/i).first()).toBeVisible();
  await expect(page.getByText(/ippure/i).first()).toBeVisible();
  await page.screenshot({ path: join(screenshots, "quality-sources-light.png"), fullPage: true });
  await page.goto(origin + "/ui/quality");
  await expect(page.getByRole("heading", { name: "节点池", exact: true })).toBeVisible();
  await expect(page.getByText("为出口建立第一份质量记录", { exact: true })).toBeVisible();
  await page.screenshot({ path: join(screenshots, "quality-light.png"), fullPage: true });
  // The private-address rejection and the quota accounting are backend
  // guarantees. Assert them through the API: WP10 moved the ad-hoc "probe an IP"
  // widget out of the quality page, so no field is left to drive, and this
  // isolated backend deliberately has no provider credentials — so the request
  // is refused before it can reach a provider with a private address.
  const invalidIPResponse = await fetch(origin + "/api/v1/quality/ip/127.0.0.1/actions/probe", {
    method: "POST",
    headers: { Authorization: "Bearer " + adminToken },
  });
  assert(!invalidIPResponse.ok, "A private address must never be accepted by the public quality probe endpoint");
  const inspectionStatus = await fetch(origin + "/api/v1/quality/status", { headers: { Authorization: "Bearer " + adminToken } }).then(response => response.json());
  assert(inspectionStatus.sources.every(source => source.used_today === 0), "Rejected IPs must not consume provider quota");
  output.push("quality workspace, source setup, node quality columns and private-IP rejection");

  // Opt-in live smoke: one public-IP query, anonymous credentials, isolated
  // database. Routine browser regression never depends on an external provider.
  if (process.env.PRISM_TEST_LIVE_QUALITY === "1") {
    await page.getByRole("textbox", { name: "检测 IP 地址", exact: true }).fill("1.1.1.1");
    await page.getByRole("button", { name: "查询网络特征", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "IP 质量详情", exact: true })).toBeVisible();
    await expect.poll(async () => {
      const data = await fetch(origin + "/api/v1/quality/ip/1.1.1.1", { headers: { Authorization: "Bearer " + adminToken } }).then(response => response.json());
      if (data.task?.error_code) throw new Error("Live quality lookup: " + data.task.error_code);
      return data.state;
    }, { timeout: 30000 }).toBe("valid");
    await expect(page.getByRole("dialog").locator(".quality-network-facts").getByText("ProxyCheck v3", { exact: true })).toBeVisible();
    await page.screenshot({ path: join(screenshots, "quality-live-detail.png"), fullPage: true });
    await page.keyboard.press("Escape");
    await page.screenshot({ path: join(screenshots, "quality-live.png"), fullPage: true });
    output.push("live ProxyCheck v3 lookup, persisted evidence and rendered risk indicators");
  }

  await verifyQualityViews({ page, origin, adminToken, screenshots });
  output.push("IPPure-only primary score, separate VPN warning, expiry retains active high risk, legacy search clears");
  await verifySettingsCategories({ page, origin, adminToken, screenshots });
  output.push("settings category cards, cross-category drafts, invalid-input protection, explicit JSON application, global save and read-only startup fields");

  await page.goto(origin + "/ui/platforms");
  await page.getByRole("button", { name: "新建", exact: true }).click();
  await page.locator("#create-name").fill("UI_Platform");
  await page.locator("#create-regex").fill("UI_Local\n!expired");
  await page.getByRole("button", { name: "确认创建", exact: true }).click();
  await expect(page).toHaveURL(/\/platforms\/[a-f0-9-]+$/);
  await page.getByRole("tab", { name: "配置", exact: true }).click();
  await page.locator("#detail-edit-regex").fill("UI_Local\n*Alpha\n!expired");
  await page.locator(".platform-config-actions button[type=submit]").click();
  await expect(
    page.getByText("平台 UI_Platform 已更新", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("tab", { name: "配置", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(page.locator("#detail-edit-regex")).toHaveValue(
    "UI_Local\n*Alpha\n!expired",
  );
  await page.getByRole("tab", { name: "接入", exact: true }).click();
  await expect(page.locator("#access-endpoint")).toHaveAttribute(
    "placeholder",
    `http://127.0.0.1:${backendPort}`,
  );
  output.push("platform creation, rule edit, reload and separate proxy port");

  await page.goto(origin + "/ui/dashboard");
  await expect(page.locator(".resource-total strong")).not.toHaveText("--");
  await expect(page.getByText("Default", { exact: true }).first()).toBeVisible();
  await page.screenshot({ path: join(screenshots, "desktop-light.png"), fullPage: true });

  const routes = [
    "dashboard",
    "nodes",
    "quality",
    "platforms",
    "subscriptions",
    "endpoints",
    "rules",
    "jobs",
    "request-logs",
    "resources",
    "audit",
    "exports",
    "system-config",
    "system-config?category=health",
    "system-config?category=logs",
    "system-config?category=storage",
    "system-config?category=network",
    "system-config?category=platform",
    "system-config?category=metrics",
    "system-config?category=deployment",
    "system-config?category=quality",
  ];
  for (const viewport of [
    { width: 1440, height: 1000 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    for (const route of routes) {
      await page.goto(origin + "/ui/" + route);
      await expect(
        page.locator(".content h1,.content h2").first(),
      ).toBeVisible();
      await expect(
        page.getByText("服务已连接", { exact: true }),
      ).toBeAttached();
      if (route.startsWith("system-config?category=")) await expect(page.locator(".settings-category-detail")).toBeVisible();
      await auditPanelSpacing(page, route, viewport.width);
      assert.equal(
        await page.evaluate(
          () =>
            document.documentElement.scrollWidth >
            document.documentElement.clientWidth,
        ),
        false,
        `Overflow: ${route} ${viewport.width}`,
      );
      if (["nodes", "platforms", "subscriptions", "endpoints", "rules", "request-logs", "resources", "system-config", "system-config?category=logs"].includes(route)) {
        await page.screenshot({ path: join(screenshots, route.replaceAll("?category=", "-") + "-" + viewport.width + "-light.png"), fullPage: true });
      }
    }
    output.push(`all routes ${viewport.width}px`);
  }
  await page.getByRole("button", { name: "打开导航", exact: true }).click();
  const mobileNav = page.getByRole("dialog", { name: "主导航", exact: true });
  await expect(
    mobileNav.getByRole("button", { name: "退出登录", exact: true }),
  ).toBeVisible();
  await expect(
    mobileNav.getByRole("group", { name: "切换语言", exact: true }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await page
    .getByRole("button", { name: "外观", exact: true })
    .filter({ visible: true })
    .click();
  await page.getByRole("menuitem", { name: "深色", exact: true }).click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await page.goto(origin + "/ui/dashboard");
  await expect(page.getByRole("heading", { name: "总览看板", exact: true })).toBeVisible();
  await expect(page.locator(".resource-total strong")).not.toHaveText("--");
  await expect(page.getByText("Default", { exact: true }).first()).toBeVisible();
  await page.screenshot({
    path: join(screenshots, "mobile-dark.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await expect.poll(async () => page.locator(".live-chart").evaluate((element) => {
    const chart = element.querySelector(".recharts-surface");
    return !chart || chart.getBoundingClientRect().width <= element.getBoundingClientRect().width + 2;
  })).toBe(true);
  await page.screenshot({
    path: join(screenshots, "desktop-dark.png"),
    fullPage: true,
  });
  for (const route of ["nodes", "subscriptions", "resources", "system-config", "system-config?category=logs"]) {
    await page.goto(origin + "/ui/" + route);
    await expect(page.locator(".content h1,.content h2").first()).toBeVisible();
    if (route.includes("category=")) await expect(page.locator(".settings-category-detail")).toBeVisible();
    await auditPanelSpacing(page, route, 1440);
    await page.screenshot({ path: join(screenshots, route.replaceAll("?category=", "-") + "-1440-dark.png"), fullPage: true });
  }
  await page.goto(origin + "/ui/dashboard");
  await page.keyboard.press("Control+k");
  await expect(
    page.getByRole("dialog", { name: "快速定位", exact: true }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await context.setOffline(true);
  await expect(
    page.getByText("当前处于离线状态", { exact: true }),
  ).toBeVisible();
  await context.setOffline(false);
  if (
    await page
      .getByRole("button", { name: "重新连接", exact: true })
      .isVisible()
  )
    await page.getByRole("button", { name: "重新连接", exact: true }).click();
  await expect(page.getByText("当前处于离线状态", { exact: true })).toHaveCount(
    0,
  );
  assert.deepEqual(errors, [], "No unhandled page errors");
  assert.deepEqual(apiErrors, [], "No backend failures hidden by empty states");
  assert.equal(
    await page
      .locator("img")
      .evaluateAll((images) =>
        images.some((image) => !image.complete || image.naturalWidth === 0),
      ),
    false,
  );
  output.push(
    "mobile controls, theme, keyboard navigation and offline recovery",
  );
  console.log(JSON.stringify({ passed: output, screenshots }, null, 2));
} catch (error) {
  if (failurePage && !failurePage.isClosed()) {
    await failurePage.screenshot({ path: join(screenshots, "failure.png"), fullPage: true }).catch(() => {});
    console.error("Browser regression page:", failurePage.url());
  }
  throw error;
} finally {
  if (browser) await browser.close();
  if (backend.exitCode === null) backend.kill("SIGTERM");
  const timer = setTimeout(() => backend.kill("SIGKILL"), 7000);
  await backendExit;
  clearTimeout(timer);
  await rm(root, { recursive: true, force: true });
}
