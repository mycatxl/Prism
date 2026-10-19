import { apiRequest } from "../../lib/api-client";
import type {
  EnvConfig,
  PurityBands,
  RuntimeConfig,
  RuntimeConfigPatch,
  SystemConfigView,
} from "./types";

const path = "/api/v1/system/config";

/*
 * One endpoint, one cache entry.
 *
 * Both urls below are read by several pages: the shell's weak-token warning, the
 * request log and the platform access panel all need the deployment values, and
 * the system config page and the policy page both edit the runtime config. A key
 * per page made them fetch the same url independently and, worse, made a save
 * invalidate only the copy its own page was reading. These keys are the single
 * answer for "which cache entry is this endpoint", so every reader shares one
 * fetch and every save can invalidate all of them at once.
 */
export const SYSTEM_CONFIG_QUERY_KEY = ["system-config"] as const;
export const SYSTEM_CONFIG_DEFAULT_QUERY_KEY = ["system-config-default"] as const;
export const SYSTEM_ENV_CONFIG_QUERY_KEY = ["system-config-env"] as const;

/*
 * The runtime config can be changed from the panel, so a reader that mounts after
 * a save inside this window is still reading the previous answer; it stays short.
 * The env config is fixed at boot - there is nothing to poll for - so its window
 * is long enough that walking between pages never re-asks, while an explicit
 * invalidate (which always refetches) still wins.
 */
export const SYSTEM_CONFIG_STALE_MS = 30_000;
export const SYSTEM_ENV_STALE_MS = 60_000;

const DEFAULT_CONFIG: RuntimeConfig = {
  request_log_enabled: true,
  reverse_proxy_log_detail_enabled: false,
  reverse_proxy_log_req_headers_max_bytes: 0,
  reverse_proxy_log_req_body_max_bytes: 0,
  reverse_proxy_log_resp_headers_max_bytes: 0,
  reverse_proxy_log_resp_body_max_bytes: 0,
  max_consecutive_failures: 0,
  max_latency_test_interval: "",
  max_authority_latency_test_interval: "",
  max_egress_test_interval: "",
  latency_test_url: "",
  latency_authorities: [],
  p2c_latency_window: "",
  latency_decay_window: "",
  cache_flush_interval: "",
  cache_flush_dirty_threshold: 0,
};

function asNumber(raw: unknown, fallback: number): number {
  const value = Number(raw);
  if (!Number.isFinite(value)) {
    return fallback;
  }
  return value;
}

function asString(raw: unknown, fallback: string): string {
  if (typeof raw !== "string") {
    return fallback;
  }
  return raw;
}

function asOptionalNumber(raw: unknown): number | undefined {
  if (raw === null || raw === undefined || raw === "") {
    return undefined;
  }
  const value = Number(raw);
  return Number.isFinite(value) ? value : undefined;
}

/*
 * The policy slice of the same response. It is passed through as the server sent
 * it: a runtime form has no opinion about retention windows or purity bands, but
 * the settings page edits them, and since both now read one cache entry this
 * function must not drop what it does not recognise.
 */
function asPurityBands(raw: unknown): PurityBands | undefined {
  return raw && typeof raw === "object" ? (raw as PurityBands) : undefined;
}

function normalizeSystemConfig(raw: Partial<SystemConfigView> | null | undefined): SystemConfigView {
  if (!raw) {
    return { ...DEFAULT_CONFIG };
  }

  return {
    request_log_enabled: Boolean(raw.request_log_enabled),
    reverse_proxy_log_detail_enabled: Boolean(raw.reverse_proxy_log_detail_enabled),
    reverse_proxy_log_req_headers_max_bytes: asNumber(
      raw.reverse_proxy_log_req_headers_max_bytes,
      DEFAULT_CONFIG.reverse_proxy_log_req_headers_max_bytes,
    ),
    reverse_proxy_log_req_body_max_bytes: asNumber(
      raw.reverse_proxy_log_req_body_max_bytes,
      DEFAULT_CONFIG.reverse_proxy_log_req_body_max_bytes,
    ),
    reverse_proxy_log_resp_headers_max_bytes: asNumber(
      raw.reverse_proxy_log_resp_headers_max_bytes,
      DEFAULT_CONFIG.reverse_proxy_log_resp_headers_max_bytes,
    ),
    reverse_proxy_log_resp_body_max_bytes: asNumber(
      raw.reverse_proxy_log_resp_body_max_bytes,
      DEFAULT_CONFIG.reverse_proxy_log_resp_body_max_bytes,
    ),
    max_consecutive_failures: asNumber(raw.max_consecutive_failures, DEFAULT_CONFIG.max_consecutive_failures),
    max_latency_test_interval: asString(raw.max_latency_test_interval, DEFAULT_CONFIG.max_latency_test_interval),
    max_authority_latency_test_interval: asString(
      raw.max_authority_latency_test_interval,
      DEFAULT_CONFIG.max_authority_latency_test_interval,
    ),
    max_egress_test_interval: asString(raw.max_egress_test_interval, DEFAULT_CONFIG.max_egress_test_interval),
    latency_test_url: asString(raw.latency_test_url, DEFAULT_CONFIG.latency_test_url),
    latency_authorities: Array.isArray(raw.latency_authorities)
      ? raw.latency_authorities.filter((item): item is string => typeof item === "string")
      : DEFAULT_CONFIG.latency_authorities,
    p2c_latency_window: asString(raw.p2c_latency_window, DEFAULT_CONFIG.p2c_latency_window),
    latency_decay_window: asString(raw.latency_decay_window, DEFAULT_CONFIG.latency_decay_window),
    cache_flush_interval: asString(raw.cache_flush_interval, DEFAULT_CONFIG.cache_flush_interval),
    cache_flush_dirty_threshold: asNumber(
      raw.cache_flush_dirty_threshold,
      DEFAULT_CONFIG.cache_flush_dirty_threshold,
    ),
    // The policy slice, passed through untouched.
    audit_retention_days: asOptionalNumber(raw.audit_retention_days),
    usage_retention_days: asOptionalNumber(raw.usage_retention_days),
    purity_bands: asPurityBands(raw.purity_bands),
    registration_mode: typeof raw.registration_mode === "string" ? raw.registration_mode : undefined,
  };
}

export async function getSystemConfig(): Promise<SystemConfigView> {
  const data = await apiRequest<SystemConfigView>(path);
  return normalizeSystemConfig(data);
}

export async function getDefaultSystemConfig(): Promise<SystemConfigView> {
  const data = await apiRequest<SystemConfigView>(path + "/default");
  return normalizeSystemConfig(data);
}

export async function patchSystemConfig(patch: RuntimeConfigPatch): Promise<SystemConfigView> {
  const data = await apiRequest<SystemConfigView>(path, {
    method: "PATCH",
    body: patch,
  });
  return normalizeSystemConfig(data);
}

export async function getEnvConfig(): Promise<EnvConfig> {
  return await apiRequest<EnvConfig>(path + "/env");
}
