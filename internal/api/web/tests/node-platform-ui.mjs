import assert from "node:assert/strict";
import { expect } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { assertOverlay, saveSurface } from "./panel-surfaces.mjs";

const nodes = "/ui/ops/nodes";
const platforms = "/ui/ops/platforms";
const screenshotDir = process.env.PRISM_NODE_UI_SCREENSHOT_DIR ?? process.env.PRISM_UI_SCREENSHOT_DIR;
const isNodes = (response) => new URL(response.url()).pathname === "/api/v1/nodes";
async function visit(page, origin, query = "") {
  const response = page.waitForResponse(isNodes);
  await page.goto(origin + nodes + query);
  assert.equal((await response).status(), 200);
  await page.getByRole("heading", { name: "节点", exact: true }).waitFor();
}
async function choose(page, label, option) {
  await page.getByRole("combobox", { name: label, exact: true }).click();
  await page.getByRole("option", { name: option, exact: true }).click();
}
async function changed(page, action) {
  const pending = page.waitForResponse(isNodes);
  await action();
  const response = await pending;
  assert.equal(response.status(), 200, `node filter rejected: ${new URL(response.url()).search}`);
  const body = await response.json();
  await expect(page.locator(".workspace-node-list .workspace-object-row")).toHaveCount(body.items.length);
  for (const [index, node] of body.items.entries()) {
    // The real API prefixes tags with the subscription name; the row presents
    // the node's alias, including any slashes inside that alias.
    const tag = node.tags[0];
    const name = tag ? tag.tag.slice(tag.subscription_name.length + 1) : node.node_hash.slice(0, 12);
    await expect(page.locator(".workspace-node-list .workspace-object-row").nth(index)).toContainText(name);
  }
  return { body, query: new URL(response.url()).searchParams };
}

