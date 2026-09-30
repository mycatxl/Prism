#!/usr/bin/env node
/*
 * The component-kit gate for the console.
 *
 * `internal/api/web/DESIGN.md:44-45` says `src/components/ui/` "is the only
 * permitted source of components. A page composes these; it does not invent a
 * control." That rule is checkable in the source, and until this file existed
 * nothing checked it: an audit found 43 native controls across 14 feature files,
 * 5 hard-coded control heights that bypass the `--control-h*` tokens, and 5
 * `rounded-full` uses outside `Badge` — the one shape the design system reserves
 * for status.
 *
 * Why a separate gate rather than a rule in an existing one: the anti-pattern
 * detector does not scan `.tsx` at all (root `DESIGN.md:235-236`), and the
 * browser regression needs a browser, which CI does not install. So every one of
 * those violations lived in the exact layer no gate could see. This script reads
 * the source directly, which is the only place the rule is decidable.
 *
 * Rules:
 *   1. Native controls are not allowed outside `src/components/ui/`, with an
 *      explicit allow-list for the cases where a native tag is the correct
 *      answer.
 *   2. Control heights must come from the `--control-h*` tokens, never from a
 *      hard-coded `h-*` step. Unlike rule 1 this has no scope guard: a page that
 *      sizes a control has made the same per-page decision the kit may not make.
 *   3. `rounded-full` belongs to `Badge` alone.
 *
 * Fails closed: a missing directory is an error, not a skip, because a gate that
 * silently checks nothing reads exactly like a gate that passes.
 *
 * Usage: node scripts/check-kit.mjs
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join, relative } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const srcDir = join(here, "..", "src");
const uiDir = join(srcDir, "components", "ui");

const failures = [];
let checkedFiles = 0;

/** Every .tsx file under a directory, recursively. */
function tsxFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) {
      out.push(...tsxFiles(path));
    } else if (entry.endsWith(".tsx")) {
      out.push(path);
    }
  }
  return out;
}

const isUiKit = (path) => path.startsWith(uiDir + "/") || path.startsWith(uiDir + "\\");

// ---------------------------------------------------------------------------
// Rule 1: native controls live only in the kit.
// ---------------------------------------------------------------------------

/*
 * Lines that legitimately contain a native tag outside the kit.
 *
 * Keep this list short and justified: every entry is a place where a native
 * element is the correct answer rather than a page inventing a control. A
 * `type="hidden"` input is not a control at all — it is how react-hook-form
 * carries a value the user never sees.
 */
const NATIVE_CONTROL_ALLOW = [
  {
    // `<input type="hidden">` is a form payload carrier, not a widget.
    file: "src/features/subscriptions/SubscriptionPage.tsx",
    pattern: /<input\s+type="hidden"/,
    reason: "type=hidden carries source_type; it is not a visible control",
  },
  {
    /*
     * The mobile nav's dismissal scrim: a full-bleed surface behind the rail
     * whose only job is to close the drawer when the rest of the screen is
     * tapped. It is not an action — it has no label to show, no focus ring and
     * no place in the tab order — so `Button` is the wrong component for it.
     * The pattern is the scrim's own class string, so a *different* native
     * button in this file would still fail the rule.
     */
    file: "src/components/AppShell.tsx",
    pattern: /className="flex-1 bg-ink\/30"/,
    reason: "a dismissal scrim, not a control",
  },
];

const NATIVE_CONTROL_RE = /<(select|input|button|textarea)\b/;

// ---------------------------------------------------------------------------
// Rule 2: control heights come from tokens.
// ---------------------------------------------------------------------------

/*
 * Hard-coded height steps that must not appear on a control. `h-0`, `h-20` (a
 * textarea's block size) and the token forms are not control heights, so they are
 * not listed: this rule is about the control step, not about every `h-*` utility
 * in the file.
 *
 * This rule applies to *every* file, not just the kit. The first version of it
 * was scoped to `src/components/ui/`, on the reasoning that the kit is where a
 * control's height is decided — and that scoping is exactly how a page kept a
 * 32px `selectClass` and thirteen 28px overrides without the gate saying a word.
 * A page that sets a control's height has made the same per-page decision the
 * rule forbids, so it is the same failure.
 *
 * 28px (`h-7`) is not exempt: it happens to equal `--control-h` today, which is
 * precisely why it must not be written down. A literal that agrees with the token
 * by coincidence stops agreeing the moment the token moves, and nothing reports
 * it. The token is the decision; the literal is a copy of it.
 */
