import { ApiError, apiRequest, type ApiErrorBody } from "../../lib/api-client";
import { getStoredAuthToken } from "../auth/auth-store";
import type {
  EgressProbeResult,
  LatencyProbeResult,
  NodeListQuery,
  NodeSummary,
  PageResponse,
} from "./types";

const basePath = "/api/v1/nodes";

// The raw export fetch cannot use apiRequest, so it reads the same base URL.
const API_BASE_URL = import.meta.env.VITE_API_BASE_URL?.trim() ?? "";

type ApiNodeSummary = Omit<NodeSummary, "tags"> & {
  tags?: NodeSummary["tags"] | null;
  enabled?: boolean | null;
  display_tag?: string | null;
  last_error?: string | null;
  circuit_open_since?: string | null;
  egress_ip?: string | null;
  reference_latency_ms?: number | null;
  region?: string | null;
  last_egress_update?: string | null;
  last_latency_probe_attempt?: string | null;
  last_authority_latency_probe_attempt?: string | null;
  last_egress_update_attempt?: string | null;
};

type ApiNodePage = {
  items?: unknown;
  total?: number | string | null;
  limit?: number | string | null;
  offset?: number | string | null;
  unique_egress_ips?: number | string | null;
  unique_healthy_egress_ips?: number | string | null;
};

function isApiNodeSummary(value: unknown): value is ApiNodeSummary {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return false;
  }
  const item = value as Record<string, unknown>;
  return (
    typeof item.node_hash === "string" &&
    item.node_hash.trim().length > 0 &&
    typeof item.created_at === "string" &&
    typeof item.has_outbound === "boolean" &&
    typeof item.failure_count === "number" &&
    Number.isFinite(item.failure_count)
  );
}

function parseNonNegativeInteger(value: unknown): number | null {
  const parsed = typeof value === "number" ? value : typeof value === "string" && value.trim() ? Number(value) : NaN;
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : null;
}

function normalizeCount(value: unknown, fallback: number): number {
  return parseNonNegativeInteger(value) ?? fallback;
}

function normalizeOptionalCount(value: unknown): number | null {
  return parseNonNegativeInteger(value);
}

function normalizeNode(raw: ApiNodeSummary): NodeSummary {
  const { reference_latency_ms, ...rest } = raw;
  const normalized: NodeSummary = {
    ...rest,
    enabled: raw.enabled !== false,
    display_tag: raw.display_tag || "",
    tags: Array.isArray(raw.tags) ? raw.tags : [],
    last_error: raw.last_error || "",
    circuit_open_since: raw.circuit_open_since || "",
    egress_ip: raw.egress_ip || "",
    region: raw.region || "",
    last_egress_update: raw.last_egress_update || "",
    last_latency_probe_attempt: raw.last_latency_probe_attempt || "",
    last_authority_latency_probe_attempt: raw.last_authority_latency_probe_attempt || "",
    last_egress_update_attempt: raw.last_egress_update_attempt || "",
  };

  // Backend uses `omitempty`; field missing means "no reference latency".
  if (typeof reference_latency_ms === "number") {
    normalized.reference_latency_ms = reference_latency_ms;
  }

  // The assessment may be missing entirely (pre-WP10 payloads). Normalising it
  // here keeps every consumer free of undefined checks; `intel.state` carries
  // the "unassessed" case.
  const intel = raw.intel;
  if (intel) {
    normalized.intel = {
      ...intel,
      native: intel.native ?? null,
      purity_score: typeof intel.purity_score === "number" ? intel.purity_score : null,
      flags: Array.isArray(intel.flags) ? intel.flags : [],
      checks: intel.checks && typeof intel.checks === "object" ? intel.checks : {},
    };
  }

  return normalized;
}

/** Appends the shared node filter vocabulary (GET /nodes and /nodes/export). */
function appendNodeFilters(query: URLSearchParams, filters: NodeListQuery): void {
  const appendIfNotEmpty = (key: string, value?: string) => {
    if (!value) {
      return;
    }
    const trimmed = value.trim();
    if (!trimmed) {
      return;
    }
    query.set(key, trimmed);
  };

  appendIfNotEmpty("platform_id", filters.platform_id);
  appendIfNotEmpty("ip_type", filters.ip_type);
  appendIfNotEmpty("quality_state", filters.quality_state);
  appendIfNotEmpty("risk_grade", filters.risk_grade);
  appendIfNotEmpty("purity_band", filters.purity_band);
  appendIfNotEmpty("protocol", filters.protocol);
  appendIfNotEmpty("subscription_id", filters.subscription_id);
  appendIfNotEmpty("tag_keyword", filters.tag_keyword);
  appendIfNotEmpty("region", filters.region?.toLowerCase());
  appendIfNotEmpty("egress_ip", filters.egress_ip);
  appendIfNotEmpty("probed_since", filters.probed_since);

  // WP10 §4 intel filters.
  if (filters.purity_min !== undefined) {
    query.set("purity_min", String(filters.purity_min));
  }
  if (filters.purity_max !== undefined) {
    query.set("purity_max", String(filters.purity_max));
  }
  appendIfNotEmpty("verdict", filters.verdict);
  appendIfNotEmpty("confidence_min", filters.confidence_min);
  appendIfNotEmpty("country", filters.country?.toUpperCase());
  if (filters.native !== undefined) {
    query.set("native", String(filters.native));
  }
  if (filters.asn !== undefined) {
    query.set("asn", String(filters.asn));
  }
  // `check` is repeatable; each entry is "<check id>:<outcome>".
  for (const check of filters.check ?? []) {
    const trimmed = check.trim();
    if (trimmed) {
      query.append("check", trimmed);
    }
  }

  if (filters.circuit_open !== undefined) {
    query.set("circuit_open", String(filters.circuit_open));
  }
  if (filters.has_outbound !== undefined) {
    query.set("has_outbound", String(filters.has_outbound));
  }
  if (filters.enabled !== undefined) {
    query.set("enabled", String(filters.enabled));
  }
}

