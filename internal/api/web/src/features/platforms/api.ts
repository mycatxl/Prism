import { apiRequest } from "../../lib/api-client";
import type {
  ListPlatformLeasesInput,
  PageResponse,
  Platform,
  PlatformCreateInput,
  PlatformLease,
  PlatformNodeFacets,
  PlatformScopePreview,
  PlatformScopeSpec,
  PlatformUpdateInput,
} from "./types";

const basePath = "/api/v1/platforms";

type ApiPlatform = Omit<
  Platform,
  "regex_filters" | "region_filters" | "ip_types" | "purity_bands" | "subscription_filters" | "protocols"
> & {
  regex_filters?: string[] | null;
  region_filters?: string[] | null;
  ip_types?: string[] | null;
  purity_bands?: string[] | null;
  subscription_filters?: string[] | null;
  protocols?: string[] | null;
  routable_node_count?: number | null;
  reverse_proxy_miss_action?: Platform["reverse_proxy_miss_action"] | null;
  reverse_proxy_empty_account_behavior?: Platform["reverse_proxy_empty_account_behavior"] | null;
  reverse_proxy_fixed_account_header?: string | null;
  passive_circuit_breaker_disabled?: boolean | null;
};

/** normalizeStringList keeps a missing or null list an empty list. */
function normalizeStringList(raw: string[] | null | undefined): string[] {
  return Array.isArray(raw) ? raw.filter((value): value is string => typeof value === "string") : [];
}

type ApiPlatformLease = Partial<PlatformLease>;

function parseMissAction(raw: ApiPlatform["reverse_proxy_miss_action"]): Platform["reverse_proxy_miss_action"] {
  if (raw === "TREAT_AS_EMPTY" || raw === "REJECT") {
    return raw;
  }
  throw new Error(`invalid reverse_proxy_miss_action: ${String(raw)}`);
}

function normalizePlatform(raw: ApiPlatform): Platform {
  return {
    ...raw,
    reverse_proxy_miss_action: parseMissAction(raw.reverse_proxy_miss_action),
    regex_filters: normalizeStringList(raw.regex_filters),
    region_filters: normalizeStringList(raw.region_filters),
    ip_types: normalizeStringList(raw.ip_types),
    purity_bands: normalizeStringList(raw.purity_bands),
    subscription_filters: normalizeStringList(raw.subscription_filters),
    protocols: normalizeStringList(raw.protocols),
    routable_node_count: typeof raw.routable_node_count === "number" ? raw.routable_node_count : 0,
    reverse_proxy_empty_account_behavior:
      raw.reverse_proxy_empty_account_behavior === "RANDOM" ||
      raw.reverse_proxy_empty_account_behavior === "FIXED_HEADER" ||
      raw.reverse_proxy_empty_account_behavior === "ACCOUNT_HEADER_RULE"
        ? raw.reverse_proxy_empty_account_behavior
        : "RANDOM",
    reverse_proxy_fixed_account_header:
      typeof raw.reverse_proxy_fixed_account_header === "string" ? raw.reverse_proxy_fixed_account_header : "",
    passive_circuit_breaker_disabled:
      typeof raw.passive_circuit_breaker_disabled === "boolean" ? raw.passive_circuit_breaker_disabled : false,
  };
}

function normalizePlatformPage(raw: PageResponse<ApiPlatform>): PageResponse<Platform> {
  return {
    ...raw,
    items: raw.items.map(normalizePlatform),
  };
}

function normalizeLease(raw: ApiPlatformLease): PlatformLease {
  return {
    platform_id: typeof raw.platform_id === "string" ? raw.platform_id : "",
    account: typeof raw.account === "string" ? raw.account : "",
    node_hash: typeof raw.node_hash === "string" ? raw.node_hash : "",
    node_tag: typeof raw.node_tag === "string" ? raw.node_tag : "",
    egress_ip: typeof raw.egress_ip === "string" ? raw.egress_ip : "",
    expiry: typeof raw.expiry === "string" ? raw.expiry : "",
    last_accessed: typeof raw.last_accessed === "string" ? raw.last_accessed : "",
  };
}

function normalizeLeasePage(raw: PageResponse<ApiPlatformLease>): PageResponse<PlatformLease> {
  return {
    ...raw,
    items: raw.items.map(normalizeLease),
  };
}

export type ListPlatformsPageInput = {
  limit?: number;
  offset?: number;
  keyword?: string;
};

