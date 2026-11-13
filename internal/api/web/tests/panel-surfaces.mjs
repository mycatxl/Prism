import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { expect } from "@playwright/test";
import { measureCardInsets } from "../scripts/card-insets.mjs";

const widths = [390, 1100, 1440, 2560];
const screenshotDir = process.env.PRISM_UI_SCREENSHOT_DIR;

// These are the creation actions rendered by the actual admin route components.
// Route rendering, rail destinations and legacy redirects are covered by the
// shared runner; this table exercises their user-facing forms and overlays.
const creationSurfaces = [
  ["subscriptions", "新建订阅", "新建订阅"],
  ["jobs", "新建任务", "新建检测任务"],
  ["platforms", "新建平台", "新建平台"],
  ["endpoints", "新建接入点", "新建接入点"],
  ["rules", "新建规则", "新建规则"],
  ["users", "新建用户", "新建用户"],
  ["plans", "新建套餐", "新建套餐"],
  ["invites", "创建邀请码", "创建注册邀请码"],
  ["redeem-codes", "生成兑换码", "生成兑换码"],
];

export async function saveSurface(page, name) {
  if (!screenshotDir) return;
  await mkdir(screenshotDir, { recursive: true });
  await page.screenshot({
    path: join(screenshotDir, `${name}-${page.viewportSize().width}.png`),
    fullPage: true,
    mask: [page.locator('input[type="password"], input[readonly], .workspace-code-block')],
  });
}

export async function assertOverlay(page, surface) {
  await expect(surface).toBeVisible();
  // Radix positions a portal after mounting it; sample its settled geometry,
  // including when a reduced-motion animation still occupies one frame.
  await expect.poll(() => surface.evaluate((element) => {
    const box = element.getBoundingClientRect();
    const centered = element.getAttribute("role") !== "dialog" ||
      (Math.abs((box.left + box.right) / 2 - innerWidth / 2) <= 1 && Math.abs((box.top + box.bottom) / 2 - innerHeight / 2) <= 1);
    return centered && box.left >= -1 && box.top >= -1 && box.right <= innerWidth + 1 && box.bottom <= innerHeight + 1;
  }), { message: "overlay must settle inside the viewport and dialogs must settle at its center" }).toBe(true);
  const geometry = await surface.evaluate((element) => {
    const box = element.getBoundingClientRect();
    const body = element.querySelector(".workspace-drawer__body, .workspace-sheet__body, .apple-dialog__body");
    const head = element.querySelector(".workspace-drawer__head, .workspace-sheet__header, .apple-dialog__header")?.getBoundingClientRect();
    const footer = element.querySelector(".workspace-drawer__footer, .workspace-sheet__footer, .apple-dialog__footer")?.getBoundingClientRect();
    const bodyBox = body?.getBoundingClientRect();
    const bodyStyle = body && getComputedStyle(body);
    return {
      left: box.left, top: box.top, right: box.right, bottom: box.bottom,
      viewportWidth: innerWidth, viewportHeight: innerHeight,
      dialog: element.getAttribute("role") === "dialog",
      width: box.width, height: box.height,
      centerX: box.left + box.width / 2, centerY: box.top + box.height / 2,
      bodyScrollable: !bodyStyle || ["auto", "scroll"].includes(bodyStyle.overflowY),
      scrollHeight: body?.scrollHeight, clientHeight: body?.clientHeight,
      overflow: element.scrollWidth - element.clientWidth,
      bodyOverflow: body ? body.scrollWidth - body.clientWidth : 0,
      bodyVisible: !bodyBox || !head || bodyBox.top >= head.bottom - 1,
      footerClear: !bodyBox || !footer || bodyBox.bottom <= footer.top + 1,
      bodyInset: !bodyStyle || [bodyStyle.paddingTop, bodyStyle.paddingRight, bodyStyle.paddingBottom, bodyStyle.paddingLeft].every((value) => parseFloat(value) >= 16),
    };
  });
  assert.ok(geometry.left >= -1 && geometry.top >= -1 && geometry.right <= geometry.viewportWidth + 1 && geometry.bottom <= geometry.viewportHeight + 1, `overlay outside viewport: ${JSON.stringify(geometry)}`);
  assert.ok(geometry.overflow <= 2 && geometry.bodyOverflow <= 2, `overlay clips horizontally: ${JSON.stringify(geometry)}`);
  assert.ok(geometry.bodyVisible && geometry.footerClear, `overlay sections overlap: ${JSON.stringify(geometry)}`);
  assert.ok(geometry.bodyInset, "dialog content must retain at least 16px of breathing room");
  if (geometry.dialog) {
    assert.ok(Math.abs(geometry.centerX - geometry.viewportWidth / 2) <= 1, `dialog must be horizontally centered: ${JSON.stringify(geometry)}`);
    assert.ok(Math.abs(geometry.centerY - geometry.viewportHeight / 2) <= 1, `dialog must be vertically centered: ${JSON.stringify(geometry)}`);
    assert.ok(geometry.width <= 640 + 1, "dialog width must stay readable on wide screens");
    assert.ok(geometry.height <= geometry.viewportHeight * 0.85 + 1, "dialog must leave at least 15% of the viewport outside it");
    assert.ok(geometry.bodyScrollable, "long dialog content must scroll internally");
  }
  for (const button of await surface.locator(".workspace-drawer__footer button, .workspace-sheet__footer button, .apple-dialog__footer button").all()) {
    const box = await button.boundingBox();
    assert.ok(box && box.x >= 0 && box.x + box.width <= page.viewportSize().width + 1, "footer action must fit");
    assert.ok(await button.evaluate((element) => {
      const r = element.getBoundingClientRect();
      return element.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2));
    }), "footer action must not be occluded");
  }
}

