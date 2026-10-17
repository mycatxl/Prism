import type { NavigationItem } from "./navigation";

/**
 * The operator rail.
 *
 * Loaded dynamically, for an admin session only — see navigation.ts. Every entry
 * here names a management route, and the server refuses those to a tenant
 * whatever the client does (internal/api/rolegate.go).
 */
export const adminItems: NavigationItem[] = [
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
  { label: "兑换码管理", path: "/admin/redeem-codes", section: "管理", adminOnly: true },
  { label: "用量统计", path: "/admin/usage", section: "管理", adminOnly: true },
  { label: "系统设置", path: "/admin/settings", section: "管理", adminOnly: true },
];
