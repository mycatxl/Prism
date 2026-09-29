#!/usr/bin/env node
/*
 * The contrast gate for the design system.
 *
 * Every colour pair the panel actually composes is checked against WCAG AA here,
 * from the tokens in `src/styles/design.css`. This exists because contrast is the
 * one design property that is decidable from the source, and because the failure
 * mode is invisible: the previous palette shipped `#8b968f` on `#f3f5f2` — 2.8:1 —
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

const failures = [];
const check = (label, ratio, need) => {
  const ok = ratio >= need - 1e-9;
  if (!ok) failures.push(`${label} = ${ratio.toFixed(2)}:1, need ${need}:1`);
  return { label, ratio, need, ok };
};

const rows = [];

for (const fg of TEXT) {
  for (const bg of SURFACES) {
    rows.push(check(`${fg} on ${bg}`, contrast(token(fg), token(bg)), 4.5));
  }
}

// A state colour is read as a chip label on its own wash, and as plain text on the
// sheet. Both compositions ship.
for (const name of STATES) {
  rows.push(check(`${name} on ${name}-wash`, contrast(token(name), token(`${name}-wash`)), 4.5));
  rows.push(check(`${name} as text on paper-raised`, contrast(token(name), token("paper-raised")), 4.5));
}
for (const name of FILLED) {
  rows.push(check(`white on ${name}`, contrast("#ffffff", token(name)), 4.5));
}

// Focus rings and control borders: non-text, so 3:1 is the bar.
for (const name of ["accent", "signal"]) {
  rows.push(check(`focus ${name} on paper`, contrast(token(name), token("paper")), 3));
}

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

if (failures.length > 0) {
  console.error(`\ncheck-contrast: ${failures.length} failure(s)`);
  for (const failure of failures) {
    console.error(`  - ${failure}`);
  }
  process.exit(1);
}

console.log(`\ncheck-contrast: ${rows.length} pairs pass WCAG AA`);
