/**
 * Prism panel — browser regression check.
 *
 * One script, one run: it boots an isolated backend, drives a real Chromium
 * against the panel it serves, and asserts the design system the rewrite
 * introduced. It replaces the old per-feature browser scripts,
 * which asserted class names that no longer exist.
 *
 * Coverage is intentionally behavioural: the design tokens actually applied, the
 * shell actually navigable, the keyboard actually visible, the hero actually
 * drawn. No check looks for a Tailwind utility by name except the two structural
 * hooks documented inline.
 */
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { once } from "node:events";
import { existsSync, readFileSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import http from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const binary =
  process.env.PRISM_TEST_BACKEND ||
  process.env.PRISMX_TEST_BACKEND ||
  fileURLToPath(new URL("../../../../bin/prism", import.meta.url));
if (!existsSync(binary))
  throw new Error("Build bin/prism first, or set PRISM_TEST_BACKEND to a backend binary.");

// Playwright resolves its own bundled browser; the escape hatch is for a machine
// whose browser lives somewhere else, so no home directory is hard-coded here.
const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || undefined;

// The destinations the panel offers, in the order the rail lists them.
const RAIL_PATHS = [
  "/ui/dashboard",
  "/ui/nodes",
  "/ui/subscriptions",
  "/ui/platforms",
  "/ui/jobs",
  "/ui/request-logs",
  "/ui/endpoints",
  "/ui/rules",
  "/ui/resources",
  "/ui/system-config",
  "/ui/audit",
  // The management console the tenant API added, and the account surface every
  // session reaches: one more destination per page, in the order the rail
  // groups them (管理, then 账号).
  "/ui/admin/users",
  "/ui/admin/plans",
  "/ui/admin/invites",
  "/ui/admin/redeem-codes",
  "/ui/admin/usage",
  "/ui/admin/settings",
  "/ui/account-security",
];

// What a tenant session gets instead: the self-service destinations, which an
// operator never sees, plus the account surface both roles share.
const TENANT_RAIL = [
  "/ui/me",
  "/ui/me/keys",
  "/ui/me/guide",
  "/ui/me/plans",
  "/ui/me/leases",
  "/ui/account-security",
];

// Class tokens the redesign deleted. A trailing dash means "prefix". `card` is
// matched as a whole token, because the kit's own utilities are named around it.
// The board's replica classes are on the list too: their CSS went with the 1536px
// layer, so a `wb-slot-*` or a `wb-folded` left in the DOM is a pane the grid does
// not know about.
const REMOVED_CLASS_TOKENS = [
  "content",
  "node-name",
  "quality-network-facts",
  "settings-category",
  "syscfg-",
  "live-chart",
  "resource-total",
  "detail-header",
  "card",
  "toast-container",
  "nav-item",
  "workspace-bar",
  "wb-slot-",
  "wb-folded",
  "wb-board-stack",
  "wb-kpi-row",
  "wb-rail-",
  "wb-brand-",
];

// The ground and the ink, read from the light primitives in src/styles/design.css
// rather than written down here. The console ships light by default and offers dark
// as a switch; both themes are verified pair by pair by `npm run check:contrast`, and
// this file only has to prove the page painted the tokens it was asked for rather
// than falling back to the browser's black on white. Reading them is deliberate: a
// literal turns the next deliberate re-solve of the palette into a false failure.
const lightTheme = (() => {
  const css = readFileSync(new URL("../src/styles/design.css", import.meta.url), "utf8");
  const block = /\n:root \{([\s\S]*?)\n\}/.exec(css);
  if (!block) throw new Error("check-ui-live: design.css has no light :root block");
  return block[1];
})();
const lightToken = (name) => {
  const value = new RegExp(`--p-${name}:\\s*(#[0-9a-fA-F]{6})\\b`).exec(lightTheme);
  if (!value) throw new Error(`check-ui-live: the light theme declares no --p-${name}`);
  const hex = value[1].slice(1);
  return `rgb(${parseInt(hex.slice(0, 2), 16)}, ${parseInt(hex.slice(2, 4), 16)}, ${parseInt(hex.slice(4, 6), 16)})`;
};
const PAPER = lightToken("canvas");
const INK = lightToken("ink");

const LOCALE = "zh-CN";
const ADMIN_USER = "admin";
const DASHBOARD = "/ui/dashboard";
const NODES = "/ui/nodes";

/*
 * The heading `src/components/ErrorBoundary.tsx` renders when a route throws.
 * It is an `<main><h1>` exactly like every real page heading, so the selector
 * "a heading is visible" was satisfied by a crashed page — which is how a
 * broken route could report clean.
 */
const RENDER_FAILURE_TITLE = "界面渲染失败";

// ---------------------------------------------------------------------------
// Check registry
// ---------------------------------------------------------------------------

const checks = [];
function check(name, run) {
  checks.push({ name, run });
}

/**
 * Collects page errors and console errors, tagged with the route that produced
 * them. `reset` starts a new window: the sign-in flow legitimately probes
 * `/api/v1/system/info` anonymously and logs the deliberate 401, so those must
 * not be charged to the first route visited afterwards.
 */
function watchPage(page) {
  const messages = [];
  let route = "startup";
  page.on("pageerror", (error) => messages.push({ route, text: `pageerror: ${error.message}` }));
  page.on("console", (message) => {
    if (message.type() === "error") messages.push({ route, text: `console.error: ${message.text()}` });
  });
  return {
    reset: (next) => {
      route = next;
      messages.length = 0;
    },
    drain: () => messages.splice(0, messages.length),
  };
}

/** Runs in the page: every class token on every element, classified against the dead list. */
function collectRemovedTokens(tokens) {
  const hits = [];
  const seen = new Set();
  for (const element of document.querySelectorAll("[class]")) {
    for (const token of (element.getAttribute("class") || "").split(/\s+/)) {
      if (!token || seen.has(token)) continue;
      const removed = tokens.some((candidate) =>
        candidate.endsWith("-") ? token.startsWith(candidate) : token === candidate,
      );
      if (removed) {
        seen.add(token);
        hits.push({ token, tag: element.tagName.toLowerCase() });
      }
    }
  }
  return hits;
}

// ---------------------------------------------------------------------------
// Backend bootstrap (kept from the script this replaces)
// ---------------------------------------------------------------------------

async function reservePort() {
  const reservation = http.createServer();
  reservation.listen(0, "127.0.0.1");
  await once(reservation, "listening");
  const { port } = reservation.address();
  await new Promise((resolve) => reservation.close(resolve));
  return port;
}

function startBackend({ root, port, proxyToken }) {
  const backend = spawn(binary, [], {
    cwd: root,
    env: {
      ...process.env,
      PRISM_PROXY_TOKEN: proxyToken,
      PRISM_STATE_DIR: join(root, "state"),
      PRISM_CACHE_DIR: join(root, "cache"),
      PRISM_LOG_DIR: join(root, "logs"),
      PRISM_LISTEN_ADDRESS: "127.0.0.1",
      PRISM_PORT: String(port),
      PRISM_PROBE_CONCURRENCY: "2",
      PRISM_RESOURCE_FETCH_TIMEOUT: "2s",
      PRISM_QUALITY_ENABLED: "true",
      PRISM_QUALITY_API_KEY: "",
      PRISM_ABUSEIPDB_API_KEY: "",
      PRISM_QUALITY_DAILY_LIMIT: "10",
      PRISM_QUALITY_WORKERS: "1",
      PRISM_QUALITY_QUEUE_SIZE: "16",
    },
    stdio: ["ignore", "ignore", "pipe"],
  });
  let stderr = "";
  backend.stderr.on("data", (data) => {
    stderr = (stderr + data).slice(-4000);
  });
  return { backend, exit: once(backend, "exit"), stderr: () => stderr };
}

/**
 * Creates the administrator the panel signs in as.
 *
 * `prism init` is a one-shot: it refuses to run against a store that already
 * holds a user, and it is what writes the key pepper the session service needs,
 * so it has to happen before the backend is started. It also mints the proxy
 * token, which is returned here because the backend has to be given that same
 * token rather than one of its own.
 */
async function initBackend({ root, adminPassword }) {
  const init = spawn(
    binary,
    ["init", "--dir", root, "--admin-username", ADMIN_USER, "--admin-password", adminPassword],
    {
      cwd: root,
      env: {
        ...process.env,
        PRISM_STATE_DIR: join(root, "state"),
        PRISM_CACHE_DIR: join(root, "cache"),
        PRISM_LOG_DIR: join(root, "logs"),
      },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  let output = "";
  init.stdout.on("data", (data) => {
    output += data;
  });
  init.stderr.on("data", (data) => {
    output += data;
  });
  const [code] = await once(init, "exit");
  if (code !== 0) throw new Error(`prism init exited ${code}: ${output.trim()}`);

  const token = /^PRISM_PROXY_TOKEN=(.*)$/m.exec(readFileSync(join(root, ".env"), "utf8"))?.[1]?.trim();
  if (!token) throw new Error("prism init did not write PRISM_PROXY_TOKEN to .env");
  return token;
}

/**
 * The headers a state-changing request made from a session needs.
 *
 * The session cookie is HttpOnly and rides along with the context on its own,
 * but the double-submit token does not: the server compares the header against
 * the cookie, so a caller that is not the console has to echo it explicitly.
 * Reading it back is also the assertion that the login issued one.
 */
async function csrfHeaders(context) {
  const cookies = await context.cookies();
  const token = cookies.find((cookie) => cookie.name === "prism_csrf")?.value ?? "";
  if (!token) throw new Error("the signed-in browser context holds no prism_csrf cookie");
  return { "X-Prism-CSRF": token };
}

async function waitForHealth(origin, backend, stderr) {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (backend.exitCode !== null) throw new Error("Test backend exited: " + stderr());
    try {
      const response = await fetch(origin + "/healthz", { signal: AbortSignal.timeout(500) });
      if (response.ok) return;
    } catch {
      // Not listening yet.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("Test backend never answered /healthz");
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

/**
 * Gives the isolated backend a real inventory: a local subscription whose two
 * outbounds point at closed loopback ports, so nothing here reaches the network
 * beyond localhost. The exit map, the node table and the subscription table all
 * need at least one row to be worth asserting on.
 *
 * It runs through the signed-in browser context, so these calls carry the same
 * session and CSRF token the panel itself uses.
 */
async function seedInventory(origin, context) {
  const headers = { "Content-Type": "application/json", ...(await csrfHeaders(context)) };
  const content = JSON.stringify({
    outbounds: [
      { type: "http", tag: "Local Alpha", server: "127.0.0.1", server_port: 9 },
      { type: "http", tag: "Local Beta", server: "127.0.0.1", server_port: 10 },
    ],
  });
  const created = await context.request.post(origin + "/api/v1/subscriptions", {
    headers,
    data: { name: "UI_Local", source_type: "local", enabled: true, content },
  });
  assert(created.ok(), `fixture subscription must be created (HTTP ${created.status()})`);
  const { id } = await created.json();

  const refreshed = await context.request.post(origin + `/api/v1/subscriptions/${id}/actions/refresh`, {
    headers,
  });
  assert(refreshed.ok(), `fixture subscription must refresh (HTTP ${refreshed.status()})`);

  for (let attempt = 0; attempt < 40; attempt++) {
    const listed = await context.request.get(origin + "/api/v1/nodes?limit=1", { headers });
    const body = await listed.json();
    if ((body.total ?? 0) > 0) return body.total;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error("the fixture subscription never imported its nodes");
}

// ---------------------------------------------------------------------------
// Checks
// ---------------------------------------------------------------------------

check("backend boots, inventory imports and the panel signs in", async ({ origin, page, context, adminPassword }) => {
  const health = await fetch(origin + "/healthz");
  assert.equal(health.status, 200, "/healthz must answer 200");

  await page.goto(origin + "/ui/");
  await page.locator("#username").waitFor({ state: "visible", timeout: 15000 });
  assert.match(page.url(), /\/ui\/login/, "an anonymous visit to /ui/ must land on the sign-in page");

  await page.locator("#username").fill(ADMIN_USER);
  await page.locator("#password").fill(adminPassword);
  await page.locator('form button[type="submit"]').click();
  await page.waitForURL(/\/ui\/dashboard$/, { timeout: 15000 });

  // The session lives in an HttpOnly cookie, so nothing that grants access may
  // be left behind in the storage a script on the page can read.
  const stored = await page.evaluate(() => [
    sessionStorage.getItem("prism.admin-session"),
    localStorage.getItem("prism.admin-session"),
  ]);
  assert.deepEqual(stored, [null, null], "the session must not be written to browser storage");

  const nodes = await seedInventory(origin, context);
  assert(nodes >= 2, `the fixture subscription must import two nodes (imported ${nodes})`);
});

check("every route renders without a page or console error", async ({ origin, page, errors }) => {
  const failures = [];
  for (const path of RAIL_PATHS) {
    errors.reset(path);
    await page.goto(origin + path);
    if (!(await rendered(page))) failures.push(`${path}: no page heading rendered`);
    // `rendered` already rejects the boundary's heading; this names the crash in
    // the report instead of leaving a route that merely "did not render".
    const crashed = await crashReport(page);
    if (crashed) failures.push(`${path}: the error boundary rendered 界面渲染失败 (${crashed})`);
    await quiet(page);
    for (const message of errors.drain()) failures.push(`${path}: ${message.text}`);
  }
  assert.deepEqual(failures, [], "routes that did not render cleanly:\n  " + failures.join("\n  "));
});

check("no removed class token survives in the DOM", async ({ origin, page }) => {
  const failures = [];
  for (const path of RAIL_PATHS) {
    await page.goto(origin + path);
    await rendered(page);
    const hits = await page.evaluate(collectRemovedTokens, REMOVED_CLASS_TOKENS);
    for (const hit of hits) failures.push(`${path}: "${hit.token}" on <${hit.tag}>`);
  }
  assert.deepEqual(failures, [], "removed class tokens found:\n  " + failures.join("\n  "));
});

check("legacy node envelopes do not crash the node pool", async ({ origin, page, errors }) => {
  const legacyItems = [
    null,
    42,
    { node_hash: "incomplete-node" },
    {
      node_hash: "legacy-node",
      created_at: "2026-01-01T00:00:00Z",
      enabled: true,
      has_outbound: true,
      failure_count: 0,
      tags: [],
    },
  ];
  const nodesRoute = /\/api\/v1\/nodes(?:\?|$)/;
  await page.route(nodesRoute, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ total: 1, items: legacyItems }),
    });
  });
  try {
    errors.reset("/ui/nodes legacy envelope");
    await page.goto(origin + NODES + "?legacy_smoke=1");
    await rendered(page);
    await page.waitForTimeout(250);
    const text = await page.locator("main").innerText();
    assert.match(text, /节点池/);
    assert.doesNotMatch(text, /界面渲染失败|toLocaleString/);
    assert.deepEqual(errors.drain(), [], "legacy node envelope must not emit a render error");
  } finally {
    await page.unroute(nodesRoute);
  }
});

// The usage alert queue is the one console surface a deployment cannot produce
// for a test run: an alert only exists once the detector has seen a real usage
// bucket cross a plan quota. Routing the two endpoint calls gives the panel a
// row to render, so the test still covers what an operator sees and clicks.
check("the usage alert queue renders and clears an alert", async ({ origin, page, errors }) => {
  const alertsRoute = /\/api\/v1\/admin\/alerts(?:\?|$)/;
  const acknowledgeRoute = /\/api\/v1\/admin\/alerts\/[^/]+\/actions\/acknowledge$/;
  const alertRow = {
    id: "alrt_ui_check",
    kind: "usage_anomaly",
    user_id: "usr_ui_check",
    username: "ui-alert-tenant",
    plan_id: "plan_ui_check",
    plan_name: "UI Alert Plan",
    window_minutes: 60,
    threshold_ratio: 0.3,
    used_bytes: 4294967296,
    quota_bytes: 10737418240,
    detected_at_ns: 1767225600000000000,
    acknowledged_at_ns: 0,
    note: "",
  };
  const listURLs = [];
  let acknowledged = 0;
  await page.route(alertsRoute, async (route) => {
    listURLs.push(route.request().url());
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [alertRow] }),
    });
  });
  await page.route(acknowledgeRoute, async (route) => {
    acknowledged += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ ...alertRow, acknowledged_at_ns: Date.now() * 1000000 }),
    });
  });
  try {
    errors.reset("/ui/admin/usage alert queue");
    await page.goto(origin + "/ui/admin/usage");
    await rendered(page);
    const main = page.locator("main");
    await main.getByText("ui-alert-tenant").first().waitFor({ timeout: 5000 });
    assert.ok(
      listURLs.some((url) => url.includes("unacknowledged=true")),
      "the queue must open on the unacknowledged alerts: " + listURLs.join(", "),
    );
    await main.getByRole("button", { name: "确认" }).first().click();
    for (let i = 0; i < 60 && acknowledged === 0; i += 1) await page.waitForTimeout(50);
    assert.equal(acknowledged, 1, "clicking 确认 must acknowledge the alert");
    assert.deepEqual(errors.drain(), [], "the alert queue must not emit a console error");
  } finally {
    await page.unroute(alertsRoute);
    await page.unroute(acknowledgeRoute);
  }
});

