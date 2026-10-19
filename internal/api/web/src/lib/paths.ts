/**
 * The console's paths: the single source of truth.
 *
 * Everything that names an address reads it from here — the route tables
 * (`src/app/routes.tsx`, `src/app/admin-routes.tsx`), the rail tables
 * (`src/lib/navigation.ts`, `src/lib/navigation-admin.ts`), this shell's own
 * links, the command palette and the in-page links each feature writes. Moving a
 * page means editing one line in this file; the router, the rail and every link
 * that leads there move with it.
 *
 * Why it exists: the console used to hold three namespaces in one shell. The
 * operator's pages were split between `/admin/*` (six of them: plans, users,
 * invites, redeem codes, usage, settings) and the root (`/dashboard`, `/nodes`,
 * `/platforms`, `/subscriptions`, `/jobs`, `/endpoints`, `/rules`,
 * `/request-logs`, `/resources`, `/system-config`, `/audit`), while the tenant's
 * pages sat under `/me/*` and the personal account page at `/account-security`.
 * The same object could therefore be reached at two different kinds of address,
 * and every link, redirect and check had to remember which convention the page
 * it pointed at happened to follow. A page moved from `/admin/x` to `/x` was a
 * hand-written edit in the rail, the router and a dozen pages.
 *
 * The console now answers in exactly two namespaces plus the one page both
 * sessions share:
 *
 *   /ops/*     the operator surface: the instance's inventory and its levers
 *   /me/*      the tenant surface: one customer's own subscription, keys, usage
 *   /account   the personal account every session owns, whatever its role
 *
 * The namespaces are not cosmetic. The shell reads them (see AppShell.tsx) to
 * decide which rail a visitor gets and whether the instance probes run, and the
 * route table mounts the operator pages only under `OPS_ROOT`.
 */

/** The operator namespace: every instance-management page lives under it. */
export const OPS_ROOT = "/ops";

/** The tenant namespace: one customer's self-service pages. */
export const ME_ROOT = "/me";

/**
 * The personal account page. Neither namespace owns it: an operator and a tenant
 * both need to change their password and enrol an authenticator, so it is the one
 * address outside both trees.
 */
export const ACCOUNT = "/account";

/**
 * The operator destinations, keyed by what the page works on rather than by the
 * address — the rail (navigation-admin.ts), the route table (admin-routes.tsx)
 * and the pages themselves all name the key, never the string.
 *
 * `dashboard` is the namespace root on purpose: an operator lands on the
 * workbench, and `/ops` is that address.
 */
export const opsPaths = {
  dashboard: OPS_ROOT,
  nodes: `${OPS_ROOT}/nodes`,
  platforms: `${OPS_ROOT}/platforms`,
  subscriptions: `${OPS_ROOT}/subscriptions`,
  jobs: `${OPS_ROOT}/jobs`,
  plans: `${OPS_ROOT}/plans`,
  users: `${OPS_ROOT}/users`,
  invites: `${OPS_ROOT}/invites`,
  redeemCodes: `${OPS_ROOT}/redeem-codes`,
  usage: `${OPS_ROOT}/usage`,
  settings: `${OPS_ROOT}/settings`,
  endpoints: `${OPS_ROOT}/endpoints`,
  rules: `${OPS_ROOT}/rules`,
  requestLogs: `${OPS_ROOT}/request-logs`,
  resources: `${OPS_ROOT}/resources`,
  config: `${OPS_ROOT}/config`,
  audit: `${OPS_ROOT}/audit`,
};

/**
 * The tenant destinations. `overview` is the namespace root: a customer lands on
 * their own overview, and `/me` is that address.
 */
export const mePaths = {
  overview: ME_ROOT,
  keys: `${ME_ROOT}/keys`,
  guide: `${ME_ROOT}/guide`,
  plans: `${ME_ROOT}/plans`,
  leases: `${ME_ROOT}/leases`,
  deposit: `${ME_ROOT}/deposit`,
};

/**
 * Every address the console used to answer at, and where that page lives now.
 *
 * These are redirects, not routes: a bookmarked `/dashboard`, an old link in a
 * ticket, or a `next=` left in a sign-in URL still lands on the right page. The
 * keys are the three old conventions — the operator pages that were served from
 * the root, the operator pages that were served from `/admin/*`, and the personal
 * account page that was `/account-security`.
 *
 * Detail pages were moved the same way and are resolved by prefix: `/platforms/abc`
 * now lives at `/ops/platforms/abc` through its own entry, and `/admin/users/usr_1`
 * at `/ops/users/usr_1` through `/admin/users`.
 */
export const legacyRedirects: Record<string, string> = {
  // The operator pages that used to sit on the root.
  "/dashboard": opsPaths.dashboard,
  "/nodes": opsPaths.nodes,
  "/platforms": opsPaths.platforms,
  "/subscriptions": opsPaths.subscriptions,
  "/jobs": opsPaths.jobs,
  "/endpoints": opsPaths.endpoints,
  "/rules": opsPaths.rules,
  "/request-logs": opsPaths.requestLogs,
  "/resources": opsPaths.resources,
  "/system-config": opsPaths.config,
  "/audit": opsPaths.audit,
  // The operator pages that used to sit under /admin/*.
  "/admin/plans": opsPaths.plans,
  "/admin/users": opsPaths.users,
  "/admin/invites": opsPaths.invites,
  "/admin/redeem-codes": opsPaths.redeemCodes,
  "/admin/usage": opsPaths.usage,
  "/admin/settings": opsPaths.settings,
  // The account page both sessions share.
  "/account-security": ACCOUNT,
};

/**
 * Where an old address lives now, or `undefined` when the path was never moved.
 *
 * The longest matching key wins and the rest of the path is carried across, so
 * `/admin/users/usr_1` resolves as `/ops/users` + `/usr_1` and `/platforms` does
 * not shadow `/platforms/abc`. The search string is the caller's business: a
 * redirect keeps it, because `/nodes?subscription_id=…` is still that query.
 */
export function legacyTargetFor(pathname: string): string | undefined {
  let matched = "";
  for (const from of Object.keys(legacyRedirects)) {
    const covers = pathname === from || pathname.startsWith(`${from}/`);
    if (covers && from.length > matched.length) matched = from;
  }
  if (!matched) return undefined;
  return `${legacyRedirects[matched]}${pathname.slice(matched.length)}`;
}

/** True for a path inside a namespace (or its root): the shell reads its surface from this. */
export function isWithinNamespace(pathname: string, root: string): boolean {
  return pathname === root || pathname.startsWith(`${root}/`);
}

/**
 * True for a page that is its own address and nobody's parent: the two namespace
 * roots and the account page. A rail entry (and the router's own `NavLink`
 * matching) must treat these as exact, or `/ops` would light up on every page
 * underneath it and the rail would show two current destinations at once.
 */
export function isExactPath(path: string): boolean {
  return path === OPS_ROOT || path === ME_ROOT || path === ACCOUNT;
}

/**
 * True when a rail entry is the current destination.
 *
 * A namespace root is a page, not a parent: `/ops` is the workbench and `/ops/nodes`
 * is the node pool, so the root must not light up on every page beneath it. Detail
 * pages (`/ops/platforms/abc`) still light up their own entry.
 */
export function isCurrentPath(pathname: string, itemPath: string): boolean {
  if (pathname === itemPath) return true;
  if (isExactPath(itemPath)) return false;
  return pathname.startsWith(`${itemPath}/`);
}
