import assert from "node:assert/strict";
import { join } from "node:path";
import { expect } from "@playwright/test";

export async function verifyQualityViews({ page, origin, adminToken, screenshots }) {
  const data = await fetch(origin + "/api/v1/nodes?limit=10", { headers: { Authorization: "Bearer " + adminToken } }).then(response => response.json());
  const node = data.items.find(item => item.tags?.some(tag => tag.tag.includes("Alpha")));
  assert(node, "A real isolated inventory node must exist before synthetic presentation fixtures");
  let mode = "pending", calls = 0;
  const now = new Date().toISOString();
  const future = new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString();
  const past = new Date(Date.now() - 60_000).toISOString();
  const ip = "8.8.8.8";
  const proxy = { id: "synthetic-network", ip, provider: "proxycheck", profile: "ui-contract", ip_type: "business", source_type: "Business", asn: "AS15169", organization: "Synthetic UI fixture", country_code: "US", risk_score: 100, grade: "severe", source_confidence: 99, observed_at: now, valid_until: future };
  const pure = { id: "synthetic-ippure", ip, provider: "ippure", profile: "ui-contract", ip_type: "residential", source_type: "Residential", asn: "AS15169", organization: "Synthetic UI fixture", country_code: "US", risk_score: 5, grade: "unknown", source_confidence: null, native: false, signals: {}, observed_at: now };
  function quality() {
    const evidence = { ...proxy, signals: { proxy: false, vpn: mode !== "expired", tor: false, hosting: false, scraper: false, anonymous: mode !== "expired", compromised: mode === "expired" } };
    return { ip, state: "valid", evidence, sources: [
      { provider: "proxycheck", configured: true, state: "valid", evidence },
      { provider: "ippure", configured: true, state: mode === "pending" ? "unobserved" : mode === "expired" ? "stale" : "valid", evidence: mode === "pending" ? null : { ...pure, valid_until: mode === "expired" ? past : future } },
    ], assessment: { state: mode === "pending" ? "partial" : mode === "expired" ? "stale" : "valid", score_source: "ippure", purity_score: mode === "reviewed" ? 95 : null, purity_band: mode === "reviewed" ? "excellent" : "unknown", network_type: "business", network_source: "proxycheck", native: mode === "pending" ? null : false, verdict: mode === "expired" ? "high_risk" : "review", reasons: mode === "expired" ? ["COMPROMISED"] : ["VPN_DETECTED"] } };
  }
  const match = url => url.pathname === "/api/v1/nodes" || url.pathname === "/api/v1/nodes/" + node.node_hash || url.pathname === "/api/v1/nodes/" + node.node_hash + "/actions/review-ippure" || url.pathname === "/api/v1/quality/status";
  const handler = async route => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (request.method() === "POST") {
      calls++; mode = "reviewed";
      await route.fulfill({ status: 200, headers: { "Cache-Control": "no-store" }, json: { evidence: { ...pure, valid_until: future }, node_hash: node.node_hash, expected_ip: ip, matches_node_ip: true, is_residential: true, score_supported: true, next_allowed_at: new Date(Date.now() + 60_000).toISOString() } });
      return;
    }
    const response = await route.fetch();
    const body = await response.json();
    const decorate = item => item.node_hash === node.node_hash ? { ...item, egress_ip: ip, region: "US", quality: quality() } : item;
    if (path === "/api/v1/nodes") body.items = body.items.map(decorate);
    else if (path === "/api/v1/quality/status") body.manual_sources = [{ id: "ippure", name: "IPPure", website: "https://ippure.com/MyIP-Info-API", busy: false, interval_seconds: 60, current_ips: mode === "reviewed" ? 1 : 0, next_allowed_at: calls ? new Date(Date.now() + 60_000).toISOString() : undefined }];
    else Object.assign(body, decorate(body));
    await route.fulfill({ response, json: body });
  };
  await page.route(match, handler);
  try {
    await page.goto(origin + "/ui/nodes?tag=Alpha");
    await expect(page.getByRole("textbox", { name: "搜索节点", exact: true })).toHaveValue("Alpha");
    await page.getByRole("textbox", { name: "搜索节点", exact: true }).fill("");
    await expect(page.locator(".node-name")).toHaveCount(2);
    assert(!new URL(page.url()).searchParams.has("tag"), "Legacy search must not reappear after clearing");
    const row = page.getByRole("row").filter({ hasText: "Local Alpha" });
    await expect(row.getByText("待 IPPure 复核", { exact: true })).toBeVisible();
    await expect(row.locator(".purity-badge-score")).toHaveCount(0);
    await row.locator(".node-name").click();
    const dialog = page.getByRole("dialog", { name: "节点详情", exact: true });
    await dialog.locator(".purity-guide > summary").click();
    await expect(dialog.locator(".purity-bands").getByText("95–100", { exact: true })).toBeVisible();
    await dialog.getByRole("button", { name: "通过此节点复核", exact: true }).click();
    await expect(dialog.locator(".quality-result-banner .purity-primary-value")).toContainText("95");
    await expect(dialog.locator(".quality-verdict").getByText("需要复核", { exact: true })).toBeVisible();
    await expect(dialog.getByRole("button", { name: "通过此节点复核", exact: true })).toBeDisabled();
    assert.equal(calls, 1, "One manual action must make one request");
    await page.screenshot({ path: join(screenshots, "ippure-review-fixture.png"), fullPage: true });
    await page.keyboard.press("Escape");
    await expect(row.locator(".purity-badge-score")).toHaveText("95");
    await expect(row.getByText("需要复核", { exact: true })).toBeVisible();
    await page.screenshot({ path: join(screenshots, "nodes-reviewed-fixture.png"), fullPage: true });
    mode = "expired";
    await page.reload();
    await expect(row.getByText("风险较高", { exact: true })).toBeVisible();
    await expect(row.getByText("部分证据过期", { exact: true })).toBeVisible();
    await expect(row.getByText("IPPure 已过期", { exact: true })).toBeVisible();
  } finally { await page.unroute(match, handler); }
}

