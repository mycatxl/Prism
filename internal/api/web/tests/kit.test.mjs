import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
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

test("Button renders exactly one child on the asChild path", () => {
  /*
   * Radix's `Slot` merges props onto a single child element and throws
   * "Slot failed to slot onto its children" when handed more than one — and it
   * counts `null` as a child. A spinner rendered as a sibling of `children`
   * therefore crashed every `asChild` button in the panel at runtime while
   * passing `check:types` and `eslint`. The rule: the loading decoration must be
   * composed *into* the content, never placed beside it.
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
   * `--control-h` is the single decision for chrome height (DESIGN.md:167-168).
   * A kit component that hard-codes a step instead is the per-page decision the
   * rule exists to prevent, and it is invisible to every other gate.
   */
  for (const file of ["Button", "Input", "Select"]) {
    const source = read(`../src/components/ui/${file}.tsx`);
    assert.match(source, /var\(--control-h/, `${file} must take its height from the token`);
    assert.doesNotMatch(source, /\bh-(?:6\.5|7|8|9|10)\b/, `${file} must not hard-code a height`);
  }
});

test("the pill shape is reserved for Badge", () => {
  /*
   * Root DESIGN.md:126-127: the fully round shape carries "this is a state", so a
   * second one spends that signal. Checked here as well as in `check-kit.mjs`
   * because this one fails with a message that says *why*, and it runs in the same
   * suite as the rest of the kit's invariants.
   */
  const source = read("../src/components/ui/Switch.tsx");
  assert.doesNotMatch(source, /rounded-full/, "Switch must be rectangular, not a pill");
  assert.match(source, /rounded-control/, "Switch must use the control radius");
});