export async function inspectSelects(page, scope, key) {
  for (const [index, select] of (await scope.getByRole("combobox").all()).entries()) {
    if (!(await select.isVisible()) || !(await select.isEnabled())) continue;
    await select.scrollIntoViewIfNeeded();
    await select.focus();
    await page.keyboard.press("ArrowDown");
    const listbox = page.getByRole("listbox");
    await assertOverlay(page, listbox);
    if (index === 0) await saveSurface(page, `${key}-select`);
    await page.keyboard.press("End");
    await page.keyboard.press("Home");
    await page.keyboard.press("Escape");
    await expect(listbox).toHaveCount(0);
    await expect(select).toBeFocused();
  }
}

export function registerPanelSurfaceChecks(check) {
  for (const [route, action, title] of creationSurfaces) {
    check(`surface: ${route} centered creation dialog, keyboard, selects and geometry at all widths`, async ({ page, origin }) => {
      try {
        for (const width of widths) {
          await page.setViewportSize({ width, height: 900 });
          await page.goto(origin + "/ui/ops/" + route);
          const opener = page.getByRole("button", { name: action, exact: true }).first();
          await opener.focus();
          await opener.press("Enter");
          const dialog = page.getByRole("dialog", { name: title, exact: true });
          await assertOverlay(page, dialog);
          for (const key of ["Tab", "Shift+Tab"]) {
            await page.keyboard.press(key);
            assert.ok(await dialog.evaluate((element) => element.contains(document.activeElement)), "keyboard focus must stay inside the dialog");
          }
          await inspectSelects(page, dialog, `create-${route}`);
          const body = dialog.locator(".workspace-drawer__body");
          await body.evaluate((element) => { element.scrollTop = 0; });
          await saveSurface(page, `create-${route}`);
          await body.evaluate((element) => { element.scrollTop = element.scrollHeight; });
          if (await body.evaluate((element) => element.scrollHeight > element.clientHeight)) {
            assert.ok(await body.evaluate((element) => element.scrollTop > 0), "long dialog content must actually scroll");
          }
          await assertOverlay(page, dialog);
          await saveSurface(page, `create-${route}-bottom`);
          await page.keyboard.press("Escape");
          await expect(dialog).toHaveCount(0);
          await expect(opener).toBeFocused();
          await opener.click();
          await dialog.getByRole("button", { name: "取消", exact: true }).click();
          await expect(dialog).toHaveCount(0);
          await expect(opener).toBeFocused();
        }
      } finally { await page.setViewportSize({ width: 1440, height: 900 }); }
    });
  }

  check("surface: long forms keep their header and actions visible in short viewports", async ({ page, origin }) => {
    try {
      for (const viewport of [{ width: 390, height: 568 }, { width: 1440, height: 600 }]) {
        await page.setViewportSize(viewport);
        await page.goto(origin + "/ui/ops/subscriptions");
        const opener = page.getByRole("button", { name: "新建订阅", exact: true }).first();
        await opener.click();
        const dialog = page.getByRole("dialog", { name: "新建订阅", exact: true });
        await assertOverlay(page, dialog);
        const body = dialog.locator(".workspace-drawer__body");
        const head = dialog.locator(".workspace-drawer__head");
        const footer = dialog.locator(".workspace-drawer__footer");
        const positions = [await head.boundingBox(), await footer.boundingBox()];
        assert.ok(await body.evaluate((element) => element.scrollHeight > element.clientHeight), "the form must exceed the available body height");
        await body.evaluate((element) => { element.scrollTop = element.scrollHeight; });
        assert.ok(await body.evaluate((element) => element.scrollTop > 0));
        assert.deepEqual([await head.boundingBox(), await footer.boundingBox()], positions, "scrolling the form must not move its header or actions");
        await assertOverlay(page, dialog);
        await saveSurface(page, "short-viewport-dialog");
        await dialog.getByRole("button", { name: "取消", exact: true }).click();
        await expect(opener).toBeFocused();
      }
    } finally { await page.setViewportSize({ width: 1440, height: 900 }); }
  });

  check("surface: loading, failed and empty subscriptions remain usable at every viewport", async ({ page, origin }) => {
    const routePattern = /\/api\/v1\/subscriptions(?:\?|$)/;
    try {
      for (const width of widths) {
        await page.setViewportSize({ width, height: 900 });
        let release;
        const gate = new Promise((resolve) => { release = resolve; });
        let failed = true;
        await page.route(routePattern, async (route) => {
          await gate;
          await route.fulfill(failed
            ? { status: 503, json: { error: { code: "UI_TEST_FAILURE", message: "合成订阅读取失败" } } }
            : { json: { items: [], total: 0, limit: 20, offset: 0 } });
        });
        try {
          await page.goto(origin + "/ui/ops/subscriptions");
          await expect(page.getByText("正在加载订阅列表", { exact: true })).toBeVisible();
          const search = page.getByLabel("搜索订阅", { exact: true });
          await search.focus();
          const inspectState = async (state) => {
            assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
            const insets = await page.evaluate(measureCardInsets);
            assert.ok(insets.checked > 0);
            assert.deepEqual(insets.failures, []);
            await expect(search).toBeFocused();
            await saveSurface(page, `subscriptions-${state}`);
          };
          await inspectState("loading");
          release();
          await expect(page.getByText("订阅目录读取失败", { exact: true })).toBeVisible({ timeout: 15000 });
          await inspectState("error");
          failed = false;
          const retry = page.getByRole("button", { name: "重试", exact: true });
          await retry.focus();
          await retry.press("Enter");
          await search.focus();
          await expect(page.getByText("尚未添加任何订阅源", { exact: true })).toBeVisible();
          await inspectState("empty");
          await page.getByRole("button", { name: "新建订阅", exact: true }).first().click();
          await assertOverlay(page, page.getByRole("dialog"));
          await page.keyboard.press("Escape");
        } finally {
          release();
          await page.unrouteAll({ behavior: "wait" });
        }
      }
    } finally { await page.setViewportSize({ width: 1440, height: 900 }); }
  });

  check("surface: login and registration validate, toggle passwords and fit every viewport", async ({ browser, origin, context: admin }) => {
    const csrf = (await admin.cookies()).find((cookie) => cookie.name === "prism_csrf");
    const headers = { "X-Prism-CSRF": decodeURIComponent(csrf.value) };
    const config = await (await admin.request.get(origin + "/api/v1/system/config")).json();
    assert.ok((await admin.request.patch(origin + "/api/v1/system/config", { headers, data: { registration_mode: "open" } })).ok());
    const context = await browser.newContext({ locale: "zh-CN", reducedMotion: "reduce" });
    await context.addInitScript(() => localStorage.setItem("prism.locale", "zh-CN"));
    const page = await context.newPage();
    try {
      for (const width of widths) {
        await page.setViewportSize({ width, height: 900 });
        for (const route of ["login", "register"]) {
          await page.goto(origin + "/ui/" + route);
          await expect(page.locator("form")).toBeVisible();
          await page.locator('form button[type="submit"]').click();
          await expect(page.locator("form")).toBeVisible();
          const password = page.locator('input[type="password"]').first();
          await password.fill("synthetic-password-only");
          await page.getByRole("button", { name: "显示密码", exact: true }).first().click();
          await expect(page.getByRole("button", { name: "隐藏密码", exact: true }).first()).toBeVisible();
          await page.getByRole("button", { name: "隐藏密码", exact: true }).first().click();
          await password.fill("");
          assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `${route} overflows at ${width}`);
          await saveSurface(page, route);
        }
      }
    } finally {
      await context.close();
      assert.ok((await admin.request.patch(origin + "/api/v1/system/config", { headers, data: { registration_mode: config.registration_mode || "" } })).ok());
    }
  });

  check("surface: user detail actions and missing route are reachable at every viewport", async ({ page, origin, context }) => {
    const csrf = (await context.cookies()).find((cookie) => cookie.name === "prism_csrf");
    const headers = { "X-Prism-CSRF": decodeURIComponent(csrf.value) };
    const response = await context.request.post(origin + "/api/v1/admin/users", { headers, data: { username: "surface-user", password: "synthetic-password-only", role: "user" } });
    assert.equal(response.status(), 201);
    const user = await response.json();
    try {
      for (const width of widths) {
        await page.setViewportSize({ width, height: 900 });
        await page.goto(origin + "/ui/ops/users/" + user.id);
        await expect(page.getByRole("heading", { name: "surface-user", exact: true })).toBeVisible();
        const insets = await page.evaluate(measureCardInsets);
        assert.deepEqual(insets.failures, []);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
        await saveSurface(page, "user-detail");
        for (const action of ["分配套餐", "新建密钥", "重置密码", "调整余额"]) {
          const opener = page.getByRole("button", { name: action, exact: true }).first();
          await opener.click();
          const dialog = page.getByRole("dialog");
          await assertOverlay(page, dialog);
          await inspectSelects(page, dialog, `user-${action}`);
          await saveSurface(page, `user-${action}`);
          await dialog.getByRole("button", { name: "取消", exact: true }).click();
          await expect(dialog).toHaveCount(0);
          await expect(opener).toBeFocused();
        }
        await page.goto(origin + "/ui/no-such-route");
        await expect(page.locator("main h1")).toBeVisible();
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
        await saveSurface(page, "not-found");
      }
    } finally {
      await context.request.delete(origin + "/api/v1/admin/users/" + user.id, { headers });
      await page.setViewportSize({ width: 1440, height: 900 });
    }
  });
}
