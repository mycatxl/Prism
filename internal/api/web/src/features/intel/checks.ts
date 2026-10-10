import { apiRequest } from "../../lib/api-client";

/**
 * One unlock check rule as GET /api/v1/intel/checks reports it.
 *
 * The panel never names the data sources behind a result, but a check rule is a
 * first-class filter value the operator picks from a list, so its id, name and
 * category are shown as they are.
 */
export type IntelCheck = {
  id: string;
  name: string;
  version: number;
  category: string;
  source: string;
  path?: string;
  calibrated?: string;
  ttl: string;
  timeout: string;
  steps: string[];
  enabled: boolean;
  enabled_override: boolean;
};

/** The outcome values a check result can carry (node_checks.outcome). */
export const INTEL_CHECK_OUTCOMES = [
  "available",
  "blocked",
  "region_limited",
  "captcha",
  "error",
  "unknown",
] as const;

export type IntelCheckOutcome = (typeof INTEL_CHECK_OUTCOMES)[number];

const basePath = "/api/v1/intel/checks";

function normalizeStringList(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.map((item) => String(item ?? "").trim()).filter(Boolean);
}

function normalizeCheck(value: unknown): IntelCheck | null {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return null;
  }
  const item = value as Record<string, unknown>;
  const id = typeof item.id === "string" ? item.id.trim() : "";
  if (!id) {
    return null;
  }
  return {
    id,
    name: typeof item.name === "string" && item.name.trim() ? item.name.trim() : id,
    version: typeof item.version === "number" && Number.isFinite(item.version) ? item.version : 0,
    category: typeof item.category === "string" ? item.category.trim() : "",
    source: typeof item.source === "string" ? item.source.trim() : "",
    path: typeof item.path === "string" ? item.path : undefined,
    calibrated: typeof item.calibrated === "string" ? item.calibrated : undefined,
    ttl: typeof item.ttl === "string" ? item.ttl : "",
    timeout: typeof item.timeout === "string" ? item.timeout : "",
    steps: normalizeStringList(item.steps),
    enabled: Boolean(item.enabled),
    enabled_override: Boolean(item.enabled_override),
  };
}

/**
 * listIntelChecks returns the unlock check rules the node filter and the platform
 * policy editor offer. It is the only place the panel learns the rule ids: the
 * backend rejects an unknown id, so a hand-typed one would fail the request.
 */
export async function listIntelChecks(signal?: AbortSignal): Promise<IntelCheck[]> {
  const data = await apiRequest<{ items?: unknown }>(`${basePath}?limit=100`, { signal });
  if (!Array.isArray(data.items)) {
    return [];
  }
  const checks: IntelCheck[] = [];
  for (const raw of data.items) {
    const check = normalizeCheck(raw);
    if (check) {
      checks.push(check);
    }
  }
  return checks;
}

/** checkFilterValue renders one "<check id>:<outcome>" filter entry. */
export function checkFilterValue(checkID: string, outcome: string): string {
  return `${checkID}:${outcome}`;
}

/**
 * parseCheckFilterValue splits one filter entry back into its two halves. An
 * entry without a colon carries the empty outcome, which the API reads as
 * "any outcome".
 */
export function parseCheckFilterValue(value: string): { checkID: string; outcome: string } {
  const trimmed = value.trim();
  const separator = trimmed.indexOf(":");
  if (separator < 0) {
    return { checkID: trimmed, outcome: "" };
  }
  return {
    checkID: trimmed.slice(0, separator).trim(),
    outcome: trimmed.slice(separator + 1).trim(),
  };
}
