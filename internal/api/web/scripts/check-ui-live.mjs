/**
 * Prism panel — browser regression check.
 *
 * One script, one run: it boots an isolated backend, drives a real Chromium
 * against the panel it serves, and asserts the design system the rewrite
 * introduced (see DESIGN.md). It replaces the old per-feature browser scripts,
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
import { existsSync } from "node:fs";
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
  "/ui/exports",
  "/ui/request-logs",
  "/ui/endpoints",
  "/ui/rules",
  "/ui/resources",
  "/ui/system-config",
  "/ui/audit",
];

// Class tokens the redesign deleted. A trailing dash means "prefix". `card` is
// matched as a whole token, because the kit's own utilities are named around it.
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
];

// DESIGN.md colour tokens, as the browser reports them.
// The console ships dark by default and offers light as a switch, so these are the
// dark primitives from src/styles/design.css. Both themes are verified pair by pair
// by `npm run check:contrast`; this file only has to prove the page painted the
// ground it was asked for rather than falling back to the browser's white.
const PAPER = "rgb(11, 18, 32)";
const INK = "rgb(241, 243, 247)";

const LOCALE = "zh-CN";
const DASHBOARD = "/ui/dashboard";
const NODES = "/ui/nodes";

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

function startBackend({ root, port, adminToken, proxyToken }) {
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
      RESIN_PORT: String(port),
      PRISM_UI_HOST: "127.0.0.1",
      PRISM_UI_PORT: String(port),
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
  let stderr = "";
  backend.stderr.on("data", (data) => {
    stderr = (stderr + data).slice(-4000);
  });
  return { backend, exit: once(backend, "exit"), stderr: () => stderr };
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
 */
