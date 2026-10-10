// WP10 §4 — 节点池页面（节点抽屉与批量情报入口）的英文词条（中文文案即 key）。
// 在 i18n/workbench.ts 中展开进 WORKBENCH_TRANSLATIONS。
export const NODES_TRANSLATIONS: Record<string, string> = {
  "为该节点创建完整检测任务，完成后刷新纯净度评估。":
    "Create a full inspection job for this node; the purity assessment refreshes when it finishes.",
  "节点检测任务已创建。": "Node inspection job created.",
  查看检测任务: "View inspection jobs",
  批量拉取情报: "Fetch intel in bulk",
  "对当前筛选结果中健康的节点批量拉取情报。":
    "Fetch intel for the healthy nodes in the current result.",
  "批量拉取只对健康节点生效，请先切换状态筛选。":
    "Bulk intel only covers healthy nodes; switch the status filter first.",
  "部分筛选条件不适用于批量拉取，实际范围可能更大。":
    "Some filters cannot be applied to bulk intel; the actual scope may be wider.",
  "已创建情报任务（预计 {{count}} 个节点）":
    "Intelligence job created (about {{count}} nodes)",
  // 节点行标签（NodesPage 的“地区 / 网络类型”列）。
  "地区 / 网络类型": "Region / network type",
  网络类型: "Network type",
  // 节点导出菜单（GET /api/v1/nodes/export）。
  导出: "Export",
  按当前筛选导出: "Export current filter",
  分享链接: "Share links",
  表格: "Spreadsheet",
  分析数据: "Analysis data",
  "已导出 {{count}} 个节点": "Exported {{count}} nodes",
  "跳过 {{count}} 个": "{{count}} skipped",
  "超出上限 {{count}} 个未导出": "{{count}} over the limit not exported",
  // 解锁检测筛选（NodesPage 的“检测结果”多选）。解锁结果是标签，不是准入门槛。
  添加检测项: "Add check",
  "添加检测项…": "Add a check…",
  任意结果: "Any outcome",
  移除: "Remove",
  "未选择检测项：不按解锁结果筛选。":
    "No check selected: results do not filter anything.",
  "解锁结果是标签，不是准入条件：这里只筛选节点，不改变节点是否可用。":
    "Unlock results are labels, not an admission gate: this only filters the list, it never changes whether a node is usable.",
};
