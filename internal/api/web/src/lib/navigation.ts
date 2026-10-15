import { isAdminRole } from "./roles";

/**
 * The console's page list, in the order the rail shows it. Labels are dictionary
 * keys; `section` groups the rail and disappears in the horizontal (<1100px) bar.
 *
 * `adminOnly` marks management surfaces: a tenant session never sees them,
 * because every request behind them is refused by the role gate on the server.
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

const items: NavigationItem[] = [
  { label: "总览", path: "/dashboard", section: "工作", adminOnly: true },
  { label: "节点", path: "/nodes", section: "工作", adminOnly: true },
  { label: "平台", path: "/platforms", section: "工作", adminOnly: true },
  { label: "订阅源", path: "/subscriptions", section: "工作", adminOnly: true },
  { label: "检测任务", path: "/jobs", section: "工作", adminOnly: true },
  { label: "接入点", path: "/endpoints", section: "接入", adminOnly: true },
  { label: "请求日志", path: "/request-logs", section: "接入", adminOnly: true },
  { label: "请求头规则", path: "/rules", section: "接入", adminOnly: true },
  { label: "GeoIP", path: "/resources", section: "系统", adminOnly: true },
  { label: "系统配置", path: "/system-config", section: "系统", adminOnly: true },
  { label: "审计日志", path: "/audit", section: "系统", adminOnly: true },
  { label: "用户管理", path: "/admin/users", section: "管理", adminOnly: true },
  { label: "套餐管理", path: "/admin/plans", section: "管理", adminOnly: true },
  { label: "邀请码管理", path: "/admin/invites", section: "管理", adminOnly: true },
  { label: "用量统计", path: "/admin/usage", section: "管理", adminOnly: true },
  { label: "系统设置", path: "/admin/settings", section: "管理", adminOnly: true },
  { label: "概览", path: "/me", section: "账号", tenantOnly: true },
  { label: "我的密钥", path: "/me/keys", section: "账号", tenantOnly: true },
  { label: "接入指南", path: "/me/guide", section: "账号", tenantOnly: true },
  { label: "账户安全", path: "/account-security", section: "账号" },
];

export function navigationFor(role: string | undefined): NavigationItem[] {
  const admin = isAdminRole(role);
  return items.filter((item) => (item.tenantOnly ? !admin : !item.adminOnly || admin));
}
