import { ApiError, apiRequest, type ApiErrorBody } from "../../lib/api-client";
import { getStoredAuthToken } from "../auth/auth-store";
import { EXPORT_FORMATS } from "./types";
import type {
  ExportFormat,
  ExportProfile,
  ExportProfileFilter,
  ExportProfileWriteInput,
  ExportReport,
  NodesExportInput,
  NodesExportOutcome,
  PageResponse,
} from "./types";

const basePath = "/api/v1/export-profiles";
const nodesExportPath = "/api/v1/nodes/export";

// api-client builds absolute URLs from the same variable; the raw export below
// has to read it too because it cannot go through apiRequest (see below).
const API_BASE_URL = import.meta.env.VITE_API_BASE_URL?.trim() ?? "";

/**
 * One export serialises up to export.MaxItems (5000) nodes, which is heavier
 * than the 30s budget api-client uses for regular JSON calls.
 */
const EXPORT_TIMEOUT_MS = 60_000;

// Fallback names, mirroring export.FileName() in internal/export/types.go. The
// server sends the real name in Content-Disposition.
const DEFAULT_FILE_NAMES: Record<ExportFormat, string> = {
  singbox: "prism.json",
  mihomo: "prism.yaml",
  v2rayn: "prism.txt",
  uri: "prism-uri.txt",
  csv: "prism.csv",
  json: "prism-export.json",
};

type ApiExportProfile = Omit<ExportProfile, "filter" | "url"> & {
  filter?: unknown;
  url?: string | null;
};

function isExportFormat(value: unknown): value is ExportFormat {
  return typeof value === "string" && (EXPORT_FORMATS as readonly string[]).includes(value);
}

const STRING_FILTER_KEYS = [
  "ip_type",
  "quality_state",
  "risk_grade",
  "purity_band",
  "protocol",
  "platform_id",
  "subscription_id",
  "region",
  "egress_ip",
  "tag_keyword",
  "probed_since",
  "verdict",
  "confidence_min",
  "country",
] as const;

const BOOLEAN_FILTER_KEYS = ["enabled", "circuit_open", "has_outbound", "native"] as const;
const NUMBER_FILTER_KEYS = ["purity_min", "purity_max", "asn"] as const;

/** Drops unknown/empty entries so a stored filter renders exactly what it means. */
export function normalizeFilter(raw: unknown): ExportProfileFilter {
  if (!raw || typeof raw !== "object") {
    return {};
  }
  const source = raw as Record<string, unknown>;
  const filter: ExportProfileFilter = {};
  for (const key of STRING_FILTER_KEYS) {
    const value = source[key];
    if (typeof value === "string" && value.trim() !== "") {
      filter[key] = value;
    }
  }
  for (const key of BOOLEAN_FILTER_KEYS) {
    const value = source[key];
    if (typeof value === "boolean") {
      filter[key] = value;
    }
  }
  for (const key of NUMBER_FILTER_KEYS) {
    const value = source[key];
    if (typeof value === "number" && Number.isFinite(value)) {
      filter[key] = value;
    }
  }
  const checks = source.checks;
  if (Array.isArray(checks)) {
    const values = checks.filter((entry): entry is string => typeof entry === "string" && entry.trim() !== "");
    if (values.length > 0) {
      filter.checks = values;
    }
  }
  return filter;
}

function normalizeProfile(raw: ApiExportProfile): ExportProfile {
  const profile: ExportProfile = {
    id: raw.id,
    name: raw.name,
    format: isExportFormat(raw.format) ? raw.format : "singbox",
    platform_id: raw.platform_id ?? "",
    filter: normalizeFilter(raw.filter),
    name_template: raw.name_template ?? "",
    enabled: raw.enabled !== false,
    last_access_at_ns: Number(raw.last_access_at_ns ?? 0),
    access_count: Number(raw.access_count ?? 0),
    created_at_ns: Number(raw.created_at_ns ?? 0),
    updated_at_ns: Number(raw.updated_at_ns ?? 0),
  };
  // `url` is the one-time plaintext subscription address; keep it only when the
  // server actually sent it (create / rotate-token).
  if (typeof raw.url === "string" && raw.url !== "") {
    profile.url = raw.url;
  }
  return profile;
}

export type ListExportProfilesInput = {
  limit?: number;
  offset?: number;
};

export async function listExportProfiles(
  input: ListExportProfilesInput = {},
): Promise<PageResponse<ExportProfile>> {
  const query = new URLSearchParams({
    limit: String(input.limit ?? 50),
    offset: String(input.offset ?? 0),
  });
  const data = await apiRequest<PageResponse<ApiExportProfile>>(`${basePath}?${query.toString()}`);
  return { ...data, items: data.items.map(normalizeProfile) };
}

export async function createExportProfile(input: ExportProfileWriteInput): Promise<ExportProfile> {
  const data = await apiRequest<ApiExportProfile>(basePath, { method: "POST", body: input });
  return normalizeProfile(data);
}

