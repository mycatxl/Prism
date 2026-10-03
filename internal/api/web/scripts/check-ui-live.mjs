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

// The ground and the ink, read from the dark primitives in src/styles/design.css
// rather than written down here. The console ships dark by default and offers light
// as a switch; both themes are verified pair by pair by `npm run check:contrast`, and
// this file only has to prove the page painted the tokens it was asked for rather
// than falling back to the browser's black on white. Reading them is deliberate: a
// literal turns the next deliberate re-solve of the palette into a false failure.
const darkTheme = (() => {
  const css = readFileSync(new URL("../src/styles/design.css", import.meta.url), "utf8");
  const block = /\[data-theme="dark"\] \{([\s\S]*?)\n\}/.exec(css);
  if (!block) throw new Error("check-ui-live: design.css has no dark theme block");
  return block[1];
})();
const darkToken = (name) => {
  const value = new RegExp(`--p-${name}:\\s*(#[0-9a-fA-F]{6})\\b`).exec(darkTheme);
  if (!value) throw new Error(`check-ui-live: the dark theme declares no --p-${name}`);
  const hex = value[1].slice(1);
  return `rgb(${parseInt(hex.slice(0, 2), 16)}, ${parseInt(hex.slice(2, 4), 16)}, ${parseInt(hex.slice(4, 6), 16)})`;
};
const PAPER = darkToken("canvas");
const INK = darkToken("ink");

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

check("legacy node envelopes do not crash the node pool", async ({ origin, page, errors }) => {
  const legacyItems = [{
    node_hash: "legacy-node",
    created_at: "2026-01-01T00:00:00Z",
    enabled: true,
    has_outbound: true,
    failure_count: 0,
    tags: [],
  }];
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
  // Two rail variants live in this nav and the 2xl breakpoint swaps which one is shown
  // (AppShell renders the board rail inside `2xl:block` and the standard list inside
  // `2xl:hidden`), so the DOM carries 24 links while 12 are reachable. Assert against the
  // rail a user can click, which is also what makes this check meaningful at 1536px.
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
  const map = page.locator("main .egress-map").first();
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
  const strip = page
    .locator("main div.grid.divide-x")
    .or(page.locator("main .panel"))
    .first();
  await strip.waitFor({ state: "visible", timeout: 15000 });
  const readouts = await strip.locator(".numeral").count();
  assert(readouts >= 4, `the instrument strip must show at least four readouts (found ${readouts})`);
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
   * The selectors the board actually paints words with. The shell's rail replica
   * (`.wb-brand-title`, `.wb-rail-top-item`, `.wb-rail-card-item`) is deliberately
   * absent: it was the 1536px layer's own rail, it is gone with that layer, and a
   * selector that matches nothing would quietly shrink this check.
   */
  const TARGETS = [
    ".wb-hero-heading",
    ".wb-hero-desc",
    ".wb-timerange-select",
    ".wb-metric-chip .micro",
    ".wb-metric-chip .numeral",
    ".wb-plate-head h2",
    ".wb-plate-head .label",
    ".wb-region-table thead th",
    ".wb-region-table tbody td",
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