import { Activity, Cable, Globe2, Logs, Network, Radar, Regex, Rss, Settings2, Waypoints } from "lucide-react";

export const navigation = [
  { label: "总览看板", path: "/dashboard", icon: Activity, section: "工作区" },
  { label: "节点池", path: "/nodes", icon: Network, section: "工作区" },
  { label: "订阅管理", path: "/subscriptions", icon: Rss, section: "工作区" },
  { label: "平台管理", path: "/platforms", icon: Waypoints, section: "工作区" },
  { label: "请求日志", path: "/request-logs", icon: Logs, section: "观测与配置" },
  { label: "接入点", path: "/endpoints", icon: Cable, section: "观测与配置" },
  { label: "请求头规则", path: "/rules", icon: Regex, section: "观测与配置" },
  { label: "GeoIP", path: "/resources", icon: Globe2, section: "观测与配置" },
  { label: "数据源与检测", path: "/intel-settings", icon: Radar, section: "观测与配置" },
  { label: "系统配置", path: "/system-config", icon: Settings2, section: "观测与配置" },
];
