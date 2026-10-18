import type { NavigationItem } from "./navigation";

/**
 * The operator's destinations, grouped by the object they act on.
 *
 * Loaded dynamically, for an admin session only — see navigation.ts. Every entry
 * here names a management route, and the server refuses those to a tenant
 * whatever the client does (internal/api/rolegate.go).
 *
 * The rule for a group: one kind of object, and its configuration and its
 * observation sit together, because the operator's question is always about one
 * object at a time ("is this pool healthy", "who is this customer").
 *
 *   节点池  what the pool is made of, and what keeps it fresh
 *   售卖    what is being sold, and on what terms
 *   客户    who buys it, how they got in, and what they spend
 *   接入    how a request reaches an exit, and what it did on the way
 *   系统    the instance's own levers, resources and history
 *
 * `总览` carries no section: it is the front door, not a category.
 */
export const adminItems: NavigationItem[] = [
  { label: "总览", path: "/dashboard", section: "", adminOnly: true },

  { label: "节点", path: "/nodes", section: "节点池", adminOnly: true },
  { label: "订阅源", path: "/subscriptions", section: "节点池", adminOnly: true },
  { label: "检测任务", path: "/jobs", section: "节点池", adminOnly: true },

  { label: "平台", path: "/platforms", section: "售卖", adminOnly: true },
  { label: "套餐", path: "/admin/plans", section: "售卖", adminOnly: true },

  { label: "用户", path: "/admin/users", section: "客户", adminOnly: true },
  { label: "邀请码", path: "/admin/invites", section: "客户", adminOnly: true },
  { label: "兑换码", path: "/admin/redeem-codes", section: "客户", adminOnly: true },
  { label: "用量", path: "/admin/usage", section: "客户", adminOnly: true },

  { label: "接入点", path: "/endpoints", section: "接入", adminOnly: true },
  { label: "请求头规则", path: "/rules", section: "接入", adminOnly: true },
  { label: "请求日志", path: "/request-logs", section: "接入", adminOnly: true },

  { label: "运行参数", path: "/system-config", section: "系统", adminOnly: true },
  { label: "策略与准入", path: "/admin/settings", section: "系统", adminOnly: true },
  { label: "资源", path: "/resources", section: "系统", adminOnly: true },
  { label: "审计日志", path: "/audit", section: "系统", adminOnly: true },
];
