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
 *   7. every destination sits in one of the console's two namespaces
 *   8. every retired address redirects to a page that exists, and no route still
 *      answers at a retired address
 *
 * Since the two-namespace rewrite the paths themselves live in `src/lib/paths.ts`
 * and every table references them (`opsPaths.nodes`, `mePaths.keys`, `ACCOUNT`).
 * This script resolves those references against that file rather than matching
 * string literals, so a rail entry that names a path the console does not have is
 * a failure here instead of a dead link at runtime.
 *
 * It prints the resulting tree, so an IA review reads the real rail instead of a
 * description of it.
 */

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const root = new URL("../", import.meta.url);
const read = (path) => readFileSync(fileURLToPath(new URL(path, root)), "utf8");

const PATHS_SOURCE = "src/lib/paths.ts";
const NAV_SOURCES = ["src/lib/navigation.ts", "src/lib/navigation-admin.ts"];
/** The route table with absolute paths: the namespaces, the auth doors, the catch-alls. */
const ROUTE_SOURCES = ["src/app/routes.tsx"];
/** The operator routes, relative to the namespace mount (`/ops/*`). */
const OPS_ROUTE_SOURCE = "src/app/admin-routes.tsx";
const DICTIONARY = "src/i18n/translations.ts";
const MAX_GROUP = 6;
const MAX_GROUPS = 6;

const failures = [];

/* ---- the single source of truth ----------------------------------------- */

/*
 * `src/lib/paths.ts` is read, not imported: it is TypeScript, and this script is
 * a plain node program. Only the shapes it actually declares are resolved — the
 * namespace constants, the two path records and the redirect table — so anything
 * this parser cannot read is reported instead of silently skipped.
 */
const pathsText = read(PATHS_SOURCE);
const constants = new Map();
for (const match of pathsText.matchAll(/export const (OPS_ROOT|ME_ROOT|ACCOUNT)\s*=\s*"([^"]*)"/g)) {
  constants.set(match[1], match[2]);
}
if (constants.size !== 3) {
  failures.push(`${PATHS_SOURCE}: expected OPS_ROOT, ME_ROOT and ACCOUNT, found ${constants.size}`);
}

const records = new Map();
for (const group of ["opsPaths", "mePaths", "legacyRedirects"]) {
  const body = new RegExp(`export const ${group}[^=]*=\\s*\\{([\\s\\S]*?)\\n\\};`).exec(pathsText)?.[1];
  if (body === undefined) {
    failures.push(`${PATHS_SOURCE}: cannot read the ${group} record`);
    continue;
  }
  const entries = new Map();
  for (const line of body.split("\n")) {
    // `nodes: `${OPS_ROOT}/nodes`,` or `"/dashboard": opsPaths.dashboard,`
    const row = /^\s*(?:"([^"]+)"|(\w+)):\s*(.+?),?\s*$/.exec(line);
    if (!row) continue;
    entries.set(row[1] ?? row[2], row[3]);
  }
  if (entries.size === 0) failures.push(`${PATHS_SOURCE}: the ${group} record declares nothing`);
  records.set(group, entries);
}

/** Resolves one path expression: a literal, a template, a constant or a record entry. */
const resolve = (expr) => {
  const source = String(expr).trim().replace(/,$/, "");
  const literal = /^"([^"]*)"$/.exec(source) ?? /^'([^']*)'$/.exec(source);
  if (literal) return literal[1];
  const template = /^`([^`]*)`$/.exec(source);
  if (template) {
    let text = template[1];
    for (const [name, value] of constants) text = text.split("${" + name + "}").join(value);
    return text.includes("${") ? undefined : text;
  }
  if (constants.has(source)) return constants.get(source);
  const member = /^(\w+)\.(\w+)$/.exec(source);
  if (member && records.has(member[1])) return resolve(records.get(member[1]).get(member[2]));
  return undefined;
};

/** Absolute operator path for a route declared relative to the `/ops/*` mount. */
const joinOps = (relative) => {
  const opsRoot = constants.get("OPS_ROOT");
  if (!relative) return opsRoot;
  return `${opsRoot}/${String(relative).replace(/^\//, "")}`;
};

/* ---- the rail ----------------------------------------------------------- */

const items = [];
for (const file of NAV_SOURCES) {
  const text = read(file);
  const pattern = /\{\s*label:\s*"([^"]+)",\s*path:\s*([^,]+),\s*section:\s*"([^"]*)"/g;
  let match;
  while ((match = pattern.exec(text))) {
    const path = resolve(match[2]);
    if (!path) {
      failures.push(`${file}: the destination "${match[1]}" names ${match[2].trim()}, which ${PATHS_SOURCE} does not declare`);
      continue;
    }
    items.push({ label: match[1], path, section: match[3], file });
  }
}

/* ---- the routes --------------------------------------------------------- */

const routes = new Set();
const componentOfPath = new Map();
const fileOfComponent = new Map();

for (const file of ROUTE_SOURCES) {
  const text = read(file);

  // `path="..."`, `path={expr}` and `path={`template`}` — all three spellings.
  const declarations = /path=(?:"([^"]*)"|\{(?:`([^`]*)`|([A-Za-z_$][\w$.]*))\})/g;
  let match;
  while ((match = declarations.exec(text))) {
    const expression = match[1] !== undefined ? JSON.stringify(match[1]) : match[2] !== undefined ? `\`${match[2]}\`` : match[3];
    const path = resolve(expression);
    if (!path) {
      failures.push(`${file}: the route path ${expression} does not resolve against ${PATHS_SOURCE}`);
      continue;
    }
    routes.add(path);
  }

  const imports = /const\s+(\w+)\s*=\s*lazy\(\(\)\s*=>\s*import\("([^"]+)"\)/g;
  while ((match = imports.exec(text))) {
    const parts = `${file.slice(0, file.lastIndexOf("/") + 1)}${match[2]}`.split("/");
    const out = [];
    for (const part of parts) {
      if (part === "." || part === "") continue;
      if (part === "..") out.pop();
      else out.push(part);
    }
    fileOfComponent.set(match[1], fileURLToPath(new URL(`${out.join("/")}.tsx`, root)));
  }

  const jsxRoutes = /path=\{(?:`([^`]*)`|([A-Za-z_$][\w$.]*))\}\s+element=\{<Page><(\w+)\s*\/><\/Page>\}/g;
  while ((match = jsxRoutes.exec(text))) {
    const path = resolve(match[1] !== undefined ? `\`${match[1]}\`` : match[2]);
    if (path) componentOfPath.set(path, match[3]);
  }
}

