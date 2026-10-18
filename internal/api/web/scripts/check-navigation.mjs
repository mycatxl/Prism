#!/usr/bin/env node
/*
 * The rail, the router and the page headings are three hand-written tables. This
 * gate keeps them one table:
 *
 *   1. no destination twice
 *   2. every rail entry is served by a route
 *   3. every page route is reachable from the rail (`:id` detail pages excepted)
 *   4. every page's own <h1> states the rail's label, word for word
 *   5. every label and group has a dictionary entry, so the English rail is not
 *      half Chinese
 *   6. groups stay scannable (max six each, admin and tenant judged separately)
 *
 * It prints the resulting tree, so an IA review reads the real rail instead of a
 * description of it.
 */

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const root = new URL("../", import.meta.url);
const read = (path) => readFileSync(fileURLToPath(new URL(path, root)), "utf8");
const fileOfImport = (base, spec) => {
  const parts = `${base.slice(0, base.lastIndexOf("/") + 1)}${spec}`.split("/");
  const out = [];
  for (const part of parts) {
    if (part === "." || part === "") continue;
    if (part === "..") out.pop();
    else out.push(part);
  }
  return new URL(`${out.join("/")}.tsx`, root);
};

const NAV_SOURCES = ["src/lib/navigation.ts", "src/lib/navigation-admin.ts"];
const ROUTE_SOURCES = ["src/app/routes.tsx", "src/app/admin-routes.tsx"];
const DICTIONARY = "src/i18n/translations.ts";
/** Routes that deliberately have no rail entry: doors, redirects, wildcards. */
const UNLISTED = new Set(["/", "/quality", "/login", "/register", "*"]);
const MAX_GROUP = 6;
const MAX_GROUPS = 6;

const failures = [];
const items = [];

for (const file of NAV_SOURCES) {
  const text = read(file);
  const pattern = /\{\s*label:\s*"([^"]+)",\s*path:\s*"([^"]+)",\s*section:\s*"([^"]*)"/g;
  let match;
  while ((match = pattern.exec(text))) {
    items.push({ label: match[1], path: match[2], section: match[3], file });
  }
}

const routes = new Set();
const componentOfPath = new Map();
const fileOfComponent = new Map();

for (const file of ROUTE_SOURCES) {
  const text = read(file);
  let match;

  const declarations = /path(?::\s*|=\s*)"([^"]+)"/g;
  while ((match = declarations.exec(text))) routes.add(match[1]);

  const imports = /const\s+(\w+)\s*=\s*lazy\(\(\)\s*=>\s*import\("([^"]+)"\)/g;
  while ((match = imports.exec(text))) {
    fileOfComponent.set(match[1], fileURLToPath(fileOfImport(file, match[2])));
  }

  const objectRoutes = /path:\s*"([^"]+)"\s*,\s*element:\s*<Page><(\w+)\s*\/><\/Page>/g;
  while ((match = objectRoutes.exec(text))) componentOfPath.set(match[1], match[2]);

  const jsxRoutes = /path="([^"]+)"\s+element=\{<Page><(\w+)\s*\/><\/Page>\}/g;
  while ((match = jsxRoutes.exec(text))) componentOfPath.set(match[1], match[2]);
}

// 1. No destination twice.
const seen = new Map();
for (const item of items) {
  if (seen.has(item.path)) {
    failures.push(`duplicate destination ${item.path} (${seen.get(item.path)} and ${item.label})`);
  }
  seen.set(item.path, item.label);
}

// 2. Every rail entry is a real route.
for (const item of items) {
  if (!routes.has(item.path)) {
    failures.push(`rail entry "${item.label}" points at ${item.path}, which no route serves`);
  }
}

// 3. Every page route is reachable from the rail. Detail pages are reached from
//    a row, not from the rail.
for (const route of routes) {
  if (route.includes(":") || UNLISTED.has(route)) continue;
  if (!seen.has(route)) {
    failures.push(`route ${route} has no rail entry, so nothing on screen leads to it`);
  }
}

// 4. The page states its own name, and it is the rail's name.
const titles = new Map();
for (const item of items) {
  const component = componentOfPath.get(item.path);
  const file = component ? fileOfComponent.get(component) : undefined;
  if (!file) {
    failures.push(`${item.path}: cannot find the page component, so its heading is unverified`);
    continue;
  }
  let source;
  try {
    source = readFileSync(file, "utf8");
  } catch {
    failures.push(`${item.path}: cannot read ${component}'s source, so its heading is unverified`);
    continue;
  }
  const title = /<PageHeader\b[\s\S]{0,240}?title=\{[\s\S]{0,200}?t\("([^"]+)"\)/.exec(source);
  if (!title) {
    failures.push(`${item.path}: the page has no readable heading (PageHeader title={t("...")})`);
    continue;
  }
  titles.set(item.path, title[1]);
  if (title[1] !== item.label) {
    failures.push(`rail says "${item.label}" but ${item.path} titles itself "${title[1]}"`);
  }
}

// 5. Every label and group is translated.
const dictionary = read(DICTIONARY);
for (const item of items) {
  for (const key of [item.label, item.section].filter(Boolean)) {
    if (!dictionary.includes(`"${key}":`)) failures.push(`no dictionary entry for "${key}"`);
  }
}

// 6. Groups stay scannable. The two tables are judged separately.
for (const file of NAV_SOURCES) {
  const groups = new Map();
  for (const item of items.filter((entry) => entry.file === file)) {
    if (!item.section) continue;
    groups.set(item.section, (groups.get(item.section) ?? 0) + 1);
  }
  for (const [name, size] of groups) {
    if (size > MAX_GROUP) failures.push(`${file}: group "${name}" holds ${size} destinations (max ${MAX_GROUP})`);
  }
  if (groups.size === 0) failures.push(`${file}: no groups at all`);
  if (groups.size > MAX_GROUPS) failures.push(`${file}: ${groups.size} groups (max ${MAX_GROUPS})`);
}

if (failures.length > 0) {
  console.error(`check-navigation: ${failures.length} problem(s)`);
  for (const failure of failures) console.error(`  - ${failure}`);
  process.exit(1);
}

const order = [];
for (const item of items) {
  if (item.section && !order.includes(item.section)) order.push(item.section);
}

console.log(`check-navigation: ${items.length} destinations in ${order.length} groups, label = page title = dictionary key`);
for (const section of order) {
  const names = items.filter((item) => item.section === section).map((item) => item.label);
  console.log(`  ${section}: ${names.join(" · ")}`);
}
const loose = items.filter((item) => !item.section).map((item) => item.label);
if (loose.length > 0) console.log(`  (ungrouped): ${loose.join(" · ")}`);
