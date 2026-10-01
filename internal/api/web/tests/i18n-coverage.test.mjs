import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative, sep } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const web = fileURLToPath(new URL("..", import.meta.url));
const src = join(web, "src");
const i18n = join(src, "i18n");

/*
 * Every operator-visible string goes through `t("中文…")`, with the Chinese text as the
 * key: `buildZhTranslations()` maps each key to itself and the English dictionary maps it
 * to its translation. A literal no dictionary carries therefore renders **Chinese in
 * English mode** — a gap nobody notices, because the default locale shows the same text
 * either way. Thirty-eight of those had accumulated, concentrated in a file the console
 * refactor never touched (the platform criteria form).
 *
 * Scanned in source, not at runtime: a missing key is a property of the text, and this
 * way the failure names the file and line-free key instead of a rendered string.
 *
 * The scan has to survive three things an earlier version of it got wrong:
 *   - dictionary keys written without quotes (`视图: "View"`) — legal, because CJK
 *     characters are identifier characters;
 *   - a URL inside a string (`//user:pass@host`), which a naive `//`-strips pass eats,
 *     so `t("http://…")` looked unterminated and the rest of the file parsed as one key;
 *   - a function whose name ends in `t` (`default("http:…")`, `format(…)` patterns).
 */

const KEY_LINE = /^\s*(?:"((?:[^"\\]|\\.)+)"|'((?:[^'\\]|\\.)+)'|([^\s:][^:]*?))\s*:/;
const CALL = /(?<![A-Za-z0-9_$])t\(\s*"((?:[^"\\]|\\.)*)"/g;
const NOT_A_KEY = /^(?:export|const|import|return|if|type|interface)\b/;

/** Blanks out comments while leaving string literals — and their URLs — intact. */
function stripComments(text) {
  const chars = [...text];
  let quote = null;
  for (let i = 0; i < chars.length; ) {
    const c = chars[i];
    if (quote) {
      if (c === "\\") i += 2;
      else {
        if (c === quote) quote = null;
        i += 1;
      }
      continue;
    }
    if (c === '"' || c === "'" || c === "`") {
      quote = c;
      i += 1;
      continue;
    }
    if (c === "/" && chars[i + 1] === "/") {
      while (i < chars.length && chars[i] !== "\n") chars[i++] = " ";
      continue;
    }
    if (c === "/" && chars[i + 1] === "*") {
      const end = text.indexOf("*/", i + 2);
      const stop = end === -1 ? chars.length : end + 2;
      for (; i < stop; i += 1) if (chars[i] !== "\n") chars[i] = " ";
      continue;
    }
    i += 1;
  }
  return chars.join("");
}

function filesIn(dir, keep) {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...filesIn(path, keep));
    else if (keep(path)) out.push(path);
  }
  return out.sort();
}

function dictionaryKeys() {
  const keys = new Map();
  for (const path of filesIn(i18n, (p) => p.endsWith(".ts"))) {
    for (const line of stripComments(readFileSync(path, "utf8")).split("\n")) {
      const match = KEY_LINE.exec(line);
      if (!match) continue;
      const key = (match[1] ?? match[2] ?? match[3] ?? "").trim();
      if (key && !NOT_A_KEY.test(key)) keys.set(key, relative(web, path));
    }
  }
  return keys;
}

function callSites() {
  const used = new Map();
  const isSource = (p) => (p.endsWith(".tsx") || p.endsWith(".ts")) && !p.includes(`${sep}i18n${sep}`);
  for (const path of filesIn(src, isSource)) {
    for (const match of stripComments(readFileSync(path, "utf8")).matchAll(CALL)) {
      const key = match[1];
      if (key.includes("\n")) continue; // a capture that ran past its literal
      if (!used.has(key)) used.set(key, new Set());
      used.get(key).add(relative(web, path));
    }
  }
  return used;
}

test("the dictionary is parsed, not silently skipped", () => {
  const keys = dictionaryKeys();
  assert.ok(
    keys.size > 1000,
    `only ${keys.size} dictionary keys were parsed, so the parser broke rather than the dictionary being tiny`,
  );
  const used = callSites();
  assert.ok(
    used.size > 800,
    `only ${used.size} t() call sites were found, which means the scan stopped reading source`,
  );
});

test("every t() literal has a dictionary entry", () => {
  const keys = dictionaryKeys();
  const missing = new Map();
  for (const [key, where] of callSites()) {
    if (!keys.has(key)) missing.set(key, where);
  }
  const report = [...missing]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, where]) => `  "${key}"  <- ${[...where].sort().join(", ")}`)
    .join("\n");
  assert.equal(
    missing.size,
    0,
    `${missing.size} t() literal(s) have no dictionary entry, so they stay Chinese in English mode:\n${report}`,
  );
});
