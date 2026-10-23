import { apiRequest } from "../../../lib/api-client";
import { listPlatformLeases } from "../../platforms/api";
import type { Platform, PlatformLease } from "../../platforms/types";

/**
 * Data clients of the node detail page. Two of the page's regions read from
 * endpoints that no other page reads (`/intel/nodes/{hash}`, and the per-node
 * lease view assembled from the platforms' own lease pages), so their shapes
 * and their one aggregation live here.
 */

// ---- GET /api/v1/intel/nodes/{hash} ---------------------------------------

/** One row of egress_history: an IP the node was seen leaving from. */
export type IntelEgressHistoryEntry = {
  id: number;
  node_hash: string;
  family: number;
  ip: string;
  observed_at_ns: number;
};

/** One row of node_checks: an unlock/availability result seen through the node. */
export type IntelNodeCheck = {
  node_hash: string;
  check_id: string;
  check_version: number;
  egress_ip: string;
  outcome: string;
  region: string;
  detail_json: string;
  latency_ms: number;
  observed_at_ns: number;
  valid_until_ns: number;
};

/** The node's current egress record. */
export type IntelNodeEgress = {
  node_hash: string;
  ipv4: string;
  ipv6: string;
  colo: string;
  loc: string;
  v4_observed_ns: number;
  v6_observed_ns: number;
  v6_checked_ns: number;
};

export type IntelNodeDetail = {
  node_hash: string;
  egress: IntelNodeEgress;
  history: IntelEgressHistoryEntry[];
  checks: IntelNodeCheck[];
};

/** nanoseconds (the intel store's clock) to the milliseconds the UI formats. */
export function nsToMs(ns: number): number {
  return Number.isFinite(ns) ? Math.round(ns / 1_000_000) : 0;
}

function stringOr(raw: unknown): string {
  return typeof raw === "string" ? raw : "";
}

function numberOr(raw: unknown): number {
  return typeof raw === "number" && Number.isFinite(raw) ? raw : 0;
}

function normalizeIntelDetail(raw: Record<string, unknown>): IntelNodeDetail {
  const egress = (raw.egress ?? {}) as Record<string, unknown>;
  const history = Array.isArray(raw.history) ? raw.history : [];
  const checks = Array.isArray(raw.checks) ? raw.checks : [];
  return {
    node_hash: stringOr(raw.node_hash),
    egress: {
      node_hash: stringOr(egress.node_hash),
      ipv4: stringOr(egress.ipv4),
      ipv6: stringOr(egress.ipv6),
      colo: stringOr(egress.colo),
      loc: stringOr(egress.loc),
      v4_observed_ns: numberOr(egress.v4_observed_ns),
      v6_observed_ns: numberOr(egress.v6_observed_ns),
      v6_checked_ns: numberOr(egress.v6_checked_ns),
    },
    history: history.map((entry) => {
      const row = (entry ?? {}) as Record<string, unknown>;
      return {
        id: numberOr(row.id),
        node_hash: stringOr(row.node_hash),
        family: numberOr(row.family),
        ip: stringOr(row.ip),
        observed_at_ns: numberOr(row.observed_at_ns),
      };
    }),
    checks: checks.map((entry) => {
      const row = (entry ?? {}) as Record<string, unknown>;
      return {
        node_hash: stringOr(row.node_hash),
        check_id: stringOr(row.check_id),
        check_version: numberOr(row.check_version),
        egress_ip: stringOr(row.egress_ip),
        outcome: stringOr(row.outcome),
        region: stringOr(row.region),
        detail_json: stringOr(row.detail_json),
        latency_ms: numberOr(row.latency_ms),
        observed_at_ns: numberOr(row.observed_at_ns),
        valid_until_ns: numberOr(row.valid_until_ns),
      };
    }),
  };
}

export async function getIntelNodeDetail(hash: string, signal?: AbortSignal): Promise<IntelNodeDetail> {
  const raw = await apiRequest<Record<string, unknown>>(`/api/v1/intel/nodes/${encodeURIComponent(hash)}`, { signal });
  return normalizeIntelDetail(raw ?? {});
}

// ---- the node's leases -----------------------------------------------------

/**
 * One lease of this node, with the facts of the platform that holds it: the
 * lease pages are per-platform, so the platform name and its sticky TTL (the
 * denominator of the remaining-time bar) travel with the row.
 */
export type NodeLeaseRow = PlatformLease & {
  platform_name: string;
  sticky_ttl: string;
};

export type NodeLeaseCollection = {
  leases: NodeLeaseRow[];
  /** How many platforms were actually scanned (bounded, see below). */
  scanned: number;
  totalPlatforms: number;
};

/**
 * How many platforms' lease pages one view may read. The leases endpoint is
 * per-platform, so "this node's leases" means reading every platform's page and
 * keeping the rows that name the node; an instance with more platforms than the
 * cap gets an honest "scan bounded" note instead of an unbounded fan-out.
 */
const MAX_LEASE_PLATFORMS = 40;

/** Lease pages read in small batches, so one slow platform does not hold the page. */
const LEASE_FETCH_CONCURRENCY = 6;

/**
 * collectNodeLeases gathers every lease whose `node_hash` names this node, by
 * reading the lease page of each platform (sorted by earliest expiry, as the
 * panel shows them). It reports how many platforms it could scan so the caller
 * can say so when the list was bounded.
 */
export async function collectNodeLeases(
  hash: string,
  platforms: Platform[],
  signal?: AbortSignal,
): Promise<NodeLeaseCollection> {
  const scoped = platforms.slice(0, MAX_LEASE_PLATFORMS);
  const rows: NodeLeaseRow[] = [];
  let failures = 0;
  let firstError: unknown = null;

  for (let start = 0; start < scoped.length; start += LEASE_FETCH_CONCURRENCY) {
    const batch = scoped.slice(start, start + LEASE_FETCH_CONCURRENCY);
    const pages = await Promise.all(
      batch.map(async (platform) => {
        try {
          const page = await listPlatformLeases(platform.id, {
            limit: 100_000,
            sort_by: "expiry",
            sort_order: "asc",
          });
          return {
            ok: true as const,
            error: null as unknown,
            matches: page.items
              .filter((lease) => lease.node_hash === hash)
              .map((lease): NodeLeaseRow => ({
                ...lease,
                platform_name: platform.name,
                sticky_ttl: platform.sticky_ttl,
              })),
          };
        } catch (error) {
          if (signal?.aborted) throw error;
          // One unreadable platform must not blank the whole list; its rows are
          // simply missing — unless every platform failed, which is a broken
          // session rather than an empty list and has to surface as an error.
          return { ok: false as const, error, matches: [] as NodeLeaseRow[] };
        }
      }),
    );
    for (const page of pages) {
      if (page.ok) rows.push(...page.matches);
      else {
        failures += 1;
        firstError = firstError ?? page.error;
      }
    }
  }

  if (scoped.length > 0 && failures === scoped.length) {
    throw firstError ?? new Error("lease scan failed");
  }

  rows.sort((a, b) => Date.parse(a.expiry || "") - Date.parse(b.expiry || ""));
  return { leases: rows, scanned: scoped.length, totalPlatforms: platforms.length };
}
