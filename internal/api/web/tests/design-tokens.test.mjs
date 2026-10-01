/**
 * A colour alias a component reads *by name* has to survive the build.
 *
 * The design system is two layers: `--p-*` primitives declared twice in
 * `design.css` (`:root` for light, `[data-theme="dark"]` for dark), and `--color-*`
 * aliases that bind Tailwind utilities to them. Tailwind emits an alias only when it
 * can see a consumer, and it reads markup and CSS — never JavaScript. A component
 * that names its colour at runtime
 *
 *     style={{ backgroundColor: `var(--color-series-${index + 1})` }}
 *
 * is therefore invisible to it. The alias is dropped from the bundle, the `var()`
 * resolves to nothing, and the element paints transparent. Nothing else catches it:
 * the type checker sees a valid string, `check-contrast.mjs` measures the primitives
 * in the source stylesheet, and the browser reports no error. This one was found by
 * reading a rendered dot's computed background and getting `rgba(0, 0, 0, 0)` — the
 * region table's six dots and the donut's legend swatches were blank, and the donut
 * had shipped that way.
 *
 * So the alias layer is declared `@theme inline static`, which keeps every alias in
 * the output whether or not a class spends it. These tests hold that line by reading
 * the source, so the edit that reintroduces the bug fails here instead of on a
 * screenshot nobody takes.
 */
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const web = fileURLToPath(new URL("..", import.meta.url));
const src = join(web, "src");
const css = readFileSync(join(src, "styles", "design.css"), "utf8");

/** Every `.ts`/`.tsx` file under `src/`, so a new component is covered the day it lands. */
function sourceFiles(dir) {
  const found = [];
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) {
      found.push(...sourceFiles(path));
    } else if (/\.(ts|tsx)$/.test(entry)) {
      found.push(path);
    }
  }
  return found;
}

/**
 * Every `@theme` block: the options in its header, and the aliases it declares.
 * Brace-matched rather than regex-ended, because the bodies hold comments with
 * braces in them.
 */
function themeBlocks() {
  const blocks = [];
  // Anchored to the line start: the file's own header prose mentions `@theme inline`.
  for (const match of css.matchAll(/^@theme([^{]*)\{/gm)) {
    const open = match.index + match[0].length;
    let depth = 1;
    let index = open;
    while (index < css.length && depth > 0) {
      if (css[index] === "{") depth += 1;
      else if (css[index] === "}") depth -= 1;
      index += 1;
    }
    const body = css.slice(open, index - 1);
    const names = new Set([...body.matchAll(/(--color-[a-z0-9-]+)\s*:/g)].map((m) => m[1]));
    blocks.push({ options: match[1], names });
  }
  assert.ok(blocks.length > 0, "design.css declares no @theme block");
  return blocks;
}

/** Alias names a component reads by name at runtime: `var(--color-…)` in TS/TSX. */
function aliasesReadByName() {
  const found = new Map();
  for (const path of sourceFiles(src)) {
    const text = readFileSync(path, "utf8");
    for (const match of text.matchAll(/var\((--color-[a-z0-9-]+)\)/g)) {
      if (!found.has(match[1])) {
        found.set(match[1], relative(web, path));
      }
    }
  }
  return found;
}

/** Every alias the stylesheet declares, in any block. */
function declaredAliases() {
  const declared = new Set();
  for (const block of themeBlocks()) {
    for (const name of block.names) declared.add(name);
  }
  for (const match of css.matchAll(/(--color-[a-z0-9-]+)\s*:/g)) {
    declared.add(match[1]);
  }
  return declared;
}

test("the alias layer is not prunable, because JavaScript reads it by name", () => {
  /*
   * The rule is about the block, not about counting consumers: an alias this file
   * cannot see being spent is exactly the alias Tailwind drops. Every block that
   * declares a colour alias must therefore be `static`.
   */
  const colourBlocks = themeBlocks().filter((block) => block.names.size > 0);
  assert.ok(colourBlocks.length > 0, "design.css declares no colour aliases in a @theme block");

  const prunable = colourBlocks.filter((block) => !/\bstatic\b/.test(block.options));
  assert.deepEqual(
    prunable.map((block) => `@theme${block.options}`.trim()),
    [],
    "these @theme blocks declare colour aliases but can be pruned, so an alias that only a component " +
      "names at runtime is dropped from the bundle and its var() resolves to nothing (the element " +
      "paints transparent). Add `static` to the block's options.",
  );
});

test("every alias a component names at runtime is declared somewhere", () => {
  // The same failure with a different cause: a typo is also a `var()` that resolves
  // to nothing, so it is checked rather than left to the eye.
  const declared = declaredAliases();
  const undeclared = [...aliasesReadByName()].filter(([name]) => !declared.has(name));
  assert.deepEqual(
    undeclared,
    [],
    "aliases named at runtime but never declared:\n  " +
      undeclared.map(([name, file]) => `${name} (${file})`).join("\n  "),
  );
});

test("the six series aliases all exist, because a colour must mean one thing", () => {
  // Six is the design system's series length, and both the donut and the region
  // table cycle through it by position, so a gap would silently fold two colours
  // into one — a chart and its legend disagreeing about what a colour means.
  const declared = declaredAliases();
  for (let index = 1; index <= 6; index++) {
    assert.ok(declared.has(`--color-series-${index}`), `--color-series-${index} is not declared`);
  }
});
