#!/usr/bin/env node
/*
 * The contrast gate for the design system.
 *
 * Every colour pair the console actually composes is checked against WCAG AA here,
 * from the tokens in `src/styles/design.css`. This exists because contrast is the
 * one design property that is decidable from the source, and because the failure
 * mode is invisible: an earlier palette shipped `#8b968f` on `#f3f5f2` — 2.8:1 —
 * for every tertiary line in the panel, which reads as "washed out" rather than as
 * "broken", so nobody notices until a reviewer measures it.
 *
 * Fails closed: a token the design system needs but that is missing from the file
 * is an error, not a skip.
 *
 * Usage: node scripts/check-contrast.mjs
 */

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const cssPath = join(here, "..", "src", "styles", "design.css");
const css = readFileSync(cssPath, "utf8");

const tokens = new Map();
for (const match of css.matchAll(/--color-([a-z0-9-]+):\s*(#[0-9a-fA-F]{6})\b/g)) {
  tokens.set(match[1], match[2].toLowerCase());
}

const token = (name) => {
  const value = tokens.get(name);
  if (!value) {
    console.error(`check-contrast: design.css does not define --color-${name}`);
    process.exit(1);
  }
  return value;
};

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

const SURFACES = ["paper", "paper-sunk", "paper-raised", "paper-inset", "rail"];
const TEXT = ["ink", "ink-soft", "ink-faint"];
const STATES = ["signal", "live", "warn", "alert", "accent"];
const FILLED = ["signal", "signal-deep", "accent", "accent-deep", "alert", "live"];
// Chart series are graphical objects, not text: WCAG 1.4.11 asks for 3:1 against
// the ground they are drawn on. Legend and axis *labels* are text and are checked
// separately below.
const SERIES = ["series-1", "series-2", "series-3", "series-4", "series-5", "series-6"];
const SERIES_GROUNDS = ["paper-raised", "paper-inset"];

const failures = [];
const rows = [];
const check = (label, ratio, need) => {
  const ok = ratio >= need - 1e-9;
  if (!ok) failures.push(`${label} = ${ratio.toFixed(2)}:1, need ${need}:1`);
  rows.push({ label, ratio, need, ok });
};

for (const fg of TEXT) {
  for (const bg of SURFACES) {
    check(`${fg} on ${bg}`, contrast(token(fg), token(bg)), 4.5);
  }
}

// A state colour is read as a chip label on its own wash, and as plain text on the
// sheet. Both compositions ship.
for (const name of STATES) {
  check(`${name} on ${name}-wash`, contrast(token(name), token(`${name}-wash`)), 4.5);
  check(`${name} as text on paper-raised`, contrast(token(name), token("paper-raised")), 4.5);
}
for (const name of FILLED) {
  check(`white on ${name}`, contrast("#ffffff", token(name)), 4.5);
}

// Focus rings and control borders: non-text, so 3:1 is the bar.
for (const name of ["accent", "signal"]) {
  check(`focus ${name} on paper`, contrast(token(name), token("paper")), 3);
}

for (const name of SERIES) {
  for (const ground of SERIES_GROUNDS) {
    check(`${name} drawn on ${ground}`, contrast(token(name), token(ground)), 3);
  }
}
check("chart axis label", contrast(token("chart-axis"), token("paper-inset")), 4.5);

const width = Math.max(...rows.map((r) => r.label.length));
for (const row of rows) {
  console.log(
    `${row.ok ? "ok  " : "FAIL"} ${row.label.padEnd(width)} ${row.ratio.toFixed(2).padStart(6)}:1  (need ${row.need}:1)`,
  );
}

// A wash that is indistinguishable from the sheet is not a state marker.
for (const name of STATES) {
  const distance = contrast(token(`${name}-wash`), token("paper-raised"));
  if (distance < 1.05) {
    failures.push(`${name}-wash is not distinguishable from paper-raised (${distance.toFixed(3)}:1)`);
  }
}

// Adjacent chart series that read as the same colour in greyscale are one series
// with two names. Luminance is the channel a printed screenshot keeps.
const lum = SERIES.map((name) => ({ name, l: luminance(token(name)) })).sort((a, b) => a.l - b.l);
for (let i = 1; i < lum.length; i += 1) {
  const [lo, hi] = [lum[i - 1].l, lum[i].l];
  const ratio = (hi + 0.05) / (lo + 0.05);
  if (ratio < 1.15) {
    failures.push(
      `series ${lum[i - 1].name} and ${lum[i].name} are ${ratio.toFixed(3)}:1 apart in luminance`,
    );
  }
}

if (failures.length > 0) {
  console.error(`\ncheck-contrast: ${failures.length} failure(s)`);
  for (const failure of failures) {
    console.error(`  - ${failure}`);
  }
  process.exit(1);
}

console.log(`\ncheck-contrast: ${rows.length} pairs pass WCAG AA`);
