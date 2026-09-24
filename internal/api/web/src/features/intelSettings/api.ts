import { apiRequest } from "../../lib/api-client";
import type {
  IntelCheck,
  IntelCheckPage,
  IntelProvider,
  IntelProviderPage,
  IntelProviderPatch,
} from "./types";

// WP09 §4/§5.4 client. The list is bounded by limit=200 (the backend caps limit
// at 100000 and rejects anything above it with 400 INVALID_ARGUMENT).

const providerListLimit = 200;

export function listIntelProviders(): Promise<IntelProviderPage> {
  return apiRequest<IntelProviderPage>(`/api/v1/intel/providers?limit=${providerListLimit}`);
}

export function patchIntelProvider(id: string, patch: IntelProviderPatch): Promise<IntelProvider> {
  // An absent field is sent as null, which the backend documents as "keep the
  // persisted value"; api_key "" clears the stored credential.
  return apiRequest<IntelProvider>(`/api/v1/intel/providers/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: {
      enabled: patch.enabled ?? null,
      api_key: patch.api_key ?? null,
      daily_limit: patch.daily_limit ?? null,
      qps: patch.qps ?? null,
      ttl: patch.ttl ?? null,
      config: patch.config ?? null,
    },
  });
}

export function resumeIntelProvider(id: string): Promise<IntelProvider> {
  return apiRequest<IntelProvider>(`/api/v1/intel/providers/${encodeURIComponent(id)}/actions/resume`, {
    method: "POST",
  });
}

export function listIntelChecks(): Promise<IntelCheckPage> {
  return apiRequest<IntelCheckPage>(`/api/v1/intel/checks?limit=${providerListLimit}`);
}

export function patchIntelCheck(id: string, enabled: boolean): Promise<IntelCheck> {
  return apiRequest<IntelCheck>(`/api/v1/intel/checks/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: { enabled },
  });
}
