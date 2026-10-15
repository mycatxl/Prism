import { apiRequest } from "../../lib/api-client";
import type {
  AccessKeyInput,
  CreateKeyResponse,
  KeysResponse,
  SelfOverview,
  SessionsResponse,
  SubscriptionResponse,
  UsageResponse,
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

export async function getOverview(): Promise<SelfOverview> {
  return apiRequest<SelfOverview>("/api/v1/me/overview");
}

export async function getSubscription(): Promise<SubscriptionResponse> {
  return apiRequest<SubscriptionResponse>("/api/v1/me/subscription");
}

export async function getUsage(params?: { days?: number }): Promise<UsageResponse> {
  const query = params ? toQueryString(params) : "";
  return apiRequest<UsageResponse>(`/api/v1/me/usage${query}`);
}

export async function getKeys(): Promise<KeysResponse> {
  return apiRequest<KeysResponse>("/api/v1/me/keys");
}

export async function createKey(input: AccessKeyInput): Promise<CreateKeyResponse> {
  return apiRequest<CreateKeyResponse>("/api/v1/me/keys", {
    method: "POST",
    body: input,
  });
}

export async function deleteKey(keyID: string): Promise<void> {
  return apiRequest<void>(`/api/v1/me/keys/${encodeURIComponent(keyID)}`, {
    method: "DELETE",
  });
}

export async function getSessions(): Promise<SessionsResponse> {
  return apiRequest<SessionsResponse>("/api/v1/me/security/sessions");
}

export async function revokeSession(sessionID: string): Promise<void> {
  return apiRequest<void>(`/api/v1/me/security/sessions/${encodeURIComponent(sessionID)}`, {
    method: "DELETE",
  });
}
