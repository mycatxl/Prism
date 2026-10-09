/**
 * The console's page list, in the order the rail shows it. Labels are dictionary
 * keys; `section` groups the rail and disappears in the horizontal (<1100px) bar.
 */
export const navigation = [
  { label: "总览", path: "/dashboard", section: "工作" },
  { label: "节点", path: "/nodes", section: "工作" },
  { label: "平台", path: "/platforms", section: "工作" },
  { label: "订阅源", path: "/subscriptions", section: "工作" },
  { label: "检测任务", path: "/jobs", section: "工作" },
  { label: "接入点", path: "/endpoints", section: "接入" },
  { label: "请求日志", path: "/request-logs", section: "接入" },
  { label: "请求头规则", path: "/rules", section: "接入" },
  { label: "GeoIP", path: "/resources", section: "系统" },
  { label: "系统配置", path: "/system-config", section: "系统" },
  { label: "审计日志", path: "/audit", section: "系统" },
];
