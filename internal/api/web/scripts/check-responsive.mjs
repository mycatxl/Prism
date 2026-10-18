#!/usr/bin/env node
/*
 * Responsive-layout gate for the console.
 *
 * The layout is semantic rather than a pixel replica: the stylesheet owns the
 * rail / instance-bar / work-area cells, the single-column collapse below
 * 1100px, and the page's own measure. This gate keeps those contracts visible
 * while preventing fixed pane geometry from returning.
 *
 * The rule that matters is rule 2: a box that is meant to fill its cell must not
 * carry a literal pixel height. `--shell-bar-h` is a token and may be used; a
 * `height: 52px` in the same place is the same decision written down where it
 * cannot move with the design.
 */

import { readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const cssPath = join(here, "..", "src", "styles", "design.css");

const FORBIDDEN_LAYER = /@media\s*\(\s*min-width\s*:\s*1536px\s*\)/;
const FLUID_BOXES = /\.(?:shell|shell-bar|shell-main|page-content)(?![\w-])/;
/* A page surface is a flow box. Only overlays — the sheet, the drawer, the skip
   link, the scrim — are taken out of flow, and they are not in this list. */
const PANES = /\.(?:panel|page-header|table-wrap|data-grid)(?![\w-])/;
const REQUIRED_RULES = [
  ".shell",
  ".shell-bar",
  ".shell-nav",
  ".shell-main",
  ".page-header",
  ".panel",
  ".data-grid",
];
/*
 * Retired names. Each belonged to a design the console no longer has: a
 * dashboard grid, a paper "register", an amendment type set, a glass layer with
 * its own shadow ladder. They were deleted, not aliased, so a rule that brings
 * one back is either a merge from an old branch or a new page reaching for a
 * world that is not there. Both deserve a failure: `.dash-cell` and `--p-glass`
 * now paint nothing at all.
 */
const RETIRED_SELECTOR_PREFIXES = [
  ".dash-",
  ".register",
  ".amendment",
  ".term-",
  ".glass",
  ".shell-topbar",
  ".shell-theme-toggle",
  ".shell-nav__extra",
];
const RETIRED_PROPERTIES = [
  "--p-glass",
  "--p-slip",
  "--p-punch",
  "--p-heat-rgb",
  "--p-shadow-card",
  "--p-ground-glow",
  "--gutter-w",
  "--margin-w",
];

const failures = [];
let parsedRules = 0;
let boxChecks = 0;
let paneChecks = 0;

function fail(message) {
  failures.push(message);
}

function parseRules(source) {
  const css = source.replace(/\/\*[\s\S]*?\*\//g, " ");
  const rules = [];
  const stack = [];
  let pending = "";
  for (const character of css) {
    if (character === "{") {
      const head = pending.trim();
      pending = "";
      stack.push({ head, isAtRule: head.startsWith("@"), at: stack.filter((frame) => frame.isAtRule).map((frame) => frame.head), body: "" });
      continue;
    }
    if (character === "}") {
      const frame = stack.pop();
      pending = "";
      if (frame && !frame.isAtRule && frame.head) rules.push({ head: frame.head, at: frame.at, body: frame.body });
      continue;
    }
    const top = stack[stack.length - 1];
    if (!top || top.isAtRule) pending += character;
    else top.body += character;
  }
  return rules;
}

function declarations(body) {
  const out = new Map();
  for (const chunk of body.split(";")) {
    const index = chunk.indexOf(":");
    if (index === -1) continue;
    const property = chunk.slice(0, index).trim();
    const value = chunk.slice(index + 1).trim().replace(/\s+/g, " ").replace(/ !important$/, "");
    if (property && value) out.set(property, value);
  }
  return out;
}

let css;
try {
  if (!statSync(cssPath).isFile()) throw new Error("not a file");
  css = readFileSync(cssPath, "utf8");
} catch (error) {
  console.error(`check-responsive: cannot read ${cssPath}: ${error.message}`);
  process.exit(1);
}

if (FORBIDDEN_LAYER.test(css)) {
  fail("rule 1: a @media (min-width: 1536px) geometry layer is back in design.css");
}

const rules = parseRules(css);
parsedRules = rules.length;
if (parsedRules < 100) {
  console.error(`check-responsive: only ${parsedRules} rules parsed from design.css; refusing to pass`);
  process.exit(1);
}

const selectors = new Set();
let inspections = 0;
for (const rule of rules) {
  const declarationsOfRule = declarations(rule.body);
  for (const selector of rule.head.split(",").map((entry) => entry.trim()).filter(Boolean)) {
    selectors.add(selector);
    inspections += 1;
    if (FLUID_BOXES.test(selector)) {
      boxChecks += 1;
      for (const property of ["height", "min-height", "max-height"]) {
        const height = declarationsOfRule.get(property);
        if (height && /^-?[\d.]+px$/.test(height)) fail(`rule 2: ${selector} declares fixed ${property}: ${height}`);
      }
    }
    const position = declarationsOfRule.get("position");
    if (position === "absolute" || position === "fixed") {
      if (PANES.test(selector)) fail(`rule 3: ${selector} is ${position}; dashboard panes must remain grid/flex children`);
      else paneChecks += 1;
    }
    for (const prefix of RETIRED_SELECTOR_PREFIXES) {
      if (selector.startsWith(prefix)) {
        fail(`rule 5: ${selector} is back; \`${prefix}\` was retired, not aliased`);
      }
    }
    for (const property of declarationsOfRule.keys()) {
      if (RETIRED_PROPERTIES.some((retired) => property === retired || property.startsWith(`${retired}-`))) {
        fail(`rule 5: ${selector} declares ${property}, a retired token`);
      }
    }
  }
}

for (const required of REQUIRED_RULES) {
  if (![...selectors].some((selector) => selector.startsWith(required))) {
    fail(`rule 4: no \`${required}\` rule is left in design.css`);
  }
}

if (inspections === 0) fail("no rule was inspected at all");
if (paneChecks === 0) fail("rule 3: no positioned non-pane rule was inspected");

if (failures.length > 0) {
  console.error(`check-responsive: ${failures.length} problem(s) in design.css`);
  for (const failure of failures) console.error(`  - ${failure}`);
  process.exit(1);
}

console.log(`check-responsive: no 1536 layer, ${parsedRules} rules parsed, ${inspections} selectors inspected — ${boxChecks} fluid boxes, ${paneChecks} positioned non-pane rules`);
