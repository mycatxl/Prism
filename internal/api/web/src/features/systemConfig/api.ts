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
  ldc_enabled: false,
  ldc_gateway_url: "https://credit.linux.do/epay",
  ldc_pid: "",
  ldc_key: "",
  ldc_rate: 1,
  ldc_notify_url: "",
  ldc_return_url: "",
  tron_deposit_enabled: false,
  tron_address: "",
  tron_api_key: "",
  tron_usdt_contract: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
  tron_trx_enabled: false,
  tron_trx_ratio: 1,
  tron_window_minutes: 15,

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
    alert_telegram_enabled: Boolean(raw.alert_telegram_enabled),
    alert_telegram_bot_token: "",
    alert_telegram_bot_token_set: Boolean(raw.alert_telegram_bot_token_set),
    alert_telegram_chat_id: asString(raw.alert_telegram_chat_id, ""),
    alert_disk_free_percent: asNumber(raw.alert_disk_free_percent, 10),
    alert_cooldown_minutes: asNumber(raw.alert_cooldown_minutes, 15),
    payment_url_checks: Array.isArray(raw.payment_url_checks) ? raw.payment_url_checks.filter((check) => check && typeof check.field === "string" && typeof check.code === "string" && ["reachable", "warning"].includes(check.status)) : [],
    ldc_enabled: Boolean(raw.ldc_enabled),
    ldc_gateway_url: asString(raw.ldc_gateway_url, DEFAULT_CONFIG.ldc_gateway_url),
    ldc_pid: asString(raw.ldc_pid, DEFAULT_CONFIG.ldc_pid),
    ldc_key: asString(raw.ldc_key, DEFAULT_CONFIG.ldc_key),
    ldc_rate: asNumber(raw.ldc_rate, DEFAULT_CONFIG.ldc_rate),
    ldc_notify_url: asString(raw.ldc_notify_url, DEFAULT_CONFIG.ldc_notify_url),
    ldc_return_url: asString(raw.ldc_return_url, DEFAULT_CONFIG.ldc_return_url),
    tron_deposit_enabled: Boolean(raw.tron_deposit_enabled),
    tron_address: asString(raw.tron_address, DEFAULT_CONFIG.tron_address),
    tron_api_key: asString(raw.tron_api_key, DEFAULT_CONFIG.tron_api_key),
    tron_usdt_contract: asString(raw.tron_usdt_contract, DEFAULT_CONFIG.tron_usdt_contract),
    tron_trx_enabled: Boolean(raw.tron_trx_enabled),
    tron_trx_ratio: asNumber(raw.tron_trx_ratio, DEFAULT_CONFIG.tron_trx_ratio),
    tron_window_minutes: asNumber(raw.tron_window_minutes, DEFAULT_CONFIG.tron_window_minutes),
    ldc_key_set: Boolean(raw.ldc_key_set),
    tron_api_key_set: Boolean(raw.tron_api_key_set),
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
    node_privacy: raw.node_privacy,
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

export async function sendTestOperationalAlert(): Promise<{ delivered: boolean }> {
  return apiRequest("/api/v1/system/alerts/test", { method: "POST", body: {} });
}
