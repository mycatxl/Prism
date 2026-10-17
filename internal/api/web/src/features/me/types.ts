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
  /** The customer's own switch; it lives on the subscription, not in the snapshot. */
  auto_renew: boolean;
  /** How many periods this subscription has been extended for. */
  period_index: number;
  /** What one period costs — the snapshot's price, which is what a renewal charges. */
  price_micro: number;
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

/** A plan a customer may buy: active and priced. */
export interface PurchasablePlan {
  id: string;
  name: string;
  platforms: string[] | null;
  ip_tiers: string[] | null;
  traffic_bytes: number;
  period: string;
  duration_days: number;
  price_micro: number;
}

/**
 * What a purchase answers with. `charged_micro` is zero on a replay, which is
 * what makes "did I just get charged twice" answerable from the response.
 */
export interface PurchaseResult {
  subscription: SubscriptionView;
  balance_micro: number;
  charged_micro: number;
  replayed: boolean;
}

/** One sticky lease as its owner sees it. The node hash is deliberately absent. */
export interface SelfLease {
  platform_id: string;
  platform_name: string;
  account: string;
  node_tag: string;
  egress_ip: string;
  expiry: string;
  last_accessed: string;
}

export interface BalanceEntry {
  id: string;
  user_id: string;
  delta_micro: number;
  balance_after_micro: number;
  reason: string;
  ref_id: string;
  request_id: string;
  note: string;
  actor: string;
  created_at_ns: number;
}

export interface BalanceView {
  balance_micro: number;
  ledger: BalanceEntry[] | null;
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

export interface KeysResponse {
  items: AccessKeyView[];
}

export interface SessionsResponse {
  items: SessionView[];
}