async function seedInventory(origin, adminToken) {
  const headers = { Authorization: "Bearer " + adminToken, "Content-Type": "application/json" };
  const content = JSON.stringify({
    outbounds: [
      { type: "http", tag: "Local Alpha", server: "127.0.0.1", server_port: 9 },
      { type: "http", tag: "Local Beta", server: "127.0.0.1", server_port: 10 },
    ],
  });
  const created = await fetch(origin + "/api/v1/subscriptions", {
    method: "POST",
    headers,
    body: JSON.stringify({ name: "UI_Local", source_type: "local", enabled: true, content }),
  });
  assert(created.ok, `fixture subscription must be created (HTTP ${created.status})`);
  const { id } = await created.json();

  const refreshed = await fetch(origin + `/api/v1/subscriptions/${id}/actions/refresh`, {
    method: "POST",
    headers,
  });
  assert(refreshed.ok, `fixture subscription must refresh (HTTP ${refreshed.status})`);

  for (let attempt = 0; attempt < 40; attempt++) {
    const page = await fetch(origin + "/api/v1/nodes?limit=1", { headers }).then((r) => r.json());
    if ((page.total ?? 0) > 0) return page.total;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error("the fixture subscription never imported its nodes");
}

// ---------------------------------------------------------------------------
// Checks
// ---------------------------------------------------------------------------

check("backend boots, inventory imports and the panel signs in", async ({ origin, page, adminToken }) => {
  const health = await fetch(origin + "/healthz");
  assert.equal(health.status, 200, "/healthz must answer 200");

  const nodes = await seedInventory(origin, adminToken);
  assert(nodes >= 2, `the fixture subscription must import two nodes (imported ${nodes})`);

  await page.goto(origin + "/ui/");
  await page.locator("#token").waitFor({ state: "visible", timeout: 15000 });
  assert.match(page.url(), /\/ui\/login/, "an anonymous visit to /ui/ must land on the sign-in page");

  await page.locator("#token").fill(adminToken);
  await page.locator('form button[type="submit"]').click();
  await page.waitForURL(/\/ui\/dashboard$/, { timeout: 15000 });

  const stored = await page.evaluate(() => sessionStorage.getItem("prism.admin-session"));
  assert.equal(stored, adminToken, "the admin token must be kept in sessionStorage under prism.admin-session");
});

check("every route renders without a page or console error", async ({ origin, page, errors }) => {
  const failures = [];
  for (const path of RAIL_PATHS) {
    errors.reset(path);
    await page.goto(origin + path);
    if (!(await rendered(page))) failures.push(`${path}: no page heading rendered`);
    await page.waitForTimeout(150);
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

check("the design tokens are the ones actually applied", async ({ origin, page }) => {
  await page.goto(origin + DASHBOARD);
  await rendered(page);
  const body = await page.evaluate(() => {
    const style = getComputedStyle(document.body);
    return { background: style.backgroundColor, color: style.color };
  });
  assert.equal(body.background, PAPER, "body must sit on the canvas token");
  assert.equal(body.color, INK, "body must be written in the ink token");

  // A data cell is mono by construction (Table.tsx TDNum + design.css .readout).
  await page.goto(origin + "/ui/subscriptions");
  await rendered(page);
  await page.locator("td.readout").first().waitFor({ state: "attached", timeout: 15000 });
  const cell = await page.evaluate(() => {
    for (const element of document.querySelectorAll("td.readout")) {
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
  assert.equal(await rail.locator("a").count(), RAIL_PATHS.length, "the rail must carry one link per destination");
  for (const path of RAIL_PATHS) {
    assert.equal(await rail.locator(`a[href="${path}"]`).count(), 1, `the rail must link to ${path}`);
  }
  // aria-current is written by React after the history entry changes, so wait for
  // it rather than reading the attribute the instant the URL moves.
  const dashboardLink = rail.locator(`a[href="${DASHBOARD}"][aria-current="page"]`);
  await dashboardLink.waitFor({ state: "attached", timeout: 15000 });
  assert.equal(await rail.locator('a[aria-current="page"]').count(), 1, "exactly one destination is current");

  await rail.locator(`a[href="${NODES}"]`).click();
  await page.waitForURL(/\/ui\/nodes$/);
  const nodesLink = rail.locator(`a[href="${NODES}"][aria-current="page"]`);
  await nodesLink.waitFor({ state: "attached", timeout: 15000 });
  assert.equal(await rail.locator('a[aria-current="page"]').count(), 1, "exactly one destination is current");
  assert.equal(
    await nodesLink.getAttribute("aria-current"),
    "page",
    "the clicked route must become current",
  );
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

  // The exit map is the only ECharts surface labelled with its own section.
  const map = page.locator('main [role="img"][aria-label="出口 / 区域"]');
  await map.waitFor({ state: "visible", timeout: 20000 });
  const canvas = map.locator("canvas").first();
  await canvas.waitFor({ state: "attached", timeout: 20000 });
  const box = await canvas.boundingBox();
  assert(box && box.width > 0 && box.height > 0, "the exit map must draw a canvas with a real size");

  // The instrument strip is the ReadoutStrip: a grid whose children are divided
  // by hairlines. Measuring it by its readouts keeps the assertion about content.
  // The dashboard's composition has changed more than once; anchor on the panel
  // the hero lives in rather than on the layout classes it happened to use.
  const strip = page
    .locator("main div.grid.divide-x")
    .or(page.locator("main .panel"))
    .first();
  await strip.waitFor({ state: "visible", timeout: 15000 });
  const readouts = await strip.locator(".readout").count();
  assert(readouts >= 4, `the instrument strip must show at least four readouts (found ${readouts})`);
});

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

/** Waits until the route has painted a page heading inside the shell's main region. */
async function rendered(page) {
  try {
    await page.locator("main h1").first().waitFor({ state: "visible", timeout: 15000 });
    return true;
  } catch {
    return false;
  }
}

const root = await mkdtemp(join(tmpdir(), "prism-live-test-"));
const adminToken = randomBytes(32).toString("hex");
const proxyToken = randomBytes(32).toString("hex");
const results = [];
let browser;
let backend;
let backendExit;
let setupError;

try {
  const port = await reservePort();
  const origin = `http://127.0.0.1:${port}`;
  const started = startBackend({ root, port, adminToken, proxyToken });
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

  const fixture = { origin, page, adminToken, errors };
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