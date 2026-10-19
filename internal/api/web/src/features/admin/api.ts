import { apiRequest } from "../../lib/api-client";
import type {
  AdminCreateKeyResponse,
  AdminInvite,
  AdminKey,
  AdminPlan,
  AdminSubscription,
  AdminSystemConfig,
  AdminSystemConfigPatch,
  AdminSystemEnvConfig,
  AdminUsageAlertItem,
  AdminUsageAlertsResponse,
  AdminUsageDailyResponse,
  AdminUsagePlansResponse,
  AdminUsagePlatformsResponse,
  AdminUsageSummary,
  AdminUsageUsersResponse,
  AdminUser,
  AdminUserDetail,
  PlanPatch,
  PlanWrite,
  AdminBalanceView,
  AdminRedeemCode,
  AdminRedeemCodeBatch,
  CreateRedeemCodeInput,
  WritePage,
} from "./types";

function toQueryString(params: Record<string, string | number | undefined | null>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && value !== "") {
      search.set(key, String(value));
    }
  }
  const qs = search.toString();
  return qs ? `?${qs}` : "";
}

// ---------------------------------------------------------------------------
// 套餐 /api/v1/admin/plans
// ---------------------------------------------------------------------------

export async function listPlans(): Promise<{ items: AdminPlan[] }> {
  return apiRequest<{ items: AdminPlan[] }>("/api/v1/admin/plans");
}

export async function createPlan(input: PlanWrite): Promise<AdminPlan> {
  return apiRequest<AdminPlan>("/api/v1/admin/plans", {
    method: "POST",
    body: input,
  });
}

