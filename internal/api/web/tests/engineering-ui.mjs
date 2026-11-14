import assert from "node:assert/strict";
import { expect } from "@playwright/test";

export function registerEngineeringChecks(check) {
  check("regression: Telegram settings preserve masked tokens and test the saved channel", async ({ page, origin, context }) => {
    const path = "**/api/v1/system/config";
    const realConfig = await (await context.request.get(origin + "/api/v1/system/config")).json();
    let saved = { ...realConfig, alert_telegram_enabled: true, alert_telegram_bot_token: "", alert_telegram_bot_token_set: true, alert_telegram_chat_id: "-1001234567", alert_disk_free_percent: 10, alert_cooldown_minutes: 15 };
    const patches = [];
    // The UI contract is exercised without enabling any external sender in the backend fixture.
    await page.route(path, async (route) => {
      if (route.request().method() === "PATCH") {
        const patch = route.request().postDataJSON();
        patches.push(patch);
        saved = { ...saved, ...patch, alert_telegram_bot_token: "", alert_telegram_bot_token_set: patch.alert_telegram_bot_token === "" ? false : saved.alert_telegram_bot_token_set || Boolean(patch.alert_telegram_bot_token) };
      }
      await route.fulfill({ json: saved });
    });
    await page.route("**/api/v1/system/alerts/test", (route) => route.fulfill({ json: { delivered: true } }));
    try {
      await page.goto(origin + "/ui/ops/settings");
      const token = page.getByLabel("Bot token", { exact: true });
      await expect(token).toHaveValue("");
      await expect(token).toHaveAttribute("type", "password");
      await page.getByLabel("Telegram 聊天 ID", { exact: true }).fill("-1007654321");
      await expect(page.getByRole("button", { name: "发送测试通知", exact: true })).toBeDisabled();
      await page.getByRole("button", { name: "保存告警设置", exact: true }).click();
      await expect(page.getByText("告警设置已保存", { exact: true })).toBeVisible();
      assert.equal(patches.at(-1).alert_telegram_chat_id, "-1007654321");
      assert.equal(Object.hasOwn(patches.at(-1), "alert_telegram_bot_token"), false);
      await page.getByRole("button", { name: "发送测试通知", exact: true }).click();
      await expect(page.getByText("测试通知已发送", { exact: true })).toBeVisible();
      await token.fill("123456:REPLACEMENT_TOKEN_FOR_LOCAL_FIXTURE");
      await page.getByRole("button", { name: "保存告警设置", exact: true }).click();
      await expect(token).toHaveValue("");
      assert.equal(patches.at(-1).alert_telegram_bot_token, "123456:REPLACEMENT_TOKEN_FOR_LOCAL_FIXTURE");
      await page.getByRole("button", { name: "移除 Bot token", exact: true }).click();
      await expect(page.getByRole("button", { name: "保存告警设置", exact: true })).toBeDisabled();
      await page.getByRole("switch", { name: "启用 Telegram 通知", exact: true }).click();
      await page.getByRole("button", { name: "保存告警设置", exact: true }).click();
      await expect(page.getByText("通知已关闭", { exact: true })).toBeVisible();
      assert.equal(patches.at(-1).alert_telegram_bot_token, "");
    } finally {
      await page.unroute(path);
      await page.unroute("**/api/v1/system/alerts/test");
    }
  });

  check("regression: saving payment URLs displays public reachability warnings", async ({ page, origin }) => {
    await page.goto(origin + "/ui/ops/config?category=payments");
    const field = page.getByLabel("付款通知 URL（可选）", { exact: true });
    await field.fill("https://merchant.invalid/api/v1/public/ldc/notify");
    const [saved] = await Promise.all([
      page.waitForResponse((item) => new URL(item.url()).pathname === "/api/v1/system/config" && item.request().method() === "PATCH"),
      page.getByRole("button", { name: "保存全部更改", exact: true }).click(),
    ]);
    assert.equal(saved.status(), 200);
    assert.equal((await saved.json()).payment_url_checks[0].code, "non_public");
    await expect(page.getByText("网址没有可供支付平台访问的公开地址，请检查域名和 DNS。", { exact: false })).toBeVisible();
    await expect(field).toHaveValue("https://merchant.invalid/api/v1/public/ldc/notify");
    await field.fill("https://merchant.invalid:70000/notify");
    const [rejected] = await Promise.all([
      page.waitForResponse((item) => new URL(item.url()).pathname === "/api/v1/system/config" && item.request().method() === "PATCH"),
      page.getByRole("button", { name: "保存全部更改", exact: true }).click(),
    ]);
    assert.equal(rejected.status(), 400);
    await field.fill("");
    await page.getByRole("button", { name: "保存全部更改", exact: true }).click();
    await expect(page.getByText("未配置网址，将使用付款时的访问域名；请确认该域名公开可达。", { exact: false })).toBeVisible();
  });
}
