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

// ---------------------------------------------------------------------------
// The console's addresses, read from the one file that declares them
// ---------------------------------------------------------------------------

/*
 * `src/lib/paths.ts` is the single source of truth: the router, the rail and every
 * in-page link reference it, so this script reads it too. The sweep below then
 * cannot pass against a rail that names paths the console no longer serves, and
 * the retired addresses are derived from the same table, which is what makes
 * "every route renders" also prove that an old address still lands on its page.
 */
const pathsSource = readFileSync(new URL("../src/lib/paths.ts", import.meta.url), "utf8");
const namespace = {};
for (const match of pathsSource.matchAll(/export const (OPS_ROOT|ME_ROOT|ACCOUNT)\s*=\s*"([^"]*)"/g)) {
  namespace[match[1]] = match[2];
}
for (const name of ["OPS_ROOT", "ME_ROOT", "ACCOUNT"]) {
  if (!namespace[name]) throw new Error(`check-ui-live: src/lib/paths.ts declares no ${name}`);
}

const readRecord = (name) => {
  const body = new RegExp(`export const ${name}[^=]*=\\s*\\{([\\s\\S]*?)\\n\\};`).exec(pathsSource)?.[1];
  if (body === undefined) throw new Error(`check-ui-live: src/lib/paths.ts declares no ${name} record`);
  const entries = new Map();
  for (const line of body.split("\n")) {
    const row = /^\s*(?:"([^"]+)"|(\w+)):\s*(.+?),?\s*$/.exec(line);
    if (row) entries.set(row[1] ?? row[2], row[3]);
  }
  if (entries.size === 0) throw new Error(`check-ui-live: the ${name} record in src/lib/paths.ts is empty`);
  return entries;
};
const opsPaths = readRecord("opsPaths");
const mePaths = readRecord("mePaths");
const legacyRedirects = readRecord("legacyRedirects");