export async function verifySettingsCategories({ page, origin, adminToken, screenshots }) {
  const headers = { Authorization: "Bearer " + adminToken };
  const baseline = await fetch(origin + "/api/v1/system/config", { headers }).then(response => response.json());
  await page.goto(origin + "/ui/system-config");
  await expect(page.locator(".settings-category")).toHaveCount(8);
  await expect(page.locator("#sys-max-fail")).toHaveCount(0);
  await page.screenshot({ path: join(screenshots, "settings-categories-light.png"), fullPage: true });
  await page.getByRole("button", { name: "探测与路由", exact: true }).click();
  const failureValue = String(baseline.max_consecutive_failures + 1);
  await page.locator("#sys-max-fail").fill(failureValue);
  await page.getByRole("button", { name: "所有配置", exact: true }).click();
  await page.getByRole("button", { name: "请求日志", exact: true }).click();
  const logging = page.getByRole("checkbox", { name: "启用请求日志", exact: true });
  // Switch renders a visually hidden input (opacity 0, zero size); click the
  // input directly so React's change handler fires exactly like a real toggle.
  await logging.evaluate((element) => { element.click(); });
  if (!baseline.request_log_enabled) {
    await expect(logging).toBeChecked();
  } else {
    await expect(logging).not.toBeChecked();
  }
  await page.getByRole("button", { name: "所有配置", exact: true }).click();
  await page.getByRole("button", { name: "探测与路由", exact: true }).click();
  await expect(page.locator("#sys-max-fail")).toHaveValue(failureValue);
  await page.locator("#sys-max-fail").fill("");
  await expect(page.getByRole("button", { name: "保存全部更改", exact: true })).toBeDisabled();
  let asked = false;
  page.once("dialog", async dialog => { asked = true; await dialog.dismiss(); });
  await page.getByRole("button", { name: "重新加载", exact: true }).click();
  assert(asked, "Invalid input is still an unsaved draft and must not be silently discarded");
  await expect(page.locator("#sys-max-fail")).toHaveValue("");
  await page.locator("#sys-max-fail").fill(failureValue);
  await page.locator(".settings-json > summary").click();
  await page.getByRole("textbox", { name: "JSON 变更内容", exact: true }).fill(JSON.stringify({ max_consecutive_failures: baseline.max_consecutive_failures + 2 }));
  await expect(page.getByRole("button", { name: "保存全部更改", exact: true })).toBeDisabled();
  await page.getByRole("button", { name: "应用 JSON 到草稿", exact: true }).click();
  await expect(page.locator("#sys-max-fail")).toHaveValue(String(baseline.max_consecutive_failures + 2));
  const unchanged = await fetch(origin + "/api/v1/system/config", { headers }).then(response => response.json());
  assert.equal(unchanged.max_consecutive_failures, baseline.max_consecutive_failures, "Applying JSON must not save to the server");
  const saved = page.waitForResponse(response => new URL(response.url()).pathname === "/api/v1/system/config" && response.request().method() === "PATCH");
  await page.getByRole("button", { name: "保存全部更改", exact: true }).click();
  assert.equal((await saved).status(), 200);
  await expect(page.getByText("当前无未保存改动", { exact: true })).toBeVisible();
  const current = await fetch(origin + "/api/v1/system/config", { headers }).then(response => response.json());
  assert.equal(current.max_consecutive_failures, baseline.max_consecutive_failures + 2);
  assert.equal(current.request_log_enabled, !baseline.request_log_enabled, "Saving must include changes from other categories");
  await page.locator(".settings-json > summary").click();
  await page.screenshot({ path: join(screenshots, "settings-probes-light.png"), fullPage: true });
  await page.getByRole("button", { name: "所有配置", exact: true }).click();
  await page.getByRole("button", { name: "服务与部署", exact: true }).click();
  await expect(page.getByRole("textbox", { name: "状态存储目录", exact: true })).toHaveAttribute("readonly", "");
  const restored = await fetch(origin + "/api/v1/system/config", { method: "PATCH", headers: { ...headers, "Content-Type": "application/json" }, body: JSON.stringify({ max_consecutive_failures: baseline.max_consecutive_failures, request_log_enabled: baseline.request_log_enabled }) });
  assert.equal(restored.status, 200, "Restore isolated configuration fixture");
}

export async function auditPanelSpacing(page, route, width) {
  const violations = await page.locator(".card").evaluateAll(cards => cards.flatMap(card => {
    const box = card.getBoundingClientRect();
    if (!box.width || !box.height || box.right <= 0 || box.left >= innerWidth) return [];
    const landmarks = card.querySelectorAll(":scope > .detail-header, :scope > .list-card-header, :scope > .form-grid, :scope > .syscfg-section");
    return [...landmarks].flatMap(element => {
      const r = element.getBoundingClientRect();
      if (!r.width || !r.height) return [];
      return r.left - box.left < 12 || box.right - r.right < 12 ? [{ card: card.className, left: r.left - box.left, right: box.right - r.right }] : [];
    });
  }));
  assert.deepEqual(violations, [], `Readable content inset: ${route} ${width}px`);
}
