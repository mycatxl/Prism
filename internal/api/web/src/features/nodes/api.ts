import { apiRequest } from "../../lib/api-client";
import type {
  EgressProbeResult,
  LatencyProbeResult,
  NodeListQuery,
  NodeSummary,
  PageResponse,
} from "./types";

const basePath = "/api/v1/nodes";

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

export async function listNodes(filters: NodeListQuery, signal?: AbortSignal): Promise<PageResponse<NodeSummary>> {
  const query = new URLSearchParams({
    limit: String(filters.limit ?? 50),
    offset: String(filters.offset ?? 0),
    sort_by: filters.sort_by || "tag",
    sort_order: filters.sort_order || "asc",
  });

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

  const data = await apiRequest<PageResponse<ApiNodeSummary>>(`${basePath}?${query.toString()}`, { signal });
  return {
    ...data,
    items: data.items.map(normalizeNode),
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