check("the design tokens are the ones actually applied", async ({ origin, page }) => {
  await page.goto(origin + DASHBOARD);
  await rendered(page);
  const body = await page.evaluate(() => {
    const style = getComputedStyle(document.body);
    return { background: style.backgroundColor, color: style.color };
  });
  assert.equal(body.background, PAPER, "body must sit on the canvas token");
  assert.equal(body.color, INK, "body must be written in the ink token");

  // A data cell is mono by construction (Table.tsx TDNum + design.css .cell-num).
  // The class the cell actually carries is the one in Table.tsx; reading it from
  // there would couple this file to the source, so it is spelled out here.
  await page.goto(origin + "/ui/subscriptions");
  await rendered(page);
  await page.locator("td.cell-num").first().waitFor({ state: "attached", timeout: 15000 });
  const cell = await page.evaluate(() => {
    for (const element of document.querySelectorAll("td.cell-num")) {
      const text = (element.textContent || "").trim();
      if (/\d/.test(text)) return { text, family: getComputedStyle(element).fontFamily };
    }
    return null;
  });
  assert(cell, "a table cell carrying a number must exist to read the mono token from");
  assert.match(cell.family, /IBM Plex Mono/, `the numeric cell "${cell.text}" must read in the mono token`);
});

check("the rail reaches every destination and marks the current one", async ({ origin, page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(origin + DASHBOARD);
  await rendered(page);

  const rail = page.locator('nav[aria-label="主导航"]');
  await rail.waitFor({ state: "visible", timeout: 15000 });
  // The rail is one list, so this is an exact count rather than a lower bound:
  // every destination the console offers is here, and nothing else is.
  assert.equal(await rail.locator("a:visible").count(), RAIL_PATHS.length, "the rail must carry one link per destination");
  for (const path of RAIL_PATHS) {
    assert.equal(await rail.locator(`a[href="${path}"]:visible`).count(), 1, `the rail must link to ${path}`);
  }
  // aria-current is written by React after the history entry changes, so wait for
  // it rather than reading the attribute the instant the URL moves.
  const dashboardLink = rail.locator(`a[href="${DASHBOARD}"][aria-current="page"]:visible`);
  await dashboardLink.waitFor({ state: "attached", timeout: 15000 });
  assert.equal(await rail.locator('a[aria-current="page"]:visible').count(), 1, "exactly one destination is current");

  await rail.locator(`a[href="${NODES}"]:visible`).click();
  await page.waitForURL(/\/ui\/nodes$/);
  const nodesLink = rail.locator(`a[href="${NODES}"][aria-current="page"]:visible`);
  await nodesLink.waitFor({ state: "attached", timeout: 15000 });
  assert.equal(await rail.locator('a[aria-current="page"]:visible').count(), 1, "exactly one destination is current");
  assert.equal(
    await nodesLink.getAttribute("aria-current"),
    "page",
    "the clicked route must become current",
  );
});

/**
 * The tenant half of the console, which no other check reaches. The bundle and
 * the routing are shared, so the honest proof is a browser: sign a tenant in and
 * assert both what they get (their own rail, their own pages) and what they must
 * never get (a management route bounces them home, the admin API answers 403).
 */
check("a tenant session reaches the self-service panel and nothing else", async ({ origin, browser, context }) => {
  const adminHeaders = { "Content-Type": "application/json", ...(await csrfHeaders(context)) };
  const suffix = randomBytes(4).toString("hex");
  const planResponse = await context.request.post(origin + "/api/v1/admin/plans", {
    headers: adminHeaders,
    data: {
      name: `UI Tenant ${suffix}`,
      platforms: [],
      ip_tiers: [],
      traffic_bytes: 0,
      period: "30d",
      max_concurrent: 5,
      max_rps: 10,
      max_bandwidth_bps: 0,
      max_keys: 2,
      duration_days: 30,
      status: "active",
    },
  });
  const plan = await planResponse.json();
  assert(planResponse.ok(), `the fixture plan must be created (HTTP ${planResponse.status()}: ${JSON.stringify(plan)})`);

  const tenantName = `ui-tenant-${suffix}`;
  const tenantPassword = randomBytes(12).toString("hex");
  const userResponse = await context.request.post(origin + "/api/v1/admin/users", {
    headers: adminHeaders,
    data: {
      username: tenantName,
      password: tenantPassword,
      role: "user",
      plan_id: plan.id,
      duration_days: 30,
    },
  });
  const user = await userResponse.json();
  assert(userResponse.ok(), `the fixture tenant must be created (HTTP ${userResponse.status()}: ${JSON.stringify(user)})`);

  const tenantContext = await browser.newContext({
    locale: LOCALE,
    viewport: { width: 1440, height: 900 },
    reducedMotion: "reduce",
  });
  await tenantContext.addInitScript((locale) => {
    localStorage.setItem("prism.locale", locale);
  }, LOCALE);
  const tenantPage = await tenantContext.newPage();
  const tenantErrors = watchPage(tenantPage);
  try {
    await tenantPage.goto(origin + "/ui/");
    await tenantPage.locator("#username").waitFor({ state: "visible", timeout: 15000 });
    await tenantPage.locator("#username").fill(tenantName);
    await tenantPage.locator("#password").fill(tenantPassword);
    await tenantPage.locator('form button[type="submit"]').click();
    // The landing page comes from the role the server returned, not from a path
    // baked into the sign-in form.
    await tenantPage.waitForURL(/\/ui\/me$/, { timeout: 15000 });

    const rail = tenantPage.locator('nav[aria-label="主导航"]');
    await rail.waitFor({ state: "visible", timeout: 15000 });
    assert.equal(
      await rail.locator("a:visible").count(),
      TENANT_RAIL.length,
      "a tenant rail holds the self-service destinations and nothing else",
    );
    for (const path of TENANT_RAIL) {
      assert.equal(await rail.locator(`a[href="${path}"]:visible`).count(), 1, `the tenant rail must link to ${path}`);
    }

    for (const path of TENANT_RAIL) {
      tenantErrors.reset(path);
      await tenantPage.goto(origin + path);
      assert(await rendered(tenantPage), `${path}: no page heading rendered`);
      const crashed = await crashReport(tenantPage);
      assert.equal(crashed, null, `${path}: the error boundary rendered 界面渲染失败 (${crashed})`);
      await quiet(tenantPage);
      const logged = tenantErrors.drain();
      assert.deepEqual(logged, [], `${path}: ${logged.map((entry) => entry.text).join("; ")}`);
    }

    // A management path must be indistinguishable from one that was never
    // routed: same page, same URL, no bounce home. Sending the tenant back to
    // /ui/me would confirm the path exists, which is what a prober wants.
    await tenantPage.goto(origin + DASHBOARD);
    await tenantPage.locator("main").waitFor({ state: "visible", timeout: 15000 });
    const managementText = await tenantPage.locator("main").innerText();
    await tenantPage.goto(origin + "/ui/no-such-route");
    await tenantPage.locator("main").waitFor({ state: "visible", timeout: 15000 });
    assert.equal(
      managementText,
      await tenantPage.locator("main").innerText(),
      "a management path must answer exactly what an unrouted path answers",
    );

    const refused = await tenantContext.request.get(origin + "/api/v1/admin/users?limit=1");
    // 404, not 403: a refusal that named the path would confirm it exists.
    assert.equal(refused.status(), 404, "the admin API must answer a tenant exactly as it answers an unrouted path");
    const unrouted = await tenantContext.request.get(origin + "/api/v1/admin/no-such-endpoint");
    assert.equal(unrouted.status(), refused.status(), "a management path and an unrouted path must be indistinguishable");
    const ownOverview = await tenantContext.request.get(origin + "/api/v1/me/overview");
    assert(ownOverview.ok(), `the tenant must read their own overview (HTTP ${ownOverview.status()})`);
  } finally {
    await tenantContext.close();
  }
});

check("keyboard focus is visible", async ({ origin, page }) => {
  await page.goto(origin + DASHBOARD);
  await rendered(page);

  let focused = null;
  for (let press = 0; press < 40 && !focused; press++) {
    await page.keyboard.press("Tab");
    focused = await page.evaluate(() => {
      const element = document.activeElement;
      if (!element || element === document.body) return null;
      const width = Number.parseFloat(getComputedStyle(element).outlineWidth);
      if (!Number.isFinite(width) || width < 2) return null;
      const label = (element.getAttribute("aria-label") || element.textContent || "").trim().slice(0, 40);
      return { tag: element.tagName.toLowerCase(), label, width };
    });
  }
  assert(focused, "tabbing must reach a control drawn with an outline of at least 2px");
});

check("the page never overflows horizontally", async ({ origin, page }) => {
  const failures = [];
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    for (const path of [DASHBOARD, NODES]) {
      await page.goto(origin + path);
      await rendered(page);
      await page.waitForTimeout(150);
      const metrics = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
        bodyScrollWidth: document.body.scrollWidth,
        innerWidth: window.innerWidth,
      }));
      if (metrics.scrollWidth > metrics.clientWidth) {
        failures.push(
          `${path} @${viewport.width}px: document scrollWidth ${metrics.scrollWidth} > viewport ${metrics.clientWidth}` +
            ` (body ${metrics.bodyScrollWidth}, window ${metrics.innerWidth})`,
        );
      }
    }
  }
  assert.deepEqual(failures, [], "horizontal overflow:\n  " + failures.join("\n  "));
});

