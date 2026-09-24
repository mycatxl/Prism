// Mirrors the JSON payloads of GET /api/v1/audit-logs (internal/api/audit.go +
// model.AuditEntry). Only successfully applied management writes are recorded,
// and credential path parameters are never part of the payload.
export type AuditLogEntry = {
  id: number;
  // Unix nanoseconds, rendered locally as a two-line date/time cell.
  at_ns: number;
  // First 8 hex characters of the admin token sha256 digest; empty in no-auth mode.
  actor: string;
  remote_addr: string;
  // "METHOD <route pattern>", for example "PATCH /api/v1/platforms/{id}".
  action: string;
  // Non-credential path parameters as "name=value,name=value".
  target: string;
  // JSON text: {"keys":["field", ...]} with request body key names only.
  detail: string;
};

export type AuditLogListResponse = {
  items: AuditLogEntry[];
  limit: number;
};

export type AuditLogListQuery = {
  // Cursor: return entries with id < before_id. Omitted or 0 starts from the newest.
  before_id?: number;
  // Defaults to 100 server-side, capped at 200.
  limit?: number;
};