export async function listPlatforms(input: ListPlatformsPageInput = {}): Promise<PageResponse<Platform>> {
  const query = new URLSearchParams({
    limit: String(input.limit ?? 50),
    offset: String(input.offset ?? 0),
    sort_by: "name",
    sort_order: "asc",
  });
  const keyword = input.keyword?.trim();
  if (keyword) {
    query.set("keyword", keyword);
  }

  const data = await apiRequest<PageResponse<ApiPlatform>>(`${basePath}?${query.toString()}`);
  return normalizePlatformPage(data);
}

export async function getPlatform(id: string): Promise<Platform> {
  const data = await apiRequest<ApiPlatform>(`${basePath}/${id}`);
  return normalizePlatform(data);
}

export async function createPlatform(input: PlatformCreateInput): Promise<Platform> {
  const data = await apiRequest<ApiPlatform>(basePath, {
    method: "POST",
    body: input,
  });
  return normalizePlatform(data);
}

export async function updatePlatform(id: string, input: PlatformUpdateInput): Promise<Platform> {
  const data = await apiRequest<ApiPlatform>(`${basePath}/${id}`, {
    method: "PATCH",
    body: input,
  });
  return normalizePlatform(data);
}

/**
 * listPlatformNodeFacets returns the option lists of the platform form, derived
 * from the live node pool (every value is one the pool actually carries).
 */
export async function listPlatformNodeFacets(): Promise<PlatformNodeFacets> {
  const data = await apiRequest<PlatformNodeFacets>(`${basePath}/node-facets`);
  return {
    total_nodes: typeof data.total_nodes === "number" ? data.total_nodes : 0,
    scanned: typeof data.scanned === "number" ? data.scanned : 0,
    truncated: Boolean(data.truncated),
    regions: normalizeStringList(data.regions),
    ip_types: normalizeStringList(data.ip_types),
    purity_bands: normalizeStringList(data.purity_bands),
    protocols: normalizeStringList(data.protocols),
    subscriptions: Array.isArray(data.subscriptions)
      ? data.subscriptions.map((sub) => ({
          id: String(sub.id ?? ""),
          name: String(sub.name ?? ""),
          enabled: Boolean(sub.enabled),
          node_count: typeof sub.node_count === "number" ? sub.node_count : 0,
        }))
      : [],
  };
}

/**
 * previewPlatformScope asks the backend how many nodes the given criteria would
 * load. The count comes from the same admission predicate the routable-view
 * rebuild uses, so it is exactly what the platform would load; `truncated` says
 * the bounded scan stopped early and the count is a lower bound.
 */
export async function previewPlatformScope(spec: PlatformScopeSpec): Promise<PlatformScopePreview> {
  const data = await apiRequest<PlatformScopePreview>(`${basePath}/preview-scope`, {
    method: "POST",
    body: { platform_spec: spec },
  });
  return {
    matched: typeof data.matched === "number" ? data.matched : 0,
    scanned: typeof data.scanned === "number" ? data.scanned : 0,
    truncated: Boolean(data.truncated),
    sample: Array.isArray(data.sample) ? data.sample : [],
    excluded_by: data.excluded_by ?? {},
  };
}

export async function deletePlatform(id: string): Promise<void> {
  await apiRequest<void>(`${basePath}/${id}`, {
    method: "DELETE",
  });
}

export async function resetPlatform(id: string): Promise<Platform> {
  const data = await apiRequest<ApiPlatform>(`${basePath}/${id}/actions/reset-to-default`, {
    method: "POST",
  });
  return normalizePlatform(data);
}

export async function rebuildPlatform(id: string): Promise<void> {
  await apiRequest<{ status: "ok" }>(`${basePath}/${id}/actions/rebuild-routable-view`, {
    method: "POST",
  });
}

export async function listPlatformLeases(id: string, input: ListPlatformLeasesInput = {}): Promise<PageResponse<PlatformLease>> {
  const query = new URLSearchParams({
    limit: String(input.limit ?? 50),
    offset: String(input.offset ?? 0),
    sort_by: input.sort_by ?? "expiry",
    sort_order: input.sort_order ?? "asc",
  });

  const account = input.account?.trim();
  if (account) {
    query.set("account", account);
  }
  if (input.fuzzy !== undefined) {
    query.set("fuzzy", String(input.fuzzy));
  }

  const data = await apiRequest<PageResponse<ApiPlatformLease>>(`${basePath}/${id}/leases?${query.toString()}`);
  return normalizeLeasePage(data);
}

export async function deletePlatformLease(id: string, account: string): Promise<void> {
  await apiRequest<void>(`${basePath}/${id}/leases/${encodeURIComponent(account)}`, {
    method: "DELETE",
  });
}

export async function clearAllPlatformLeases(id: string): Promise<void> {
  await apiRequest<void>(`${basePath}/${id}/leases`, {
    method: "DELETE",
  });
}