check("the dashboard hero renders", async ({ origin, page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(origin + DASHBOARD);
  await rendered(page);

  // The exit plate is an offline SVG projection. Assert the rendered surface
  // rather than an implementation-specific canvas node.
  const map = page.locator("main .dash-map").first();
  await map.waitFor({ state: "visible", timeout: 20000 });
  const svg = map.locator("svg").first();
  await svg.waitFor({ state: "attached", timeout: 20000 });
  const box = await svg.boundingBox();
  assert(box && box.width > 0 && box.height > 0, "the exit map must draw an SVG with a real size");

  // The instrument strip is the ReadoutStrip: a row of cells divided by hairlines.
  // Each cell's number is a `Numeral`, which is the class that carries the value
  // (`.readout` is only on a unit suffix, and these cells have no unit). Measuring
  // by the value keeps the assertion about content rather than about the layout
  // classes the strip happened to use, which have already changed once.
  const stats = page.locator("main .dash-map__stats").first();
  await stats.waitFor({ state: "visible", timeout: 15000 });
  assert(/\d/.test((await stats.textContent()) || ""), "the map stats must show numbers");
});

check("every theme is legible, not just the one the art was drawn in", async ({ origin, page }) => {
  /*
   * The console ships dark and the reference art is a dark board, so a literal is
   * the natural thing to write while matching it — and a literal in a component's
   * own rule is invisible to `check-contrast.mjs`, which reads the `--p-*`
   * primitives in `design.css`. That is not hypothetical: the workbench's hero
   * heading, status card, metric values and labels were all measured against a dark
   * ground, and on paper they rendered as white on white — 1.02:1, with every gate
   * green. This check is the one that would have caught it.
   *
   * Two decisions worth knowing before editing it:
   *
   *  - **The ground is read from the rendered pixels**, not from a walk up the
   *    ancestor chain. An earlier version of this check composited
   *    `backgroundColor` by hand and treated the dark panel's
   *    `rgba(255, 255, 255, 0.043)` as opaque white, which turned four legible
   *    badges into false failures. Gradients, glass, and images all paint, and only
   *    the pixels know what the ground under a word actually is. Text is made
   *    transparent for the screenshot — colour does not affect layout — so the
   *    sample is the ground itself rather than a glyph.
   *  - **Ink comes from the computed style**, because that is the colour the word is
   *    painted in; sampling glyph pixels would measure antialiasing instead.
   */
  /*
   * The board now uses the Precision Glass composition: a welcome band, a
   * freshness bar, panel headers, pool fact tiles and traffic readouts.
   * These semantic hooks are stable across loading and populated fixtures, and
   * they cover the words operators actually read in both authored themes.
   */
  const TARGETS = [
    ".dash-card__head",
    ".dash-card__meta",
    ".dash-map__stats",
    ".dash-map__legend",
    ".dash-line__name",
    ".dash-platform-name",
    ".dash-change__text",
    ".dash-issue__text",
    ".dash-muted",
    ".readout",
  ];

  /*
   * The current board uses shared Readout components and token-bound surfaces.
   * Keep this list empty: every collected target must meet its WCAG floor. If a
   * future reference exception is required, it must name the selector, theme and
   * measured ratio so the exemption cannot silently widen.
   */
  const ART_EXEMPTIONS = [];
  const exemptionFor = (sel, theme) =>
    ART_EXEMPTIONS.find((entry) => entry.sel === sel && entry.theme === theme);

  /** Runs in the page: every target that carries words and is on screen. */
  const COLLECT = (selectors) => {
    const rows = [];
    for (const sel of selectors) {
      for (const el of document.querySelectorAll(sel)) {
        // A mark with no text is a colour sample, not a word: the dots carry no
        // ink, so their contrast is meaningless.
        const text = (el.textContent || "").trim();
        if (!text) continue;
        const rect = el.getBoundingClientRect();
        if (rect.width < 2 || rect.height < 2) continue;
        const cx = Math.round(rect.left + rect.width / 2);
        const cy = Math.round(rect.top + rect.height / 2);
        // Off-screen text is not what the operator reads, so it is not measured;
        // the count is reported so a target that silently left the viewport shows.
        if (cx < 0 || cy < 0 || cx >= innerWidth || cy >= innerHeight) continue;
        const cs = getComputedStyle(el);
        rows.push({
          sel,
          text: text.slice(0, 24),
          ink: cs.color,
          size: parseFloat(cs.fontSize),
          weight: parseInt(cs.fontWeight, 10) || 400,
          cx,
          cy,
        });
      }
    }
    return rows;
  };

  /**
   * Runs in the page: hides every word without touching a single background.
   *
   * Inline, not a stylesheet. Several of the rules under test declare their colour
   * with `!important` (the art's control and its badge washes), and a stylesheet
   * `!important` outranks an injected one by specificity — measured: the injected
   * rule lost, the glyphs stayed painted, and the probe reported the select's own
   * antialiased text (`#98a7ca`) as its ground. An inline `!important` is the one
   * author declaration that wins over every other author declaration.
   */
  const HIDE_TEXT = () => {
    for (const el of document.querySelectorAll("*")) {
      if (el.tagName === "svg" || el.tagName === "path") continue;
      el.style.setProperty("color", "transparent", "important");
    }
  };

  /** Runs in the page: the painted colour under each point, from the screenshot. */
  const SAMPLE = async ({ b64, points, scale }) => {
    const image = new Image();
    await new Promise((resolve, reject) => {
      image.onload = resolve;
      image.onerror = reject;
      image.src = "data:image/png;base64," + b64;
    });
    const canvas = document.createElement("canvas");
    canvas.width = image.naturalWidth;
    canvas.height = image.naturalHeight;
    const ctx = canvas.getContext("2d");
    ctx.drawImage(image, 0, 0);
    return points.map((point) => {
      const data = ctx.getImageData(Math.round(point.cx * scale), Math.round(point.cy * scale), 1, 1).data;
      return [data[0], data[1], data[2]];
    });
  };

  const parseInk = (value) => {
    const m = /rgba?\(([^)]+)\)/.exec(value);
    if (!m) return null;
    const parts = m[1].split(/[,/]/).map((p) => parseFloat(p.trim()));
    return { r: parts[0], g: parts[1], b: parts[2], a: parts.length > 3 ? parts[3] : 1 };
  };
  const luminance = ({ r, g, b }) => {
    const f = (v) => {
      const s = v / 255;
      return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    };
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
  };
  const contrast = (a, b) => {
    const [hi, lo] = [luminance(a), luminance(b)].sort((p, q) => q - p);
    return (hi + 0.05) / (lo + 0.05);
  };
  const over = (fg, bg) => ({
    r: fg.r * fg.a + bg.r * (1 - fg.a),
    g: fg.g * fg.a + bg.g * (1 - fg.a),
    b: fg.b * fg.a + bg.b * (1 - fg.a),
  });
  const hex = (c) => "#" + c.map((v) => Math.round(v).toString(16).padStart(2, "0")).join("");

  const problems = [];
  for (const theme of ["dark", "light"]) {
    await page.goto(origin + DASHBOARD);
    await rendered(page);
    await page.evaluate((next) => {
      localStorage.setItem("prism.theme", next);
      document.documentElement.dataset.theme = next;
    }, theme);
    await page.goto(origin + DASHBOARD);
    await rendered(page);
    await page.waitForTimeout(800);

    // The theme the page actually painted, not the one that was asked for: a
    // mismatch here would silently grade one theme twice.
    const applied = await page.evaluate(() => document.documentElement.dataset.theme);
    assert.equal(applied, theme, `the page painted the ${applied} theme after ${theme} was requested`);

    const rows = await page.evaluate(COLLECT, TARGETS);
    assert(rows.length >= 12, `only ${rows.length} legible targets were found on screen`);

    await page.evaluate(HIDE_TEXT);
    const shot = await page.screenshot();
    const scale = await page.evaluate(() => window.devicePixelRatio || 1);
    const grounds = await page.evaluate(SAMPLE, {
      b64: shot.toString("base64"),
      points: rows.map(({ cx, cy }) => ({ cx, cy })),
      scale,
    });
    // The page is navigated again on the next pass, which drops the inline styles.

    for (let index = 0; index < rows.length; index++) {
      const row = rows[index];
      const ground = { r: grounds[index][0], g: grounds[index][1], b: grounds[index][2] };
      const ink = parseInk(row.ink);
      if (!ink) continue;
      // WCAG 1.4.3: 3:1 for large text (24px, or 18.66px bold), else 4.5:1.
      const large = row.size >= 24 || (row.weight >= 700 && row.size >= 18.66);
      const floor = large ? 3 : 4.5;
      const measured = contrast(over(ink, ground), ground);
      if (measured < floor) {
        const exemption = exemptionFor(row.sel, theme);
        if (exemption) {
          // The art's own value, or the exemption is stale: if the colour moved,
          // the ratio here moves too and this becomes a failure again.
          if (Math.abs(measured - exemption.ratio) > 0.02) {
            problems.push(
              `[${theme}] ${row.sel} is exempted at ${exemption.ratio}:1 as the art's own value, ` +
                `but now measures ${measured.toFixed(2)}:1 — the exemption no longer describes the art`,
            );
          }
          continue;
        }
        problems.push(
          `[${theme}] ${row.sel} "${row.text}": ${measured.toFixed(2)}:1 ` +
            `(ink ${hex([ink.r, ink.g, ink.b])} over ${hex([ground.r, ground.g, ground.b])}), needs ${floor}:1`,
        );
      }
    }
  }
  assert.deepEqual(problems, [], "text that is not legible against its own ground:\n  " + problems.join("\n  "));
});

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

