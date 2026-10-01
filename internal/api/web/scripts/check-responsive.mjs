#!/usr/bin/env node
/*
 * The responsive-layout gate for the console.
 *
 * What this gate used to protect: a `@media (min-width: 1536px)` layer in
 * `src/styles/design.css` that reproduced an operator's reference dashboard as
 * pixel geometry - unequal KPI widths, a quick-action card overlapping the hero, a
 * rail replica and two panes folded away. The gate checked that every slot was a
 * percentage of a 1288x1024 canvas and that the percentages resolved back to the
 * measured art.
 *
 * That layer is gone. The board is a responsive Bento grid: twelve columns at
 * `xl`, panels that stack below it, and not one pixel of pane geometry in the
 * stylesheet - the grid classes live in the JSX (`xl:grid-cols-12`,
 * `xl:col-span-8`). The gate now protects *that*, which is a property of the
 * stylesheet and therefore still decidable without a browser:
 *
 *   1. No `@media (min-width: 1536px)` block reappears. A breakpoint layer that
 *      positions panes is exactly the regression this file exists to catch.
 *   2. No `.wb-board` / `.wb-shell-root` / `.wb-main-zone` rule carries a fixed
 *      pixel height. These boxes must be fluid, and a fixed height is how the old
 *      board collapsed and how it would collapse again. Today none of the three
 *      carries a rule at all - their layout is utility classes in the JSX - so
 *      this is a guard: the moment one is styled again, a pixel height on it
 *      fails.
 *   3. No board pane is absolutely positioned. Panes are grid and flex children,
 *      so a `position: absolute` rule that names one is pane-positioning code
 *      coming back. (Absolute positioning that is *not* a board pane - the range
 *      picker's chevron, `.wb-timerange-icon` - is not board geometry and is not
 *      flagged.)
 *   4. The board's own rules are still present. A gate that passes because the
 *      board's rules were deleted or renamed is a false clean, which is the one
 *      failure mode a gate must not have.
 *
 * Fails closed: a missing stylesheet, an unparseable file, an implausible rule
 * count, a missing board rule or an empty check set all exit non-zero.
 *
 * Usage: node scripts/check-responsive.mjs
 */

import { readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const cssPath = join(here, "..", "src", "styles", "design.css");

/** The breakpoint layer whose return would undo the whole rework. */
const FORBIDDEN_LAYER = /@media\s*\(\s*min-width\s*:\s*1536px\s*\)/;

/**
 * Boxes that must never be given a fixed height again.
 *
 * The board's own boxes carry no CSS rule today - their layout is utility classes
 * in the JSX - so this is a guard rather than a measurement: the moment one of
 * them is styled again, a pixel height on it is a regression.
 */
const FLUID_BOXES = /\.wb-(board|shell-root|main-zone)\b/;

/**
 * The board's panes. A `position: absolute` rule naming one of these is pane
 * geometry: panes belong to the grid, and this list is the vocabulary the board
 * actually uses.
 */
const PANES =
  /\.wb-(board|board-stack|slot|folded|kpi|hero-banner|metric-chips|plate|plate-head|plate-side|plate-map|status-card|status-title|status-desc)\b/;

/**
 * Board rules that must still be in the stylesheet, matched by selector prefix.
 *
 * A gate that passes because the board's rules were renamed or deleted reads
 * exactly like a gate that passes, so each of these must exist to be checked:
 * the hero band and its chips, the status line, the map plate (`.wb-plate`,
 * `.wb-plate-head`, `.wb-plate-map`) and the region table's `.wb-region-table …`
 * group.
 */
const REQUIRED_RULES = [
  ".wb-hero-banner",
  ".wb-hero-heading",
  ".wb-metric-chips",
  ".wb-status-card",
  ".wb-plate",
  ".wb-region-table",
];

const failures = [];
let parsedRules = 0;
let boxChecks = 0;
let paneChecks = 0;

function fail(message) {
  failures.push(message);
}

/**
 * Every rule in the stylesheet, flattened.
 *
 * Comments are stripped first so a `{` inside prose cannot open a block. The scan
 * keeps a stack of frames: an at-rule frame collects its prelude as it opens and
 * its children are ordinary rules, which is all the structure this gate needs.
 * A rule's `at` list is the at-rules it sits inside, so a future breakpoint layer
 * is visible in the parse rather than only in a regex.
 */
function parseRules(source) {
  const css = source.replace(/\/\*[\s\S]*?\*\//g, " ");
  const rules = [];
  const stack = [];
  let pending = "";

  for (const character of css) {
    if (character === "{") {
      const head = pending.trim();
      pending = "";
      stack.push({
        head,
        isAtRule: head.startsWith("@"),
        at: stack.filter((frame) => frame.isAtRule).map((frame) => frame.head),
        body: "",
      });
      continue;
    }
    if (character === "}") {
      const frame = stack.pop();
      pending = "";
      if (frame && !frame.isAtRule && frame.head) {
        rules.push({ head: frame.head, at: frame.at, body: frame.body });
      }
      continue;
    }
    const top = stack[stack.length - 1];
    if (!top || top.isAtRule) {
      pending += character;
    } else {
      top.body += character;
    }
  }

  return rules;
}

/** The declarations of one rule body, `!important` stripped. */
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

// ---------------------------------------------------------------------------

let css;
try {
  if (!statSync(cssPath).isFile()) throw new Error("not a file");
  css = readFileSync(cssPath, "utf8");
} catch (error) {
  console.error(`check-responsive: cannot read ${cssPath}: ${error.message}`);
  process.exit(1);
}

if (FORBIDDEN_LAYER.test(css)) {
  fail(
    "rule 1: a `@media (min-width: 1536px)` block is back in design.css; the board is a responsive Bento grid and pane geometry in a breakpoint layer is the regression this gate exists to catch",
  );
}

const rules = parseRules(css);
parsedRules = rules.length;
if (parsedRules < 100) {
  console.error(
    `check-responsive: only ${parsedRules} rules parsed from design.css; that is implausible, refusing to pass`,
  );
  process.exit(1);
}

const selectors = new Set();
let inspections = 0;
for (const rule of rules) {
  const declarationsOfRule = declarations(rule.body);
  for (const selector of rule.head.split(",").map((entry) => entry.trim()).filter(Boolean)) {
    selectors.add(selector);
    inspections += 1;

    // Rule 2: the board's own boxes stay fluid.
    if (FLUID_BOXES.test(selector)) {
      boxChecks += 1;
      for (const property of ["height", "min-height", "max-height"]) {
        const height = declarationsOfRule.get(property);
        if (height && /^-?[\d.]+px$/.test(height)) {
          fail(
            `rule 2: ${selector} declares ${property}: ${height}; the board's boxes must be fluid, so a fixed pixel height here is a regression`,
          );
        }
      }
    }

    // Rule 3: panes are grid children, never absolutely positioned.
    const position = declarationsOfRule.get("position");
    if (position === "absolute" || position === "fixed") {
      if (PANES.test(selector)) {
        fail(
          `rule 3: ${selector} is ${position}; a board pane belongs to the grid or the flex stack, so pane positioning is back`,
        );
      } else {
        paneChecks += 1;
      }
    }
  }
}

// Rule 4: the board's own rules must still be there to be checked at all.
for (const required of REQUIRED_RULES) {
  if (![...selectors].some((selector) => selector.startsWith(required))) {
    fail(`rule 4: no \`${required}\` rule is left in design.css; the board's own rules are what this gate protects`);
  }
}

if (inspections === 0) {
  fail("no rule was inspected at all; the gate cannot run");
}
if (paneChecks === 0) {
  fail("rule 3: no positioned rule was inspected at all; the gate cannot run");
}

// ---------------------------------------------------------------------------

if (failures.length > 0) {
  console.error(`check-responsive: ${failures.length} problem(s) in design.css`);
  for (const failure of failures) console.error(`  - ${failure}`);
  process.exit(1);
}

console.log(
  `check-responsive: no 1536 layer, ${parsedRules} rules parsed, ${inspections} selectors inspected — ${boxChecks} board boxes carry a rule (their layout is utility classes), ${paneChecks} positioned rules and none of them a pane; the board carries no pane geometry`,
);
