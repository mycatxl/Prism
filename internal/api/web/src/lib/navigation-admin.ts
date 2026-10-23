import type { NavigationItem } from "./navigation";
import { opsPaths } from "./paths";

/**
 * Operator destinations grouped by the work the operator is doing. The route
 * table stays unchanged; only the console's mental model changes here.
 */
export const adminItems: NavigationItem[] = [
  { label: "控制平面", path: opsPaths.dashboard, section: "", adminOnly: true },

  { label: "节点", path: opsPaths.nodes, section: "资产", adminOnly: true },
  { label: "订阅源", path: opsPaths.subscriptions, section: "资产", adminOnly: true },
  { label: "检测任务", path: opsPaths.jobs, section: "资产", adminOnly: true },

  { label: "平台", path: opsPaths.platforms, section: "交付", adminOnly: true },
  { label: "接入点", path: opsPaths.endpoints, section: "交付", adminOnly: true },
  { label: "请求头规则", path: opsPaths.rules, section: "交付", adminOnly: true },
  { label: "请求日志", path: opsPaths.requestLogs, section: "交付", adminOnly: true },

  { label: "用户", path: opsPaths.users, section: "客户", adminOnly: true },
  { label: "套餐", path: opsPaths.plans, section: "客户", adminOnly: true },
  { label: "邀请码", path: opsPaths.invites, section: "客户", adminOnly: true },
  { label: "兑换码", path: opsPaths.redeemCodes, section: "客户", adminOnly: true },
  { label: "用量", path: opsPaths.usage, section: "客户", adminOnly: true },

  { label: "系统配置", path: opsPaths.config, section: "治理", adminOnly: true },
  { label: "策略与准入", path: opsPaths.settings, section: "治理", adminOnly: true },
  { label: "资源", path: opsPaths.resources, section: "治理", adminOnly: true },
  { label: "审计日志", path: opsPaths.audit, section: "治理", adminOnly: true },
];