export async function listNodes(filters: NodeListQuery, signal?: AbortSignal): Promise<PageResponse<NodeSummary>> {
  const query = new URLSearchParams({
    limit: String(filters.limit ?? 50),
    offset: String(filters.offset ?? 0),
    sort_by: filters.sort_by || "tag",
    sort_order: filters.sort_order || "asc",
  });

  appendNodeFilters(query, filters);

  const data = await apiRequest<ApiNodePage | null>(`${basePath}?${query.toString()}`, { signal });
  const items = Array.isArray(data?.items) ? data.items.filter(isApiNodeSummary) : [];
  return {
    items: items.map(normalizeNode),
    total: normalizeCount(data?.total, items.length),
    limit: normalizeCount(data?.limit, filters.limit ?? 50),
    offset: normalizeCount(data?.offset, filters.offset ?? 0),
    unique_egress_ips: normalizeOptionalCount(data?.unique_egress_ips),
    unique_healthy_egress_ips: normalizeOptionalCount(data?.unique_healthy_egress_ips),
  };
}

export async function getNode(hash: string): Promise<NodeSummary> {
  const data = await apiRequest<ApiNodeSummary>(`${basePath}/${encodeURIComponent(hash)}`);
  return normalizeNode(data);
}

export async function probeEgress(hash: string): Promise<EgressProbeResult> {
  return apiRequest<EgressProbeResult>(`${basePath}/${hash}/actions/probe-egress`, {
    method: "POST",
  });
}

export async function probeLatency(hash: string): Promise<LatencyProbeResult> {
  return apiRequest<LatencyProbeResult>(`${basePath}/${hash}/actions/probe-latency`, {
    method: "POST",
  });
}

export const NODE_EXPORT_FORMATS = ["singbox", "mihomo", "v2rayn", "uri", "csv", "json"] as const;
export type NodeExportFormat = (typeof NODE_EXPORT_FORMATS)[number];

// Fallback names, mirroring export.FileName() in internal/export/types.go. The
// server sends the real name in Content-Disposition.
const EXPORT_FILE_NAMES: Record<NodeExportFormat, string> = {
  singbox: "prism.json",
  mihomo: "prism.yaml",
  v2rayn: "prism.txt",
  uri: "prism-uri.txt",
  csv: "prism.csv",
  json: "prism-export.json",
};

// One export renders up to export.MaxItems (5000) nodes, heavier than the 30s
// budget apiRequest uses for JSON calls.
const EXPORT_TIMEOUT_MS = 60_000;

export type NodeExportOutcome = {
  blob: Blob;
  fileName: string;
  exported: number;
  skipped: number;
  truncated: number;
};

function headerCount(value: string | null): number {
  const parsed = Number.parseInt(value ?? "", 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
}

/**
 * GET /api/v1/nodes/export with the node list's filters (paging and sorting
 * are not part of an export). The body is the file itself, so this cannot go
 * through apiRequest; counts come from the X-Prism-Export-* headers.
 */
export async function exportNodes(
  format: NodeExportFormat,
  filters: NodeListQuery,
  signal?: AbortSignal,
): Promise<NodeExportOutcome> {
  const query = new URLSearchParams({ format });
  appendNodeFilters(query, filters);

  const headers = new Headers();
  const token = getStoredAuthToken();
  if (token) {
    headers.set("Authorization", `Bearer ${token}`);
  }
  const timeout = AbortSignal.timeout(EXPORT_TIMEOUT_MS);
  const response = await fetch(`${API_BASE_URL}${basePath}/export?${query.toString()}`, {
    method: "GET",
    headers,
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  });
  if (!response.ok) {
    let body: ApiErrorBody | null = null;
    try {
      body = (await response.json()) as ApiErrorBody;
    } catch {
      body = null;
    }
    throw new ApiError(
      response.status,
      body?.error?.code ?? "HTTP_ERROR",
      body?.error?.message ?? `HTTP ${response.status}`,
      body,
    );
  }

  const disposition = response.headers.get("content-disposition") ?? "";
  const match = /filename="?([^";]+)"?/i.exec(disposition);
  return {
    blob: await response.blob(),
    fileName: match?.[1]?.trim() || EXPORT_FILE_NAMES[format],
    exported: headerCount(response.headers.get("x-prism-export-exported")),
    skipped: headerCount(response.headers.get("x-prism-export-skipped")),
    truncated: headerCount(response.headers.get("x-prism-export-truncated")),
  };
}
