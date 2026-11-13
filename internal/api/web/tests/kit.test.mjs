import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";

/**
 * The kit's own invariants, checked in the source.
 *
 * These are not style rules — each one is a defect this codebase actually shipped,
 * and each one type-checks and lints clean, so nothing else here would catch it.
 * `check-kit.mjs` covers where components may be used; this file covers what they
 * are allowed to render.
 */

const read = (relative) =>
  readFileSync(fileURLToPath(new URL(relative, import.meta.url)), "utf8");

/*
 * Every `.tsx` under `src/`, so a rule can be stated over the whole surface
 * rather than over a hand-listed set of files that quietly goes stale.
 */
function tsxSources() {
  const srcDir = fileURLToPath(new URL("../src", import.meta.url));
  const out = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) walk(path);
      else if (entry.name.endsWith(".tsx")) out.push(path);
    }
  };
  walk(srcDir);
  return out;
}

test("Button renders exactly one child on the asChild path", () => {
  /*
   * The `asChild` path merges the button's props onto a single child element and
   * throws when handed more than one — and it counts `null` as a child. A spinner
   * rendered as a sibling of `children` therefore crashed every `asChild` button
   * in the panel at runtime while passing `check:types` and `eslint`. The rule:
   * the loading decoration must be composed *into* the content, never beside it.
   */
  const source = read("../src/components/ui/Button.tsx");
  assert.match(source, /const content = /, "the content must be a single value");
  assert.doesNotMatch(
    source,
    /\{\s*loading \?[\s\S]*?\}\s*\n\s*\{children\}/,
    "children must not be rendered as a second sibling of the loading spinner",
  );
  assert.match(
    source,
    /loading\?: never/,
    "asChild must forbid loading, since Slot cannot carry the extra node",
  );
});

test("the kit declares the control height it uses", () => {
  /*
   * `--control-h` is the single decision for chrome height. A kit component that
   * hard-codes a step instead is the per-page decision the rule exists to
   * prevent, and it is invisible to every other gate.
   *
   * Select is a Radix primitive sized from the same token in design.css.
   */
  for (const file of ["Button", "Input"]) {
    const source = read(`../src/components/ui/${file}.tsx`);
    assert.match(source, /var\(--control-h/, `${file} must take its height from the token`);
    assert.doesNotMatch(source, /\bh-(?:6\.5|7|8|9|10)\b/, `${file} must not hard-code a height`);
  }
});

test("the control-height rule is not scoped to the kit", () => {
  /*
   * The first version of rule 2 in `check-kit.mjs` only inspected
   * `src/components/ui/`, on the reasoning that the kit is where a control's
   * height is decided. That scoping is how a page kept a 32px `selectClass` and
   * thirteen 28px `h-7` overrides — the exact class of defect the rule was added
   * to catch — while the gate reported clean. A page that sets a control's height
   * has made the same per-page decision, so the rule must see every file.
   */
  const gate = read("../scripts/check-kit.mjs");
  assert.doesNotMatch(
    gate,
    /isUiKit\(path\)\)\s*\{\s*\n\s*if \(CONTROL_HEIGHT_RE/,
    "the control-height rule must not be guarded by isUiKit",
  );
  assert.match(
    gate,
    /if \(CONTROL_HEIGHT_RE\.test\(line\)/,
    "the control-height rule must run unconditionally",
  );
});

test("no control is rendered without a way to operate it", () => {
  /*
   * `ExitRecordsPanel` shipped a `<Select>` whose `onChange` was an empty
   * function and whose only option was the value already selected. It looked
   * like a page-size selector, could not change anything, and no gate could see
   * it: it is a valid control with a valid handler. A control that cannot be
   * operated is worse than a missing one, because the reader tries it.
   */
  for (const file of tsxSources()) {
    const source = read(file);
    assert.doesNotMatch(
      source,
      /onChange=\{\(\) => \{\}\}/,
      `${file}: a control with an empty onChange is not operable`,
    );
  }
});

test("the pill shape is reserved for Badge", () => {
  /*
   * Root the design system: the fully round shape carries "this is a state", so a
   * second one spends that signal. Checked here as well as in `check-kit.mjs`
   * because this one fails with a message that says *why*, and it runs in the same
   * suite as the rest of the kit's invariants.
   *
   * Stated over every component so the rule holds where shapes are written.
   */
  const offenders = tsxSources()
    .filter((path) => !path.endsWith("/ui/Badge.tsx"))
    .filter((path) => readFileSync(path, "utf8").includes("rounded-full"));

  assert.deepEqual(offenders, [], `only Badge may round fully, found: ${offenders.join(", ")}`);
});

test("drawers use Radix Dialog portals with an overlay and content", () => {
  for (const relative of [
    "../src/components/control-plane/Primitives.tsx",
    "../src/components/ui/Sheet.tsx",
  ]) {
    const source = read(relative);
    assert.match(source, /@radix-ui\/react-dialog/, `${relative}: must import Radix Dialog`);
    assert.match(source, /<Dialog\.Portal>/, `${relative}: overlay and content must render in a portal`);
    assert.match(source, /<Dialog\.Overlay\b/, `${relative}: dialog must render a backdrop`);
    assert.match(source, /<Dialog\.Content\b/, `${relative}: dialog must render focus-managed content`);
  }
});

test("Sheet restores its opener around the Radix focus lifecycle and becomes a full-screen mobile work surface", () => {
  const source = read("../src/components/ui/Sheet.tsx");
  const styles = read("../src/styles/design.css");

  assert.match(source, /useRef<HTMLElement \| null>\(null\)/, "Sheet must retain the opener element");
  assert.match(source, /onOpenAutoFocus=/, "Sheet must capture focus before Radix moves it");
  assert.match(source, /onCloseAutoFocus=/, "Sheet must own controlled-dialog focus restoration");
  assert.match(source, /target\.focus\(\{ preventScroll: true \}\)/, "focus restoration must not jump the page");
  assert.match(source, /<Dialog\.Root open=\{open\} onOpenChange=\{onOpenChange\}>/, "Sheet must give Radix control of the open state");
  assert.match(source, /<Dialog\.Close\b/, "Sheet must expose a Radix close control");
  assert.doesNotMatch(source, /truncate text-base font-semibold/, "sheet titles must be allowed to wrap");
  assert.match(
    styles,
    /@media \(max-width: 760px\)[\s\S]*?\.workspace-sheet[\s\S]*?width: 100%;[\s\S]*?border-radius: 0;/,
    "small screens must use a full-screen sheet",
  );
});
