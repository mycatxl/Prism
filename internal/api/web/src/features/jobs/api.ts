import { apiRequest } from "../../lib/api-client";
import type {
  CancelIntelJobResponse,
  CreateIntelJobRequest,
  CreateIntelJobResponse,
  IntelJobDetail,
  IntelJobItemPage,
  IntelJobPage,
  RetryFailedJobResponse,
} from "./types";

// /client. Endpoints are registered in internal/api/server.go:144-149
// and the SSE stream in server.go:232.
//
// The UI is served from the same origin as the API (vite.config.ts proxies
// /api in dev); VITE_API_BASE_URL only matters for split deployments and is
// read here exactly like lib/api-client.ts:3 does, because apiRequest builds
// the URL internally and cannot be reused for an EventSource.

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL?.trim() ?? "";
const JOBS_PATH = "/api/v1/intel/jobs";

// The list endpoints paginate with limit/offset (api_helpers.go:38 caps limit
// at 100000; limit=0 falls back to 50).
const DEFAULT_PAGE_SIZE = 20;
export const JOB_PAGE_SIZE_OPTIONS = [10, 20, 50, 100] as const;
export const JOB_ITEM_PAGE_SIZE_OPTIONS = [20, 50, 100, 200] as const;

function pageQuery(status: string, limit: number, offset: number): string {
  const query = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  if (status) {
    query.set("status", status);
  }
  return query.toString();
}

export function listIntelJobs(status: string, limit = DEFAULT_PAGE_SIZE, offset = 0): Promise<IntelJobPage> {
  return apiRequest<IntelJobPage>(`${JOBS_PATH}?${pageQuery(status, limit, offset)}`);
}

export function getIntelJob(jobID: string): Promise<IntelJobDetail> {
  return apiRequest<IntelJobDetail>(`${JOBS_PATH}/${encodeURIComponent(jobID)}`);
}

export function listIntelJobItems(
  jobID: string,
  status: string,
  limit = DEFAULT_PAGE_SIZE,
  offset = 0,
): Promise<IntelJobItemPage> {
  return apiRequest<IntelJobItemPage>(
    `${JOBS_PATH}/${encodeURIComponent(jobID)}/items?${pageQuery(status, limit, offset)}`,
  );
}

export function createIntelJob(request: CreateIntelJobRequest): Promise<CreateIntelJobResponse> {
  return apiRequest<CreateIntelJobResponse>(JOBS_PATH, { method: "POST", body: request });
}

export function cancelIntelJob(jobID: string): Promise<CancelIntelJobResponse> {
  return apiRequest<CancelIntelJobResponse>(`${JOBS_PATH}/${encodeURIComponent(jobID)}/actions/cancel`, {
    method: "POST",
  });
}

export function retryFailedIntelJob(jobID: string): Promise<RetryFailedJobResponse> {
  return apiRequest<RetryFailedJobResponse>(`${JOBS_PATH}/${encodeURIComponent(jobID)}/actions/retry-failed`, {
    method: "POST",
  });
}
// EventSource sends same-origin cookies automatically, so the stream does not
// need a credential in the URL or in browser storage.
export function buildIntelJobEventsURL(jobID: string): string {
  return `${API_BASE_URL}${JOBS_PATH}/${encodeURIComponent(jobID)}/events`;
}