export async function updatePlan(id: string, patch: PlanPatch): Promise<AdminPlan> {
  return apiRequest<AdminPlan>(`/api/v1/admin/plans/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: patch,
  });
}

export async function deletePlan(id: string): Promise<void> {
  return apiRequest<void>(`/api/v1/admin/plans/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}

// ---------------------------------------------------------------------------
// 用户 /api/v1/admin/users
// ---------------------------------------------------------------------------

export type ListUsersParams = {
  q?: string;
  status?: string;
  role?: string;
  limit?: number;
  offset?: number;
};

export async function listUsers(params: ListUsersParams = {}): Promise<WritePage<AdminUser>> {
  const qs = toQueryString(params);
  return apiRequest<WritePage<AdminUser>>(`/api/v1/admin/users${qs}`);
}

export type CreateUserInput = {
  username: string;
  password?: string;
  role: string;
  plan_id?: string;
  duration_days?: number;
};

export async function createUser(input: CreateUserInput): Promise<AdminUser> {
  return apiRequest<AdminUser>("/api/v1/admin/users", {
    method: "POST",
    body: input,
  });
}

export async function getUser(id: string): Promise<AdminUserDetail> {
  return apiRequest<AdminUserDetail>(`/api/v1/admin/users/${encodeURIComponent(id)}`);
}

export type UpdateUserPatch = {
  status?: string;
  role?: string;
  password?: string;
};

export async function updateUser(id: string, patch: UpdateUserPatch): Promise<AdminUserDetail> {
  return apiRequest<AdminUserDetail>(`/api/v1/admin/users/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: patch,
  });
}

export async function deleteUser(id: string): Promise<void> {
  return apiRequest<void>(`/api/v1/admin/users/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}

// ---------------------------------------------------------------------------
// 用户密钥 /api/v1/admin/users/{id}/keys
// ---------------------------------------------------------------------------

export async function listUserKeys(userId: string): Promise<{ items: AdminKey[] }> {
  return apiRequest<{ items: AdminKey[] }>(`/api/v1/admin/users/${encodeURIComponent(userId)}/keys`);
}

export type CreateUserKeyInput = {
  name: string;
  platforms?: string[];
  ip_allowlist?: string[];
  expires_in_days?: number;
};

export async function createUserKey(
  userId: string,
  input: CreateUserKeyInput,
): Promise<AdminCreateKeyResponse> {
  return apiRequest<AdminCreateKeyResponse>(`/api/v1/admin/users/${encodeURIComponent(userId)}/keys`, {
    method: "POST",
    body: input,
  });
}

export async function deleteUserKey(userId: string, keyId: string): Promise<void> {
  return apiRequest<void>(
    `/api/v1/admin/users/${encodeURIComponent(userId)}/keys/${encodeURIComponent(keyId)}`,
    {
      method: "DELETE",
    },
  );
}

// ---------------------------------------------------------------------------
// 订阅 /api/v1/admin/subscriptions
// ---------------------------------------------------------------------------

export type ListSubscriptionsParams = {
  user_id?: string;
  status?: string;
  limit?: number;
  offset?: number;
};

export async function listSubscriptions(
  params: ListSubscriptionsParams = {},
): Promise<WritePage<AdminSubscription>> {
  const qs = toQueryString(params);
  return apiRequest<WritePage<AdminSubscription>>(`/api/v1/admin/subscriptions${qs}`);
}

export type CreateSubscriptionInput = {
  user_id: string;
  plan_id: string;
  duration_days?: number;
};

export async function createSubscription(input: CreateSubscriptionInput): Promise<AdminSubscription> {
  return apiRequest<AdminSubscription>("/api/v1/admin/subscriptions", {
    method: "POST",
    body: input,
  });
}

export type UpdateSubscriptionPatch = {
  status?: string;
  extend_days?: number;
  expires_at_ns?: number;
  plan_id?: string;
  duration_days?: number;
};

export async function updateSubscription(
  id: string,
  patch: UpdateSubscriptionPatch,
): Promise<AdminSubscription> {
  return apiRequest<AdminSubscription>(`/api/v1/admin/subscriptions/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: patch,
  });
}

export async function deleteSubscription(id: string): Promise<void> {
  return apiRequest<void>(`/api/v1/admin/subscriptions/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}

// ---------------------------------------------------------------------------
// 邀请码 /api/v1/admin/invites
// ---------------------------------------------------------------------------

export type ListInvitesParams = {
  limit?: number;
  offset?: number;
};

export async function listInvites(params: ListInvitesParams = {}): Promise<WritePage<AdminInvite>> {
  const qs = toQueryString(params);
  return apiRequest<WritePage<AdminInvite>>(`/api/v1/admin/invites${qs}`);
}

export type CreateInviteInput = {
  plan_id?: string;
  max_uses?: number;
  expires_in_days?: number;
  note?: string;
};

export async function createInvite(input: CreateInviteInput): Promise<AdminInvite> {
  return apiRequest<AdminInvite>("/api/v1/admin/invites", {
    method: "POST",
    body: input,
  });
}

export async function deleteInvite(id: string): Promise<void> {
  return apiRequest<void>(`/api/v1/admin/invites/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}

// ---------------------------------------------------------------------------
// 兑换码 /api/v1/admin/redeem-codes
// ---------------------------------------------------------------------------

export type ListRedeemCodesParams = {
  limit?: number;
  offset?: number;
};

export async function listRedeemCodes(
  params: ListRedeemCodesParams = {},
): Promise<WritePage<AdminRedeemCode>> {
  const qs = toQueryString(params);
  return apiRequest<WritePage<AdminRedeemCode>>(`/api/v1/admin/redeem-codes${qs}`);
}

export async function createRedeemCodes(
  input: CreateRedeemCodeInput,
): Promise<AdminRedeemCodeBatch> {
  return apiRequest<AdminRedeemCodeBatch>("/api/v1/admin/redeem-codes", {
    method: "POST",
    body: {
      amount_micro: input.amount_micro,
      max_uses: input.max_uses ?? 0,
      expires_in_days: input.expires_in_days ?? 0,
      note: input.note ?? "",
      count: input.count ?? 1,
    },
  });
}

export async function deleteRedeemCode(id: string): Promise<void> {
  return apiRequest<void>(`/api/v1/admin/redeem-codes/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}

// ---------------------------------------------------------------------------
// 余额 /api/v1/admin/users/{id}/balance
// ---------------------------------------------------------------------------

export async function getUserBalance(
  userID: string,
  limit?: number,
): Promise<AdminBalanceView> {
  const qs = toQueryString({ limit });
  return apiRequest<AdminBalanceView>(
    `/api/v1/admin/users/${encodeURIComponent(userID)}/balance${qs}`,
  );
}

export async function adjustUserBalance(
  userID: string,
  deltaMicro: number,
  note: string,
): Promise<AdminBalanceView> {
  return apiRequest<AdminBalanceView>(
    `/api/v1/admin/users/${encodeURIComponent(userID)}/balance`,
    { method: "POST", body: { delta_micro: deltaMicro, note } },
  );
}

// ---------------------------------------------------------------------------
// 用量 /api/v1/admin/usage
// ---------------------------------------------------------------------------

export async function getUsageSummary(): Promise<AdminUsageSummary> {
  return apiRequest<AdminUsageSummary>("/api/v1/admin/usage/summary");
}

export type GetUsageUsersParams = {
  from_ns?: number;
  to_ns?: number;
  limit?: number;
  days?: number;
};

export async function getUsageUsers(
  params: GetUsageUsersParams = {},
): Promise<AdminUsageUsersResponse> {
  const qs = toQueryString(params);
  return apiRequest<AdminUsageUsersResponse>(`/api/v1/admin/usage/users${qs}`);
}

export type GetUsageDailyParams = {
  days?: number;
};

export async function getUsageDaily(
  params: GetUsageDailyParams = {},
): Promise<AdminUsageDailyResponse> {
  const qs = toQueryString(params);
  return apiRequest<AdminUsageDailyResponse>(`/api/v1/admin/usage/daily${qs}`);
}

export type GetUsagePlatformsParams = {
  days?: number;
};

export async function getUsagePlatforms(
  params: GetUsagePlatformsParams = {},
): Promise<AdminUsagePlatformsResponse> {
  const qs = toQueryString(params);
  return apiRequest<AdminUsagePlatformsResponse>(`/api/v1/admin/usage/platforms${qs}`);
}

export type GetUsagePlansParams = {
  days?: number;
};

export async function getUsagePlans(
  params: GetUsagePlansParams = {},
): Promise<AdminUsagePlansResponse> {
  const qs = toQueryString(params);
  return apiRequest<AdminUsagePlansResponse>(`/api/v1/admin/usage/plans${qs}`);
}

export type GetUsageAlertsParams = {
  limit?: number;
  since_ns?: number;
  unacknowledged?: boolean;
};

export async function getUsageAlerts(
  params: GetUsageAlertsParams = {},
): Promise<AdminUsageAlertsResponse> {
  /*
   * `toQueryString` carries primitives, and the backend accepts only the
   * `strconv.ParseBool` spellings for this flag, so the boolean leaves here as
   * "true"/"false" rather than as 1/0 — a spelling it would reject with 400.
   */
  const qs = toQueryString({
    limit: params.limit,
    since_ns: params.since_ns,
    unacknowledged:
      params.unacknowledged === undefined ? undefined : String(params.unacknowledged),
  });
  return apiRequest<AdminUsageAlertsResponse>(`/api/v1/admin/alerts${qs}`);
}

export async function acknowledgeAlert(id: string): Promise<AdminUsageAlertItem> {
  return apiRequest<AdminUsageAlertItem>(
    `/api/v1/admin/alerts/${encodeURIComponent(id)}/actions/acknowledge`,
    { method: "POST" },
  );
}

export async function getUserUsage(
  userId: string,
  days?: number,
): Promise<AdminUsageDailyResponse> {
  const qs = days ? `?days=${days}` : "";
  return apiRequest<AdminUsageDailyResponse>(
    `/api/v1/admin/users/${encodeURIComponent(userId)}/usage${qs}`,
  );
}

// ---------------------------------------------------------------------------
// 设置 /api/v1/system/config
// ---------------------------------------------------------------------------

export async function getSystemConfig(): Promise<AdminSystemConfig> {
  return apiRequest<AdminSystemConfig>("/api/v1/system/config");
}

export async function updateSystemConfig(
  patch: AdminSystemConfigPatch,
): Promise<AdminSystemConfig> {
  return apiRequest<AdminSystemConfig>("/api/v1/system/config", {
    method: "PATCH",
    body: patch,
  });
}

export async function getDefaultSystemConfig(): Promise<AdminSystemConfig> {
  return apiRequest<AdminSystemConfig>("/api/v1/system/config/default");
}

// The deployment's own configuration. The console cannot change it, but it needs
// the values to explain what a cleared runtime override falls back to.
export async function getSystemEnvConfig(): Promise<AdminSystemEnvConfig> {
  return apiRequest<AdminSystemEnvConfig>("/api/v1/system/config/env");
}
