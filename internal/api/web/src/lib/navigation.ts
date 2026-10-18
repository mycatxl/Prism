import { isAdminRole } from "./roles";

/**
 * The console's page list, in the order the rail shows it. Labels are dictionary
 * keys; `section` groups the rail and disappears in the horizontal (<1100px) bar.
 *
 * The management half of this table lives in navigation-admin.ts and is imported
 * dynamically, for an admin session only: a tenant's browser never downloads
 * those paths. The server refuses every request behind them whatever the client
 * does (internal/api/rolegate.go).
 *
 * `tenantOnly` marks the self-service surfaces, which an operator session has no
 * data for (an admin holds no subscription of their own).
 */
export type NavigationItem = {
  label: string;
  path: string;
  section: string;
  adminOnly?: boolean;
  tenantOnly?: boolean;
};

/** The self-service destinations: the only ones a tenant session is shown. */
export const tenantItems: NavigationItem[] = [
  { label: "概览", path: "/me", section: "使用", tenantOnly: true },
  { label: "我的密钥", path: "/me/keys", section: "使用", tenantOnly: true },
  { label: "接入指南", path: "/me/guide", section: "使用", tenantOnly: true },
  { label: "购买套餐", path: "/me/plans", section: "费用", tenantOnly: true },
  { label: "我的租约", path: "/me/leases", section: "费用", tenantOnly: true },
  { label: "链上充值", path: "/me/deposit", section: "费用", tenantOnly: true },
  { label: "账户安全", path: "/account-security", section: "账号" },
];

/**
 * The operator rail: the management table, then the shared account surface.
 *
 * An operator holds no subscription of their own, so the tenant self-service
 * entries are left out — they would only render an empty page.
 */
export function adminNavigation(adminItems: NavigationItem[]): NavigationItem[] {
  const accountSecurity = tenantItems.filter((item) => !item.tenantOnly);
  return [...adminItems, ...accountSecurity];
}

/** What the rail shows before the management table has loaded, and all a tenant gets. */
export function navigationFor(role: string | undefined, adminItems?: NavigationItem[]): NavigationItem[] {
  if (!isAdminRole(role)) return tenantItems;
  return adminItems?.length ? adminNavigation(adminItems) : [];
}