export function registerNodePlatformChecks(check) {
  check("regression: node list and centered details show business facts without source metadata", async ({ page, origin }) => {
    try {
      for (const width of [390, 1100, 1440, 2560]) {
        await page.setViewportSize({ width, height: 900 });
        await visit(page, origin, "?capability=gemini");
        const row = page.locator(".workspace-node-list .workspace-object-row").first();
        await expect(row).toContainText("Local Alpha");
        await expect(row).toContainText("HTTP");
        await expect(row).toContainText("纯净度");
        await expect(row).toContainText("Gemini：");
        await expect(page.getByRole("combobox", { name: "订阅来源", exact: true })).toHaveCount(0);
        const forbidden = /UI_Local|无订阅来源|订阅来源|来源与标签|数据来源|技术来源|网络字段来源|来源发现|等待来源数据|供应商声明|MEASUREMENT_DISABLED|provider=|IPPure|IPPURE_|fraudScore/;
        assert.doesNotMatch(await page.locator("main").innerText(), forbidden);
        await row.focus();
        await row.press("Enter");
        const dialog = page.getByRole("dialog");
        await assertOverlay(page, dialog);
        assert.ok(await dialog.locator(".workspace-node-detail__identity > .workspace-status").evaluate((element) => {
          const label = element.lastElementChild;
          return label && element.scrollWidth <= element.clientWidth + 1 && label.scrollWidth <= label.clientWidth + 1;
        }), "the node status must remain readable on mobile");
        assert.doesNotMatch(await dialog.innerText(), forbidden);
        await expect(dialog.getByRole("heading", { name: "节点标签", exact: true })).toBeVisible();
        await saveSurface(page, "node-centered-dialog");
        await dialog.getByRole("button", { name: "打开完整页面", exact: true }).click();
        await expect(page.getByRole("heading", { name: "节点属性", exact: true })).toBeVisible();
        assert.doesNotMatch(await page.locator("main").innerText(), forbidden);
        await saveSurface(page, "node-business-facts");
      }
    } finally { await page.setViewportSize({ width: 1440, height: 900 }); }
  });

  check("regression: lease labels omit subscription prefixes on both workspaces", async ({ page, origin, context }) => {
    const { items } = await (await context.request.get(origin + "/api/v1/platforms?limit=1")).json();
    const platform = items[0];
    const lease = {platform_id:platform.id,platform_name:platform.name,account:"lease-label-account",node_hash:"lease-label-node",node_tag:"Internal source/Local Alpha",egress_ip:"192.0.2.20",expiry:new Date(Date.now() + 3_600_000).toISOString(),last_accessed:new Date().toISOString()};
    const ownPattern = "**/api/v1/me/leases";
    const platformPattern = `**/api/v1/platforms/${platform.id}/leases?**`;
    await page.route(ownPattern, (route) => route.fulfill({json:{items:[lease]}}));
    await page.route(platformPattern, (route) => route.fulfill({json:{items:[lease],total:1,limit:50,offset:0}}));
    try {
      for (const width of [390, 1440]) {
        await page.setViewportSize({width,height:900});
        await page.goto(origin + "/ui/me/leases");
        const row = page.locator(".workspace-object-row").filter({hasText:lease.account});
        await expect(row).toContainText("Local Alpha");
        assert.doesNotMatch(await row.innerText(), /Internal source/);
        await row.press("Enter");
        const dialog = page.getByRole("dialog");
        await assertOverlay(page, dialog);
        await expect(dialog).toContainText("Local Alpha");
        assert.doesNotMatch(await dialog.innerText(), /Internal source/);
        await page.keyboard.press("Escape");
        await page.goto(origin + `${platforms}/${platform.id}?tab=ops`);
        const platformRow = page.locator(".workspace-lease-list .workspace-object-row");
        await expect(platformRow).toContainText("Local Alpha");
        assert.doesNotMatch(await platformRow.innerText(), /Internal source/);
        await saveSurface(page, "lease-node-labels");
      }
    } finally {
      await page.unroute(ownPattern);
      await page.unroute(platformPattern);
      await page.setViewportSize({width:1440,height:900});
    }
  });

  check("regression: advanced filter button opens the controls", async ({ page, origin }) => {
    await visit(page, origin);
    await page.getByRole("button", { name: "高级筛选", exact: true }).click();
    await expect(page.getByRole("combobox", { name: "质量状态", exact: true })).toBeVisible();
  });

  for (const label of ["质量状态", "风险等级"]) {
    check(`regression: every ${label} option is accepted by the real node API`, async ({ page, origin }) => {
      await visit(page, origin);
      await page.locator("#node-advanced-filters summary").click();
      await page.getByRole("combobox", { name: label, exact: true }).click();
      const options = (await page.getByRole("option").allTextContents()).filter((text) => text !== "全部");
      await page.keyboard.press("Escape");
      for (const option of options) await changed(page, () => choose(page, label, option));
    });
  }

  check("regression: all remaining visible enum filters match the real API contract", async ({ page, origin }) => {
    for (const label of ["IP 类型", "纯净度状态", "纯净度等级", "协议", "判定", "最低置信度", "原生 IP"]) {
      await visit(page, origin);
      await page.locator("#node-advanced-filters summary").click();
      await page.getByRole("combobox", { name: label, exact: true }).click();
      const options = (await page.getByRole("option").allTextContents()).filter((text) => text !== "全部");
      await page.keyboard.press("Escape");
      for (const option of options) await changed(page, () => choose(page, label, option));
    }
  });

  check("regression: probe time sends an RFC3339 instant", async ({ page, origin }) => {
    await visit(page, origin);
    await page.locator("#node-advanced-filters summary").click();
    const result = await changed(page, () => page.getByLabel("探测时间", { exact: true }).fill("2026-01-02T03:04"));
    assert.match(result.query.get("probed_since"), /T.*(?:Z|[+-]\d\d:\d\d)$/);
    assert.equal(result.body.total, 0);
  });

  check("regression: removing a legacy health chip clears its actual filter", async ({ page, origin }) => {
    await visit(page, origin, "?enabled=false");
    await changed(page, () => page.locator(".workspace-filter-chip button").first().click());
    assert.equal(new URL(page.url()).searchParams.has("enabled"), false);
    await expect(page.locator(".workspace-node-list .workspace-object-row")).toHaveCount(2);
  });

  check("regression: sorting changes field atomically and resets pagination", async ({ page, origin }) => {
    await visit(page, origin, "?sort=tag&order=desc");
    const { query } = await changed(page, () => choose(page, "排序字段", "连续失败"));
    assert.equal(query.get("sort_by"), "failure_count");
    assert.equal(query.get("sort_order"), "asc");
    assert.equal(query.get("offset"), "0");
  });

  check("regression: real node filters combine, return empty, clear and preserve URL state", async ({ page, origin }) => {
    await visit(page, origin);
    let result = await changed(page, () => page.getByLabel("搜索节点", { exact: true }).fill("Alpha"));
    assert.equal(result.body.total, 1);
    assert.match(result.body.items[0].display_tag, /Alpha/);
    await page.locator("#node-advanced-filters summary").click();
    result = await changed(page, () => choose(page, "协议", "HTTP"));
    assert.equal(result.body.total, 1);
    assert.equal(result.query.get("tag_keyword"), "Alpha");
    result = await changed(page, () => choose(page, "协议", "SOCKS"));
    assert.equal(result.body.total, 0);
    await expect(page.getByText("没有匹配的节点", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "清除全部", exact: true }).click();
    await expect(page.locator(".workspace-node-list .workspace-object-row")).toHaveCount(2);
    assert.equal(new URL(page.url()).search, "");
  });

  check("regression: Gemini capability shows reachability and unknown unlock truth", async ({ page, origin }) => {
    await visit(page, origin);
    await page.locator("#node-advanced-filters summary").click();
    await choose(page, "平台能力", "Gemini");
    await expect(page.getByText("Gemini 当前仅检测网页可达性，完整解锁状态未知。", { exact: true })).toBeVisible();
    await page.getByRole("combobox", { name: "Gemini 检测结果", exact: true }).click();
    const options = await page.getByRole("option").allTextContents();
    assert.ok(options.includes("网页可达（解锁未知）"));
    assert.ok(options.includes("探测结果未知"));
    assert.ok(!options.some((text) => /^(available|blocked|region_limited)$/.test(text)));
    await page.keyboard.press("Escape");
    await expect(page.locator(".workspace-node-list")).toContainText("Gemini：未知 / 无有效探测");
    const result = await changed(page, () => choose(page, "Gemini 检测结果", "探测结果未知"));
    assert.deepEqual(result.query.getAll("check"), ["gemini:unknown"]);
    assert.equal(result.body.total, 0, "the API only matches explicit, unexpired unknown outcomes");
    await expect(page.getByText("结果筛选仅匹配有效探测；未检测或已过期的节点请在全部结果中查看。", { exact: true })).toBeVisible();
  });

  check("regression: platform entry is a direct link with reversible history", async ({ page, origin }) => {
    await page.goto(origin + platforms + "?keyword=Default");
    const link = page.locator(".workspace-platform-list a.workspace-object-row").first();
    await expect(link).toHaveAttribute("href", /\/ops\/platforms\/[^/?]+$/);
    const href = await link.getAttribute("href");
    await link.focus();
    await page.keyboard.press("Enter");
    await page.waitForURL(origin + href);
    await expect(page.getByRole("tab", { name: "监控", exact: true })).toBeVisible();
    await expect(page.getByText("打开完整工作面", { exact: true })).toHaveCount(0);
    await page.getByRole("link", { name: "返回平台目录", exact: true }).click();
    await expect(page).toHaveURL(origin + platforms + "?keyword=Default");
    await page.goForward();
    await expect(page).toHaveURL(origin + href);
  });

  check("regression: invalid numeric filters cannot silently show unfiltered nodes", async ({ page, origin }) => {
    await visit(page, origin);
    await page.locator("#node-advanced-filters summary").click();
    await page.getByLabel("纯净度下限", { exact: true }).fill("-1");
    await expect(page.getByRole("alert").filter({ hasText: "纯净度" })).toBeVisible();
    await expect(page.locator(".workspace-node-list .workspace-object-row")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "导出", exact: true })).toBeDisabled();
  });

  check("regression: a failed new filter cannot leave old rows looking like matches", async ({ page, origin }) => {
    await visit(page, origin);
    await page.route("**/api/v1/nodes?**", (route) => route.fulfill({ status: 503, json: { error: { code: "UNAVAILABLE", message: "Fixture unavailable" } } }));
    try {
      await page.getByLabel("搜索节点", { exact: true }).fill("no-match");
      await expect(page.locator(".workspace-node-list .workspace-object-row")).toHaveCount(0);
      await expect(page.getByText("节点数据读取失败", { exact: true })).toBeVisible({ timeout: 15000 });
    } finally { await page.unroute("**/api/v1/nodes?**"); }
  });

  check("regression: Gemini policy uses platform-specific evidence labels", async ({ page, origin, context }) => {
    const { items } = await (await context.request.get(origin + "/api/v1/platforms?limit=1")).json();
    await page.goto(origin + platforms + `/${items[0].id}?tab=config`);
    await page.locator(".workspace-criteria__advanced summary").click();
    await choose(page, "添加解锁要求", "Gemini (gemini)");
    await expect(page.getByText("Gemini 当前仅检测网页可达性，完整解锁状态未知。", { exact: true })).toBeVisible();
    await page.getByRole("combobox", { name: "要求的结果", exact: true }).click();
    await expect(page.getByRole("option", { name: "网页可达（解锁未知）", exact: true })).toBeVisible();
    await expect(page.getByRole("option", { name: "available", exact: true })).toHaveCount(0);
    await page.keyboard.press("Escape");
  });

  check("regression: selecting the Gemini routing platform changes the capability context", async ({ page, origin, context }) => {
    const csrf = (await context.cookies()).find((cookie) => cookie.name === "prism_csrf");
    const headers = { "X-Prism-CSRF": decodeURIComponent(csrf.value) };
    const response = await context.request.post(origin + "/api/v1/platforms", { headers, data: { name: "Gemini" } });
    assert.equal(response.status(), 201);
    const platform = await response.json();
    try {
      await visit(page, origin, "?capability=chatgpt");
      await choose(page, "路由平台", "Gemini");
      await page.locator("#node-advanced-filters summary").click();
      await expect(page.getByRole("combobox", { name: "Gemini 检测结果", exact: true })).toBeVisible();
      await expect(page.getByText("Gemini 当前仅检测网页可达性，完整解锁状态未知。", { exact: true })).toBeVisible();
      assert.equal(new URL(page.url()).searchParams.get("platform_id"), platform.id);
    } finally { assert.ok((await context.request.delete(origin + `/api/v1/platforms/${platform.id}`, { headers })).ok()); }
  });

  check("regression: Gemini history describes the measured web reachability", async ({ page, origin, context }) => {
    const { items } = await (await context.request.get(origin + "/api/v1/nodes?limit=1")).json();
    const hash = items[0].node_hash;
    await page.route(`**/api/v1/intel/nodes/${hash}`, (route) => route.fulfill({ json: {
      node_hash: hash, egress: {}, history: [], checks: [{ check_id: "gemini", outcome: "available", observed_at_ns: Date.now() * 1e6, valid_until_ns: (Date.now() + 60000) * 1e6 }],
    } }));
    try {
      await page.goto(origin + nodes + `/${hash}`);
      await expect(page.getByText("检测 gemini：网页可达（解锁未知）", { exact: true })).toBeVisible();
    } finally { await page.unroute(`**/api/v1/intel/nodes/${hash}`); }
  });

  check("regression: Gemini evidence never becomes a full unlock claim in node details", async ({ page, origin }) => {
    await page.route("**/api/v1/nodes**", async (route) => {
      const response = await route.fetch();
      const body = await response.json();
      const withEvidence = (node) => ({ ...node, intel: { ...node.intel, checks: { gemini: "available" } } });
      await route.fulfill({ response, json: body.items ? { ...body, items: body.items.map(withEvidence) } : withEvidence(body) });
    });
    try {
      await visit(page, origin, "?capability=gemini");
      await page.locator(".workspace-node-list .workspace-object-row").first().click();
      const dialog = page.getByRole("dialog");
      await expect(dialog.getByText("网页可达（解锁未知）", { exact: true })).toBeVisible();
      await expect(dialog.getByText("Gemini 当前仅检测网页可达性，完整解锁状态未知。", { exact: true })).toBeVisible();
    } finally { await page.unroute("**/api/v1/nodes**"); }
  });

  check("regression: legacy platform selection redirects directly", async ({ page, origin, context }) => {
    const { items } = await (await context.request.get(origin + "/api/v1/platforms?limit=1")).json();
    await page.goto(origin + platforms + `?selected=${items[0].id}`);
    await expect(page).toHaveURL(origin + platforms + `/${items[0].id}`);
    await expect(page.getByRole("tab", { name: "监控", exact: true })).toBeVisible();
  });

  check("regression: node detail back preserves the filtered inspector history", async ({ page, origin }) => {
    await visit(page, origin, "?tag_keyword=Alpha&sort=created_at&order=desc");
    await page.locator(".workspace-node-list .workspace-object-row").first().click();
    const previous = page.url();
    await page.getByRole("button", { name: "打开完整页面", exact: true }).click();
    await page.getByRole("link", { name: "返回列表", exact: true }).click();
    await expect(page).toHaveURL(previous);
    await expect(page.getByRole("dialog")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(origin + nodes + "?tag_keyword=Alpha&sort=created_at&order=desc");
  });

  check("regression: back navigation is top left at 390, 1100 and 1440", async ({ page, origin, context }) => {
    const { items: pool } = await (await context.request.get(origin + "/api/v1/nodes?limit=1")).json();
    const { items: services } = await (await context.request.get(origin + "/api/v1/platforms?limit=1")).json();
    for (const width of [390, 1100, 1440]) {
      await page.setViewportSize({ width, height: 900 });
      for (const [path, name, key] of [[`${platforms}/${services[0].id}`, "返回平台目录", "platform"], [`${nodes}/${pool[0].node_hash}`, "返回列表", "node"]]) {
        await page.goto(origin + path);
        const back = page.getByRole("link", { name, exact: true });
        await expect(back).toBeVisible();
        const box = await back.boundingBox();
        const title = await page.getByRole("heading", { level: 1 }).boundingBox();
        assert.ok(box.x <= title.x + 8, `${name} must align with the left title edge`);
        assert.ok(box.y < title.y, `${name} must precede the page title`);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no horizontal overflow");
        await expect(page.locator(".workspace-register-state")).toHaveCount(0);
        if (screenshotDir) {
          await mkdir(screenshotDir, { recursive: true });
          await page.screenshot({ path: join(screenshotDir, `regression-${key}-${width}.png`), fullPage: true });
        }
      }
      await visit(page, origin, "?capability=gemini");
      await page.locator("#node-advanced-filters summary").click();
      await expect(page.getByRole("combobox", { name: "Gemini 检测结果", exact: true })).toBeVisible();
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "expanded filters must fit");
      if (screenshotDir) {
        await page.screenshot({ path: join(screenshotDir, `regression-filters-${width}.png`), fullPage: true });
        await page.getByRole("combobox", { name: "Gemini 检测结果", exact: true }).scrollIntoViewIfNeeded();
        await page.getByRole("combobox", { name: "Gemini 检测结果", exact: true }).click();
        await page.screenshot({ path: join(screenshotDir, `regression-gemini-${width}.png`), fullPage: true });
        await page.keyboard.press("Escape");
      }
    }
    await page.setViewportSize({ width: 1440, height: 900 });
  });

  check("regression: real pagination preserves intersections and resets on filter, size and clear", async ({ page, origin, context }) => {
    const csrf = (await context.cookies()).find((cookie) => cookie.name === "prism_csrf");
    const headers = { "X-Prism-CSRF": decodeURIComponent(csrf.value) };
    const created = await context.request.post(origin + "/api/v1/subscriptions", { headers, data: {
      name: "UI_Paging", source_type: "local", enabled: true, auto_intel: false,
      content: JSON.stringify({ outbounds: Array.from({ length: 25 }, (_, index) => ({ type: "http", tag: `Page ${String(index).padStart(2, "0")}`, server: "127.0.0.1", server_port: 20000 + index, tls: { enabled: true, server_name: "localhost" } })) }),
    } });
    assert.equal(created.status(), 201);
    const source = await created.json();
    const refreshed = await context.request.post(origin + `/api/v1/subscriptions/${source.id}/actions/refresh`, { headers });
    assert.ok(refreshed.ok());
    await expect.poll(async () => (await (await context.request.get(origin + `/api/v1/nodes?subscription_id=${source.id}`)).json()).total).toBe(25);
    try {
      await visit(page, origin, `?size=20&subscription_id=${source.id}&protocol=http`);
      const rows = page.locator(".workspace-node-list .workspace-object-row");
      await expect(rows).toHaveCount(20);
      const first = await rows.allTextContents();
      const next = await changed(page, () => page.getByRole("button", { name: "下一页", exact: true }).click());
      assert.ok(next.query.get("cursor"));
      assert.equal(next.body.total, 25);
      assert.equal(next.query.get("subscription_id"), source.id);
      assert.equal(next.query.get("protocol"), "http");
      assert.equal(next.body.items.length, 5);
      assert.ok(!(await rows.allTextContents()).some((text) => first.includes(text)));
      await page.getByRole("button", { name: "上一页", exact: true }).click();
      await expect(rows).toHaveCount(20);
      assert.deepEqual(await rows.allTextContents(), first);
      await page.getByRole("button", { name: "下一页", exact: true }).click();
      await expect(rows).toHaveCount(5);
      const filtered = await changed(page, () => page.getByLabel("搜索节点", { exact: true }).fill("Page 00"));
      assert.equal(filtered.body.total, 1);
      assert.equal(filtered.query.has("cursor"), false);
      assert.equal(filtered.query.get("offset"), "0");
      const resized = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return isNodes(response) && url.searchParams.get("limit") === "50" && url.searchParams.get("tag_keyword") === "Page 00";
      });
      await choose(page, "每页", "50 / 页");
      assert.equal((await resized).status(), 200);
      // The filtered row count stays at one across the resize, so it cannot
      // prove that the router has committed the new page size before clearing.
      await expect.poll(() => new URL(page.url()).searchParams.get("size")).toBe("50");
      await expect(page.getByRole("combobox", { name: "每页", exact: true })).toContainText("50 / 页");
      await expect(rows).toHaveCount(1);
      const cleared = await changed(page, () => page.getByRole("button", { name: "清除全部", exact: true }).click());
      assert.equal(cleared.query.get("limit"), "50");
      assert.equal(cleared.body.total, 27);
      await expect(rows).toHaveCount(27);
      await expect(page).toHaveURL(origin + nodes + "?size=50");
    } finally {
      assert.ok((await context.request.delete(origin + `/api/v1/subscriptions/${source.id}`, { headers })).ok());
    }
  });
}
