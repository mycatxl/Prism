import { ACCOUNT, ME_ROOT, OPS_ROOT } from "./paths";

/**
 * Session role helpers.
 *
 * The console serves two audiences from one bundle, each in its own namespace:
 * operators (the `/ops/*` surfaces) and tenants (their own subscription, keys and
 * usage under `/me/*`), with `/account` shared by both. The server is the
 * control — `requireAdmin` in `internal/api/rolegate.go` refuses every management
 * path to a tenant, whatever the UI does — these helpers only keep the console
 * from offering navigation and metrics that would 403.
 */
export function isAdminRole(role: string | undefined): boolean {
  return role === "admin";
}

/** Where a session lands after login, and where a tenant is sent when they reach for a management page. */
export function homePathFor(role: string | undefined): string {
  return isAdminRole(role) ? OPS_ROOT : ME_ROOT;
}

/** True for the personal account page, which belongs to neither namespace. */
export function isAccountPath(pathname: string): boolean {
  return pathname === ACCOUNT;
}