/** Resolves a value from paths.ts: a literal, a `template`, a constant or a record entry. */
const resolvePath = (source) => {
  const expr = String(source).trim().replace(/,$/, "");
  const literal = /^"([^"]*)"$/.exec(expr);
  if (literal) return literal[1];
  const template = /^`([^`]*)`$/.exec(expr);
  if (template) {
    const text = template[1].replace(/\$\{(\w+)\}/g, (whole, name) => namespace[name] ?? whole);
    if (text.includes("${")) throw new Error(`check-ui-live: cannot resolve ${expr} in src/lib/paths.ts`);
    return text;
  }
  if (expr in namespace) return namespace[expr];
  const member = /^(\w+)\.(\w+)$/.exec(expr);
  if (member && { opsPaths, mePaths, legacyRedirects }[member[1]]?.has(member[2])) {
    return resolvePath({ opsPaths, mePaths, legacyRedirects }[member[1]].get(member[2]));
  }
  throw new Error(`check-ui-live: cannot resolve ${expr} in src/lib/paths.ts`);
};
/** The absolute path of one record entry, and the browser address the panel serves. */
const recordPath = (record, key) => resolvePath(record.get(key));
const ui = (path) => `/ui${path}`;

// The destinations the panel offers, in the order the rail lists them: the
// operator namespace, then the account page every session shares.
const RAIL_PATHS = [
  "dashboard",
  "nodes",
  "subscriptions",
  "jobs",
  "platforms",
  "plans",
  "users",
  "invites",
  "redeemCodes",
  "usage",
  "endpoints",
  "rules",
  "requestLogs",
  "config",
  "settings",
  "resources",
  "audit",
].map((key) => ui(recordPath(opsPaths, key)));
RAIL_PATHS.push(ui(namespace.ACCOUNT));

// What a tenant session gets instead: the self-service destinations, which an
// operator never sees, plus the account page both roles share.
const TENANT_RAIL = ["overview", "keys", "guide", "plans", "leases", "deposit"].map((key) =>
  ui(recordPath(mePaths, key)),
);
TENANT_RAIL.push(ui(namespace.ACCOUNT));

/*
 * Every retired address, the page it lands on, and whether its search string is
 * expected to survive the move. `/quality` is in this list although it is not in
 * the redirect table: its redirect is the page's own component
 * (src/app/legacy-quality-redirect.tsx), which rewrites the query on purpose, and
 * an old bookmark of it is just as reachable.
 */
const LEGACY_REDIRECTS = [...legacyRedirects].map(([from, to]) => [ui(from), ui(resolvePath(to)), true]);
LEGACY_REDIRECTS.push(["/ui/quality", ui(recordPath(opsPaths, "nodes")), false]);

// Class tokens the redesign deleted. A trailing dash means "prefix". `card` is
// matched as a whole token, because the kit's own utilities are named around it.
// The board's replica classes are on the list too: their CSS went with the 1536px
// layer, so a `wb-slot-*` or a `wb-folded` left in the DOM is a pane the grid does
// not know about.
// The worlds before this one, kept listed because Tailwind classes do not fail
// when the utility behind them is gone: `bg-glass-soft` in a page renders as a
// transparent box and no gate notices. `check:responsive` guards the stylesheet;
// this list guards the markup a page could still carry.
const RETIRED_WORLD_TOKENS = [
  "dash-",
  "glass-",
  "register__",
  "amendment",
  "term-",
  "shell-topbar",
  "shell-theme-toggle",
  "shell-nav__extra",
  // Tailwind utilities whose CSS was deleted, so they paint nothing at all.
  "bg-glass",
  "bg-glass-soft",
  "bg-glass-strong",
  "hover:bg-glass",
  "border-glass-edge",
  "border-glass-edge-strong",
];

const REMOVED_CLASS_TOKENS = [
  "content",
  "node-name",
  "quality-network-facts",
  "settings-category",
  "syscfg-",
  "live-chart",
  "resource-total",
  "detail-header",
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
const DASHBOARD = ui(recordPath(opsPaths, "dashboard"));
const NODES = ui(recordPath(opsPaths, "nodes"));
const SUBSCRIPTIONS = ui(recordPath(opsPaths, "subscriptions"));
const USAGE = ui(recordPath(opsPaths, "usage"));
const AUDIT = ui(recordPath(opsPaths, "audit"));
const REQUEST_LOGS = ui(recordPath(opsPaths, "requestLogs"));

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
      PRISM_QUALITY_API_KEY: "",
      PRISM_ABUSEIPDB_API_KEY: "",
      PRISM_QUALITY_DAILY_LIMIT: "10",
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
  await page.waitForURL((url) => url.pathname === DASHBOARD, { timeout: 15000 });

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

  /*
   * A retired address is not a dead one: every path the console used to answer at
   * has to land on the page that replaced it, with the query it carried. This is
   * the reachability half of the two-namespace move — an old bookmark, a link in a
   * ticket or a stale `next=` in a sign-in URL is still a working entry point.
   */
  for (const [from, to, keepsSearch] of LEGACY_REDIRECTS) {
    errors.reset(from);
    // A query on the old address is part of the link (`/nodes?subscription_id=…`),
    // so it has to arrive with it; `/quality` is the one page that rewrites it.
    await page.goto(origin + from + (keepsSearch ? "?keep=1" : ""));
    const moved = await page
      .waitForURL((url) => url.pathname === to, { timeout: 15000 })
      .then(() => true)
      .catch(() => false);
    if (!moved) {
      failures.push(`${from}: landed on ${new URL(page.url()).pathname}, not on ${to}`);
      continue;
    }
    if (keepsSearch && new URL(page.url()).search !== "?keep=1") {
      failures.push(`${from}: the query string was dropped on the way to ${to}`);
    }
    if (!(await rendered(page))) failures.push(`${from}: ${to} rendered no page heading`);
    await quiet(page);
    for (const message of errors.drain()) failures.push(`${from}: ${message.text}`);
  }
  assert.deepEqual(failures, [], "routes that did not render cleanly:\n  " + failures.join("\n  "));
});

check("no removed class token survives in the DOM", async ({ origin, page }) => {
  const failures = [];
  for (const path of RAIL_PATHS) {
    await page.goto(origin + path);
    await rendered(page);
    const hits = await page.evaluate(collectRemovedTokens, [...REMOVED_CLASS_TOKENS, ...RETIRED_WORLD_TOKENS]);
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
    errors.reset(`${NODES} legacy envelope`);
    await page.goto(origin + NODES + "?legacy_smoke=1");
    await rendered(page);
    await page.waitForTimeout(250);
    const text = await page.locator("main").innerText();
    assert.match(text, /legacy-node/, "the legacy row must still render");
    assert.doesNotMatch(text, /界面渲染失败|toLocaleString/);
    assert.deepEqual(errors.drain(), [], "legacy node envelope must not emit a render error");
  } finally {
    await page.unroute(nodesRoute);
  }
});

// Shared HeroUI primitives must work against their actual rendered DOM, not only
// satisfy TypeScript. This reaches an anonymous tooltip plus the signed-in node
// filters and inspector, including the single global toast queue.
check("shared HeroUI overlays open, close and restore focus", async ({ origin, page, context, errors, browser }) => {
  const anonymous = await browser.newContext({
    locale: LOCALE,
    viewport: { width: 1440, height: 900 },
    reducedMotion: "reduce",
  });
  await anonymous.addInitScript((locale) => localStorage.setItem("prism.locale", locale), LOCALE);
  const loginPage = await anonymous.newPage();
  const loginErrors = watchPage(loginPage);
  try {
    loginErrors.reset("login tooltip");
    await loginPage.goto(origin + "/ui/login");
    const passwordToggle = loginPage.getByRole("button", { name: "显示密码" });
    await passwordToggle.waitFor({ state: "visible", timeout: 15000 });
    const tooltipTrigger = passwordToggle.locator('xpath=ancestor::*[@data-slot="tooltip-trigger"]');
    assert.equal(await tooltipTrigger.count(), 1, "the password toggle must have one HeroUI tooltip trigger");
    const triggerBox = await tooltipTrigger.boundingBox();
    assert(triggerBox && triggerBox.width >= 32 && triggerBox.height >= 32, "the tooltip trigger must own a real hit target");

    let tooltipText = [];
    for (let attempt = 0; attempt < 3 && !tooltipText.length; attempt += 1) {
      await loginPage.mouse.move(8 + attempt, 8 + attempt);
      await loginPage.waitForTimeout(80);
      await tooltipTrigger.hover({ position: { x: 16, y: 16 } });
      await loginPage.waitForTimeout(700);
      tooltipText = await loginPage.locator('[role="tooltip"]:visible').allTextContents();
    }
    assert(tooltipText.some((text) => text.includes("显示密码")), "hovering the password toggle must render its tooltip");
    await quiet(loginPage);
    assert.deepEqual(loginErrors.drain(), [], "the login tooltip must not emit a browser error");
  } finally {
    await anonymous.close();
  }

  const nodeListRoute = /\/api\/v1\/nodes(?:\?|$)/;
  const nodeDetailRoute = /\/api\/v1\/nodes\/[^/?]+(?:\?|$)/;
  await page.route(nodeListRoute, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    const items = (body.items ?? []).map((item) => ({
      ...item,
      egress_ip: item.egress_ip || "198.51.100.20",
      has_outbound: true,
    }));
    await route.fulfill({ response, body: JSON.stringify({ ...body, items }) });
  });
  await page.route(nodeDetailRoute, async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({
      response,
      body: JSON.stringify({ ...body, egress_ip: body.egress_ip || "198.51.100.20", has_outbound: true }),
    });
  });
  try {
    errors.reset(`${NODES} shared overlays`);
    await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
    await page.goto(origin + NODES);
    await rendered(page);

    const healthSelect = page
      .locator(".workspace-select")
      .filter({ hasText: "健康状态" })
      .locator('[data-slot="select-trigger"]')
      .first();
    await healthSelect.waitFor({ state: "visible", timeout: 10000 });
    await healthSelect.click();
    const listbox = page.locator('[role="listbox"]:visible').last();
    await listbox.waitFor({ state: "visible", timeout: 5000 });
    const options = await listbox.getByRole("option").allTextContents();
    assert(options.some((text) => text.trim() === "健康"), "the health Select must render its options");
    await page.keyboard.press("Escape");
    assert.equal(await healthSelect.getAttribute("aria-expanded"), "false", "Escape must close the Select");
    await listbox.waitFor({ state: "hidden", timeout: 3000 });

    const nodeRow = page.locator('.workspace-object-row[role="button"]').first();
    await nodeRow.waitFor({ state: "visible", timeout: 15000 });
    await nodeRow.focus();
    await nodeRow.click();
    const drawer = page.locator('[data-slot="drawer-dialog"][role="dialog"]').last();
    await drawer.waitFor({ state: "visible", timeout: 15000 });
    assert.equal(await page.locator('[data-slot="drawer-backdrop"]:visible').count(), 1, "the inspector must render one backdrop");

    await drawer.getByRole("button", { name: "复制出口 IP" }).click();
    const toast = page.locator('[data-slot="toast"]:visible').first();
    await toast.waitFor({ state: "visible", timeout: 5000 });
    assert.match(await toast.innerText(), /已复制/, "copying the exit IP must use the global ToastProvider");
    assert.equal(await toast.locator('xpath=ancestor::*[@data-slot="toast-region"]').count(), 1, "the toast must live in the global region");

    await drawer.getByRole("button", { name: "关闭" }).click();
    await drawer.waitFor({ state: "hidden", timeout: 3000 });
    const focusTarget = await nodeRow.elementHandle();
    assert(focusTarget, "the drawer opener must remain mounted");
    try {
      await page.waitForFunction((target) => document.activeElement === target, focusTarget, { timeout: 3000 });
    } catch (error) {
      const focusState = await nodeRow.evaluate((element) => ({ connected: element.isConnected, active: document.activeElement?.outerHTML }));
      throw new Error(`drawer focus did not settle on its opener: ${JSON.stringify(focusState)}; ${error.message}`);
    } finally {
      await focusTarget.dispose();
    }
    assert.equal(await nodeRow.evaluate((element) => document.activeElement === element), true, "closing the drawer must restore row focus");
    await quiet(page);
    assert.deepEqual(errors.drain(), [], "shared overlays must not emit a browser error");
  } finally {
    await page.unroute(nodeListRoute);
    await page.unroute(nodeDetailRoute);
  }
});

// The isolated backend has no retained request traffic, so replay one complete
// row to exercise the HeroUI Table collection and its row action.
function requestLogFixture(overrides = {}) {
  return {
    id: "log_ui_check",
    ts: "2026-10-27T00:00:00Z",
    proxy_type: 1,
    client_ip: "192.0.2.10",
    platform_id: "platform_ui_check",
    platform_name: "UI Check Platform",
    account: "ui-check-account",
    target_host: "api.example.test",
    target_url: "https://api.example.test/orders",
    node_hash: "node_ui_check",
    node_tag: "UI check node",
    egress_ip: "198.51.100.10",
    duration_ms: 42,
    first_byte_duration_ms: 12,
    net_ok: true,
    http_method: "GET",
    http_status: 200,
    prism_error: "",
    upstream_stage: "",
    upstream_err_kind: "",
    upstream_errno: "",
    upstream_err_msg: "",
    ingress_bytes: 123,
    egress_bytes: 456,
    payload_present: false,
    req_headers_len: 0,
    req_body_len: 0,
    resp_headers_len: 0,
    resp_body_len: 0,
    req_headers_truncated: false,
    req_body_truncated: false,
    resp_headers_truncated: false,
    resp_body_truncated: false,
    ...overrides,
  };
}

check("the request log table exposes row headers and opens its inspector", async ({ origin, page, errors }) => {
  const log = requestLogFixture();
  const listRoute = /\/api\/v1\/request-logs(?:\?|$)/;
  const detailRoute = /\/api\/v1\/request-logs\/log_ui_check(?:\?|$)/;
  await page.route(listRoute, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [log], limit: 100, has_more: false }),
    });
  });
  await page.route(detailRoute, async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(log) });
  });
  try {
    errors.reset(`${REQUEST_LOGS} table interaction`);
    await page.goto(origin + REQUEST_LOGS);
    await rendered(page);
    const table = page.locator('[data-slot="table-content"]');
    await table.waitFor({ state: "visible", timeout: 15000 });
    assert.equal(await table.getAttribute("role"), "grid", "ControlTable must expose grid semantics");
    assert((await table.getByRole("columnheader").count()) >= 10, "the request table must expose its columns");
    assert((await table.getByRole("rowheader").count()) >= 1, "the request table must expose a row header");

    const row = table.locator('[data-slot="table-row"]').filter({ hasText: "api.example.test" }).first();
    await row.waitFor({ state: "visible", timeout: 5000 });
    await row.click();
    const drawer = page.locator('[data-slot="drawer-dialog"][role="dialog"]').last();
    await drawer.waitFor({ state: "visible", timeout: 10000 });
    assert.match(await drawer.innerText(), /api\.example\.test/, "the table row action must open the matching inspector");
    await drawer.getByRole("button", { name: "关闭" }).click();
    await drawer.waitFor({ state: "hidden", timeout: 3000 });
    await quiet(page);
    assert.deepEqual(errors.drain(), [], "the request table interaction must not emit a browser error");
  } finally {
    await page.unroute(listRoute);
    await page.unroute(detailRoute);
  }
});

check("log pages retain the settled page during delayed failure and recover on retry", async ({ origin, page, errors }) => {
  const cases = [
    {
      path: REQUEST_LOGS,
      route: /\/api\/v1\/request-logs(?:\?|$)/,
      first: "first-request.example.test",
      second: "second-request.example.test",
      isNext: (url) => Boolean(url.searchParams.get("cursor")),
      response: (next) => ({
        items: [requestLogFixture({
          id: next ? "log_page_2" : "log_page_1",
          target_host: next ? "second-request.example.test" : "first-request.example.test",
        })],
        limit: 100,
        has_more: !next,
        next_cursor: next ? undefined : "ui-page-2",
      }),
    },
    {
      path: AUDIT,
      route: /\/api\/v1\/audit-logs(?:\?|$)/,
      first: "first-audit-target",
      second: "second-audit-target",
      isNext: (url) => Number(url.searchParams.get("before_id")) > 0,
      response: (next) => ({
        items: Array.from({ length: next ? 1 : 51 }, (_, index) => ({
          id: next ? 1 : 100 - index,
          at_ns: 1767225600000000000,
          actor: "ui-check-actor",
          remote_addr: "192.0.2.10",
          action: "PATCH /api/v1/platforms/{id}",
          target: next ? "second-audit-target" : index === 0 ? "first-audit-target" : `audit-target-${index}`,
          detail: JSON.stringify({ keys: ["name"] }),
        })),
        limit: 51,
      }),
    },
  ];
  for (const scenario of cases) {
    // A full navigation resets component/query state, so both replays start clean.
    for (let replay = 0; replay < 2; replay += 1) {
      let releaseFailure;
      const failureGate = new Promise((resolve) => { releaseFailure = resolve; });
      let failNext = true;
      let failedRequests = 0;
      await page.route(scenario.route, async (route) => {
        const next = scenario.isNext(new URL(route.request().url()));
        if (next && failNext) {
          failedRequests += 1;
          await failureGate;
          await route.fulfill({
            status: 503,
            contentType: "application/json",
            body: JSON.stringify({ error: { code: "INTERNAL_ERROR", message: "ui-stale-fixture" } }),
          });
          return;
        }
        await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(scenario.response(next)) });
      });
      try {
        errors.reset(`${scenario.path} settled-page replay ${replay}`);
        await page.goto(origin + scenario.path);
        await rendered(page);
        await page.getByText(scenario.first, { exact: false }).first().waitFor({ timeout: 10000 });
        await page.getByRole("button", { name: "下一页", exact: true }).click();
        await page.getByText("更新中...", { exact: true }).first().waitFor({ timeout: 5000 });
        assert(await page.getByText(scenario.first, { exact: false }).first().isVisible(), "delayed pagination must keep the old list");
        assert.match(await page.locator(".workspace-pagination").innerText(), /第 1 页/, "pending pagination must label the actual visible page");
        releaseFailure();
        await page.getByText("数据可能已过期", { exact: false }).first().waitFor({ timeout: 15000 });
        assert(failedRequests >= 2, "the configured single retry must finish before stale is shown");
        assert(await page.getByText(scenario.first, { exact: false }).first().isVisible(), "a failed page request must not erase the settled list");
        assert.match(await page.locator(".workspace-pagination").innerText(), /第 1 页/, "a failed page must keep the settled page number");
        const expectedErrors = errors.drain();
        assert(expectedErrors.every((entry) => /^console.error: Failed to load resource:.*503/.test(entry.text)), `only the deliberate 503 resource errors are allowed: ${JSON.stringify(expectedErrors)}`);
        failNext = false;
        errors.reset(`${scenario.path} recovery replay ${replay}`);
        await page.getByRole("button", { name: "重试", exact: true }).first().click();
        await page.getByText(scenario.second, { exact: false }).first().waitFor({ timeout: 10000 });
        assert.match(await page.locator(".workspace-pagination").innerText(), /第 2 页/, "successful retry must advance the displayed page");
        assert.deepEqual(errors.drain(), [], "recovery must not produce a browser error");
      } finally {
        releaseFailure();
        await page.unroute(scenario.route, { behavior: "wait" });
      }
    }
  }
});

check("platform destructive actions cancel, surface failure and retry", async ({ origin, page, errors }) => {
  const platformId = "ui-confirm-platform";
  const platformPath = `${ui(recordPath(opsPaths, "platforms"))}/${platformId}?tab=ops`;
  const platform = {
    id: platformId,
    name: "UI Confirm Platform",
    sticky_ttl: "168h",
    regex_filters: [],
    region_filters: [],
    ip_types: [],
    purity_bands: [],
    subscription_filters: [],
    protocols: ["http"],
    routable_node_count: 3,
    reverse_proxy_miss_action: "TREAT_AS_EMPTY",
    reverse_proxy_empty_account_behavior: "RANDOM",
    reverse_proxy_fixed_account_header: "",
    allocation_policy: "BALANCED",
    passive_circuit_breaker_disabled: false,
    updated_at: "2026-10-27T00:00:00Z",
  };
  const platformRoute = /\/api\/v1\/platforms\/ui-confirm-platform$/;
  const leasesRoute = /\/api\/v1\/platforms\/ui-confirm-platform\/leases(?:\?.*)?$/;
  let clearRequests = 0;
  await page.route(platformRoute, async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(platform) });
  });
  await page.route(leasesRoute, async (route) => {
    if (route.request().method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [], total: 0, limit: 10, offset: 0 }),
      });
      return;
    }
    if (route.request().method() === "DELETE") {
      clearRequests += 1;
      if (clearRequests === 1) {
        await route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({ error: { code: "INTERNAL", message: "clear leases failed" } }),
        });
      } else {
        await route.fulfill({ status: 204 });
      }
      return;
    }
    await route.continue();
  });
  try {
    errors.reset("platform destructive action states");
    await page.goto(origin + platformPath);
    await rendered(page);
    await page.getByRole("heading", { name: "危险操作" }).waitFor({ timeout: 10000 });
    const clearButton = page.getByRole("button", { name: "清除全部", exact: true });
    const drawer = page.locator('[data-slot="drawer-dialog"][role="dialog"]').last();

    await clearButton.click();
    await drawer.waitFor({ state: "visible", timeout: 5000 });
    await drawer.getByRole("button", { name: "取消", exact: true }).click();
    await page.locator('[data-slot="drawer-backdrop"]').last().waitFor({ state: "detached", timeout: 5000 });
    assert.equal(clearRequests, 0, "cancelling a destructive action must send no request");

    await clearButton.click();
    await drawer.waitFor({ state: "visible", timeout: 5000 });
    const confirmButton = drawer.getByRole("button", { name: "确认清除", exact: true });
    await confirmButton.click();
    await drawer.getByText(/INTERNAL: 服务器内部错误/).waitFor({ timeout: 10000 });
    assert(await drawer.isVisible(), "a failed destructive action must keep the confirmation drawer open");
    assert(await confirmButton.isEnabled(), "a failed destructive action must be retryable");

    await confirmButton.click();
    await drawer.waitFor({ state: "hidden", timeout: 10000 });
    assert.equal(clearRequests, 2, "retrying after a failed destructive action must send one new request");
    const expectedErrors = errors.drain();
    assert(
      expectedErrors.every((entry) => /^console.error: Failed to load resource:.*503/.test(entry.text)),
      `only the deliberate destructive-action 503 may reach the browser console: ${JSON.stringify(expectedErrors)}`,
    );
  } finally {
    await page.unroute(platformRoute);
    await page.unroute(leasesRoute, { behavior: "wait" });
  }
});

check("server-paged invite filtering remains current-page scoped and paginates", async ({ origin, page, errors }) => {
  const invitesRoute = /\/api\/v1\/admin\/invites(?:\?.*)?$/;
  const plansRoute = /\/api\/v1\/admin\/plans(?:\?.*)?$/;
  const requests = [];
  const baseInvite = {
    plan_id: "",
    plan_name: "UI Invite Plan",
    max_uses: 1,
    used_count: 0,
    status: "active",
    expires_at_ns: 0,
    note: "",
    created_at_ns: 1761523200000000000,
  };
  const firstPage = { ...baseInvite, id: "invite_page_1", code: "PAGE_ONE_NON_MATCH" };
  const secondPage = { ...baseInvite, id: "invite_page_2", code: "PAGE_TWO_MATCH" };
  await page.route(invitesRoute, async (route) => {
    const url = new URL(route.request().url());
    const offset = Number(url.searchParams.get("offset") ?? 0);
    requests.push({ offset, limit: Number(url.searchParams.get("limit") ?? 0) });
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [offset === 0 ? firstPage : secondPage], total: 40, limit: 20, offset }),
    });
  });
  await page.route(plansRoute, async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [] }) });
  });
  try {
    errors.reset("current-page invite filtering");
    await page.goto(origin + ui(recordPath(opsPaths, "invites")));
    await rendered(page);
    const input = page.getByRole("textbox", { name: "搜索邀请码", exact: true });
    await input.waitFor({ timeout: 10000 });
    await input.fill("PAGE_TWO_MATCH");
    await page.getByText("当前页没有匹配的邀请码", { exact: true }).waitFor({ timeout: 5000 });

    const pagination = page.locator(".workspace-pagination");
    await pagination.getByRole("button", { name: "下一页", exact: true }).click();
    await page.getByText("PAGE_TWO_MATCH", { exact: true }).waitFor({ timeout: 10000 });
    assert(requests.some((request) => request.offset === 0 && request.limit === 20), "filtering must first load page one");
    assert(requests.some((request) => request.offset === 20 && request.limit === 20), "pagination must request page two while the filter stays active");
    assert.match(await pagination.innerText(), /第 2 \/ 2 页/, "the page summary must follow the server page, not the filtered row count");
    assert.deepEqual(errors.drain(), [], "current-page filtering and pagination must not emit a browser error");
  } finally {
    await page.unroute(invitesRoute, { behavior: "wait" });
    await page.unroute(plansRoute);
  }
});

check("unknown balance blocks purchase without inventing zero or a negative remainder", async ({ origin, page, errors }) => {
  const plansRoute = /\/api\/v1\/me\/plans(?:\?|$)/;
  const balanceRoute = /\/api\/v1\/me\/balance(?:\?|$)/;
  const purchaseRoute = /\/api\/v1\/me\/purchases(?:\?|$)/;
  const plan = { id: "plan_balance_check", name: "UI balance check plan", platforms: [], ip_tiers: [], traffic_bytes: 0, period: "30d", duration_days: 30, price_micro: 2000000 };
  let releaseBalance;
  const balanceGate = new Promise((resolve) => { releaseBalance = resolve; });
  let purchaseRequests = 0;
  await page.route(plansRoute, async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: [plan] }) });
  });
  await page.route(balanceRoute, async (route) => {
    await balanceGate;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ balance_micro: 3500000, ledger: [] }) });
  });
  await page.route(purchaseRoute, async (route) => {
    purchaseRequests += 1;
    await route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: { code: "INTERNAL_ERROR", message: "unexpected purchase" } }) });
  });
  try {
    errors.reset("unknown-balance purchase boundary");
    await page.goto(origin + ui(recordPath(mePaths, "plans")));
    await rendered(page);
    await page.getByText(plan.name, { exact: true }).first().waitFor({ timeout: 10000 });
    await page.getByText(plan.name, { exact: true }).first().click();
    const drawer = page.locator('[data-slot="drawer-dialog"][role="dialog"]').last();
    await drawer.waitFor({ state: "visible", timeout: 10000 });
    await drawer.getByText("余额未知，无法确认购买", { exact: true }).waitFor();
    const confirm = drawer.getByRole("button", { name: "确认购买", exact: true });
    assert(await confirm.isDisabled(), "unknown balance must block purchase");
    assert.doesNotMatch(await drawer.locator("dl").innerText(), /余额不足|-2\.000000/, "unknown balance is neither zero nor a negative projected balance");
    assert.equal(purchaseRequests, 0, "an unknown balance must send no purchase");
    releaseBalance();
    await page.waitForFunction(() => {
      const dialog = document.querySelector('[data-slot="drawer-dialog"]');
      return dialog && !dialog.textContent.includes("余额未知，无法确认购买") && dialog.textContent.includes("1.500000");
    });
    assert(await confirm.isEnabled(), "a funded balance must re-enable confirmation in the same drawer");
    assert.equal(purchaseRequests, 0, "loading the balance must not automatically purchase");
    await drawer.getByRole("button", { name: "取消", exact: true }).click();
    await drawer.waitFor({ state: "hidden", timeout: 5000 });
    assert.deepEqual(errors.drain(), [], "balance boundary must not emit a browser error");
  } finally {
    releaseBalance();
    await page.unroute(plansRoute);
    await page.unroute(balanceRoute, { behavior: "wait" });
    await page.unroute(purchaseRoute);
  }
});

check("deposit amount validation exposes format and range errors before sending micro-USDT", async ({ origin, page, errors }) => {
  const infoRoute = /\/api\/v1\/me\/deposit(?:\?|$)/;
  const intentsRoute = /\/api\/v1\/me\/deposits(?:\?|$)/;
  const info = { enabled: true, address: "TUiDepositReceiver", contract: "TUiDepositContract", min_micro: 1000000, max_micro: 10000000, window_secs: 3600 };
  const intent = { id: "deposit_ui_check", amount_micro: 2500000, status: "pending", address: info.address, tx_id: "", created_at_ns: Date.now() * 1000000, expires_at_ns: (Date.now() + 3600000) * 1000000, hold_until_ns: (Date.now() + 7200000) * 1000000, paid_at_ns: 0 };
  const submitted = [];
  await page.route(infoRoute, async (route) => {
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(info) });
  });
  await page.route(intentsRoute, async (route) => {
    if (route.request().method() === "POST") {
      submitted.push(route.request().postDataJSON());
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(intent) });
    } else {
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: submitted.length ? [intent] : [] }) });
    }
  });
  try {
    errors.reset("deposit amount validation");
    await page.goto(origin + ui(recordPath(mePaths, "deposit")));
    await rendered(page);
    const input = page.getByRole("textbox", { name: "充值金额 (USDT)", exact: true });
    await input.waitFor({ timeout: 10000 });
    const create = page.getByRole("button", { name: "生成充值单", exact: true });
    assert(await create.isDisabled(), "empty amount must not submit");
    for (const [value, message] of [["abc", "请输入有效金额，最多六位小数。"], ["1.0000001", "请输入有效金额，最多六位小数。"], ["0.999999", "低于最小充值额"], ["10.000001", "超过最大充值额"]]) {
      await input.fill(value);
      await page.getByRole("alert").filter({ hasText: message }).first().waitFor({ timeout: 5000 });
      assert(await create.isDisabled(), `${value}: invalid amount must block submission`);
      assert.equal(await input.getAttribute("aria-invalid"), "true", `${value}: input must expose the validation error`);
      assert.equal(submitted.length, 0, `${value}: invalid input must send no request`);
    }
    await input.fill("2.500000");
    assert(await create.isEnabled(), "a valid in-range amount must enable creation");
    await create.click();
    await page.getByText("deposit_ui_check", { exact: true }).first().waitFor({ timeout: 10000 });
    assert.equal(submitted.length, 1, "one click must create exactly one intent");
    assert.equal(submitted[0].amount_micro, 2500000, "the request must use exact micro-USDT");
    assert.equal(await input.inputValue(), "", "a successful creation must clear the amount field");
    assert.deepEqual(errors.drain(), [], "deposit validation must not emit a browser error");
  } finally {
    await page.unroute(infoRoute);
    await page.unroute(intentsRoute);
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
    errors.reset(`${USAGE} alert queue`);
    await page.goto(origin + USAGE);
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

  // A numeric readout in the current control-plane data grid uses the mono token.
  // Keep this tied to the rendered control-plane surface rather than a retired table.
  const cell = await page.evaluate(() => {
    const mono = getComputedStyle(document.documentElement).getPropertyValue("--font-mono").trim();
    for (const element of document.querySelectorAll(".workspace-data-grid__value")) {
      const text = (element.textContent || "").trim();
      if (/\d/.test(text)) return { text, family: getComputedStyle(element).fontFamily, mono };
    }
    return null;
  });
  assert(cell, "a numeric control-plane readout must exist to read the mono token from");
  const normalizeFamily = (value) => value.replaceAll('"', "").replaceAll("'", "").replace(/\s+/g, " ").trim();
  assert.equal(
    normalizeFamily(cell.family),
    normalizeFamily(cell.mono),
    `the numeric readout "${cell.text}" must read in the mono token`,
  );
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
  await page.waitForURL((url) => url.pathname === NODES);
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
 * The command palette only exists on a keystroke, which makes it the one surface
 * a route sweep cannot reach. This check is the proof that the lazy chunk arrives,
 * that the index is the rail plus the five shell actions and nothing invented,
 * that typing narrows it, and that both ways out (Enter, Escape) work.
 */
check("the command palette opens from the keyboard, filters and routes", async ({ origin, page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(origin + DASHBOARD);
  await rendered(page);

  /*
   * The palette is HeroUI's modal, search field and listbox now, so the markers
   * are the vendor's: a dialog, a searchbox and listbox options. What is proven is
   * unchanged — the lazy chunk arrives, the index is the rail plus the five shell
   * actions, typing narrows it, and both ways out work.
   */
  const palette = page.getByRole("dialog");
  const options = page.getByRole("option");
  assert.equal(await page.getByRole("listbox").count(), 0, "the palette must not be mounted before it is asked for");

  await page.keyboard.press("Control+k");
  await palette.first().waitFor({ state: "visible", timeout: 15000 });
  // One destination for every rail link, plus 刷新数据 / 切换主题 / 切换语言 /
  // 账户安全 / 退出登录. An exact count, so adding one without the other fails here.
  assert.equal(
    await options.count(),
    RAIL_PATHS.length + 5,
    "the palette indexes the rail plus the shell's own actions, and nothing else",
  );
  assert.equal(
    await page.evaluate(() => document.activeElement?.tagName === "INPUT"),
    true,
    "the palette takes the keyboard when it opens",
  );

  await page.getByRole("searchbox").fill("审计日志");
  await page.waitForFunction(() => document.querySelectorAll('[role="option"]').length === 1, null, { timeout: 5000 });
  await page.keyboard.press("Enter");
  await page.waitForURL((url) => url.pathname === AUDIT);
  await palette.first().waitFor({ state: "hidden", timeout: 15000 });

  await page.keyboard.press("Control+k");
  await palette.first().waitFor({ state: "visible", timeout: 15000 });
  await page.keyboard.press("Escape");
  await palette.first().waitFor({ state: "hidden", timeout: 15000 });
});

/**
 * The English dictionary is a chunk now, so the language control has two jobs:
 * fetch it, and only then change the language. This check watches the network for
 * the chunk and the document for the language, which is exactly the pair that can
 * silently break — a switch that repaints Chinese keys looks like a working button
 * with a broken dictionary.
 */
check("switching to English fetches its dictionary and stays English", async ({ origin, page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(origin + DASHBOARD);
  await rendered(page);

  // The suite runs in Chinese: the English map must not be on the wire yet.
  const seen = async () =>
    page.evaluate(() =>
      performance.getEntriesByType("resource").map((entry) => entry.name).filter((name) => /assets\/translations-/.test(name)),
    );
  assert.deepEqual(await seen(), [], "a Chinese session must never download the English dictionary");

  await page.locator('button[aria-label="切换语言"]').click();
  await page.waitForFunction(() => document.documentElement.lang === "en-US", null, { timeout: 15000 });
  // The rail's own name is translated, so it is the cheapest honest witness that
  // the tree re-rendered in English rather than only the <html lang> attribute.
  await page.locator('nav[aria-label="Main Navigation"]').waitFor({ state: "visible", timeout: 15000 });
  assert.equal(await page.title(), "Prism · Proxy Workbench", "the document title follows the language");
  assert.equal((await seen()).length, 1, "the chunk is fetched once, not per render");

  await page.locator('button[aria-label="Switch Language"]').click();
  await page.waitForFunction(() => document.documentElement.lang === "zh-CN", null, { timeout: 15000 });
  await page.locator('nav[aria-label="主导航"]').waitFor({ state: "visible", timeout: 15000 });
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

    /* The tenant uses the shared control-plane shell with its own role surface and rail. */
    const selfSurface = await tenantPage.evaluate(() => {
      const shell = document.querySelector(".workspace-shell");
      const box = (selector) => {
        const el = document.querySelector(selector);
        if (!el) return null;
        const r = el.getBoundingClientRect();
        return { w: Math.round(r.width), h: Math.round(r.height) };
      };
      return { cls: shell?.className ?? "", rail: box(".workspace-nav"), link: box(".workspace-nav__link") };
    });
    assert.match(selfSurface.cls, /workspace-shell--me/, "a tenant session must mount the self-service workspace");
    assert(
      selfSurface.rail !== null && selfSurface.rail.w > 0,
      "the self-service rail must be mounted and visible",
    );
    assert(
      selfSurface.link !== null && selfSurface.link.h >= 36,
      `self-service rows are ${selfSurface.link?.h}px, below the minimum control height`,
    );

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
    { width: 1024, height: 900 },
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

check("the workbench bounds its exit sample at 200 and platform pulses at 8 visible rows", async ({ origin, page, errors }) => {
  const platformRoute = /\/api\/v1\/platforms(?:\?.*)?$/;
  const nodesRoute = /\/api\/v1\/nodes(?:\?.*)?$/;
  const pulseRoute = /\/api\/v1\/metrics\/(?:snapshots\/platform-node-pool|realtime\/leases)(?:\?.*)?$/;
  const platformRequests = [];
  const nodeRequests = [];
  const poolPulseIds = new Set();
  const leasePulseIds = new Set();
  let nodeItemCount = 0;
  const platforms = Array.from({ length: 100 }, (_, index) => ({
    id: `workbench-platform-${index + 1}`,
    name: `Workbench Platform ${index + 1}`,
    sticky_ttl: "168h",
    regex_filters: [],
    region_filters: [],
    ip_types: [],
    purity_bands: [],
    subscription_filters: [],
    protocols: ["http"],
    routable_node_count: 1,
    reverse_proxy_miss_action: "TREAT_AS_EMPTY",
    reverse_proxy_empty_account_behavior: "RANDOM",
    reverse_proxy_fixed_account_header: "",
    allocation_policy: "BALANCED",
    passive_circuit_breaker_disabled: false,
    updated_at: "2026-10-27T00:00:00Z",
  }));
  const nodes = Array.from({ length: 200 }, (_, index) => ({
    region: "US",
    egress_ip: `198.51.${Math.floor(index / 250)}.${(index % 250) + 1}`,
    enabled: true,
    has_outbound: true,
    circuit_open_since: "",
    intel: { ip_type: "residential", purity_band: "clean" },
    reference_latency_ms: 10,
  }));
  await page.route(platformRoute, async (route) => {
    const url = new URL(route.request().url());
    platformRequests.push(url);
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: platforms, total: 200, limit: 100, offset: 0 }) });
  });
  await page.route(nodesRoute, async (route) => {
    const url = new URL(route.request().url());
    nodeRequests.push(url);
    nodeItemCount = nodes.length;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ items: nodes }) });
  });
  await page.route(pulseRoute, async (route) => {
    const url = new URL(route.request().url());
    const platformId = url.searchParams.get("platform_id") ?? "";
    if (url.pathname.endsWith("/snapshots/platform-node-pool")) {
      poolPulseIds.add(platformId);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ generated_at: "2026-10-27T00:00:00Z", platform_id: platformId, routable_node_count: 1, egress_ip_count: 1 }),
      });
      return;
    }
    leasePulseIds.add(platformId);
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ platform_id: platformId, step_seconds: 60, items: [{ ts: "2026-10-27T00:00:00Z", active_leases: 2 }] }),
    });
  });
  try {
    errors.reset("workbench bounded samples");
    await page.goto(origin + `${DASHBOARD}?workbench-capacity-check=1`);
    await page.reload();
    await rendered(page);
    await page.getByRole("heading", { name: "平台配线" }).waitFor({ timeout: 20000 });
    const platformSurface = page.locator(".workspace-card").filter({ has: page.getByRole("heading", { name: "平台配线" }) });
    assert.equal(await platformSurface.locator(".workspace-object-row").count(), 8, "the workbench must render at most eight platform rows");
    for (let attempt = 0; attempt < 100 && (nodeItemCount !== 200 || poolPulseIds.size < 8 || leasePulseIds.size < 8); attempt += 1) {
      await page.waitForTimeout(50);
    }
    assert(nodeRequests.some((url) => Number(url.searchParams.get("limit")) === 200), "the exit fact request must use a 200-row sample");
    assert.equal(nodeItemCount, 200, "the bounded exit sample fixture must reach the workbench");
    assert(platformRequests.some((url) => Number(url.searchParams.get("limit")) === 100), "the platform register must retain its bounded server page");
    assert.equal(poolPulseIds.size, 8, "platform pool pulses must be limited to the eight visible rows");
    assert.equal(leasePulseIds.size, 8, "platform lease pulses must be limited to the eight visible rows");
    assert.deepEqual(errors.drain(), [], "bounded workbench sampling must not emit a browser error");
  } finally {
    await page.unroute(platformRoute, { behavior: "wait" });
    await page.unroute(nodesRoute, { behavior: "wait" });
    await page.unroute(pulseRoute, { behavior: "wait" });
  }
});

check("the dashboard states its condition and works from real registers", async ({ origin, page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(origin + DASHBOARD);
  await rendered(page);

  /* The current workbench is organized around state, action queues, objects and evidence. */
  await page.getByRole("heading", { name: "控制平面" }).waitFor({ state: "visible", timeout: 20000 });
  await page.locator("main .workspace-data-grid").waitFor({ state: "visible", timeout: 20000 });
  const statusCells = page.locator("main .workspace-data-grid__cell");
  assert.equal(await statusCells.count(), 4, "the workbench must state four global facts");
  const statusText = await statusCells.allInnerTexts();
  assert(statusText.some((text) => /\d/.test(text)), "at least one global fact must carry a number");
  await page.getByRole("heading", { name: "需要处理", exact: true }).waitFor({ state: "visible", timeout: 20000 });
  await page.getByRole("heading", { name: "平台配线" }).waitFor({ state: "visible", timeout: 20000 });
  await page.getByRole("heading", { name: "最近变更" }).waitFor({ state: "visible", timeout: 20000 });
  const platformSurface = page.locator(".workspace-card").filter({ has: page.getByRole("heading", { name: "平台配线" }) });
  assert(
    (await platformSurface.locator(".workspace-object-row").count()) > 0 || (await platformSurface.locator(".workspace-empty").count()) > 0,
    "the platform register must expose real objects or an explicit empty state",
  );
  const evidenceSurface = page.locator(".workspace-card").filter({ has: page.getByRole("heading", { name: "最近变更" }) });
  assert(
    (await evidenceSurface.locator(".workspace-evidence-row").count()) > 0 || (await evidenceSurface.locator(".workspace-empty").count()) > 0,
    "the evidence register must expose real entries or an explicit empty state",
  );
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

  /*
   * The current board uses shared Readout components and token-bound surfaces.
   * Keep this list empty: every collected target must meet its WCAG floor. If a
   * future reference exception is required, it must name the selector, theme and
   * measured ratio so the exemption cannot silently widen.
   */
  const ART_EXEMPTIONS = [];
  const exemptionFor = (sel, theme) =>
    ART_EXEMPTIONS.find((entry) => entry.sel === sel && entry.theme === theme);

  /**
   * Runs in the page: every element that carries its own words and is on screen.
   *
   * Deliberately selector-free. The previous version listed the class names of the
   * one page it was written for, so a composition change shrank the sample to a
   * handful of survivors and the check kept passing on a tenth of the text.
   */
  const COLLECT = () => {
    const rows = [];
    const root = document.querySelector("main");
    if (!root) return rows;
    for (const el of root.querySelectorAll("*")) {
      const own = [...el.childNodes]
        .filter((node) => node.nodeType === Node.TEXT_NODE)
        .map((node) => (node.textContent || "").trim())
        .join(" ")
        .trim();
      if (!own) continue;
      const rect = el.getBoundingClientRect();
      if (rect.width < 2 || rect.height < 2) continue;
      const cx = Math.round(rect.left + rect.width / 2);
      const cy = Math.round(rect.top + rect.height / 2);
      // Off-screen text is not what the operator reads, so it is not measured.
      if (cx < 0 || cy < 0 || cx >= innerWidth || cy >= innerHeight) continue;
      const cs = getComputedStyle(el);
      if (cs.visibility === "hidden" || cs.opacity === "0") continue;
      rows.push({
        sel: el.tagName.toLowerCase(),
        text: own.slice(0, 24),
        ink: cs.color,
        size: parseFloat(cs.fontSize),
        weight: parseInt(cs.fontWeight, 10) || 400,
        cx,
        cy,
      });
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

    const rows = await page.evaluate(COLLECT);
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