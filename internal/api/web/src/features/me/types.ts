export interface SelfPlatform {
  platform_id: string;
  platform_name: string;
}

export interface SubscriptionView {
  id: string;
  user_id: string;
  username: string;
  plan_id: string;
  plan_name: string;
  status: string;
  starts_at_ns: number;
  expires_at_ns: number;
  period_start_ns: number;
  period_used_bytes: number;
  traffic_bytes: number;
  remaining_bytes: number | null;
}

export interface UsageDay {
  day_ns: number;
  ingress_bytes: number;
  egress_bytes: number;
  total_bytes: number;
}

export interface SelfOverview {
  subscription: SubscriptionView | null;
  traffic_bytes: number;
  remaining_bytes: number | null;
  period_used_bytes: number;
  max_concurrent: number;
  max_keys: number;
  active_keys: number;
  live_concurrent: number;
  usage_daily: UsageDay[];
  platforms: SelfPlatform[];
}

export interface SessionView {
  id: string;
  created_at_ns: number;
  expires_at_ns: number;
  ip: string;
  user_agent: string;
  current: boolean;
}

export interface AccessKeyView {
  id: string;
  user_id: string;
  name: string;
  scope: string;
  status: string;
  platforms: string[];
  ip_allowlist: string[];
  expires_at_ns: number;
  last_used_ns: number;
  created_at_ns: number;
  credential_hint: string;
}

export type AccessKeyInput = {
  name: string;
  platforms?: string[];
  ip_allowlist?: string[];
  expires_in_days?: number;
};

export interface CreateKeyResponse {
  key: AccessKeyView;
  credential: string;
}

export interface SubscriptionResponse {
  subscription: SubscriptionView | null;
}

export interface UsageResponse {
  items: UsageDay[];
}

export interface KeysResponse {
  items: AccessKeyView[];
}

export interface SessionsResponse {
  items: SessionView[];
}
