// WP09 §4/§5.4: the types of the intel data source and unlock check settings.
// A credential is never part of a response: only has_key is (R6).

export type IntelProviderUsage = {
  day: string;
  used: number;
  remaining: number;
  exhausted: boolean;
  next_request_at_ns: number;
  next_allowed_at_ns: number;
  blocked_until_ns: number;
  paused: boolean;
  error_code: string;
  queued: number;
  running: number;
  done: number;
  failed: number;
};

export type IntelProvider = {
  id: string;
  name: string;
  website?: string;
  terms?: string;
  category: string;
  via_node: boolean;
  direct: boolean;
  profile: string;
  batch_size: number;
  requires_key: boolean;
  supports_ipv6: boolean;
  default_enabled: boolean;
  default_daily_limit: number;
  default_daily_limit_with_key?: number;
  default_qps: number;
  default_ttl: string;
  max_daily_limit?: number;
  credential_fields?: string[];
  enabled: boolean;
  runnable: boolean;
  has_key: boolean;
  daily_limit: number;
  qps: number;
  ttl: string;
  config: Record<string, unknown>;
  source: string;
  updated_at_ns: number;
  usage: IntelProviderUsage;
};

export type IntelProviderPage = {
  items: IntelProvider[];
  total: number;
  limit: number;
  offset: number;
};

// IntelConfigValue mirrors the config_json values the API accepts.
export type IntelConfigValue = string | number | boolean | null | string[];

export type IntelProviderPatch = {
  enabled?: boolean;
  api_key?: string;
  daily_limit?: number;
  qps?: number;
  ttl?: string;
  config?: Record<string, IntelConfigValue>;
};

export type IntelCheck = {
  id: string;
  name: string;
  version: number;
  category: string;
  source: string;
  path?: string;
  calibrated?: string;
  ttl: string;
  timeout: string;
  steps: string[];
  enabled: boolean;
  enabled_override: boolean;
};

export type IntelCheckLoadError = {
  path?: string;
  error: string;
};

export type IntelCheckPage = {
  items: IntelCheck[];
  total: number;
  limit: number;
  offset: number;
  load_errors: IntelCheckLoadError[];
};
