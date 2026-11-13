import assert from "node:assert/strict";
import http from "node:http";
import { once } from "node:events";
import { expect } from "@playwright/test";
import { assertOverlay, inspectSelects, saveSurface } from "./panel-surfaces.mjs";

async function choose(page, label, name) {
  await page.getByRole("combobox", { name: label, exact: true }).click();
  await page.getByRole("option", { name, exact: true }).click();
}

async function csrfHeaders(context) {
  const cookie = (await context.cookies()).find((item) => item.name === "prism_csrf");
  assert.ok(cookie);
  return { "X-Prism-CSRF": decodeURIComponent(cookie.value) };
}

export function registerPanelTaskChecks(check) {
  check("task: local subscription validates, creates, edits, refreshes, filters and deletes", async ({ page, origin }) => {
    await page.goto(origin + "/ui/ops/subscriptions");
    await page.getByRole("button", { name: "新建订阅", exact: true }).first().click();
    let dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "确认创建", exact: true }).click();
    await expect(dialog.locator(".workspace-form-error")).toContainText("订阅名称不能为空");
    await dialog.getByLabel("订阅名称", { exact: true }).fill("Workflow source");
    await dialog.getByRole("tab", { name: "本地", exact: true }).click();
    await dialog.getByRole("button", { name: "确认创建", exact: true }).click();
    await expect(dialog.locator(".workspace-form-error")).toContainText("订阅内容不能为空");
    await dialog.getByLabel("订阅内容", { exact: true }).fill('{"outbounds":[]}');
    await dialog.getByRole("switch", { name: "自动 Intel 检测" }).click();
    await dialog.getByRole("button", { name: "确认创建", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await page.getByLabel("搜索订阅", { exact: true }).fill("Workflow source");
    const row = page.locator(".workspace-object-row").filter({ hasText: "Workflow source" });
    await expect(row).toHaveCount(1);
    await row.click();
    dialog = page.getByRole("dialog");
    await dialog.getByLabel("订阅名称", { exact: true }).fill("Workflow source edited");
    await dialog.getByRole("button", { name: "保存配置", exact: true }).click();
    await expect(dialog.getByRole("heading", { name: "Workflow source edited", exact: true })).toBeVisible();
    await dialog.getByRole("button", { name: "立即刷新", exact: true }).click();
    await expect(page.locator(".toast-item").filter({ hasText: "已手动刷新" })).toBeVisible();
    await dialog.getByRole("button", { name: "停用", exact: true }).click();
    await expect(dialog.getByRole("button", { name: "启用", exact: true })).toBeVisible();
    await dialog.getByRole("button", { name: "启用", exact: true }).click();
    await expect(dialog.getByRole("button", { name: "停用", exact: true })).toBeVisible();
    await dialog.getByRole("button", { name: "立即清理", exact: true }).click();
    let confirm = page.getByRole("dialog", { name: "确认清理失效节点", exact: true });
    await assertOverlay(page, confirm);
    await confirm.getByRole("button", { name: "取消", exact: true }).click();
    await expect(dialog.getByRole("button", { name: "立即清理", exact: true })).toBeFocused();
    await dialog.getByRole("button", { name: "删除", exact: true }).click();
    confirm = page.getByRole("dialog", { name: "确认删除订阅", exact: true });
    await saveSurface(page, "subscription-delete");
    await confirm.getByRole("button", { name: "确认删除", exact: true }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(row).toHaveCount(0);
    await page.getByRole("button", { name: "清除筛选", exact: true }).first().click();
    await expect(page.locator(".workspace-object-row").filter({ hasText: "UI_Local" })).toBeVisible();
  });

  check("task: header rules validate, persist edits, resolve, cancel and confirm deletion", async ({ page, origin }) => {
    const prefix = "tasks.example.test";
    await page.goto(origin + "/ui/ops/rules");
    await page.getByRole("button", { name: "新建规则", exact: true }).first().click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "确认创建", exact: true }).click();
    await expect(dialog.locator(".workspace-form-error")).toContainText("地址前缀不能为空");
    await dialog.getByLabel("地址前缀", { exact: true }).fill(prefix);
    await dialog.getByLabel("请求头", { exact: true }).fill("X-Workflow");
    await dialog.getByRole("button", { name: "确认创建", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await page.getByLabel("搜索规则", { exact: true }).fill(prefix);
    const row = page.locator(".workspace-object-row").filter({ hasText: prefix });
    await row.getByRole("button", { name: "编辑", exact: true }).click();
    await expect(dialog.getByLabel("地址前缀", { exact: true })).toBeDisabled();
    await dialog.getByLabel("请求头", { exact: true }).fill("X-Workflow-Edited");
    await dialog.getByRole("button", { name: "保存规则", exact: true }).click();
    await expect(page.locator(".toast-item").filter({ hasText: "已保存" })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(row).toContainText("X-Workflow-Edited");
    await row.getByRole("button", { name: "删除", exact: true }).click();
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    await expect(row.getByRole("button", { name: "删除", exact: true })).toBeFocused();
    await row.getByRole("button", { name: "删除", exact: true }).click();
    await dialog.getByRole("button", { name: "确认删除", exact: true }).click();
    await expect(row).toHaveCount(0);
    await page.getByRole("button", { name: "清除筛选", exact: true }).first().click();
    await expect(page.getByRole("button", { name: '兜底规则 "*" 不可删除', exact: true })).toBeDisabled();
  });

  check("task: endpoint creation validates capabilities and persists edit and deletion", async ({ page, origin }) => {
    const reservation = http.createServer();
    reservation.listen(0, "127.0.0.1");
    await once(reservation, "listening");
    const port = reservation.address().port;
    await new Promise((resolve) => reservation.close(resolve));
    assert.ok(![1260, 1265].includes(port));
    await page.goto(origin + "/ui/ops/endpoints");
    await page.getByRole("button", { name: "新建接入点", exact: true }).first().click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "确认创建", exact: true }).click();
    await expect(dialog.locator(".workspace-form-error")).toContainText("端口必须");
    await dialog.getByLabel("监听端口", { exact: true }).fill(String(port));
    await dialog.getByRole("switch", { name: "启用", exact: true }).click();
    for (const label of ["HTTP 正向代理", "HTTP 反向代理", "SOCKS5 代理"]) await dialog.getByRole("switch", { name: label, exact: true }).click();
    await expect(dialog.getByRole("switch", { name: "强制客户端认证" })).toBeDisabled();
    await dialog.getByRole("button", { name: "确认创建", exact: true }).click();
    await expect(dialog.locator(".workspace-form-error")).toContainText("至少启用");
    await dialog.getByRole("switch", { name: "SOCKS5 代理", exact: true }).click();
    await expect(dialog.getByRole("switch", { name: "强制客户端认证" })).toBeEnabled();
    await dialog.getByRole("button", { name: "确认创建", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    const row = page.locator(".workspace-object-row").filter({ hasText: ":" + port });
    await row.getByRole("button", { name: "编辑接入点", exact: true }).click();
    await expect(dialog.getByLabel("监听端口", { exact: true })).toHaveValue(String(port));
    await dialog.getByRole("switch", { name: "HTTP 正向代理", exact: true }).click();
    await dialog.getByRole("button", { name: "保存配置", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(row).toContainText("正向");
    await row.getByRole("button", { name: "删除接入点", exact: true }).click();
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    await row.getByRole("button", { name: "删除接入点", exact: true }).click();
    await dialog.getByRole("button", { name: "确认删除", exact: true }).click();
    await expect(row).toHaveCount(0);
  });

  check("task: shell menus and mobile navigation route and return keyboard focus", async ({ page, origin }) => {
    for (const width of [390, 1100, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto(origin + "/ui/ops");
      const account = page.getByRole("button", { name: "账户菜单", exact: true });
      await account.click();
      await assertOverlay(page, page.getByRole("menu"));
      await saveSurface(page, "account-menu");
      await page.keyboard.press("Escape");
      await expect(account).toBeFocused();
      await account.click();
      await page.getByRole("menuitem", { name: "账户安全", exact: true }).click();
      await expect(page).toHaveURL(origin + "/ui/account");
      if (width <= 1100) {
        const navigation = page.getByRole("button", { name: "打开导航", exact: true });
        await navigation.click();
        const dialog = page.getByRole("dialog");
        await assertOverlay(page, dialog);
        await saveSurface(page, "mobile-navigation");
        await page.keyboard.press("Escape");
        await expect(navigation).toBeFocused();
        await navigation.click();
        await dialog.getByRole("link", { name: "订阅源", exact: true }).click();
        await expect(page).toHaveURL(origin + "/ui/ops/subscriptions");
        await expect(dialog).toHaveCount(0);
      }
      await page.keyboard.press("Control+k");
      await expect(page.getByRole("dialog")).toBeVisible();
      await saveSurface(page, "command-palette");
      await page.keyboard.press("Escape");
    }
    await page.setViewportSize({ width: 1440, height: 900 });
  });

  check("task: all configuration categories preserve drafts and reload confirmation cancels", async ({ page, origin }) => {
    const categories = ["支付网关", "探测与路由", "请求日志", "缓存与持久化", "网络与性能", "默认平台策略", "指标与统计", "服务与部署"];
    for (const width of [390, 1100, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      await page.goto(origin + "/ui/ops/config");
      for (const category of categories) {
        await page.locator(".workspace-object-row").filter({ has: page.locator("strong", { hasText: category }) }).click();
        await expect(page.getByRole("button", { name: "配置目录", exact: true })).toBeVisible();
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
        await inspectSelects(page, page.locator("main"), `config-${category}`);
        await saveSurface(page, `config-${category}`);
        await page.getByRole("button", { name: "配置目录", exact: true }).click();
      }
    }
    await page.locator(".workspace-object-row").filter({ hasText: "缓存与持久化" }).click();
    const patch = page.getByLabel("JSON 变更内容", { exact: true });
    await patch.fill("{");
    await page.getByRole("button", { name: "应用 JSON 到草稿", exact: true }).click();
    await expect(page.locator(".toast-item").filter({ hasText: /JSON/ })).toBeVisible();
    await patch.fill('{"cache_flush_dirty_threshold":7}');
    await page.getByRole("button", { name: "应用 JSON 到草稿", exact: true }).click();
    await expect(page.getByRole("button", { name: "保存全部更改", exact: true })).toBeEnabled();
    await page.getByRole("button", { name: "重新加载", exact: true }).first().click();
    const dialog = page.getByRole("dialog");
    await assertOverlay(page, dialog);
    await saveSurface(page, "discard-draft");
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    await expect(page.getByRole("button", { name: "保存全部更改", exact: true })).toBeEnabled();
    await page.getByRole("button", { name: "重新加载", exact: true }).first().click();
    await dialog.getByRole("button", { name: "丢弃并重新加载", exact: true }).click();
    await expect(page.getByRole("button", { name: "保存全部更改", exact: true })).toBeDisabled();
    await page.setViewportSize({ width: 1440, height: 900 });
  });

  check("task: tenant key creation, secret copy, filtering and revocation use real session data", async ({ browser, origin, context }) => {
    const headers = await csrfHeaders(context);
    const planResponse = await context.request.post(origin + "/api/v1/admin/plans", { headers, data: { name: "Workflow tenant plan", platforms: [], ip_tiers: [], traffic_bytes: 0, period: "month", max_concurrent: 5, max_rps: 10, max_keys: 5, duration_days: 30, price_micro: 1000000, status: "active" } });
    assert.equal(planResponse.status(), 201);
    const plan = await planResponse.json();
    const userResponse = await context.request.post(origin + "/api/v1/admin/users", { headers, data: { username: "workflow-tenant", password: "synthetic-password-only", role: "user", plan_id: plan.id, duration_days: 30 } });
    assert.equal(userResponse.status(), 201);
    const user = await userResponse.json();
    const tenant = await browser.newContext({ locale: "zh-CN", reducedMotion: "reduce" });
    await tenant.addInitScript(() => localStorage.setItem("prism.locale", "zh-CN"));
    await tenant.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
    const page = await tenant.newPage();
    try {
      await page.goto(origin + "/ui/login");
      await page.getByLabel("用户名", { exact: true }).fill("workflow-tenant");
      await page.locator("#password").fill("synthetic-password-only");
      await page.locator('form button[type="submit"]').click();
      await page.waitForURL(origin + "/ui/overview");
      for (const width of [390, 1100, 1440]) {
        await page.setViewportSize({ width, height: 900 });
        await page.goto(origin + "/ui/keys");
        await page.getByRole("button", { name: "创建密钥", exact: true }).first().click();
        let dialog = page.getByRole("dialog");
        await assertOverlay(page, dialog);
        await dialog.getByRole("button", { name: "确认创建", exact: true }).click();
        await expect(dialog.getByRole("alert")).toContainText("密钥名称不能为空");
        await dialog.getByLabel("密钥名称 *", { exact: true }).fill(`Workflow key ${width}`);
        await saveSurface(page, "tenant-key-create");
        await dialog.getByRole("button", { name: "确认创建", exact: true }).click();
        dialog = page.getByRole("dialog", { name: "密钥凭据已生成", exact: true });
        await assertOverlay(page, dialog);
        await saveSurface(page, "tenant-secret-masked");
        await dialog.getByRole("button", { name: "复制", exact: true }).click();
        await expect(dialog.getByRole("button", { name: "已复制", exact: true })).toBeVisible();
        assert.ok(await page.evaluate(() => navigator.clipboard.readText().then((text) => text.startsWith("pk_"))), "clipboard holds the new credential without logging it");
        await page.evaluate(() => navigator.clipboard.writeText(""));
        await dialog.getByRole("button", { name: "我已保存，关闭", exact: true }).click();
        await page.getByLabel("搜索密钥", { exact: true }).fill("missing");
        await expect(page.locator(".workspace-object-row")).toHaveCount(0);
        await page.getByRole("button", { name: "清除筛选", exact: true }).click();
        await page.locator(".workspace-object-row").filter({ hasText: `Workflow key ${width}` }).click();
        dialog = page.getByRole("dialog");
        await assertOverlay(page, dialog);
        await saveSurface(page, "tenant-key-inspector");
        await dialog.getByRole("button", { name: "删除", exact: true }).click();
        const confirm = page.getByRole("dialog", { name: "确认删除密钥", exact: true });
        await assertOverlay(page, confirm);
        await saveSurface(page, "tenant-key-delete");
        await confirm.getByRole("button", { name: "取消", exact: true }).click();
        await page.getByRole("dialog").getByRole("button", { name: "删除", exact: true }).click();
        await confirm.getByRole("button", { name: "确认删除", exact: true }).click();
        await expect(page.getByRole("dialog")).toHaveCount(0);
        await expect(page.locator(".workspace-object-row")).toHaveCount(0);
      }
    } finally {
      await tenant.close();
      await context.request.delete(origin + "/api/v1/admin/users/" + user.id, { headers });
      await context.request.delete(origin + "/api/v1/admin/plans/" + plan.id, { headers });
    }
  });
}