{
  const text = read(OPS_ROUTE_SOURCE);
  let match;

  const imports = /const\s+(\w+)\s*=\s*lazy\(\(\)\s*=>\s*import\("([^"]+)"\)/g;
  while ((match = imports.exec(text))) {
    const parts = `${OPS_ROUTE_SOURCE.slice(0, OPS_ROUTE_SOURCE.lastIndexOf("/") + 1)}${match[2]}`.split("/");
    const out = [];
    for (const part of parts) {
      if (part === "." || part === "") continue;
      if (part === "..") out.pop();
      else out.push(part);
    }
    fileOfComponent.set(match[1], fileURLToPath(new URL(`${out.join("/")}.tsx`, root)));
  }

  const declarations = /path:\s*(?:"([^"]*)"|`([^`]*)`|([A-Za-z_$][\w$.]*))/g;
  while ((match = declarations.exec(text))) {
    // A relative route path: the quoted form, a template, or an expression.
    const expression = match[1] !== undefined ? JSON.stringify(match[1]) : match[2] !== undefined ? `\`${match[2]}\`` : match[3];
    const relative = resolve(expression);
    if (relative === undefined) {
      failures.push(`${OPS_ROUTE_SOURCE}: the route path ${expression} does not resolve against ${PATHS_SOURCE}`);
      continue;
    }
    routes.add(joinOps(relative));
  }

  const objectRoutes = /path:\s*"([^"]*)"\s*,\s*element:\s*<Page><(\w+)\s*\/><\/Page>/g;
  while ((match = objectRoutes.exec(text))) componentOfPath.set(joinOps(match[1]), match[2]);
}

/* ---- the retired addresses ---------------------------------------------- */

const legacy = records.get("legacyRedirects") ?? new Map();
const OPS_ROOT = constants.get("OPS_ROOT");
const ME_ROOT = constants.get("ME_ROOT");
const ACCOUNT = constants.get("ACCOUNT");
const inNamespace = (path) =>
  path === ACCOUNT || (OPS_ROOT && (path === OPS_ROOT || path.startsWith(`${OPS_ROOT}/`))) ||
  (ME_ROOT && (path === ME_ROOT || path.startsWith(`${ME_ROOT}/`)));

/** Routes that deliberately have no rail entry: doors, redirects, wildcards. */
const UNLISTED = new Set(["/", "/login", "/register", "*", `${OPS_ROOT}/*`, `${OPS_ROOT}/quality`]);

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
  if (route.includes(":") || route.includes("*") || UNLISTED.has(route)) continue;
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

// 7. One of two namespaces, or the account page both sessions share. This is what
//    the shell reads to decide which rail and which instance probes it runs, so a
//    destination outside them would be a page the shell cannot frame.
for (const item of items) {
  if (!inNamespace(item.path)) {
    failures.push(`rail entry "${item.label}" points at ${item.path}, outside ${OPS_ROOT}/*, ${ME_ROOT}/* and ${ACCOUNT}`);
  }
}

// 8. Retired addresses: every one redirects to a real page, and no route still
//    answers at a retired address (which would make the redirect dead code).
for (const [from, target] of legacy) {
  const to = resolve(target);
  if (!to) {
    failures.push(`redirect ${from} names ${target}, which ${PATHS_SOURCE} does not declare`);
    continue;
  }
  if (!inNamespace(to)) {
    failures.push(`redirect ${from} points at ${to}, outside ${OPS_ROOT}/*, ${ME_ROOT}/* and ${ACCOUNT}`);
  }
  if (!routes.has(to)) {
    failures.push(`redirect ${from} points at ${to}, which no route serves`);
  }
  if (routes.has(from)) {
    failures.push(`${from} is still declared as a route, so its redirect can never run`);
  }
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

console.log(
  `check-navigation: ${items.length} destinations in ${order.length} groups and ${legacy.size} retired addresses, label = page title = dictionary key`,
);
for (const section of order) {
  const names = items.filter((item) => item.section === section).map((item) => item.label);
  console.log(`  ${section}: ${names.join(" · ")}`);
}
const loose = items.filter((item) => !item.section).map((item) => item.label);
if (loose.length > 0) console.log(`  (ungrouped): ${loose.join(" · ")}`);
