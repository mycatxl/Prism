import { apiRequest } from "../../lib/api-client";
import type { InspectionResult, IPPureReview, QualityStatus, QualitySummary } from "./types";

const base = "/api/v1/quality";
export const getQualityStatus = () => apiRequest<QualityStatus>(base + "/status");
export const getIPQuality = (ip: string) => apiRequest<QualitySummary>(base + "/ip/" + encodeURIComponent(ip));
export const inspectIP = (ip: string) => apiRequest<InspectionResult>(base + "/ip/" + encodeURIComponent(ip) + "/actions/probe", { method: "POST" });
export const inspectNode = (hash: string) => apiRequest<InspectionResult>("/api/v1/nodes/" + encodeURIComponent(hash) + "/actions/probe-quality", { method: "POST" });
export const reviewIPPure = (hash: string) => apiRequest<IPPureReview>("/api/v1/nodes/" + encodeURIComponent(hash) + "/actions/review-ippure", { method: "POST" });
export const listQuality = (q = "", page = 0) => apiRequest<{ items: QualitySummary[]; total: number; limit: number; offset: number }>(
  base + "/assessments?" + new URLSearchParams({ q, limit: "25", offset: String(page * 25) }),
);

export function qualityPollingInterval(status?: QualityStatus) {
  return status?.manual_sources?.some(source => source.busy) || status?.sources.some(source => source.running > 0 ||
    (source.queued > 0 && !source.paused && (!source.next_allowed_at || Date.parse(source.next_allowed_at) < Date.now() + 5000)))
    ? 3000 : 15000;
}
