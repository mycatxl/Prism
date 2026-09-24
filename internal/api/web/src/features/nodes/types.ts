import type { QualitySummary } from "../quality/types";

export type NodeTag = {
  subscription_id: string;
  subscription_name: string;
  tag: string;
};

// NodeIntel is the WP10 purity assessment of a node's egress IP (API field
// `intel`). Every field of an unassessed node is empty and state is
// "unassessed": a missing assessment is never reported as a score of 0.
export type NodeIntel = {
  state: "unassessed" | "pending" | "valid" | "unsupported" | "stale" | string;
  egress_ipv4: string;
  egress_ipv6: string;
  colo: string;
  asn: number;
  as_org: string;
  country: string;
  city: string;
  ip_type: string;
  native: boolean | null;
  purity_score: number | null;
  purity_band: string;
  confidence: string;
  verdict: string;
  flags: string[];
  checks: Record<string, string>;
  assessed_at: string;
};

export type NodeSummary = {
  quality?: QualitySummary;
  intel?: NodeIntel | null;
  node_hash: string;
  protocol?: string;
  created_at: string;
  enabled: boolean;
  display_tag?: string;
  has_outbound: boolean;
  last_error?: string;
  circuit_open_since?: string;
  failure_count: number;
  egress_ip?: string;
  reference_latency_ms?: number;
  region?: string;
  last_egress_update?: string;
  last_latency_probe_attempt?: string;
  last_authority_latency_probe_attempt?: string;
  last_egress_update_attempt?: string;
  tags: NodeTag[];
};

export type PageResponse<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
  unique_egress_ips: number;
  unique_healthy_egress_ips: number;
};

export type NodeSortBy =
  | "tag"
  | "created_at"
  | "failure_count"
  | "region"
  | "purity_score"
  | "latency"
  | "assessed_at";

export type SortOrder = "asc" | "desc";

export type NodeListFilters = {
  ip_type?: string;
  quality_state?: string;
  risk_grade?: string;
  purity_band?: string;
  protocol?: string;
  platform_id?: string;
  subscription_id?: string;
  tag_keyword?: string;
  region?: string;
  egress_ip?: string;
  probed_since?: string;
  enabled?: boolean;
  circuit_open?: boolean;
  has_outbound?: boolean;
  // WP10 §4 intel filters. `check` is repeatable and each entry is
  // "<check id>:<outcome>".
  purity_min?: number;
  purity_max?: number;
  verdict?: string;
  confidence_min?: string;
  native?: boolean;
  asn?: number;
  country?: string;
  check?: string[];
};

export type NodeListQuery = NodeListFilters & {
  sort_by?: NodeSortBy;
  sort_order?: SortOrder;
  limit?: number;
  offset?: number;
};

export type EgressProbeResult = {
  egress_ip: string;
  region?: string;
  latency_ewma_ms: number;
};

export type LatencyProbeResult = {
  latency_ewma_ms: number;
};
