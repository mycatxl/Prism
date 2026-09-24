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
};
