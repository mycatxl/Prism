import assert from "node:assert/strict";
import { expect } from "@playwright/test";
import { assertOverlay, saveSurface } from "./panel-surfaces.mjs";

const subscriptionPath = "/ui/ops/subscriptions";
const rulesPath = "/ui/ops/rules";
const resourcePath = "/ui/ops/resources";

export function registerPanelWorkflowChecks(check) {
  check("workflow: toast feedback does not cover a centered dialog at any viewport", async ({ page, origin }) => {
    const pattern = "**/api/v1/account-header-rules:resolve";
    await page.route(pattern, (route) => route.fulfill({ status: 503, json: { error: { code: "UI_TEST_FAILURE", message: "合成规则解析失败，请检查目标地址后重试。" } } }));
    try {
      for (const viewport of [{width:390,height:900}, {width:1100,height:900}, {width:1440,height:900}, {width:2560,height:900}, {width:390,height:568}]) {
        await page.setViewportSize(viewport);
        await page.goto(origin + rulesPath);
        const opener = page.getByRole("button", { name: "规则测试", exact: true });
        await opener.click();
        const dialog = page.getByRole("dialog");
        await dialog.getByLabel("目标地址", { exact: true }).fill("https://example.test/feedback");
        for (let attempt = 0; attempt < (viewport.height < 600 ? 4 : 2); attempt += 1) {
          await dialog.getByRole("button", { name: "开始测试", exact: true }).click();
          await expect(page.locator(".toast-item")).toHaveCount(attempt + 1);
          await expect.poll(() => dialog.evaluate((element) => {
            const box = element.getBoundingClientRect();
            return [...document.querySelectorAll(".toast-item")].every((toast) => {
              const item = toast.getBoundingClientRect();
              const clip = toast.closest(".toast-viewport").getBoundingClientRect();
              const visible = {left:Math.max(item.left,clip.left),right:Math.min(item.right,clip.right),top:Math.max(item.top,clip.top),bottom:Math.min(item.bottom,clip.bottom)};
              return visible.top >= visible.bottom || box.right <= visible.left || box.left >= visible.right || box.bottom <= visible.top || box.top >= visible.bottom;
            });
          }), { timeout: 1500, message: "feedback must not obscure the dialog title, form or actions" }).toBe(true);
        }
        await assertOverlay(page, dialog);
        await saveSurface(page, "rule-feedback-clear");
        await page.keyboard.press("Escape");
        await expect(opener).toBeFocused();
      }
    } finally {
      await page.unroute(pattern);
      await page.setViewportSize({ width: 1440, height: 900 });
    }
  });

  check("workflow: user creation cannot submit twice while its request is pending", async ({ page, origin }) => {
    let release;
    const gate = new Promise((resolve) => { release = resolve; });
    let requests = 0;
    let handled;
    const completed = new Promise((resolve) => { handled = resolve; });
    await page.route("**/api/v1/admin/users", async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      requests += 1;
      await gate;
      await route.fulfill({ status: 503, json: { error: { code: "UI_TEST_FAILURE", message: "合成创建失败" } } });
      handled();
    });
    try {
      await page.goto(origin + "/ui/ops/users");
      await page.getByRole("button", { name: "新建用户", exact: true }).first().click();
      const dialog = page.getByRole("dialog");
      await dialog.getByLabel("用户名", { exact: true }).fill("workflow-user");
      await dialog.getByLabel("初始密码", { exact: true }).fill("synthetic-password-only");
      const submit = dialog.getByRole("button", { name: "创建用户", exact: true });
      await submit.click();
      await expect.poll(() => requests).toBe(1);
      await expect(submit).toBeDisabled();
      await dialog.getByLabel("用户名", { exact: true }).press("Enter");
      assert.equal(requests, 1, "Enter during a pending request must not submit again");
      await page.keyboard.press("Escape");
      await expect(dialog).toBeVisible();
      await assertOverlay(page, dialog);
      await saveSurface(page, "user-create-pending");
      release();
      await expect(dialog.locator(".workspace-form-error")).toContainText("合成创建失败");
      await saveSurface(page, "user-create-error");
      await expect(submit).toBeEnabled();
      assert.equal(requests, 1);
      await dialog.getByRole("button", { name: "取消", exact: true }).click();
    } finally { release(); if (requests) await completed; await page.unroute("**/api/v1/admin/users"); }
  });

  check("workflow: a pending destructive confirmation cannot imply cancellation", async ({ page, origin, context }) => {
    const csrf = (await context.cookies()).find((cookie) => cookie.name === "prism_csrf");
    const headers = { "X-Prism-CSRF": decodeURIComponent(csrf.value) };
    const prefix = "workflow.example.test";
    const api = origin + "/api/v1/account-header-rules/" + prefix;
    assert.ok((await context.request.put(api, { headers, data: { headers: ["X-Test"] } })).ok());
    let release;
    const gate = new Promise((resolve) => { release = resolve; });
    let requested = false;
    let handled;
    const completed = new Promise((resolve) => { handled = resolve; });
    await page.route("**/api/v1/account-header-rules/" + prefix, async (route) => {
      if (route.request().method() !== "DELETE") return route.continue();
      requested = true;
      await gate;
      await route.fulfill({ status: 503, json: { error: { code: "UI_TEST_FAILURE", message: "合成删除失败" } } });
      handled();
    });
    try {
      await page.goto(origin + rulesPath);
      await page.locator(".workspace-object-row").filter({ hasText: prefix }).getByRole("button", { name: "删除", exact: true }).click();
      const dialog = page.getByRole("dialog", { name: "确认删除规则", exact: true });
      await dialog.getByRole("button", { name: "确认删除", exact: true }).click();
      await expect(dialog.getByRole("button", { name: "取消", exact: true })).toBeDisabled();
      await page.keyboard.press("Escape");
      await expect(dialog).toBeVisible();
      await expect(dialog.getByRole("button", { name: "关闭", exact: true })).toBeDisabled();
      await assertOverlay(page, dialog);
      await saveSurface(page, "delete-pending");
      release();
      await expect(dialog.getByRole("button", { name: "取消", exact: true })).toBeEnabled();
      await dialog.getByRole("button", { name: "取消", exact: true }).click();
      await expect(page.locator(".workspace-object-row").filter({ hasText: prefix })).toBeVisible();
    } finally {
      release();
      if (requested) await completed;
      await page.unroute("**/api/v1/account-header-rules/" + prefix);
      await context.request.delete(api, { headers });
    }
  });

  check("workflow: subscription search chips describe only applied filters", async ({ page, origin }) => {
    await page.goto(origin + subscriptionPath);
    await page.getByLabel("搜索订阅", { exact: true }).fill("UI_Local");
    await expect(page.locator(".workspace-object-row")).toHaveCount(1);
    await expect(page.locator(".workspace-active-filters")).toContainText("关键词: UI_Local");
    await expect(page.locator(".workspace-active-filters")).not.toContainText("仅禁用");
    await page.getByRole("button", { name: "清除筛选", exact: true }).first().click();
    await expect(page.getByLabel("搜索订阅", { exact: true })).toHaveValue("");
    await expect(page.locator(".workspace-active-filters")).toHaveCount(0);
  });

  check("workflow: keyboard activation of a row action does not open its inspector", async ({ page, origin }) => {
    await page.goto(origin + subscriptionPath);
    const row = page.locator(".workspace-object-row").filter({ hasText: "UI_Local" });
    const toggle = row.getByRole("button", { name: "停用", exact: true });
    await toggle.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(row.getByRole("button", { name: "启用", exact: true })).toBeVisible();
    await row.getByRole("button", { name: "启用", exact: true }).click();
    await expect(row.getByRole("button", { name: "停用", exact: true })).toBeVisible();
  });

  check("workflow: editing a subscription cannot silently change its immutable source type", async ({ page, origin }) => {
    await page.goto(origin + subscriptionPath);
    await page.locator(".workspace-object-row").filter({ hasText: "UI_Local" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByLabel("订阅内容", { exact: true })).toBeVisible();
    await expect(dialog.getByRole("tab", { name: "远程", exact: true })).toHaveCount(0);
    await expect(dialog.getByText("本地订阅", { exact: true }).last()).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
  });

  check("workflow: rule resolution submits with Enter and clears obsolete results", async ({ page, origin }) => {
    await page.goto(origin + rulesPath);
    await page.getByRole("button", { name: "规则测试", exact: true }).click();
    const dialog = page.getByRole("dialog");
    const input = dialog.getByLabel("目标地址", { exact: true });
    await input.fill("https://example.test/first");
    await input.press("Enter");
    await expect(dialog.getByRole("heading", { name: "命中结果", exact: true })).toBeVisible();
    await input.fill("https://example.test/second");
    await expect(dialog.getByRole("heading", { name: "命中结果", exact: true })).toHaveCount(0);
    await dialog.getByRole("button", { name: "开始测试", exact: true }).click();
    await expect(dialog.getByRole("heading", { name: "命中结果", exact: true })).toBeVisible();
    await dialog.getByRole("button", { name: "关闭", exact: true }).last().click();
    await page.getByRole("button", { name: "规则测试", exact: true }).click();
    await expect(dialog.getByRole("heading", { name: "命中结果", exact: true })).toHaveCount(0);
    await page.keyboard.press("Escape");
  });

  check("workflow: rule resolution clears a successful result before a failed retry", async ({ page, origin }) => {
    await page.goto(origin + rulesPath);
    await page.getByRole("button", { name: "规则测试", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByLabel("目标地址", { exact: true }).fill("https://example.test/first");
    await dialog.getByRole("button", { name: "开始测试", exact: true }).click();
    await expect(dialog.getByRole("heading", { name: "命中结果", exact: true })).toBeVisible();
    await page.route("**/api/v1/account-header-rules:resolve", (route) => route.fulfill({ status: 503, json: { error: { code: "UI_TEST_FAILURE", message: "合成规则失败" } } }));
    try {
      await dialog.getByRole("button", { name: "开始测试", exact: true }).click();
      await expect(page.locator(".toast-item").filter({ hasText: "合成规则失败" })).toBeVisible();
      await assertOverlay(page, dialog);
      await saveSurface(page, "rule-resolve-error");
      await expect(dialog.getByRole("heading", { name: "命中结果", exact: true })).toHaveCount(0);
    } finally { await page.unroute("**/api/v1/account-header-rules:resolve"); }
  });

  check("workflow: resource lookup failures are handled and obsolete results disappear", async ({ page, origin }) => {
    const errors = [];
    const onError = (error) => errors.push(error.message);
    page.on("pageerror", onError);
    await page.route("**/api/v1/geoip/lookup?**", (route) => route.fulfill({ json: { ip: "192.0.2.1", region: "US" } }));
    try {
      await page.goto(origin + resourcePath);
      await page.getByLabel("IP 地址", { exact: true }).fill("192.0.2.1");
      await page.getByRole("button", { name: "查询", exact: true }).click();
      await expect(page.locator("dd").filter({ hasText: "192.0.2.1" })).toBeVisible();
      await page.unroute("**/api/v1/geoip/lookup?**");
      await page.route("**/api/v1/geoip/lookup?**", (route) => route.fulfill({ status: 503, json: { error: { code: "UI_TEST_FAILURE", message: "合成查询失败" } } }));
      await page.getByRole("button", { name: "查询", exact: true }).click();
      await expect(page.locator(".toast-item").filter({ hasText: "合成查询失败" })).toBeVisible();
      assert.deepEqual(errors, [], "handled lookup failure must not become an unhandled rejection");
      await expect(page.locator("dd").filter({ hasText: "192.0.2.1" })).toHaveCount(0);
      await saveSurface(page, "resource-lookup-error");
    } finally {
      await page.unroute("**/api/v1/geoip/lookup?**");
      page.off("pageerror", onError);
    }
  });

  check("workflow: resource update errors stay in the user feedback channel", async ({ page, origin }) => {
    const errors = [];
    const onError = (error) => errors.push(error.message);
    page.on("pageerror", onError);
    await page.route("**/api/v1/geoip/actions/update-now", (route) => route.fulfill({ status: 503, json: { error: { code: "UI_TEST_FAILURE", message: "合成更新失败" } } }));
    try {
      await page.goto(origin + resourcePath);
      await page.getByRole("button", { name: "立即更新", exact: true }).click();
      await expect(page.locator(".toast-item").filter({ hasText: "合成更新失败" })).toBeVisible();
      assert.deepEqual(errors, [], "handled update failure must not become an unhandled rejection");
      await saveSurface(page, "resource-update-error");
    } finally {
      await page.unroute("**/api/v1/geoip/actions/update-now");
      page.off("pageerror", onError);
    }
  });
}
