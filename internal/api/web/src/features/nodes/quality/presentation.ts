import type { BadgeProps } from "../../../components/ui/Badge";
import type { QualitySummary } from "./types";
import type { EvidenceFact } from "../types";

export const riskLabels = { unknown: "评级未知", low: "较低风险", moderate: "一般风险", high: "较高风险", severe: "严重风险" };
export const typeLabels = { unknown: "类型未知", residential: "住宅网络", non_residential: "非住宅网络", datacenter: "数据中心", mobile: "移动网络", business: "企业网络", wireless: "无线网络", conflicting: "类型有分歧" };

export const verdictLabels = { pending: "评分未知", incomplete: "特征未齐", review: "需要复核", conflicting: "类型有分歧", caution: "谨慎使用", high_risk: "风险较高", favorable: "未见明显风险" };
export const assessmentReasons: Record<string, string> = {
  IPPURE_REQUIRED: "尚未取得纯净度评分", IPPURE_SCORE_UNAVAILABLE: "未提供纯净度评分",
  NETWORK_EVIDENCE_MISSING: "缺少有效的网络特征", NETWORK_EVIDENCE_INCOMPLETE: "部分网络标记未知",
  PROXY_DETECTED: "检测到代理特征", VPN_DETECTED: "检测到 VPN 特征",
  TOR_DETECTED: "检测到 Tor 特征", SCRAPER_DETECTED: "检测到爬取特征",
  ANONYMOUS_DETECTED: "检测到匿名网络特征", COMPROMISED: "检测到被入侵记录",
  ATTACK_HISTORY: "存在攻击记录", RECENT_ABUSE: "存在近期高置信度举报",
  IPPURE_HIGH_RISK: "纯净度风险分偏高", NETWORK_TYPE_CONFLICT: "网络分类存在分歧",
  TOR_PUBLIC_RELAY: "公开中继资料中存在此地址",
};
export const torRoleLabels = { exit: "Tor 出口", guard: "Tor 入口守卫（候选）", relay: "Tor 中继" };

// Intel label maps, shared with the node list cell and the detail panels: the
// same outcomes must read the same everywhere, so they live here rather than
// beside one component.
export const stateLabels: Record<string, string> = {
  unassessed: "未评估",
  pending: "无有效评估",
  unsupported: "不支持评估",
  stale: "评估已过期",
  valid: "评估有效",
  conflicting: "类型有分歧",
};

export const confidenceLabels: Record<string, string> = {
  high: "置信度高",
  medium: "置信度中",
  low: "置信度低",
  none: "置信度未知",
};

export const flagLabels: Record<string, string> = {
  compromised: "被入侵记录",
  tor: "Tor",
  vpn: "VPN",
  proxy: "代理",
  scraper: "爬取特征",
  abuse: "滥用记录",
  anonymous: "匿名网络",
  hosting: "托管网络",
  dnsbl: "黑名单命中",
  attack_history: "攻击历史",
};

export const outcomeLabels: Record<string, string> = {
  unknown: "未知",
  available: "可用",
  blocked: "被拦截",
  region_limited: "地区受限",
  captcha: "验证码",
  error: "检测失败",
};

type Tone = NonNullable<BadgeProps["tone"]>;

export const outcomeTones: Record<string, Tone> = {
  available: "signal",
  blocked: "alert",
  region_limited: "warn",
  captcha: "warn",
  error: "alert",
  unknown: "neutral",
};

export function evidenceFor(summary: QualitySummary | null | undefined, provider: string) {
  const matches = (id: string | undefined) => id === provider || id === `${provider}_node`;
  const source = summary?.sources?.find(source => matches(source.provider))?.evidence;
  return matches(source?.provider) ? source : matches(summary?.evidence?.provider) ? summary?.evidence : null;
}

export function sourceIsFresh(summary: QualitySummary | null | undefined, provider: string, now: number) {
  const evidence = evidenceFor(summary, provider);
  const source = summary?.sources?.find(source => source.provider === provider || source.provider === `${provider}_node`);
  return Boolean(source && ["valid", "conflicting"].includes(source.state) && evidence && evidence.ip === summary?.ip && Date.parse(evidence.valid_until) > now);
}

export const purityBands = [
  { id: "excellent", label: "极度纯净", variant: "success" },
  { id: "clean", label: "纯净", variant: "success" },
  { id: "fair", label: "较纯净", variant: "info" },
  { id: "mixed", label: "一般", variant: "warning" },
  { id: "poor", label: "风险较高", variant: "danger" },
] as const;