/**
 * Waits until the route has painted a page heading inside the shell's main region.
 *
 * The presence of a heading is not enough: the error boundary renders
 * `<main><h1>界面渲染失败</h1>`, which matches the very same selector. A crash
 * therefore used to read as a clean render on every route but /ui/nodes (the one
 * check that asserted the text). The heading's own text is the discriminator.
 */
async function rendered(page) {
  try {
    const heading = page.locator("main h1").first();
    await heading.waitFor({ state: "visible", timeout: 15000 });
    return (await heading.innerText()).trim() !== RENDER_FAILURE_TITLE;
  } catch {
    return false;
  }
}

/**
 * The first line of a crashed page, or null when the page rendered normally.
 *
 * Returned as text for the failure report: "no page heading rendered" alone does
 * not say whether the route is broken or the boundary is.
 */
async function crashReport(page) {
  try {
    const text = await page.locator("main").first().innerText({ timeout: 2000 });
    if (!text.includes(RENDER_FAILURE_TITLE)) return null;
    return text.split("\n").map((line) => line.trim()).filter(Boolean).slice(0, 3).join(" / ");
  } catch {
    return null;
  }
}

/**
 * Waits for the route to go quiet before its console output is judged.
 *
 * A fixed 150ms sleep was the old clock, and a render error that landed after it
 * was thrown away by the next route's `errors.reset()` — charged to nobody.
 * Pending requests are the clock that matters, because the tables are filled from
 * effects. Bounded and swallowed: the dashboard polls on an interval, so "never
 * idle" must cost a few seconds here, not hang the gate.
 */
