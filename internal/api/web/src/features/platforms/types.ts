export type PlatformMissAction = "TREAT_AS_EMPTY" | "REJECT";
export type PlatformEmptyAccountBehavior = "RANDOM" | "FIXED_HEADER" | "ACCOUNT_HEADER_RULE";
export type PlatformAllocationPolicy = "BALANCED" | "PREFER_LOW_LATENCY" | "PREFER_IDLE_IP";

/**
 * Node-selection criteria of a platform.
 *
 * Semantics: every criterion the operator set must hold at the same time (AND),
 * and the values inside one criterion are alternatives (OR) — a node that
 * matches any single value of a criterion passes that criterion. An empty list
 * means "do not restrict on this criterion". `regex_filters` is the legacy
 * escape hatch and keeps its own line-oriented tag semantics.
 */
export type Platform = {
  id: string;
  name: string;
  sticky_ttl: string;
  regex_filters: string[];
  region_filters: string[];
  ip_types: string[];
  purity_bands: string[];
  subscription_filters: string[];
  protocols: string[];
  routable_node_count: number;
  reverse_proxy_miss_action: PlatformMissAction;
  reverse_proxy_empty_account_behavior: PlatformEmptyAccountBehavior;
  reverse_proxy_fixed_account_header: string;
  allocation_policy: PlatformAllocationPolicy;
  passive_circuit_breaker_disabled: boolean;
  updated_at: string;
};

export type PageResponse<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
};

/** The criteria subset of a platform, used for the live scope preview. */
export type PlatformScopeSpec = {
  regex_filters?: string[];
  region_filters?: string[];
  ip_types?: string[];
  purity_bands?: string[];
  subscription_filters?: string[];
  protocols?: string[];
};

export type PlatformCreateInput = {
  name: string;
  sticky_ttl?: string;
  regex_filters?: string[];
  region_filters?: string[];
  ip_types?: string[];
  purity_bands?: string[];
  subscription_filters?: string[];
  protocols?: string[];
  reverse_proxy_miss_action?: PlatformMissAction;
  reverse_proxy_empty_account_behavior?: PlatformEmptyAccountBehavior;
  reverse_proxy_fixed_account_header?: string;
  allocation_policy?: PlatformAllocationPolicy;
  passive_circuit_breaker_disabled?: boolean;
};

export type PlatformUpdateInput = PlatformCreateInput;

/**
 * Option lists of the platform form, derived from the live node pool. Every
 * value here is one the pool actually carries.
 */
export type PlatformNodeFacets = {
  total_nodes: number;
  scanned: number;
  truncated: boolean;
  regions: string[];
  ip_types: string[];
  purity_bands: string[];
  protocols: string[];
  subscriptions: {
    id: string;
    name: string;
    enabled: boolean;
    node_count: number;
  }[];
};

/** One node of a preview sample. */
export type PlatformScopeSampleNode = {
  node_hash: string;
  display_tag?: string;
  region?: string;
  protocol?: string;
  ip_type?: string;
  purity_band?: string;
  subscription_names: string[];
};

/**
 * Live preview of a criteria spec: `matched` is the number of nodes the
 * platform would load, `scan_limit` the number of pool entries the bounded scan
 * evaluated, and `truncated` says that `matched` is a lower bound.
 */
export type PlatformScopePreview = {
  matched: number;
  scanned: number;
  truncated: boolean;
  sample: PlatformScopeSampleNode[];
  excluded_by: Record<string, number>;
};

export type PlatformLease = {
  platform_id: string;
  account: string;
  node_hash: string;
  node_tag: string;
  egress_ip: string;
  expiry: string;
  last_accessed: string;
};

export type PlatformLeaseSortBy = "account" | "expiry" | "last_accessed";
export type SortOrder = "asc" | "desc";

export type ListPlatformLeasesInput = {
  limit?: number;
  offset?: number;
  account?: string;
  fuzzy?: boolean;
  sort_by?: PlatformLeaseSortBy;
  sort_order?: SortOrder;
};
