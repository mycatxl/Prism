export type RuntimeConfig = {
  request_log_enabled: boolean;
  reverse_proxy_log_detail_enabled: boolean;
  reverse_proxy_log_req_headers_max_bytes: number;
  reverse_proxy_log_req_body_max_bytes: number;
  reverse_proxy_log_resp_headers_max_bytes: number;
  reverse_proxy_log_resp_body_max_bytes: number;
  max_consecutive_failures: number;
  max_latency_test_interval: string;
  max_authority_latency_test_interval: string;
  max_egress_test_interval: string;
  latency_test_url: string;
  latency_authorities: string[];
  p2c_latency_window: string;
  latency_decay_window: string;
  cache_flush_interval: string;
  cache_flush_dirty_threshold: number;
};

export type EnvConfig = {
  cache_dir: string;
  state_dir: string;
  log_dir: string;
  listen_address: string;
  prism_port: number;
  api_max_body_bytes: number;
  max_latency_table_entries: number;
  probe_concurrency: number;
  geoip_update_schedule: string;
  default_platform_sticky_ttl: string;
  default_platform_regex_filters: string[] | null;
  default_platform_region_filters: string[] | null;
  default_platform_reverse_proxy_miss_action: string;
  default_platform_reverse_proxy_empty_account_behavior: string;
  default_platform_reverse_proxy_fixed_account_header: string;
  default_platform_allocation_policy: string;
  probe_timeout: string;
  resource_fetch_timeout: string;
  node_dns_upstreams: string[] | null;
  proxy_transport_max_idle_conns: number;
  proxy_transport_max_idle_conns_per_host: number;
  proxy_transport_idle_conn_timeout: string;
  proxy_bypass_rules: string[] | null;
  request_log_queue_size: number;
  request_log_queue_flush_batch_size: number;
  request_log_queue_flush_interval: string;
  request_log_db_max_mb: number;
  request_log_db_retain_count: number;
  metric_throughput_interval_seconds: number;
  metric_throughput_retention_seconds: number;
  metric_bucket_seconds: number;
  metric_connections_interval_seconds: number;
  metric_connections_retention_seconds: number;
  metric_leases_interval_seconds: number;
  metric_leases_retention_seconds: number;
  metric_latency_bin_width_ms: number;
  metric_latency_bin_overflow_ms: number;
  proxy_token_set: boolean;
  proxy_token_weak: boolean;
  auth_version: string;
  /**
   * The deployment's PRISM_REGISTRATION policy, which is what a runtime override
   * falls back to. It sits on the env endpoint, not on the runtime config.
   */
  registration: string;
};

/**
 * The four purity thresholds the server accepts (internal/config/runtime.go
 * PurityBands). Anything below `mixed` is the `poor` *result* band; it is not
 * configurable.
 */
export type PurityBands = {
  excellent: number;
  clean: number;
  fair: number;
  mixed: number;
};

/** The sign-up policies the server accepts (internal/config RegistrationMode). */
export type RegistrationMode = "off" | "invite" | "open";

/**
 * Everything GET /api/v1/system/config answers: the runtime form's fields *and*
 * the operator policy (retention windows, purity bands, sign-up mode). They are
 * different slices of one response, read by different pages, so they share one
 * type - and therefore one fetch and one cache entry.
 */
export type SystemConfigView = RuntimeConfig & {
  audit_retention_days?: number;
  usage_retention_days?: number;
  purity_bands?: PurityBands;
  /** `""` clears the panel override; absent means this server carries none. */
  registration_mode?: string;
};

/** A PATCH body: any subset of what the same url answers. */
export type RuntimeConfigPatch = Partial<SystemConfigView>;
