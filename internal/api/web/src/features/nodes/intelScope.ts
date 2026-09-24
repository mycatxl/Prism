import type { IntelJobScope, IntelJobScopeFilter } from "../jobs/types";

// The node pool URL carries more filters than a job scope accepts, so this
// module is the single place that maps one onto the other
// (POST /api/v1/intel/jobs, internal/intel/jobs/jobs.go:132, and
// docs/plan/08-intel-store-jobs.md:181 "filter 的键与 GET /api/v1/nodes 的查询参数一致").
//
// Two rules shape the mapping:
//   - scope entries are unioned by the server, so `all` is only sent when the
//     URL selects nothing else;
//   - every scope carries filter.healthy, because a manual intel run must never
//     spend provider quota on nodes that are not healthy.

/** URL filters the scope accepts under the same name. */
const SCOPE_FILTER_KEYS = ["protocol", "region", "ip_type", "purity_band", "verdict"] as const;

/** URL filters the scope has no equivalent for (yet). */
const UNSUPPORTED_FILTER_KEYS = [
  "tag_keyword",
  "tag",
  "egress_ip",
  "quality_state",
  "risk_grade",
  "purity_min",
  "purity_max",
  "confidence_min",
  "native",
  "asn",
  "country",
  "check",
  "probed_since",
] as const;

function param(params: URLSearchParams, key: string): string {
  return (params.get(key) || "").trim();
}

/**
 * buildBulkIntelScope turns the current node pool URL into the scope of a bulk
 * intel job: `subscription_id` / `platform_id` select nodes by source, the
 * supported intel filters go into `filter`, and `all` is added when the URL
 * selects nothing else.
 */
export function buildBulkIntelScope(params: URLSearchParams): IntelJobScope {
  const filter: IntelJobScopeFilter = { healthy: "true" };
  const mapped = SCOPE_FILTER_KEYS.filter((key) => param(params, key) !== "");
  for (const key of mapped) {
    filter[key] = param(params, key);
  }

  const scope: IntelJobScope = { filter };
  const subscriptionID = param(params, "subscription_id");
  if (subscriptionID) scope.subscription_ids = [subscriptionID];
  const platformID = param(params, "platform_id");
  if (platformID) scope.platform_ids = [platformID];
  if (mapped.length === 0 && !subscriptionID && !platformID) {
    scope.all = true;
  }
  return scope;
}

/**
 * hasUnsupportedFilters reports whether the URL narrows the list in a way a
 * bulk intel job cannot reproduce; the button then warns that the job covers
 * more nodes than the list shows.
 */
export function hasUnsupportedFilters(params: URLSearchParams): boolean {
  return UNSUPPORTED_FILTER_KEYS.some((key) => param(params, key) !== "");
}