const CONTROL_HEIGHT_RE = /\bh-(6\.5|7|8|9|10)\b/;
const CONTROL_HEIGHT_ALLOW = [
  {
    // A textarea's height is its line count, not the control step.
    file: "src/components/ui/Textarea.tsx",
    pattern: /h-20/,
    reason: "textarea block size, not a control step",
  },
  {
    /*
     * The command palette's query field. This is not a chrome control: it is the
     * input of a modal search surface, sized to the dialog's own row rather than
     * to a toolbar, and it sits in no form. Sizing it to `--control-h` would make
     * the palette's single most-used element shorter than the list it searches.
     * The pattern is the element's own class string, so another `h-10` in this
     * file still fails.
     */
    file: "src/components/QuickSearch.tsx",
    pattern: /className="h-10 min-w-0 flex-1 bg-transparent/,
    reason: "command palette query field, sized to the dialog row",
  },
];

// ---------------------------------------------------------------------------
// Rule 3: the pill shape means "state".
// ---------------------------------------------------------------------------

/*
 * Root `DESIGN.md:126-127`: Badge is the only fully round shape in the system,
 * so the shape itself carries "this is a state"; a second pill spends that
 * signal. This applies everywhere in `src/`, not just inside the kit: a pill
 * drawn by a page spends exactly the same signal as a pill drawn by the kit.
 */
const ROUNDED_FULL_RE = /\brounded-full\b/;
const ROUNDED_FULL_ALLOW = [
  {
    // The one shape the system reserves for state.
    file: "src/components/ui/Badge.tsx",
    pattern: /rounded-full/,
    reason: "Badge is the only fully round shape in the system",
  },
];

// ---------------------------------------------------------------------------

function allowed(file, ruleAllow, line) {
  const rel = relative(join(here, ".."), file).split("\\").join("/");
  return ruleAllow.some(
    (entry) => rel === entry.file && entry.pattern.test(line),
  );
}

function checkFile(path) {
  const rel = relative(join(here, ".."), path).split("\\").join("/");
  const lines = readFileSync(path, "utf8").split("\n");
  checkedFiles += 1;

  /*
   * The opening tag that starts at `start`, so a rule can look at the whole tag
   * rather than one line. A JSX opening tag routinely spans lines (attributes on
   * their own), and an allow-list entry that could only see the first line would
   * either miss the distinguishing attribute or have to match something too
   * generic to be a real guard.
   */
  const openTag = (start) => {
    let block = "";
    for (let i = start; i < Math.min(start + 12, lines.length); i++) {
      block += lines[i] + "\n";
      if (/\/?>/.test(lines[i])) break;
    }
    return block;
  };

  lines.forEach((line, index) => {
    const lineNo = index + 1;

    if (!isUiKit(path)) {
      if (NATIVE_CONTROL_RE.test(line) && !allowed(path, NATIVE_CONTROL_ALLOW, openTag(index))) {
        const tag = NATIVE_CONTROL_RE.exec(line)[1];
        failures.push(
          `${rel}:${lineNo} page composes a native <${tag}>; use the component from src/components/ui/`,
        );
      }
    }

    // Rule 2 has no scope guard: a page that sets a control's height has made
    // the same per-page decision the kit is forbidden from making.
    if (CONTROL_HEIGHT_RE.test(line) && !allowed(path, CONTROL_HEIGHT_ALLOW, line)) {
      failures.push(
        `${rel}:${lineNo} hard-codes a control height; use h-[var(--control-h)] (or -sm/-lg)`,
      );
    }
    if (ROUNDED_FULL_RE.test(line) && !allowed(path, ROUNDED_FULL_ALLOW, line)) {
      failures.push(
        `${rel}:${lineNo} uses rounded-full; the pill shape is reserved for Badge`,
      );
    }
  });
}

// ---------------------------------------------------------------------------

if (!statSync(srcDir).isDirectory() || !statSync(uiDir).isDirectory()) {
  console.error("check-kit: src/ or src/components/ui/ is missing; the gate cannot run");
  process.exit(1);
}

for (const file of tsxFiles(srcDir)) {
  checkFile(file);
}

if (checkedFiles === 0) {
  console.error("check-kit: no .tsx files were read; the gate cannot run");
  process.exit(1);
}

if (failures.length > 0) {
  console.error(`check-kit: ${failures.length} violation(s) across ${checkedFiles} files`);
  for (const failure of failures) {
    console.error(`  - ${failure}`);
  }
  process.exit(1);
}

console.log(`check-kit: ${checkedFiles} .tsx files pass the component-kit rules`);
