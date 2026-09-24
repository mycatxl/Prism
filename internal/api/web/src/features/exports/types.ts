export type PageResponse<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
};

// Mirrors internal/export.Formats().
export const EXPORT_FORMATS = ["singbox", "mihomo", "v2rayn", "uri", "csv", "json"] as const;
export type ExportFormat = (typeof EXPORT_FORMATS)[number];

/**
 * Stored/queried node filter of one export. The field set and the JSON names
 * mirror `exportProfileFilter` in internal/api/handler_export.go, which in turn
 * shares the vocabulary of GET /api/v1/nodes. The API rejects unknown fields, so
 * both shapes must keep the same names.
 */
export type ExportProfileFilter = {
  ip_type?: string;
  quality_state?: string;
  risk_grade?: string;
  purity_band?: string;
  protocol?: string;
  platform_id?: string;
  subscription_id?: string;
  enabled?: boolean;
  region?: string;
  circuit_open?: boolean;
  has_outbound?: boolean;
  egress_ip?: string;
  tag_keyword?: string;
  probed_since?: string;
  purity_min?: number;
  purity_max?: number;
  verdict?: string;
  confidence_min?: string;
  native?: boolean;
  asn?: number;
  country?: string;
  checks?: string[];
};

/** Never carries the plaintext token: only its SHA-256 digest is stored. */
export type ExportProfile = {
  id: string;
  name: string;
  format: ExportFormat;
  platform_id?: string;
  filter: ExportProfileFilter;
  name_template: string;
  enabled: boolean;
  last_access_at_ns: number;
  access_count: number;
  created_at_ns: number;
  updated_at_ns: number;
  /**
   * `/sub/{token}` subscription URL. Returned by the server exactly once, in the
   * create and rotate-token responses; every other response omits it.
   */
  url?: string;
};

export type ExportProfileWriteInput = {
  name?: string;
  format?: ExportFormat;
  platform_id?: string;
  filter?: ExportProfileFilter;
  name_template?: string;
  enabled?: boolean;
};

/** Mirrors export.Skip / export.Report. */
export type ExportSkip = {
  name: string;
  reason: string;
};

export type ExportReport = {
  exported: number;
  skipped: ExportSkip[];
  truncated?: number;
};

export type NodesExportInput = {
  format: ExportFormat;
  nameTemplate?: string;
  filter?: ExportProfileFilter | null;
  healthyOnly?: boolean;
  limit?: number;
};

export type NodesExportOutcome = {
  blob: Blob;
  fileName: string;
  exported: number;
  skipped: number;
  truncated: number;
  /**
   * Per-node skip reasons. Only the `json` format embeds the report in its body;
   * every other format reports the counts in the X-Prism-Export-* headers.
   */
  report: ExportReport | null;
};

/** One sample node used for the local name-template preview. */
export type NamePreviewSample = {
  /** The node's current name, shown next to the rendered preview. */
  source: string;
  vars: Record<string, string>;
};