export function purityBandLabel(id: string) {
  return purityBands.find(band => band.id === id)?.label ?? ({ unknown: "未评估", review: "人工复核" }[id] ?? id);
}

// Every purity surface consumes the authoritative assessment. Provider risk is
// never inverted into another purity value in the browser.
export function canonicalPurity(summary: QualitySummary | null | undefined) {
  const value = summary?.assessment?.purity_score;
  const until = summary?.assessment?.facts?.purity?.valid_until;
  const expired = summary?.state === "stale" || (until !== undefined && !(Date.parse(until) > Date.now()));
  const score = expired ? null : validPurityScore(value);
  return { score, band: score === null ? undefined : purityBands.find(band => band.id === summary?.assessment?.purity_band) };
}
export function hasCurrentEvidence(summary: QualitySummary | null | undefined, now: number) {
  return Boolean(summary?.evidence) && (summary?.state === "valid" || summary?.state === "conflicting") && Date.parse(summary!.evidence!.valid_until) > now;
}

export function hasContradictingSignals(summary: QualitySummary | null | undefined, now: number) {
  if (!hasCurrentEvidence(summary, now)) return false;
  const signals = summary!.evidence!.signals;
  if (signals.compromised === true) return true;
  const { score } = canonicalPurity(summary);
  return score !== null && score >= 80 && [signals.proxy, signals.vpn, signals.tor, signals.anonymous, signals.scraper].some(value => value === true);
}

export function isFresh(summary: QualitySummary | null | undefined, now: number) {
  return summary?.state === "valid" && Boolean(summary.evidence) && Date.parse(summary.evidence!.valid_until) > now;
}

export function needsReview(summary: QualitySummary | null | undefined, now: number) {
  return summary?.sources?.some(source => source.state === "valid" && source.evidence &&
    Date.parse(source.evidence.valid_until) > now &&
    (source.evidence.abuse_confidence ?? 0) >= 75 && (source.evidence.total_reports ?? 0) > 0) ?? false;
}

const errorLabels: Record<string, string> = {
  PROVIDER_LIMIT: "检测暂时受限，请稍后重试",
  PROVIDER_AUTH: "检测暂时不可用，请稍后重试",
  PROVIDER_RESPONSE: "检测结果不完整，请重新检测",
  PROVIDER_UNAVAILABLE: "检测暂时不可用，请稍后重试",
  ATTEMPTS_EXHAUSTED: "重试次数已用完，可稍后手动检测",
  QUALITY_STORAGE_UNAVAILABLE: "检测结果暂时无法保存",
};
export const inspectionErrorLabel = (code?: string) => errorLabels[code ?? ""] || "检测暂未完成";

const measurementStatusLabels: Record<string, string> = {
  NO_EGRESS: "请先完成出口探测",
  IPPURE_TIMEOUT: "检测超时，请稍后重试",
  IPPURE_RATE_LIMITED: "检测暂时受限，请稍后重试",
  IPPURE_AUTH_FAILED: "检测暂时不可用，请稍后重试",
  IPPURE_EGRESS_MISMATCH: "出口已变化，请重新检测",
  IPPURE_INVALID_RESPONSE: "检测结果不完整，请重新检测",
  IPPURE_NO_FRAUD_SCORE: "暂未获得有效评分，请重新检测",
  IPPURE_IPV6_SCORE_UNAVAILABLE: "当前 IPv6 出口暂不支持评分",
  EVIDENCE_EXPIRED: "检测结果已过期，请重新检测",
  ...errorLabels,
};

// Keep provider identifiers and diagnostic payloads out of business surfaces.
export function measurementStatusLabel(fact?: Pick<EvidenceFact, "state" | "cause" | "error_code">): string {
  if (fact?.state === "expired") return measurementStatusLabels.EVIDENCE_EXPIRED;
  return measurementStatusLabels[fact?.cause ?? ""] || measurementStatusLabels[fact?.error_code ?? ""] || "尚无有效检测结果，请运行完整检测";
}

export const evidenceClassLabels: Record<string, string> = {
  static_config: "静态配置检查", active_probe: "主动网络测量", vendor_claim: "供应商声明", derived: "计算值", heuristic: "启发式",
};


export function validPurityScore(value: unknown): number | null {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 && value <= 100 ? value : null;
}
