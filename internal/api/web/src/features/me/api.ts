import { apiRequest } from "../../lib/api-client";
import type {
  AccessKeyInput,
  BalanceView,
  CreateKeyResponse,
  KeysResponse,
  SelfOverview,
  SessionsResponse,
} from "./types";

export async function getOverview(): Promise<SelfOverview> {
  return apiRequest<SelfOverview>("/api/v1/me/overview");
}

export async function getBalance(limit?: number): Promise<BalanceView> {
  const query = limit && limit > 0 ? `?limit=${limit}` : "";
  return apiRequest<BalanceView>(`/api/v1/me/balance${query}`);
}

export async function redeemCode(code: string): Promise<BalanceView> {
  return apiRequest<BalanceView>("/api/v1/me/redeem", {
    method: "POST",
    body: { code },
  });
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
