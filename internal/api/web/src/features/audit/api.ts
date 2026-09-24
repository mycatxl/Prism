import { apiRequest } from "../../lib/api-client";
import type { AuditLogListQuery, AuditLogListResponse } from "./types";

const basePath = "/api/v1/audit-logs";

export async function listAuditLogs(query: AuditLogListQuery = {}): Promise<AuditLogListResponse> {
  const params = new URLSearchParams();

  if (typeof query.before_id === "number" && query.before_id > 0) {
    params.set("before_id", String(query.before_id));
  }
  if (typeof query.limit === "number" && query.limit > 0) {
    params.set("limit", String(query.limit));
  }

  const search = params.toString();
  return apiRequest<AuditLogListResponse>(search ? `${basePath}?${search}` : basePath);
}
