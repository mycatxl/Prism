import { useSyncExternalStore } from "react";
import type { QualitySummary } from "./types";

export const riskLabels = { unknown: "评级未知", low: "较低风险", moderate: "一般风险", high: "较高风险", severe: "严重风险" };
export const typeLabels = { unknown: "类型未知", residential: "住宅网络", non_residential: "非住宅网络", datacenter: "数据中心", mobile: "移动网络", business: "企业网络", wireless: "无线网络", conflicting: "来源有分歧" };

export const verdictLabels = { pending: "等待 IPPure 评分", incomplete: "特征未齐", review: "需要复核", conflicting: "类型有分歧", caution: "谨慎使用", high_risk: "风险较高", favorable: "未见明显风险" };
export const assessmentReasons: Record<string, string> = {
  IPPURE_REQUIRED: "尚未取得 IPPure 评分", IPPURE_SCORE_UNAVAILABLE: "IPPure 未提供评分",
  NETWORK_EVIDENCE_MISSING: "缺少有效的网络特征", NETWORK_EVIDENCE_INCOMPLETE: "部分网络标记未知",
  PROXY_DETECTED: "ProxyCheck 标记为代理", VPN_DETECTED: "ProxyCheck 标记为 VPN",
  TOR_DETECTED: "ProxyCheck 标记为 Tor", SCRAPER_DETECTED: "来源发现爬取特征",
  ANONYMOUS_DETECTED: "来源发现匿名网络特征", COMPROMISED: "来源发现被入侵记录",
  ATTACK_HISTORY: "来源存在攻击记录", RECENT_ABUSE: "存在近期高置信度举报",
  IPPURE_HIGH_RISK: "IPPure 风险分偏高", NETWORK_TYPE_CONFLICT: "两家来源的网络分类有分歧",
  TOR_PUBLIC_RELAY: "Tor Project 公开资料中存在此地址",
};
export const torRoleLabels = { exit: "Tor 出口", guard: "Tor 入口守卫（候选）", relay: "Tor 中继" };

export function evidenceFor(summary: QualitySummary | null | undefined, provider: string) {
  const source = summary?.sources?.find(source => source.provider === provider)?.evidence;
  return source?.provider === provider ? source : summary?.evidence?.provider === provider ? summary.evidence : null;
}

export function sourceIsFresh(summary: QualitySummary | null | undefined, provider: string, now: number) {
  const evidence = evidenceFor(summary, provider);
  return Boolean(evidence && evidence.ip === summary?.ip && Date.parse(evidence.valid_until) > now);
}

export const purityBands = [
  { id: "excellent", min: 95, max: 100, label: "极度纯净", variant: "success" },
  { id: "clean", min: 90, max: 94, label: "纯净", variant: "success" },
  { id: "fair", min: 80, max: 89, label: "较纯净", variant: "info" },
  { id: "mixed", min: 60, max: 79, label: "一般", variant: "warning" },
  { id: "poor", min: 0, max: 59, label: "风险较高", variant: "danger" },
] as const;

export function purityScore(risk?: number | null): number | null {
  return typeof risk === "number" && Number.isInteger(risk) && risk >= 0 && risk <= 100 ? 100 - risk : null;
}
export const purityBand = (score: number | null) => score === null ? undefined : purityBands.find(band => score >= band.min && score <= band.max);
export const providerName = (id?: string) => id === "proxycheck" ? "ProxyCheck v3" : id === "ippure" ? "IPPure" : id === "abuseipdb" ? "AbuseIPDB" : id || "—";

export function hasCurrentEvidence(summary: QualitySummary | null | undefined, now: number) {
  return Boolean(summary?.evidence) && (summary?.state === "valid" || summary?.state === "conflicting") && Date.parse(summary!.evidence!.valid_until) > now;
}

export function hasContradictingSignals(summary: QualitySummary | null | undefined, now: number) {
  if (!hasCurrentEvidence(summary, now)) return false;
  const signals = summary!.evidence!.signals;
  if (signals.compromised === true) return true;
  const score = purityScore(summary!.evidence!.risk_score);
  return score !== null && score >= 80 && [signals.proxy, signals.vpn, signals.tor, signals.anonymous, signals.scraper].some(value => value === true);
}

let currentTime = Date.now();
let clockTimer: ReturnType<typeof setInterval> | undefined;
const clockSubscribers = new Set<() => void>();
function subscribeClock(listener: () => void) {
  if (clockSubscribers.size === 0) {
    currentTime = Date.now();
    clockTimer = setInterval(() => {
      currentTime = Date.now();
      clockSubscribers.forEach(notify => notify());
    }, 1000);
  }
  clockSubscribers.add(listener);
  return () => {
    clockSubscribers.delete(listener);
    if (clockSubscribers.size === 0) { clearInterval(clockTimer); clockTimer = undefined; }
  };
}
const clockSnapshot = () => currentTime;
const serverClockSnapshot = () => 0;
export const useQualityTime = () => useSyncExternalStore(subscribeClock, clockSnapshot, serverClockSnapshot);

export function isFresh(summary: QualitySummary | null | undefined, now: number) {
  return summary?.state === "valid" && Boolean(summary.evidence) && Date.parse(summary.evidence!.valid_until) > now;
}

export function needsReview(summary: QualitySummary | null | undefined, now: number) {
  return summary?.sources?.some(source => source.state === "valid" && source.evidence &&
    Date.parse(source.evidence.valid_until) > now &&
    (source.evidence.abuse_confidence ?? 0) >= 75 && (source.evidence.total_reports ?? 0) > 0) ?? false;
}

const errorLabels: Record<string, string> = {
  PROVIDER_LIMIT: "来源额度已用完，稍后自动重试",
  PROVIDER_AUTH: "数据源凭据需要检查",
  PROVIDER_RESPONSE: "来源返回的数据不完整，未生成评级",
  PROVIDER_UNAVAILABLE: "暂时无法访问数据源",
  ATTEMPTS_EXHAUSTED: "重试次数已用完，可稍后手动检测",
  QUALITY_STORAGE_UNAVAILABLE: "检测结果暂时无法保存",
};
export const inspectionErrorLabel = (code?: string) => errorLabels[code ?? ""] || "检测暂未完成";