async function quiet(page) {
  await page.waitForLoadState("networkidle", { timeout: 3000 }).catch(() => {});
}

const root = await mkdtemp(join(tmpdir(), "prism-live-test-"));
const adminPassword = randomBytes(24).toString("hex");
const results = [];
let browser;
let backend;
let backendExit;
let setupError;

try {
  const port = await reservePort();
  const origin = `http://127.0.0.1:${port}`;
  const proxyToken = await initBackend({ root, adminPassword });
  const started = startBackend({ root, port, proxyToken });
  backend = started.backend;
  backendExit = started.exit;
  await waitForHealth(origin, backend, started.stderr);

  browser = await chromium.launch({ headless: true, executablePath, args: ["--no-sandbox"] });
  const context = await browser.newContext({
    locale: LOCALE,
    viewport: { width: 1440, height: 900 },
    reducedMotion: "reduce",
  });
  await context.addInitScript((locale) => {
    localStorage.setItem("prism.locale", locale);
  }, LOCALE);
  const page = await context.newPage();
  const errors = watchPage(page);

  const fixture = { origin, page, context, adminPassword, errors, browser };
  for (const { name, run } of checks) {
    try {
      await run(fixture);
      results.push({ name, ok: true });
    } catch (error) {
      results.push({ name, ok: false, message: error?.message ?? String(error) });
    }
  }
} catch (error) {
  setupError = error;
} finally {
  if (browser) await browser.close();
  if (backend) {
    if (backend.exitCode === null) backend.kill("SIGTERM");
    const timer = setTimeout(() => backend.kill("SIGKILL"), 7000);
    await backendExit;
    clearTimeout(timer);
  }
  await rm(root, { recursive: true, force: true });
}

if (setupError) {
  console.error("SETUP FAILED: " + setupError.message);
  process.exitCode = 1;
} else {
  const passed = results.filter((result) => result.ok).length;
  console.log("\nPrism panel browser check\n");
  for (const result of results) {
    console.log(`${result.ok ? "PASS" : "FAIL"}  ${result.name}`);
    if (!result.ok) console.log("      " + result.message.split("\n").join("\n      "));
  }
  console.log(`\n${results.length} checks, ${passed} passed, ${results.length - passed} failed`);
  if (passed !== results.length) process.exitCode = 1;
}