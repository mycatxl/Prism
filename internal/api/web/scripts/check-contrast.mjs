#!/usr/bin/env node
/*
 * The contrast gate for the design system.
 *
 * Every colour pair the console composes is checked against WCAG AA here, from the
 * primitives in `src/styles/design.css` — and it checks BOTH themes, because a dark
 * theme is not an inversion: on a near-black panel the same hue needs a different
 * luminance, a wash has to sit *below* the state colour instead of above it, and a
 * filled button takes dark ink (white on a bright accent measures 2.9:1 and fails).
 *
 * This exists because contrast is the one design property that is decidable from
 * the source, and because the failure mode is invisible: an earlier palette shipped
 * `#8b968f` on `#f3f5f2` — 2.8:1 — for every tertiary line, which reads as "washed
 * out" rather than as "broken", so nobody notices until a reviewer measures it.
 *
 * Fails closed: a primitive the system needs but that is missing from a theme is an
 * error, not a skip.
 *
 * Usage: node scripts/check-contrast.mjs
 */

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const cssPath = join(here, "..", "src", "styles", "design.css");
const css = readFileSync(cssPath, "utf8");

/** Reads the `--p-*` primitives declared inside one CSS block. */
function primitives(selector) {
  const start = css.indexOf(selector);
  if (start < 0) {
    console.error(`check-contrast: design.css has no ${selector} block`);
    process.exit(1);
  }
  const open = css.indexOf("{", start);
  // The first closing brace at column 0 ends the block; the file is written that way.
  const close = css.indexOf("\n}", open);
  const body = css.slice(open, close < 0 ? undefined : close);
  const found = new Map();
  for (const match of body.matchAll(/--p-([a-z0-9-]+):\s*(#[0-9a-fA-F]{6})\b/g)) {
    found.set(match[1], match[2].toLowerCase());
  }
  if (found.size === 0) {
    console.error(`check-contrast: ${selector} declares no --p-* colour primitives`);
    process.exit(1);
  }
  return found;
}

const channel = (v) => {
  const c = v / 255;
  return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
};

const luminance = (hex) => {
  const h = hex.slice(1);
  return (
    0.2126 * channel(parseInt(h.slice(0, 2), 16)) +
    0.7152 * channel(parseInt(h.slice(2, 4), 16)) +
    0.0722 * channel(parseInt(h.slice(4, 6), 16))
  );
};

const contrast = (a, b) => {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};

const SURFACES = ["canvas", "sunk", "raised", "inset", "rail", "elevated"];
const TEXT = ["ink", "ink-soft", "ink-faint"];
const STATES = ["signal", "live", "warn", "alert", "accent"];
const SERIES = ["series-1", "series-2", "series-3", "series-4", "series-5", "series-6"];

const themes = [
  { name: "light", selector: ":root {" },
  { name: "dark", selector: '[data-theme="dark"] {' },
];

const failures = [];
let checked = 0;

for (const theme of themes) {
  const p = primitives(theme.selector);
  const token = (name) => {
    const value = p.get(name);
    if (!value) {
      console.error(`check-contrast: theme ${theme.name} is missing --p-${name}`);
      process.exit(1);
    }
    return value;
  };
  const rows = [];
  const check = (label, ratio, need) => {
    checked += 1;
    if (ratio < need - 1e-9) {
      failures.push(`[${theme.name}] ${label} = ${ratio.toFixed(2)}:1, need ${need}:1`);
    }
    rows.push({ label, ratio, need, ok: ratio >= need - 1e-9 });
  };

  for (const fg of TEXT) {
    for (const bg of SURFACES) {
      check(`${fg} on ${bg}`, contrast(token(fg), token(bg)), 4.5);
    }
  }

  // A state colour is read as a chip label on its own wash, as plain text on the
  // sheet, and as dark-or-white ink on itself when it fills a button.
  for (const name of STATES) {
    check(`${name} on ${name}-wash`, contrast(token(name), token(`${name}-wash`)), 4.5);
    check(`${name} as text on raised`, contrast(token(name), token("raised")), 4.5);
    check(`${name}-wash differs from raised`, contrast(token(`${name}-wash`), token("raised")), 1.05);
  }
  for (const name of ["accent", "signal", "alert", "live"]) {
    const on = token(`on-${name}`);
    if (!on) {
      failures.push(`[${theme.name}] theme does not define --p-on-${name}`);
      continue;
    }
    check(`on-${name} over ${name}`, contrast(on, token(name)), 4.5);
  }

  // Non-text: focus rings and control boundaries.
  for (const name of ["accent", "signal"]) {
    check(`focus ${name} on canvas`, contrast(token(name), token("canvas")), 3);
  }

  /*
   * The hero pane is the console's one gradient surface, and it is not one of the six
   * composited surface primitives — it is a separate fill with its own two stops, so it
   * is checked as its own pair set. `ink` and `ink-soft` are what the pane carries (a
   * 20px heading and a 12px description). `ink-faint` is deliberately not checked here
   * and must not be placed on the hero: it measures 4.3:1 on the darker stop, which is
   * exactly the kind of near-miss this file exists to catch when the pair *is* used.
   */
  for (const stop of ["hero", "hero-lift"]) {
    for (const fg of ["ink", "ink-soft"]) {
      check(`${fg} on ${stop}`, contrast(token(fg), token(stop)), 4.5);
    }
  }

  // Chart series are graphical objects: WCAG 1.4.11 asks for 3:1 against the ground.
  for (const name of SERIES) {
    check(`${name} drawn on raised`, contrast(token(name), token("raised")), 3);
  }
  check("chart axis label", contrast(token("chart-axis"), token("inset")), 4.5);

  // Adjacent luminances must differ, or two series are one series in greyscale.
  const lum = SERIES.map((name) => ({ name, l: luminance(token(name)) })).sort((a, b) => a.l - b.l);
  for (let i = 1; i < lum.length; i += 1) {
    const r = (lum[i].l + 0.05) / (lum[i - 1].l + 0.05);
    if (r < 1.15) {
      failures.push(
        `[${theme.name}] series ${lum[i - 1].name} and ${lum[i].name} are ${r.toFixed(3)}:1 apart in luminance`,
      );
    }
  }

  console.log(`\n--- ${theme.name} --- ${rows.length} pairs`);
  for (const row of rows.filter((r) => !r.ok)) {
    console.log(`  FAIL ${row.label} ${row.ratio.toFixed(2)}:1 (need ${row.need}:1)`);
  }
  if (rows.every((r) => r.ok)) {
    console.log("  all pass");
  }
}

if (failures.length > 0) {
  console.error(`\ncheck-contrast: ${failures.length} failure(s)`);
  for (const failure of failures) {
    console.error(`  - ${failure}`);
  }
  process.exit(1);
}

console.log(`\ncheck-contrast: ${checked} pairs across 2 themes pass WCAG AA`);
