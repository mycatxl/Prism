export type PlanStatus = "active" | "disabled";

export type PlanWrite = {
  name: string;
  platforms: string[];
  ip_tiers: string[];
  traffic_bytes: number;
  period: string;
  max_concurrent: number;
  max_rps: number;
  max_bandwidth_bps: number;
  max_keys: number;
  duration_days: number;
  status: PlanStatus;
};

export type PlanPatch = Partial<PlanWrite> & {
  apply_to_existing?: boolean;
};

export type AdminPlan = PlanWrite & {
  id: string;
  active_user_count: number;
  created_at_ns: number;
  updated_at_ns: number;
};

export type WritePage<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
};

export type AdminUserSubscription = {
  id: string;
  plan_id: string;
  plan_name: string;
  status: string;
  starts_at_ns: number;
  expires_at_ns: number;
  period_start_ns: number;
  period_used_bytes: number;
  traffic_bytes: number;
  remaining_bytes: number | null;
};

export type AdminUser = {
  id: string;
  username: string;
  role: "admin" | "user" | string;
  status: "active" | "disabled" | string;
  has_totp: boolean;
  created_at_ns: number;
  updated_at_ns: number;
  last_login_ns: number;
  subscription: AdminUserSubscription | null;
  key_count: number;
};

export type CreateUserInput = {
  username: string;
  password?: string;
  role: string;
  plan_id?: string;
  duration_days?: number;
};
export type AdminKey = {
  id: string;
  user_id: string;
  name: string;
  scope?: string;
  status: string;
  platforms: string[];
  ip_allowlist: string[];
  expires_at_ns: number;
  last_used_ns: number;
  created_at_ns: number;
  credential_hint: string;
};

export type AdminCreateKeyResponse = {
  key: AdminKey;
  credential: string;
};

export type UsageDay = {
  day_ns: number;
  ingress_bytes: number;
  egress_bytes: number;
  total_bytes: number;
};

export type AdminUserDetail = AdminUser & {
  keys: AdminKey[];
  usage_daily: UsageDay[];
};

export type AdminSubscription = {
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
};

export type AdminInvite = {
  id: string;
  code: string;
  plan_id: string;
  plan_name: string;
  max_uses: number;
  used_count: number;
  status: string;
  expires_at_ns: number;
  note: string;
  created_at_ns: number;
};
export type CreateInviteInput = {
  plan_id?: string;
  max_uses?: number;
  expires_in_days?: number;
  note?: string;
};

export type AdminUsageSummary = {
  active_users: number;
  total_users: number;
  today_bytes: number;
  period_bytes: number;
  expiring_soon_users: number;
  expiring_within_days: number;
};

export type AdminUsageUserItem = {
  user_id: string;
  username: string;
  plan_name: string;
  total_bytes: number;
  ingress_bytes: number;
  egress_bytes: number;
  period_used_bytes: number;
  traffic_bytes: number;
  remaining_bytes: number | null;
  expires_at_ns: number;
};

export type AdminUsageUsersResponse = {
  items: AdminUsageUserItem[];
};

export type AdminUsageDailyResponse = {
  items: UsageDay[];
};

export type AdminUsagePlatformItem = {
  platform_id: string;
  platform_name: string;
  total_bytes: number;
};

export type AdminUsagePlatformsResponse = {
  items: AdminUsagePlatformItem[];
};

/** One plan's footprint. `plan_id` is empty for the row that collects accounts with no active subscription. */
export type AdminUsagePlanItem = {
  plan_id: string;
  plan_name: string;
  subscribers: number;
  total_bytes: number;
  period_bytes: number;
};

export type AdminUsagePlansResponse = {
  items: AdminUsagePlanItem[];
};

/** One usage-anomaly detection. `acknowledged_at_ns` is 0 while the alert is still open. */
export type AdminUsageAlertItem = {
  id: string;
  kind: string;
  user_id: string;
  username: string;
  plan_id: string;
  plan_name: string;
  window_minutes: number;
  threshold_ratio: number;
  used_bytes: number;
  quota_bytes: number;
  detected_at_ns: number;
  acknowledged_at_ns: number;
  note: string;
};

export type AdminUsageAlertsResponse = {
  items: AdminUsageAlertItem[];
};

// The four thresholds the server accepts (internal/config/runtime.go PurityBands).
// Anything below `mixed` is the `poor` *result* band; it is not configurable.
export type PurityBands = {
  excellent: number;
  clean: number;
  fair: number;
  mixed: number;
};

export type AdminSystemConfig = {
  audit_retention_days: number;
  usage_retention_days: number;
  purity_bands: PurityBands;
  [key: string]: unknown;
};

export type AdminSystemConfigPatch = {
  audit_retention_days?: number;
  usage_retention_days?: number;
  purity_bands?: PurityBands;
};

// A redemption code as the console lists it. The plaintext is not here: it is
// returned once when the code is minted and never stored, so `code_hint` is the
// only part that can be shown afterwards.
export interface AdminRedeemCode {
  id: string;
  code_hint: string;
  amount_micro: number;
  max_uses: number;
  used_count: number;
  status: string;
  expires_at_ns: number;
  note: string;
  created_at_ns: number;
}

// A freshly minted code: the listing shape plus the one-time plaintext.
export interface AdminRedeemCodeItem extends AdminRedeemCode {
  code: string;
}

export interface AdminRedeemCodeBatch {
  codes: AdminRedeemCodeItem[];
}

export interface CreateRedeemCodeInput {
  amount_micro: number;
  max_uses?: number;
  expires_in_days?: number;
  note?: string;
  count?: number;
}

export interface AdminBalanceEntry {
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

export interface AdminBalanceView {
  balance_micro: number;
  ledger: AdminBalanceEntry[] | null;
}
