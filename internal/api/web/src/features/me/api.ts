import { apiRequest } from "../../lib/api-client";
import type {
  AccessKeyInput,
  CreateKeyResponse,
  KeysResponse,
  SelfOverview,
  SessionsResponse,
} from "./types";

export async function getOverview(): Promise<SelfOverview> {
  return apiRequest<SelfOverview>("/api/v1/me/overview");
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
