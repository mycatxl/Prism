#!/usr/bin/env node
/*
 * The responsive-layout gate for the console.
 *
 * The 1536 breakpoint layer in `src/styles/design.css` is a replica of the reference dashboard,
 * and it used to be a fixed 1288x1024 canvas: correct at exactly 1536x1024, and a board glued to
 * the top-left corner of the main column at every larger window (measured: 40% of a 2560x1440
 * window covered, 1048px of empty space to the right). The fix was to place every slot as a
 * percentage of the canvas, which is only correct as long as nobody writes a fixed length back
 * into that layer - and nothing in the repo could see that, because the browser regression needs
 * a browser and CI does not install one (`check-kit.mjs` made the same argument for the kit).
 *
 * This script reads the stylesheet and checks the layer directly. Rules:
 *   1. The canvas fills the window: the board is absolutely positioned with
 *      `height: max(100%, 1024px)` and `width: 100%`, and neither the board, the shell, the main
 *      column nor the rail column may carry a fixed pixel height there. (The `100%` only resolves
 *      because the board is positioned against the main column's padding box; a wrapper with
 *      `height: auto` between the column and the board silently collapses it - that happened, and
 *      every slot collapsed with it.)
 *   2. Every board slot places itself with percentages on both axes, and every rail anchor that
 *      moves with the window does too.
 *   3. Those percentages must resolve to the reference geometry at 1288x1024 (and to the card's
 *      835px height for the rail card's children), within 0.6px. The expected numbers are the
 *      measured reference positions, and the comments next to each rule must agree with them.
 *   4. Apart from a documented allow-list, no positional pixel value may appear in the layer:
 *      type, radii, borders, padding and the fixed 248px rail stay in px on purpose, because this
 *      is a dashboard that gets wider, not a screenshot that gets scaled.
 *
 * Fails closed: a missing stylesheet, a missing media block, an implausible number of parsed
 * rules or an empty allow-list all exit non-zero.
 *
 * Usage: node scripts/check-responsive.mjs
 */

import { readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const cssPath = join(here, "..", "src", "styles", "design.css");

/** Reference canvas, in css pixels, as measured off the reference dashboard. */
const CANVAS = { width: 1288, height: 1024 };
/** The rail card is a child of the rail column; the widgets inside it are children of the card. */
const RAIL_CARD = { width: 215, height: 835 };

/** Slot -> reference geometry inside the canvas. */
const SLOTS = {
  ".wb-slot-a": { left: 9, top: 0, width: 1255, height: 233 },
  ".wb-slot-b": { left: 989, top: 163, width: 275, height: 198 },
  ".wb-slot-d": { left: 9, top: 250, width: 797, height: 312 },
  ".wb-slot-e": { left: 822, top: 250, width: 151, height: 111 },
  ".wb-slot-f": { left: 822, top: 378, width: 442, height: 201 },
  ".wb-slot-g1": { left: 9, top: 579, width: 206, height: 109 },
  ".wb-slot-g2": { left: 230, top: 579, width: 188, height: 109 },
  ".wb-slot-g3": { left: 433, top: 579, width: 186, height: 109 },
  ".wb-slot-g4": { left: 634, top: 579, width: 173, height: 109 },
  ".wb-slot-h": { left: 822, top: 600, width: 442, height: 334 },
  ".wb-slot-i": { left: 9, top: 704, width: 470, height: 283 },
  ".wb-slot-j": { left: 495, top: 704, width: 311, height: 283 },
};

/** Rail anchors that move with the window, and the base their percentages resolve against. */
const RAIL_ANCHORS = {
  ".wb-rail-top-links": { base: CANVAS.height, top: 68 },
  ".wb-rail-card": { base: CANVAS.height, top: 188, height: RAIL_CARD.height },
  // both are children of .wb-rail-card (AppShell.tsx), so they scale with the card, not the column
  ".wb-rail-status": { base: RAIL_CARD.height, top: 525 },
  ".wb-rail-footer": { base: RAIL_CARD.height, top: 649 },
};

/** Selectors whose box must never be a fixed height inside this layer. */
const FLUID_BOXES = [
  ".wb-board",
  ".wb-shell-root:has(.wb-board) .wb-shell-root",
  ".wb-shell-root:has(.wb-board) .wb-main-zone",
  ".wb-rail-zone",
];

/** Positional px that is deliberate. Everything else in the layer must be relative. */
const ALLOWED_PX = new Set([
  "min-width: 1536px",
  "min-width: 1288px",
  "width: 248px",
  "height: 76px",
  "left: 11px",
  "width: 481px",
  "top: 18px",
  "height: 32px",
  "min-height: 32px",
  "top: 6px",
  "height: 28px",
  // rail furniture that stays put: brand, the two top links, the card's items and the widgets
  "left: 50px",
  "top: 34px",
  "width: 13px",
  "height: 13px",
  "left: 75px",
  "top: 33px",
  "height: 14px",
  "left: 20px",
  "width: 215px",
  "height: 112px",
  "left: 8px",
  "width: 199px",
  "height: 36px",
  "top: 0px",
  "top: 74px",
  "left: 18px",
  "height: 13px",
  "left: 37px",
  "width: 146px",
  "height: 102px",
  "width: 100px",
  "height: 25px",
  "width: 96px",
  "height: 96px",
  "min-height: 54px",
]);

const failures = [];
const notes = [];

function fail(message) {
  failures.push(message);
}

function parseLayer(text) {
  const lines = text.split("\n");
  const start = lines.findIndex((line) => line.startsWith("@media (min-width: 1536px)"));
  if (start === -1) return null;
  let end = -1;
  for (let i = start + 1; i < lines.length; i++) {
    if (lines[i] === "}") {
      end = i;
      break;
    }
  }
  if (end === -1) return null;
  const body = lines.slice(start + 1, end);

  const rules = [];
  let pendingComment = [];
  let selectorLines = [];
  let bodyLines = [];
  let inBlock = false;
  let inComment = false;
  for (const rawLine of body) {
    const line = rawLine;
    if (!inBlock) {
      const trimmed = line.trim();
      if (trimmed.startsWith("/*")) {
        pendingComment.push(trimmed);
        if (!trimmed.includes("*/")) inComment = true;
        continue;
      }
      if (inComment) {
        pendingComment.push(trimmed);
        if (trimmed.includes("*/")) inComment = false;
        continue;
      }
      if (trimmed === "") continue;
      selectorLines.push(trimmed);
      if (trimmed.endsWith("{")) {
        inBlock = true;
        bodyLines = [];
      }
      continue;
    }
    if (line.trim() === "}" || line.trim() === "};") {
      const selectors = selectorLines
        .join(" ")
        .replace(/\{$/, "")
        .split(",")
        .map((entry) => entry.trim().replace(/\s+/g, " "))
        .filter(Boolean);
      const declarations = {};
      const withoutComments = bodyLines.join(" ").replace(/\/\*[\s\S]*?\*\//g, " ");
      for (const chunk of withoutComments.split(";")) {
        const index = chunk.indexOf(":");
        if (index === -1) continue;
        const property = chunk.slice(0, index).trim();
        const value = chunk.slice(index + 1).trim().replace(/\s+/g, " ");
        if (property && value) declarations[property] = value.replace(/ !important$/, "");
      }
      rules.push({ selectors, declarations, comment: pendingComment.join(" ") });
      pendingComment = [];
      selectorLines = [];
      bodyLines = [];
      inBlock = false;
      continue;
    }
    bodyLines.push(line);
  }
  return rules;
}

function cascade(rules) {
  const merged = new Map();
  for (const rule of rules) {
    for (const selector of rule.selectors) {
      const existing = merged.get(selector) ?? { declarations: {}, comment: rule.comment };
      merged.set(selector, {
        declarations: { ...existing.declarations, ...rule.declarations },
        comment: existing.comment || rule.comment,
      });
    }
  }
  return merged;
}

function value(declarations, property) {
  return declarations[property];
}

function length(valueText) {
  if (typeof valueText !== "string") return null;
  const match = /^(-?[\d.]+)px$/.exec(valueText.trim());
  return match ? Number(match[1]) : null;
}

function percent(valueText) {
  if (typeof valueText !== "string") return null;
  const match = /^(-?[\d.]+)%$/.exec(valueText.trim().replace(/\s+/g, ""));
  return match ? Number(match[1]) : null;
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

const rules = parseLayer(css);
if (!rules || rules.length === 0) {
  console.error("check-responsive: the 1536px layer is missing or empty; the gate cannot run");
  process.exit(1);
}
if (rules.length < 20) {
  console.error(`check-responsive: only ${rules.length} rules parsed from the layer; that is implausible, refusing to pass`);
  process.exit(1);
}
if (ALLOWED_PX.size === 0) {
  console.error("check-responsive: the px allow-list is empty; the gate cannot run");
  process.exit(1);
}

const bySelector = cascade(rules);
notes.push(`layer parsed: ${rules.length} rules, ${bySelector.size} selectors`);

// Rule 1: the canvas fills the window.
for (const selector of FLUID_BOXES) {
  const rule = bySelector.get(selector);
  if (!rule) {
    fail(`rule 1: ${selector} has no rule in the 1536 layer`);
    continue;
  }
  const height = rule.declarations.height;
  if (!height) {
    fail(`rule 1: ${selector} declares no height`);
  } else if (/^[\d.]+px$/.test(height)) {
    fail(`rule 1: ${selector} has a fixed height (${height}); the canvas must fill the window`);
  } else if (!/max\(\s*100%\s*,\s*[\d.]+px\s*\)/.test(height) && height !== "100%" && height !== "100dvh") {
    fail(`rule 1: ${selector} has height ${height}, which neither fills the window nor keeps the reference floor`);
  }
}

const board = bySelector.get(".wb-board");
if (!board) {
  fail("rule 1: .wb-board has no rule in the 1536 layer");
} else {
  if (board.declarations.position !== "absolute") {
    fail(`rule 1: .wb-board is ${board.declarations.position ?? "static"}; a percentage height inside a wrapper with auto height collapses to 0, so the board must be absolutely positioned`);
  }
  if (value(board.declarations, "width") !== "100%") {
    fail(`rule 1: .wb-board width is ${value(board.declarations, "width") ?? "(unset)"}, expected 100% so the art grows with the window`);
  }
  const height = value(board.declarations, "height") ?? "";
  const floor = /max\(\s*100%\s*,\s*([\d.]+)px\s*\)/.exec(height);
  if (!floor) {
    fail(`rule 1: .wb-board height is ${height || "(unset)"}; expected max(100%, 1024px) so a short window keeps the art's height and scrolls`);
  } else if (Number(floor[1]) !== CANVAS.height) {
    fail(`rule 1: .wb-board floor is ${floor[1]}px, expected the reference height ${CANVAS.height}px`);
  }
}

// Rule 2 + 3: slots and rail anchors are relative, and resolve to the reference geometry.
for (const [selector, expected] of Object.entries(SLOTS)) {
  const rule = bySelector.get(selector);
  if (!rule) {
    fail(`rule 2: ${selector} has no rule in the 1536 layer`);
    continue;
  }
  const axes = [
    ["left", CANVAS.width, expected.left],
    ["width", CANVAS.width, expected.width],
    ["top", CANVAS.height, expected.top],
    ["height", CANVAS.height, expected.height],
  ];
  for (const [property, base, want] of axes) {
    const declared = value(rule.declarations, property);
    // a zero offset cannot drift with the window, so a length of 0 is fine either way
    if (declared !== undefined && /^(0|0px|0%)$/.test(declared.trim())) continue;
    const asPercent = percent(declared);
    if (asPercent === null) {
      const asLength = length(declared);
      if (asLength !== null && Math.abs(asLength - want) < 0.6) {
        fail(`rule 2: ${selector} ${property} is ${asLength}px; at the reference size that is right, but it will not grow with the window`);
      } else {
        fail(`rule 2: ${selector} ${property} is ${declared ?? "(unset)"}, expected a percentage of the canvas`);
      }
      continue;
    }
    const resolved = (asPercent / 100) * base;
    if (Math.abs(resolved - want) > 0.6) {
      fail(`rule 3: ${selector} ${property} resolves to ${resolved.toFixed(2)}px at the reference size, expected ${want}px`);
    }
  }
  const comment = rule.comment;
  const stated = /left:\s*(-?[\d.]+),\s*top:\s*(-?[\d.]+),\s*w:\s*([\d.]+),\s*h:\s*([\d.]+)/.exec(comment);
  if (stated) {
    const [, left, top, width, height] = stated.map(Number);
    if (left !== expected.left || top !== expected.top || width !== expected.width || height !== expected.height) {
      fail(`rule 3: the comment on ${selector} says left:${left} top:${top} w:${width} h:${height}, but the reference geometry is left:${expected.left} top:${expected.top} w:${expected.width} h:${expected.height}`);
    }
  }
}

for (const [selector, expected] of Object.entries(RAIL_ANCHORS)) {
  const rule = bySelector.get(selector);
  if (!rule) {
    fail(`rule 2: ${selector} has no rule in the 1536 layer`);
    continue;
  }
  const top = percent(value(rule.declarations, "top"));
  if (top === null) {
    fail(`rule 2: ${selector} top is ${value(rule.declarations, "top") ?? "(unset)"}, expected a percentage`);
  } else {
    const resolved = (top / 100) * expected.base;
    if (Math.abs(resolved - expected.top) > 0.6) {
      fail(`rule 3: ${selector} top resolves to ${resolved.toFixed(2)}px, expected ${expected.top}px (base ${expected.base}px)`);
    }
  }
  if (expected.height !== undefined) {
    const height = percent(value(rule.declarations, "height"));
    if (height === null) {
      fail(`rule 2: ${selector} height is ${value(rule.declarations, "height") ?? "(unset)"}, expected a percentage`);
    } else {
      const resolved = (height / 100) * expected.base;
      if (Math.abs(resolved - expected.height) > 0.6) {
        fail(`rule 3: ${selector} height resolves to ${resolved.toFixed(2)}px, expected ${expected.height}px`);
      }
    }
  }
}

// Rule 4: no undocumented positional pixels.
const POSITIONAL = new Set(["left", "top", "right", "bottom", "width", "height", "min-width", "max-width", "min-height", "max-height"]);
let positionalChecked = 0;
for (const rule of rules) {
  for (const [property, declaredValue] of Object.entries(rule.declarations)) {
    if (!POSITIONAL.has(property)) continue;
    if (!/[\d.]px/.test(declaredValue)) continue;
    positionalChecked++;
    const entry = `${property}: ${declaredValue}`;
    if (/max\(\s*100%\s*,/.test(declaredValue)) continue;
    if (!ALLOWED_PX.has(entry)) {
      fail(`rule 4: ${rule.selectors.join(", ")} sets ${entry}; positional pixels in the fluid layer must be allow-listed in this gate with a reason`);
    }
  }
}
if (positionalChecked === 0) {
  fail("rule 4: no pixel values were inspected; the gate cannot run");
}

// A gradient stop that keeps a px length is the same class of bug and is invisible to rule 4.
// The value is inspected on its own, never together with the rest of the rule: a neighbouring
// `padding: 0 14px` must not be read as a gradient stop.
let gradientPx = 0;
for (const rule of rules) {
  for (const [property, declaredValue] of Object.entries(rule.declarations)) {
    if (!/[a-z-]+gradient\(/.test(declaredValue)) continue;
    if (!/[\d.]+px/.test(declaredValue)) continue;
    if (/#0a152e 248px/.test(declaredValue) && /#071025 248px/.test(declaredValue)) continue;
    gradientPx++;
    fail(`rule 4: ${rule.selectors.join(", ")} ${property} keeps a pixel stop: ${declaredValue.slice(0, 90)}`);
  }
}
if (gradientPx === 0 && !rules.some((rule) => Object.values(rule.declarations).some((declaredValue) => /[a-z-]+gradient\(/.test(declaredValue)))) {
  fail("rule 4: no gradient declarations were inspected; the gate cannot run");
}

// ---------------------------------------------------------------------------

if (failures.length > 0) {
  console.error(`check-responsive: ${failures.length} problem(s) in the 1536 layer`);
  for (const failure of failures) console.error(`  - ${failure}`);
  process.exit(1);
}

console.log(
  `check-responsive: ${notes[0]}; ${Object.keys(SLOTS).length} slots and ${Object.keys(RAIL_ANCHORS).length} rail anchors resolve to the reference geometry, ${positionalChecked} pixel values all allow-listed`,
);
