// gemini.yaml v1 only inspects HTTP status: 200 is not proof of account,
// regional, model, API or full product access. Keep this distinction in every UI.
export const GEMINI_EVIDENCE_NOTE = "Gemini 当前仅检测网页可达性，完整解锁状态未知。";
export const CHECK_FILTER_NOTE = "结果筛选仅匹配有效探测；未检测或已过期的节点请在全部结果中查看。";

const outcomeLabels: Record<string, string> = {
  available: "探测可用", blocked: "访问被拒", region_limited: "地区受限",
  captcha: "请求受限 / 验证码", error: "探测失败", unknown: "探测结果未知",
};
const geminiLabels: Record<string, string> = {
  available: "网页可达（解锁未知）", blocked: "网页访问被拒",
  captcha: "请求受限（HTTP 429）", error: "探测失败", unknown: "探测结果未知",
};

export function checkOutcomeLabel(checkID: string, outcome?: string): string {
  if (!outcome) return "未知 / 无有效探测";
  return (checkID === "gemini" ? geminiLabels[outcome] : undefined) ?? outcomeLabels[outcome] ?? "探测结果未知";
}

export function checkOutcomeOptions(checkID: string) {
  return Object.entries(checkID === "gemini" ? geminiLabels : outcomeLabels).map(([id, label]) => ({ id, label }));
}