export async function updateExportProfile(
  id: string,
  input: ExportProfileWriteInput,
): Promise<ExportProfile> {
  const data = await apiRequest<ApiExportProfile>(`${basePath}/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: input,
  });
  return normalizeProfile(data);
}

export async function deleteExportProfile(id: string): Promise<void> {
  await apiRequest<void>(`${basePath}/${encodeURIComponent(id)}`, { method: "DELETE" });
}

/** The previous token stops working immediately; the new one is returned once. */
export async function rotateExportProfileToken(id: string): Promise<ExportProfile> {
  const data = await apiRequest<ApiExportProfile>(
    `${basePath}/${encodeURIComponent(id)}/actions/rotate-token`,
    { method: "POST" },
  );
  return normalizeProfile(data);
}

function appendFilter(query: URLSearchParams, filter: ExportProfileFilter | null | undefined): void {
  if (!filter) {
    return;
  }
  const setText = (key: string, value: string | undefined) => {
    const trimmed = value?.trim();
    if (trimmed) {
      query.set(key, trimmed);
    }
  };
  setText("ip_type", filter.ip_type);
  setText("quality_state", filter.quality_state);
  setText("risk_grade", filter.risk_grade);
  setText("purity_band", filter.purity_band);
  setText("protocol", filter.protocol);
  setText("platform_id", filter.platform_id);
  setText("subscription_id", filter.subscription_id);
  setText("region", filter.region);
  setText("egress_ip", filter.egress_ip);
  setText("tag_keyword", filter.tag_keyword);
  setText("probed_since", filter.probed_since);
  setText("verdict", filter.verdict);
  setText("confidence_min", filter.confidence_min);
  setText("country", filter.country);
  if (filter.enabled !== undefined) {
    query.set("enabled", String(filter.enabled));
  }
  if (filter.circuit_open !== undefined) {
    query.set("circuit_open", String(filter.circuit_open));
  }
  if (filter.has_outbound !== undefined) {
    query.set("has_outbound", String(filter.has_outbound));
  }
  if (filter.native !== undefined) {
    query.set("native", String(filter.native));
  }
  if (filter.purity_min !== undefined) {
    query.set("purity_min", String(filter.purity_min));
  }
  if (filter.purity_max !== undefined) {
    query.set("purity_max", String(filter.purity_max));
  }
  if (filter.asn !== undefined) {
    query.set("asn", String(filter.asn));
  }
  for (const check of filter.checks ?? []) {
    const trimmed = check.trim();
    if (trimmed) {
      query.append("check", trimmed);
    }
  }
}

async function responseToApiError(response: Response): Promise<ApiError> {
  const contentType = response.headers.get("content-type") ?? "";
  let body: ApiErrorBody | null = null;
  if (contentType.includes("application/json")) {
    try {
      body = (await response.json()) as ApiErrorBody;
    } catch {
      body = null;
    }
  }
  const code = body?.error?.code ?? "HTTP_ERROR";
  const message = body?.error?.message ?? response.statusText;
  return new ApiError(response.status, code, message, body);
}

function parseFileName(header: string | null, format: ExportFormat): string {
  const quoted = header?.match(/filename="([^"]+)"/i);
  if (quoted?.[1]) {
    return quoted[1];
  }
  const bare = header?.match(/filename=([^;]+)/i);
  if (bare?.[1]?.trim()) {
    return bare[1].trim();
  }
  return DEFAULT_FILE_NAMES[format];
}

function parseCount(header: string | null): number {
  const value = Number(header ?? "0");
  return Number.isFinite(value) && value > 0 ? Math.trunc(value) : 0;
}

function parseReport(text: string): ExportReport | null {
  try {
    const parsed: unknown = JSON.parse(text);
    if (!parsed || typeof parsed !== "object") {
      return null;
    }
    const body = parsed as { exported?: unknown; skipped?: unknown; truncated?: unknown };
    const skipped = Array.isArray(body.skipped)
      ? body.skipped.flatMap((entry) => {
          if (!entry || typeof entry !== "object") {
            return [];
          }
          const record = entry as { name?: unknown; reason?: unknown };
          return [
            {
              name: typeof record.name === "string" ? record.name : "",
              reason: typeof record.reason === "string" ? record.reason : "",
            },
          ];
        })
      : [];
    return {
      exported: typeof body.exported === "number" ? body.exported : skipped.length,
      skipped,
      ...(typeof body.truncated === "number" ? { truncated: body.truncated } : {}),
    };
  } catch {
    return null;
  }
}

/**
 * GET /api/v1/nodes/export.
 *
 * The response body is the export file itself, so this cannot use apiRequest
 * (which only returns parsed JSON). Counts come from the X-Prism-Export-*
 * headers; the per-node skip reasons are embedded in the body of the `json`
 * format only and therefore stay null for every other format.
 */
export async function fetchNodesExport(
  input: NodesExportInput,
  signal?: AbortSignal,
): Promise<NodesExportOutcome> {
  const query = new URLSearchParams({ format: input.format });
  const template = input.nameTemplate?.trim();
  if (template) {
    query.set("name_template", template);
  }
  if (input.healthyOnly) {
    query.set("healthy_only", "true");
  }
  if (input.limit !== undefined && input.limit > 0) {
    query.set("limit", String(input.limit));
  }
  appendFilter(query, input.filter);

  const headers = new Headers();
  const token = getStoredAuthToken();
  if (token) {
    headers.set("Authorization", `Bearer ${token}`);
  }

  const timeout = AbortSignal.timeout(EXPORT_TIMEOUT_MS);
  const response = await fetch(`${API_BASE_URL}${nodesExportPath}?${query.toString()}`, {
    method: "GET",
    headers,
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
  });
  if (!response.ok) {
    throw await responseToApiError(response);
  }

  const blob = await response.blob();
  const report = input.format === "json" ? parseReport(await blob.text()) : null;
  const skipped = parseCount(response.headers.get("x-prism-export-skipped"));

  return {
    blob,
    fileName: parseFileName(response.headers.get("content-disposition"), input.format),
    exported: parseCount(response.headers.get("x-prism-export-exported")),
    skipped: skipped || (report ? report.skipped.length : 0),
    truncated: parseCount(response.headers.get("x-prism-export-truncated")),
    report,
  };
}
