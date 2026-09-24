import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createColumnHelper } from "@tanstack/react-table";
import { AlertTriangle, Copy, Download, Info, KeyRound, Pencil, Plus, RefreshCw, Trash2, X } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { useForm, type UseFormReturn } from "react-hook-form";
import { z } from "zod";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { DataTable } from "../../components/ui/DataTable";
import { DialogSurface } from "../../components/ui/DialogSurface";
import { Input } from "../../components/ui/Input";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
import { QueryState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import { Switch } from "../../components/ui/Switch";
import { ToastContainer } from "../../components/ui/Toast";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime, formatRelativeTime } from "../../lib/time";
import { listNodes } from "../nodes/api";
import type { NodeListQuery, NodeSummary } from "../nodes/types";
import { listPlatforms } from "../platforms/api";
import { listSubscriptions } from "../subscriptions/api";
import {
  createExportProfile,
  deleteExportProfile,
  fetchNodesExport,
  listExportProfiles,
  rotateExportProfileToken,
  updateExportProfile,
} from "./api";
import {
  EXPORT_FORMATS,
  type ExportFormat,
  type ExportProfile,
  type ExportProfileFilter,
  type ExportProfileWriteInput,
  type ExportReport,
  type NamePreviewSample,
} from "./types";

const PAGE_SIZE_OPTIONS = [10, 20, 50, 100] as const;
const EMPTY_PROFILES: ExportProfile[] = [];
const EXPORT_LIMIT_OPTIONS = [100, 500, 2000, 5000] as const;
const PREVIEW_SAMPLE_COUNT = 6;

// Mirrors the placeholder set of internal/export/names.go templateVariables().
const TEMPLATE_VARIABLES = [
  "name",
  "flag",
  "country",
  "city",
  "asn",
  "org",
  "ip_type",
  "purity",
  "band",
  "verdict",
  "engine",
  "protocol",
  "latency",
  "index",
] as const;
const TEMPLATE_VARIABLE_HINT = TEMPLATE_VARIABLES.map((name) => `{${name}}`).join(" ");

// export.maxNameLength: the hard cap of a rendered name.
const NAME_MAX_LENGTH = 64;
// export.maxNameVariants: the bounded deduplication budget.
const MAX_NAME_VARIANTS = 4096;

const FORMAT_LABELS: Record<ExportFormat, string> = {
  singbox: "sing-box 配置",
  mihomo: "Mihomo (Clash Meta) 配置",
  v2rayn: "v2rayN 分享链接",
  uri: "纯分享链接 (URI)",
  csv: "CSV 表格",
  json: "JSON（含跳过明细）",
};

// Enum vocabulary accepted by the API for the stored filter (WP10 §4).
const IP_TYPE_OPTIONS = ["unknown", "residential", "non_residential", "datacenter", "business", "wireless", "mobile", "conflicting"];
const QUALITY_STATE_OPTIONS = ["unobserved", "pending", "partial", "valid", "stale", "conflicting", "unsupported"];
const RISK_GRADE_OPTIONS = ["unknown", "low", "moderate", "high", "severe", "review"];
const PURITY_BAND_OPTIONS = ["unknown", "excellent", "clean", "fair", "mixed", "poor", "review"];

type FilterFieldKind = "text" | "number" | "select" | "boolean" | "platform" | "subscription";

type FilterFieldDescriptor = {
  key: keyof ExportProfileFilter;
  label: string;
  kind: FilterFieldKind;
  options?: readonly string[];
  placeholder?: string;
};

// One descriptor per field of exportProfileFilter, so the form and the filter
// chips can never drift apart from the API contract.
const FILTER_FIELDS: readonly FilterFieldDescriptor[] = [
  { key: "ip_type", label: "IP 类型", kind: "select", options: IP_TYPE_OPTIONS },
  { key: "quality_state", label: "质量状态", kind: "select", options: QUALITY_STATE_OPTIONS },
  { key: "risk_grade", label: "风险等级", kind: "select", options: RISK_GRADE_OPTIONS },
  { key: "purity_band", label: "纯净度分级", kind: "select", options: PURITY_BAND_OPTIONS },
  { key: "protocol", label: "协议", kind: "text", placeholder: "vless" },
  { key: "country", label: "国家地区代码", kind: "text", placeholder: "US" },
  { key: "region", label: "地区", kind: "text", placeholder: "HKG" },
  { key: "verdict", label: "判定", kind: "text" },
  { key: "confidence_min", label: "最低置信度", kind: "text" },
  { key: "egress_ip", label: "出口 IP", kind: "text" },
  { key: "tag_keyword", label: "标签关键字", kind: "text" },
  { key: "asn", label: "ASN", kind: "number" },
  { key: "purity_min", label: "纯净度下限", kind: "number" },
  { key: "purity_max", label: "纯净度上限", kind: "number" },
  { key: "probed_since", label: "探测时间晚于（RFC3339）", kind: "text", placeholder: "2026-01-01T00:00:00Z" },
  { key: "enabled", label: "节点启用状态", kind: "boolean" },
  { key: "circuit_open", label: "熔断状态", kind: "boolean" },
  { key: "has_outbound", label: "出站可用", kind: "boolean" },
  { key: "native", label: "原生 IP", kind: "boolean" },
  { key: "platform_id", label: "平台", kind: "platform" },
  { key: "subscription_id", label: "订阅", kind: "subscription" },
  { key: "checks", label: "检测项（check_id:outcome，逗号分隔）", kind: "text", placeholder: "chatgpt:unblocked" },
];

type TriState = "" | "true" | "false";

/** Filter fields whose stored value is a plain string. */
type TextFilterKey =
  | "ip_type"
  | "quality_state"
  | "risk_grade"
  | "purity_band"
  | "protocol"
  | "country"
  | "region"
  | "verdict"
  | "confidence_min"
  | "egress_ip"
  | "tag_keyword"
  | "probed_since"
  | "platform_id"
  | "subscription_id";

type ExportFilterForm = {
  ip_type: string;
  quality_state: string;
  risk_grade: string;
  purity_band: string;
  protocol: string;
  country: string;
  region: string;
  verdict: string;
  confidence_min: string;
  egress_ip: string;
  tag_keyword: string;
  asn: string;
  purity_min: string;
  purity_max: string;
  probed_since: string;
  enabled: TriState;
  circuit_open: TriState;
  has_outbound: TriState;
  native: TriState;
  platform_id: string;
  subscription_id: string;
  checks: string;
};

type ExportProfileForm = {
  name: string;
  format: ExportFormat;
  platform_id: string;
  name_template: string;
  enabled: boolean;
  filter: ExportFilterForm;
};

/** Fresh empty filter per form instance: the object is never shared. */
function emptyFilterForm(): ExportFilterForm {
  return {
    ip_type: "",
    quality_state: "",
    risk_grade: "",
    purity_band: "",
    protocol: "",
    country: "",
    region: "",
    verdict: "",
    confidence_min: "",
    egress_ip: "",
    tag_keyword: "",
    asn: "",
    purity_min: "",
    purity_max: "",
    probed_since: "",
    enabled: "",
    circuit_open: "",
    has_outbound: "",
    native: "",
    platform_id: "",
    subscription_id: "",
    checks: "",
  };
}

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const RFC3339_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

function optionalBound(value: string, empty: string): string {
  const trimmed = value.trim();
  if (!trimmed) {
    return empty;
  }
  const parsed = Number(trimmed);
  if (!Number.isInteger(parsed) || parsed < 0 || parsed > 100) {
    return empty;
  }
  return "";
}

const exportProfileSchema = z
  .object({
    name: z.string().trim().min(1, "配置名称不能为空").max(128, "配置名称不能超过 128 个字符"),
    format: z.enum(["singbox", "mihomo", "v2rayn", "uri", "csv", "json"]),
    platform_id: z.string(),
    name_template: z.string().max(256, "命名模板不能超过 256 个字符"),
    enabled: z.boolean(),
    filter: z.object({
      ip_type: z.string(),
      quality_state: z.string(),
      risk_grade: z.string(),
      purity_band: z.string(),
      protocol: z.string(),
      country: z.string(),
      region: z.string(),
      verdict: z.string(),
      confidence_min: z.string(),
      egress_ip: z.string(),
      tag_keyword: z.string(),
      asn: z.string(),
      purity_min: z.string(),
      purity_max: z.string(),
      probed_since: z.string(),
      enabled: z.enum(["", "true", "false"]),
      circuit_open: z.enum(["", "true", "false"]),
      has_outbound: z.enum(["", "true", "false"]),
      native: z.enum(["", "true", "false"]),
      platform_id: z.string(),
      subscription_id: z.string(),
      checks: z.string(),
    }),
  })
  .superRefine((value, ctx) => {
    if (value.platform_id.trim() && !UUID_PATTERN.test(value.platform_id.trim())) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["platform_id"], message: "关联平台必须是 UUID" });
    }
    const { filter } = value;
    if (filter.platform_id.trim() && !UUID_PATTERN.test(filter.platform_id.trim())) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["filter", "platform_id"], message: "关联平台必须是 UUID" });
    }
    if (filter.subscription_id.trim() && !UUID_PATTERN.test(filter.subscription_id.trim())) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["filter", "subscription_id"], message: "订阅（可选）必须是 UUID" });
    }
    if (filter.probed_since.trim() && !RFC3339_PATTERN.test(filter.probed_since.trim())) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ["filter", "probed_since"],
        message: "探测时间必须是 RFC3339 时间（例如 2026-01-01T00:00:00Z）",
      });
    }
    const purityMin = optionalBound(filter.purity_min, "纯净度下限必须是 0-100 的整数");
    if (purityMin) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["filter", "purity_min"], message: purityMin });
    }
    const purityMax = optionalBound(filter.purity_max, "纯净度上限必须是 0-100 的整数");
    if (purityMax) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["filter", "purity_max"], message: purityMax });
    }
    if (filter.asn.trim() && !/^\d+$/.test(filter.asn.trim())) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["filter", "asn"], message: "ASN 必须是整数" });
    }
  });

// --- name template helpers (mirror internal/export/names.go) ---

function compressSpace(raw: string): string {
  return raw.replace(/\s+/g, " ").trim();
}

function byteLength(value: string): number {
  return new TextEncoder().encode(value).length;
}

/** Cuts raw to at most `limit` bytes on a rune boundary. */
function truncateName(raw: string, limit: number): string {
  if (limit <= 0) {
    return "";
  }
  if (byteLength(raw) <= limit) {
    return raw;
  }
  const encoder = new TextEncoder();
  const decoder = new TextDecoder();
  let slice = encoder.encode(raw).slice(0, limit);
  let text = decoder.decode(slice);
  // A cut multi-byte rune decodes to U+FFFD; drop it and retry once.
  if (text.includes("\uFFFD")) {
    slice = slice.slice(0, -1);
    text = decoder.decode(slice);
  }
  return text;
}

function clampName(raw: string): string {
  return compressSpace(truncateName(raw, NAME_MAX_LENGTH));
}

/** Substitutes every known placeholder; unknown ones render as an empty string. */
function renderTemplate(template: string, vars: Record<string, string>): string {
  let rendered = "";
  for (let index = 0; index < template.length; index += 1) {
    const char = template[index];
    if (char !== "{") {
      rendered += char;
      continue;
    }
    const end = template.indexOf("}", index);
    if (end < 0) {
      rendered += template.slice(index);
      break;
    }
    const token = template.slice(index + 1, end);
    rendered += vars[token] ?? "";
    index = end;
  }
  return rendered;
}

/**
 * Renders one name per sample and resolves duplicates the way RenderNames does:
 * " #2", " #3", ... in input order, within the bounded variant budget.
 */
function renderPreviewNames(template: string, samples: NamePreviewSample[]): string[] {
  const effective = template.trim() || "{name}";
  const bases = samples.map((sample) => clampName(renderTemplate(effective, sample.vars)));
  const taken = new Set(bases.filter((base) => base !== ""));
  return bases.map((base, index) => {
    if (base === "") {
      return "";
    }
    if (!bases.slice(0, index).includes(base)) {
      return base;
    }
    for (let variant = 2; variant <= MAX_NAME_VARIANTS; variant += 1) {
      const suffix = ` #${variant}`;
      const room = NAME_MAX_LENGTH - byteLength(suffix);
      const candidate = (byteLength(base) > room ? truncateName(base, room) : base) + suffix;
      if (!taken.has(candidate)) {
        taken.add(candidate);
        return candidate;
      }
    }
    return "";
  });
}

function countryFlag(code: string): string {
  const normalized = code.trim().toUpperCase();
  if (normalized.length !== 2) {
    return "";
  }
  const points = [...normalized].map((char) => {
    if (char < "A" || char > "Z") {
      return null;
    }
    return 0x1f1e6 + (char.charCodeAt(0) - 65);
  });
  if (points.some((point) => point === null)) {
    return "";
  }
  return String.fromCodePoint(...(points as number[]));
}

function nodeName(node: NodeSummary): string {
  return node.display_tag || node.tags[0]?.tag || node.node_hash.slice(0, 12);
}

/** Builds the preview variables a node summary can supply locally. */
function nodeToPreviewSample(node: NodeSummary, index: number): NamePreviewSample {
  const intel = node.intel ?? null;
  return {
    source: nodeName(node),
    vars: {
      name: nodeName(node),
      flag: countryFlag(intel?.country ?? ""),
      country: (intel?.country ?? "").toUpperCase(),
      city: intel?.city ?? "",
      asn: intel && intel.asn > 0 ? String(intel.asn) : "",
      org: intel?.as_org ?? "",
      ip_type: intel?.ip_type ?? "",
      purity: intel?.purity_score === null || intel?.purity_score === undefined ? "" : String(intel.purity_score),
      band: intel?.purity_band ?? "",
      verdict: intel?.verdict ?? "",
      // Only the node document knows the engine; the backend resolves it during
      // the export, so the local preview leaves it empty.
      engine: "",
      protocol: node.protocol ?? "",
      latency: node.reference_latency_ms === undefined ? "" : String(node.reference_latency_ms),
      index: String(index + 1),
    },
  };
}

function toPreviewQuery(filter: ExportProfileFilter | null): NodeListQuery {
  const base: NodeListQuery = { limit: PREVIEW_SAMPLE_COUNT, sort_by: "created_at", sort_order: "desc" };
  if (!filter) {
    return base;
  }
  const { checks, ...rest } = filter;
  return { ...base, ...rest, check: checks };
}

// --- filter <-> form conversion ---

function filterToForm(filter: ExportProfileFilter): ExportFilterForm {
  const triState = (value: boolean | undefined): TriState =>
    value === undefined ? "" : value ? "true" : "false";
  const text = (value: string | undefined) => value ?? "";
  return {
    ip_type: text(filter.ip_type),
    quality_state: text(filter.quality_state),
    risk_grade: text(filter.risk_grade),
    purity_band: text(filter.purity_band),
    protocol: text(filter.protocol),
    country: text(filter.country),
    region: text(filter.region),
    verdict: text(filter.verdict),
    confidence_min: text(filter.confidence_min),
    egress_ip: text(filter.egress_ip),
    tag_keyword: text(filter.tag_keyword),
    asn: filter.asn === undefined ? "" : String(filter.asn),
    purity_min: filter.purity_min === undefined ? "" : String(filter.purity_min),
    purity_max: filter.purity_max === undefined ? "" : String(filter.purity_max),
    probed_since: text(filter.probed_since),
    enabled: triState(filter.enabled),
    circuit_open: triState(filter.circuit_open),
    has_outbound: triState(filter.has_outbound),
    native: triState(filter.native),
    platform_id: text(filter.platform_id),
    subscription_id: text(filter.subscription_id),
    checks: (filter.checks ?? []).join(", "),
  };
}

/** Drops every empty field so the stored filter says exactly what it filters. */
function formToFilter(form: ExportFilterForm): ExportProfileFilter {
  const filter: ExportProfileFilter = {};
  const assignText = (key: TextFilterKey, value: string) => {
    const trimmed = value.trim();
    if (trimmed) {
      filter[key] = trimmed;
    }
  };
  assignText("ip_type", form.ip_type);
  assignText("quality_state", form.quality_state);
  assignText("risk_grade", form.risk_grade);
  assignText("purity_band", form.purity_band);
  assignText("protocol", form.protocol);
  assignText("country", form.country);
  assignText("region", form.region);
  assignText("verdict", form.verdict);
  assignText("confidence_min", form.confidence_min);
  assignText("egress_ip", form.egress_ip);
  assignText("tag_keyword", form.tag_keyword);
  assignText("probed_since", form.probed_since);
  assignText("platform_id", form.platform_id);
  assignText("subscription_id", form.subscription_id);
  const triple = (value: TriState): boolean | undefined =>
    value === "" ? undefined : value === "true";
  filter.enabled = triple(form.enabled);
  filter.circuit_open = triple(form.circuit_open);
  filter.has_outbound = triple(form.has_outbound);
  filter.native = triple(form.native);
  if (filter.enabled === undefined) delete filter.enabled;
  if (filter.circuit_open === undefined) delete filter.circuit_open;
  if (filter.has_outbound === undefined) delete filter.has_outbound;
  if (filter.native === undefined) delete filter.native;
  if (form.asn.trim()) {
    filter.asn = Number(form.asn.trim());
  }
  if (form.purity_min.trim()) {
    filter.purity_min = Number(form.purity_min.trim());
  }
  if (form.purity_max.trim()) {
    filter.purity_max = Number(form.purity_max.trim());
  }
  const checks = form.checks
    .split(",")
    .map((entry) => entry.trim())
    .filter(Boolean);
  if (checks.length > 0) {
    filter.checks = checks;
  }
  return filter;
}

function profileToForm(profile: ExportProfile): ExportProfileForm {
  return {
    name: profile.name,
    format: profile.format,
    platform_id: profile.platform_id ?? "",
    name_template: profile.name_template,
    enabled: profile.enabled,
    filter: filterToForm(profile.filter),
  };
}

function formToPayload(form: ExportProfileForm): ExportProfileWriteInput {
  return {
    name: form.name.trim(),
    format: form.format,
    platform_id: form.platform_id.trim(),
    name_template: form.name_template.trim(),
    enabled: form.enabled,
    filter: formToFilter(form.filter),
  };
}

function activeFilterChips(filter: ExportProfileFilter): FilterFieldDescriptor[] {
  return FILTER_FIELDS.filter((field) => {
    const value = filter[field.key];
    if (value === undefined) {
      return false;
    }
    return Array.isArray(value) ? value.length > 0 : true;
  });
}

function filterValueText(field: FilterFieldDescriptor, filter: ExportProfileFilter): string {
  const value = filter[field.key];
  if (value === undefined) {
    return "";
  }
  if (Array.isArray(value)) {
    return value.join(", ");
  }
  if (typeof value === "boolean") {
    return value ? "是" : "否";
  }
  return String(value);
}

function nsToIso(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return "";
  }
  return new Date(value / 1e6).toISOString();
}

function triggerDownload(blob: Blob, fileName: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = fileName;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

// --- shared bits ---

function ExportSkipList({ report, note }: { report: ExportReport | null; note?: string }) {
  const { t } = useI18n();
  if (!report) {
    return null;
  }
  if (report.skipped.length === 0) {
    return <p className="platform-op-hint">{t("没有节点被跳过")}</p>;
  }
  return (
    <>
      {note ? <p className="platform-op-hint">{note}</p> : null}
      <ul className="platform-ops-list">
        {report.skipped.map((entry, index) => (
          <li key={`${entry.reason}|${entry.name}|${index}`} className="platform-op-item">
            <div className="platform-op-copy">
              <h5>{entry.name || t("未命名")}</h5>
              <p className="platform-op-hint">{entry.reason}</p>
            </div>
          </li>
        ))}
      </ul>
    </>
  );
}

type PlatformOption = { id: string; name: string };
type SubscriptionOption = { id: string; name: string };

function ExportProfileFields({
  form,
  idPrefix,
  platforms,
  subscriptions,
}: {
  form: UseFormReturn<ExportProfileForm>;
  idPrefix: string;
  platforms: readonly PlatformOption[];
  subscriptions: readonly SubscriptionOption[];
}) {
  const { t } = useI18n();
  const { register, formState } = form;

  const renderFilterField = (field: FilterFieldDescriptor) => {
    const inputId = `${idPrefix}-filter-${field.key}`;
    const error = formState.errors.filter?.[field.key]?.message;
    const path = `filter.${field.key}` as const;
    return (
      <div className="field-group" key={field.key}>
        <label className="field-label" htmlFor={inputId}>
          {t(field.label)}
        </label>
        {field.kind === "select" ? (
          <Select id={inputId} invalid={Boolean(error)} {...register(path)}>
            <option value="">{t("不限")}</option>
            {(field.options ?? []).map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </Select>
        ) : field.kind === "boolean" ? (
          <Select id={inputId} invalid={Boolean(error)} {...register(path)}>
            <option value="">{t("不限")}</option>
            <option value="true">{t("是")}</option>
            <option value="false">{t("否")}</option>
          </Select>
        ) : field.kind === "platform" ? (
          <Select id={inputId} invalid={Boolean(error)} {...register(path)}>
            <option value="">{t("不限")}</option>
            {platforms.map((platform) => (
              <option key={platform.id} value={platform.id}>
                {platform.name}
              </option>
            ))}
          </Select>
        ) : field.kind === "subscription" ? (
          <Select id={inputId} invalid={Boolean(error)} {...register(path)}>
            <option value="">{t("不限")}</option>
            {subscriptions.map((subscription) => (
              <option key={subscription.id} value={subscription.id}>
                {subscription.name}
              </option>
            ))}
          </Select>
        ) : (
          <Input
            id={inputId}
            type={field.kind === "number" ? "number" : "text"}
            inputMode={field.kind === "number" ? "numeric" : undefined}
            placeholder={field.placeholder ? t(field.placeholder) : undefined}
            invalid={Boolean(error)}
            {...register(path)}
          />
        )}
        {error ? <p className="field-error">{t(error)}</p> : null}
      </div>
    );
  };

  return (
    <form className="form-grid" onSubmit={(event) => event.preventDefault()}>
      <div className="field-group field-span-2">
        <label className="field-label" htmlFor={`${idPrefix}-name`}>
          {t("配置名称")}
        </label>
        <Input
          id={`${idPrefix}-name`}
          invalid={Boolean(formState.errors.name)}
          {...register("name")}
        />
        {formState.errors.name?.message ? (
          <p className="field-error">{t(formState.errors.name.message)}</p>
        ) : null}
      </div>

      <div className="field-group">
        <label className="field-label" htmlFor={`${idPrefix}-format`}>
          {t("导出格式")}
        </label>
        <Select id={`${idPrefix}-format`} {...register("format")}>
          {EXPORT_FORMATS.map((format) => (
            <option key={format} value={format}>
              {t(FORMAT_LABELS[format])}
            </option>
          ))}
        </Select>
      </div>

      <div className="field-group">
        <label className="field-label" htmlFor={`${idPrefix}-platform`}>
          {t("关联平台")}
        </label>
        <Select id={`${idPrefix}-platform`} invalid={Boolean(formState.errors.platform_id)} {...register("platform_id")}>
          <option value="">{t("不限制")}</option>
          {platforms.map((platform) => (
            <option key={platform.id} value={platform.id}>
              {platform.name}
            </option>
          ))}
        </Select>
        {formState.errors.platform_id?.message ? (
          <p className="field-error">{t(formState.errors.platform_id.message)}</p>
        ) : null}
      </div>

      <div className="field-group field-span-2">
        <label className="field-label" htmlFor={`${idPrefix}-template`}>
          {t("命名模板")}
        </label>
        <Input
          id={`${idPrefix}-template`}
          placeholder="{flag} {country} {city} #{index}"
          invalid={Boolean(formState.errors.name_template)}
          {...register("name_template")}
        />
        {formState.errors.name_template?.message ? (
          <p className="field-error">{t(formState.errors.name_template.message)}</p>
        ) : null}
        <p className="platform-op-hint">
          {t("命名模板支持：{{variables}}。未知占位符渲染为空字符串，名称超过 64 字节会被截断，重名会追加 \" #2\"、\" #3\"。", {
            variables: TEMPLATE_VARIABLE_HINT,
          })}
        </p>
      </div>

      <div className="field-group subscription-switch-item field-span-2">
        <label className="subscription-switch-label" htmlFor={`${idPrefix}-enabled`}>
          <span>{t("启用")}</span>
          <span
            className="subscription-info-icon"
            title={t("禁用后订阅地址会立即失效，导出配置本身会保留。")}
            aria-label={t("禁用后订阅地址会立即失效，导出配置本身会保留。")}
            tabIndex={0}
          >
            <Info size={13} />
          </span>
        </label>
        <Switch id={`${idPrefix}-enabled`} {...register("enabled")} />
      </div>

      <section className="platform-drawer-section field-span-2">
        <div className="platform-drawer-section-head">
          <h4>{t("过滤条件（可选）")}</h4>
          <p>{t("过滤条件与节点列表共用同一套查询词汇；留空表示不过滤。")}</p>
        </div>
        <div className="exports-filter-grid">{FILTER_FIELDS.map(renderFilterField)}</div>
      </section>
    </form>
  );
}

type ExportDialogTarget = {
  profile: ExportProfile | null;
};

// Toasts are raised through the page-level list: a nested component must not
// create its own toast store, which nothing renders.
type ExportToast = (tone: "success" | "error", text: string) => void;

function ExportDialog({
  target,
  onClose,
  onMessage,
}: {
  target: ExportDialogTarget;
  onClose: () => void;
  onMessage: ExportToast;
}) {
  const { t } = useI18n();
  const showToast = onMessage;
  const profile = target.profile;
  const [format, setFormat] = useState<ExportFormat>(profile?.format ?? "singbox");
  const [nameTemplate, setNameTemplate] = useState(profile?.name_template ?? "{name}");
  const [healthyOnly, setHealthyOnly] = useState(false);
  const [limit, setLimit] = useState<number>(EXPORT_LIMIT_OPTIONS[1]);
  const [running, setRunning] = useState(false);
  const [outcome, setOutcome] = useState<Awaited<ReturnType<typeof fetchNodesExport>> | null>(null);
  const [detailReport, setDetailReport] = useState<ExportReport | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailFailed, setDetailFailed] = useState(false);
  const debouncedTemplate = useDebouncedValue(nameTemplate, 200);
  const filter = profile?.filter ?? null;

  const previewQuery = useQuery({
    queryKey: ["exports", "preview-nodes", filter],
    queryFn: () => listNodes(toPreviewQuery(filter)),
    staleTime: 30_000,
  });

  const samples = useMemo(
    () => (previewQuery.data?.items ?? []).map(nodeToPreviewSample),
    [previewQuery.data],
  );
  const previewNames = useMemo(() => renderPreviewNames(debouncedTemplate, samples), [debouncedTemplate, samples]);
  const title = profile ? `${t("立即导出")} · ${profile.name}` : t("节点导出");

  const runExport = async () => {
    setRunning(true);
    setOutcome(null);
    setDetailReport(null);
    setDetailFailed(false);
    try {
      const result = await fetchNodesExport({
        format,
        // The request uses exactly what is typed; the debounced value is only
        // for the local preview.
        nameTemplate,
        filter,
        healthyOnly,
        limit,
      });
      setOutcome(result);
      if (result.exported === 0) {
        showToast("error", t("没有可导出的节点，请检查过滤条件。"));
      }
      if (result.skipped > 0 && format !== "json") {
        setDetailLoading(true);
        try {
          const detail = await fetchNodesExport({
            format: "json",
            nameTemplate,
            filter,
            healthyOnly,
            limit,
          });
          setDetailReport(detail.report);
        } catch {
          setDetailFailed(true);
        } finally {
          setDetailLoading(false);
        }
      }
    } catch (error) {
      showToast("error", formatApiErrorMessage(error, t));
    } finally {
      setRunning(false);
    }
  };

  const chips = filter ? activeFilterChips(filter) : [];
  const skipReport = outcome?.report ?? detailReport;

  return (
    <DialogSurface title={title} variant="modal" onClose={onClose}>
      <Card className="modal-card exports-dialog-card">
        <div className="modal-header">
          <h3>{title}</h3>
          <Button aria-label={t("关闭")} variant="ghost" size="sm" onClick={onClose}>
            <X size={16} />
          </Button>
        </div>

        {profile ? (
          <p className="platform-op-hint">
            {chips.length > 0
              ? t("将沿用该配置的过滤条件。")
              : t("本次导出不带过滤条件。")}
          </p>
        ) : null}

        {chips.length > 0 ? (
          <div className="exports-chip-row">
            {chips.map((field) => (
              <Badge key={field.key} variant="muted">
                {`${t(field.label)}: ${filterValueText(field, filter ?? {})}`}
              </Badge>
            ))}
          </div>
        ) : null}

        <div className="form-grid">
          <div className="field-group">
            <label className="field-label" htmlFor="export-dialog-format">
              {t("导出格式")}
            </label>
            <Select
              id="export-dialog-format"
              value={format}
              onChange={(event) => setFormat(event.target.value as ExportFormat)}
            >
              {EXPORT_FORMATS.map((option) => (
                <option key={option} value={option}>
                  {t(FORMAT_LABELS[option])}
                </option>
              ))}
            </Select>
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="export-dialog-limit">
              {t("导出上限")}
            </label>
            <Select
              id="export-dialog-limit"
              value={String(limit)}
              onChange={(event) => setLimit(Number(event.target.value))}
            >
              {EXPORT_LIMIT_OPTIONS.map((option) => (
                <option key={option} value={option}>
                  {option}
                </option>
              ))}
            </Select>
          </div>

          <div className="field-group field-span-2">
            <label className="field-label" htmlFor="export-dialog-template">
              {t("命名模板")}
            </label>
            <Input
              id="export-dialog-template"
              value={nameTemplate}
              placeholder="{flag} {country} {city} #{index}"
              onChange={(event) => setNameTemplate(event.target.value)}
            />
            <p className="platform-op-hint">
              {t("命名模板支持：{{variables}}。未知占位符渲染为空字符串，名称超过 64 字节会被截断，重名会追加 \" #2\"、\" #3\"。", {
                variables: TEMPLATE_VARIABLE_HINT,
              })}
            </p>
          </div>

          <div className="field-group subscription-switch-item field-span-2">
            <label className="subscription-switch-label" htmlFor="export-dialog-healthy">
              <span>{t("仅健康节点")}</span>
            </label>
            <Switch
              id="export-dialog-healthy"
              checked={healthyOnly}
              onChange={(event) => setHealthyOnly(event.target.checked)}
            />
          </div>
        </div>

        <section className="platform-drawer-section">
          <div className="platform-drawer-section-head">
            <h4>{t("命名预览")}</h4>
            <p>
              {t("以下名称基于节点池前 {{count}} 个节点实时渲染。", {
                count: previewQuery.data?.items.length ?? 0,
              })}
            </p>
          </div>
          {previewQuery.isPending ? (
            <QueryState loading />
          ) : previewQuery.error ? (
            <QueryState error={previewQuery.error} onRetry={() => void previewQuery.refetch()} />
          ) : samples.length === 0 ? (
            <QueryState empty emptyText={t("无节点可预览")} />
          ) : (
            <ul className="exports-preview-list">
              {samples.map((sample, index) => (
                <li key={`${sample.source}-${index}`} className="exports-preview-item">
                  <code className="exports-preview-name">{previewNames[index] || "—"}</code>
                  <span className="exports-preview-source">{sample.source}</span>
                </li>
              ))}
            </ul>
          )}
          <p className="platform-op-hint">
            {t("{engine} 由后端按节点文档解析，本地预览留空。")}
          </p>
        </section>

        <div className="exports-dialog-actions">
          <Button onClick={() => void runExport()} disabled={running}>
            {running ? t("导出中...") : outcome ? t("重新运行") : t("运行导出")}
          </Button>
          {outcome ? (
            <Button
              variant="secondary"
              onClick={() => triggerDownload(outcome.blob, outcome.fileName)}
              title={t("下载 {{name}}", { name: outcome.fileName })}
            >
              <Download size={16} />
              {t("下载文件")}
            </Button>
          ) : null}
        </div>

        {outcome ? (
          <section className="platform-drawer-section">
            <div className="platform-drawer-section-head">
              <h4>{t("节点导出")}</h4>
            </div>
            <div className="exports-result-summary">
              <Badge variant="success">{t("导出 {{count}} 个节点", { count: outcome.exported })}</Badge>
              {outcome.skipped > 0 ? (
                <Badge variant="warning">{t("跳过 {{count}} 个节点", { count: outcome.skipped })}</Badge>
              ) : null}
              {outcome.truncated > 0 ? (
                <Badge variant="muted">
                  {t("因导出上限截断 {{count}} 个节点", { count: outcome.truncated })}
                </Badge>
              ) : null}
            </div>

            {outcome.skipped === 0 ? (
              <p className="platform-op-hint">{t("没有节点被跳过")}</p>
            ) : detailLoading ? (
              <p className="platform-op-hint">{t("正在读取跳过明细...")}</p>
            ) : detailFailed ? (
              <p className="platform-op-hint">{t("跳过原因无法读取")}</p>
            ) : (
              <ExportSkipList
                report={skipReport}
                note={format === "json" ? undefined : t("跳过明细只有 JSON 格式会写进响应体；下面是同条件 JSON 导出的跳过原因。")}
              />
            )}
          </section>
        ) : null}
      </Card>
    </DialogSurface>
  );
}

function SubscriptionUrlDialog({
  profileName,
  url,
  onClose,
  onMessage,
}: {
  profileName: string;
  url: string;
  onClose: () => void;
  onMessage: ExportToast;
}) {
  const { t } = useI18n();
  const showToast = onMessage;
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
      showToast("success", t("订阅地址已复制"));
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      showToast("error", t("复制失败，请手动选择文本"));
    }
  };

  return (
    <DialogSurface title={t("订阅地址只显示这一次")} variant="modal" onClose={onClose}>
      <Card className="modal-card exports-token-card">
        <div className="modal-header">
          <h3>{t("订阅地址只显示这一次")}</h3>
          <Button aria-label={t("关闭")} variant="ghost" size="sm" onClick={onClose}>
            <X size={16} />
          </Button>
        </div>
        <div className="callout callout-error">
          <AlertTriangle size={14} />
          <span>
            {t("服务端只保存令牌的 SHA-256 摘要；关闭本窗口后无法再次查看明文地址。请立即复制并保存到安全的位置。")}
          </span>
        </div>
        <p className="muted">{t("订阅地址只在创建或轮换令牌时返回一次；这里不保存也不缓存令牌。")}</p>
        <p className="exports-token-owner">{profileName}</p>
        <div className="exports-token-row">
          <code className="exports-token-value" title={url}>
            {url}
          </code>
          <Button variant="secondary" onClick={() => void handleCopy()}>
            <Copy size={14} />
            {copied ? t("已复制") : t("复制订阅地址")}
          </Button>
        </div>
        <div className="detail-actions">
          <Button onClick={onClose}>{t("我已保存，关闭")}</Button>
        </div>
      </Card>
    </DialogSurface>
  );
}

export function ExportsPage() {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const queryClient = useQueryClient();
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(20);
  const [createOpen, setCreateOpen] = useState(false);
  const [editingId, setEditingId] = useState("");
  const [exportTarget, setExportTarget] = useState<ExportDialogTarget | null>(null);
  const [reveal, setReveal] = useState<{ name: string; url: string } | null>(null);
  const [pendingEnabledIds, setPendingEnabledIds] = useState<ReadonlySet<string>>(() => new Set());

  const profilesQuery = useQuery({
    queryKey: ["exports", "profiles", page, pageSize],
    queryFn: () => listExportProfiles({ limit: pageSize, offset: page * pageSize }),
    placeholderData: (previous) => previous,
  });

  const profiles = useMemo(() => profilesQuery.data?.items ?? EMPTY_PROFILES, [profilesQuery.data]);
  const totalProfiles = profilesQuery.data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(totalProfiles / pageSize));
  const currentPage = Math.min(page, totalPages - 1);
  const selectedProfile = useMemo(
    () => profiles.find((profile) => profile.id === editingId) ?? null,
    [profiles, editingId],
  );
  const formOpen = createOpen || Boolean(selectedProfile);

  // Platform names are also rendered in the table, so this list loads with the
  // page; the subscription list is only needed once a form is open.
  const platformsQuery = useQuery({
    queryKey: ["exports", "platforms"],
    queryFn: () => listPlatforms({ limit: 200 }),
    staleTime: 60_000,
  });
  const subscriptionsQuery = useQuery({
    queryKey: ["exports", "subscriptions"],
    queryFn: () => listSubscriptions({ limit: 200 }),
    enabled: formOpen,
    staleTime: 60_000,
  });

  const platformOptions = useMemo(
    () => (platformsQuery.data?.items ?? []).map((platform) => ({ id: platform.id, name: platform.name })),
    [platformsQuery.data],
  );
  const subscriptionOptions = useMemo(
    () =>
      (subscriptionsQuery.data?.items ?? []).map((subscription) => ({
        id: subscription.id,
        name: subscription.name,
      })),
    [subscriptionsQuery.data],
  );

  const createForm = useForm<ExportProfileForm>({
    resolver: zodResolver(exportProfileSchema),
    defaultValues: {
      name: "",
      format: "singbox",
      platform_id: "",
      name_template: "{name}",
      enabled: true,
      filter: emptyFilterForm(),
    },
  });

  const editForm = useForm<ExportProfileForm>({
    resolver: zodResolver(exportProfileSchema),
    defaultValues: {
      name: "",
      format: "singbox",
      platform_id: "",
      name_template: "{name}",
      enabled: true,
      filter: emptyFilterForm(),
    },
  });

  const invalidateProfiles = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: ["exports"] });
  }, [queryClient]);

  const openEdit = useCallback(
    (profile: ExportProfile) => {
      editForm.reset(profileToForm(profile));
      setEditingId(profile.id);
    },
    [editForm],
  );

  const createMutation = useMutation({
    mutationFn: createExportProfile,
    onSuccess: async (created) => {
      await invalidateProfiles();
      setCreateOpen(false);
      createForm.reset({
        name: "",
        format: "singbox",
        platform_id: "",
        name_template: "{name}",
        enabled: true,
        filter: emptyFilterForm(),
      });
      showToast("success", t("导出配置 {{name}} 已创建", { name: created.name }));
      // The plaintext URL (and therefore the token) exists only in this response;
      // it is kept in component state, never in storage or the URL.
      if (created.url) {
        setReveal({ name: created.name, url: created.url });
      }
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const updateMutation = useMutation({
    mutationFn: async (form: ExportProfileForm) => {
      if (!selectedProfile) {
        throw new Error("请先选择导出配置");
      }
      return updateExportProfile(selectedProfile.id, formToPayload(form));
    },
    onSuccess: async (updated) => {
      await invalidateProfiles();
      editForm.reset(profileToForm(updated));
      showToast("success", t("导出配置 {{name}} 已更新", { name: updated.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (profile: ExportProfile) => {
      await deleteExportProfile(profile.id);
      return profile;
    },
    onSuccess: async (deleted) => {
      await invalidateProfiles();
      if (editingId === deleted.id) {
        setEditingId("");
      }
      showToast("success", t("导出配置 {{name}} 已删除", { name: deleted.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const rotateMutation = useMutation({
    mutationFn: (profile: ExportProfile) => rotateExportProfileToken(profile.id),
    onSuccess: async (rotated) => {
      await invalidateProfiles();
      showToast("success", t("订阅令牌已轮换"));
      if (rotated.url) {
        setReveal({ name: rotated.name, url: rotated.url });
      }
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const toggleMutation = useMutation({
    mutationFn: (input: { profile: ExportProfile; enabled: boolean }) =>
      updateExportProfile(input.profile.id, { enabled: input.enabled }),
    onSuccess: async (updated, variables) => {
      await invalidateProfiles();
      showToast(
        "success",
        variables.enabled
          ? t("导出配置 {{name}} 已启用", { name: updated.name })
          : t("导出配置 {{name}} 已禁用", { name: updated.name }),
      );
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });
  const toggleEnabled = toggleMutation.mutateAsync;

  const handleToggleEnabled = useCallback(
    async (profile: ExportProfile, enabled: boolean) => {
      if (pendingEnabledIds.has(profile.id)) {
        return;
      }
      setPendingEnabledIds((previous) => new Set(previous).add(profile.id));
      try {
        await toggleEnabled({ profile, enabled });
      } catch {
        // The mutation callbacks already surface the failure.
      } finally {
        setPendingEnabledIds((previous) => {
          const next = new Set(previous);
          next.delete(profile.id);
          return next;
        });
      }
    },
    [pendingEnabledIds, toggleEnabled],
  );

  const handleDelete = useCallback(
    async (profile: ExportProfile) => {
      const confirmed = window.confirm(
        t("确认删除导出配置 {{name}}？订阅地址会立即失效。", { name: profile.name }),
      );
      if (!confirmed) {
        return;
      }
      await deleteMutation.mutateAsync(profile);
    },
    [deleteMutation, t],
  );

  const handleRotate = useCallback(
    async (profile: ExportProfile) => {
      const confirmed = window.confirm(
        t("确认轮换 {{name}} 的订阅令牌？旧地址会立即失效，且新地址只显示一次。", { name: profile.name }),
      );
      if (!confirmed) {
        return;
      }
      await rotateMutation.mutateAsync(profile);
    },
    [rotateMutation, t],
  );

  const onCreateSubmit = createForm.handleSubmit((values) => {
    createMutation.mutate(formToPayload(values));
  });

  const onEditSubmit = editForm.handleSubmit((values) => {
    updateMutation.mutate(values);
  });

  const col = useMemo(() => createColumnHelper<ExportProfile>(), []);

  const columns = useMemo(
    () => [
      col.accessor("name", {
        header: t("名称"),
        cell: (info) => (
          <div className="exports-name-cell">
            <p>{info.getValue()}</p>
            <span className="exports-name-meta">
              {info.row.original.platform_id
                ? t("关联平台：{{name}}", {
                    name:
                      platformOptions.find((platform) => platform.id === info.row.original.platform_id)?.name ??
                      info.row.original.platform_id,
                  })
                : t("未关联平台")}
            </span>
          </div>
        ),
      }),
      col.accessor("format", {
        header: t("格式"),
        cell: (info) => <Badge variant="accent">{t(FORMAT_LABELS[info.getValue()])}</Badge>,
      }),
      col.display({
        id: "filter",
        header: t("过滤条件"),
        cell: (info) => {
          const filter = info.row.original.filter;
          const chips = activeFilterChips(filter);
          if (chips.length === 0) {
            return <span className="exports-filter-empty">{t("全部节点（未设置过滤条件）")}</span>;
          }
          return (
            <div className="exports-chip-row">
              {chips.map((field) => (
                <Badge key={field.key} variant="muted">
                  {`${t(field.label)}: ${filterValueText(field, filter)}`}
                </Badge>
              ))}
            </div>
          );
        },
      }),
      col.accessor("name_template", {
        header: t("命名模板"),
        cell: (info) => <code className="exports-template-cell">{info.getValue() || "{name}"}</code>,
      }),
      col.display({
        id: "enabled",
        header: t("状态"),
        cell: (info) => {
          const profile = info.row.original;
          const enabled = profile.enabled;
          const toggleLabel = enabled
            ? t("停用导出配置 {{name}}", { name: profile.name })
            : t("启用导出配置 {{name}}", { name: profile.name });
          return (
            <div className="exports-status-cell" onClick={(event) => event.stopPropagation()}>
              <Switch
                checked={enabled}
                disabled={pendingEnabledIds.has(profile.id)}
                onChange={(event) => void handleToggleEnabled(profile, event.target.checked)}
                aria-label={toggleLabel}
              />
              <span className="exports-status-text">{enabled ? t("已启用") : t("已禁用")}</span>
            </div>
          );
        },
      }),
      col.display({
        id: "access",
        header: t("订阅访问"),
        cell: (info) => {
          const profile = info.row.original;
          const lastAccess = nsToIso(profile.last_access_at_ns);
          return (
            <div className="exports-access-cell">
              <span>{t("{{count}} 次访问", { count: profile.access_count })}</span>
              <span className="exports-name-meta">
                {lastAccess ? t("最近访问 {{time}}", { time: formatRelativeTime(lastAccess) }) : t("从未访问")}
              </span>
            </div>
          );
        },
      }),
      col.display({
        id: "actions",
        header: t("操作"),
        cell: (info) => {
          const profile = info.row.original;
          return (
            <div className="exports-row-actions" onClick={(event) => event.stopPropagation()}>
              <Button
                size="sm"
                variant="ghost"
                title={t("立即导出")}
                onClick={() => setExportTarget({ profile })}
              >
                <Download size={14} />
              </Button>
              <Button size="sm" variant="ghost" title={t("编辑")} onClick={() => openEdit(profile)}>
                <Pencil size={14} />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                title={t("轮换令牌")}
                onClick={() => void handleRotate(profile)}
                disabled={rotateMutation.isPending}
              >
                <KeyRound size={14} />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                title={t("删除")}
                onClick={() => void handleDelete(profile)}
                disabled={deleteMutation.isPending}
                style={{ color: "var(--delete-btn-color, #c27070)" }}
              >
                <Trash2 size={14} />
              </Button>
            </div>
          );
        },
      }),
    ],
    [
      col,
      deleteMutation.isPending,
      handleDelete,
      handleRotate,
      handleToggleEnabled,
      openEdit,
      pendingEnabledIds,
      platformOptions,
      rotateMutation.isPending,
      t,
    ],
  );

  const changePageSize = (next: number) => {
    setPageSize(next);
    setPage(0);
  };

  return (
    <section className="platform-page exports-page">
      <header className="module-header">
        <div>
          <h2>{t("导出与订阅")}</h2>
          <p className="module-description">{t("把节点池导出成客户端配置，或用一次性令牌把配置发布成订阅。")}</p>
        </div>
        <div className="exports-header-actions">
          <Button variant="secondary" onClick={() => setExportTarget({ profile: null })}>
            <Download size={16} />
            {t("立即导出节点")}
          </Button>
          <Button onClick={() => setCreateOpen(true)}>
            <Plus size={16} />
            {t("新建导出配置")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void profilesQuery.refetch()}
            disabled={profilesQuery.isFetching}
            title={t("刷新")}
            aria-label={t("刷新")}
          >
            <RefreshCw size={16} className={profilesQuery.isFetching ? "spin" : undefined} />
          </Button>
        </div>
      </header>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <Card className="exports-notice-card">
        <div className="callout callout-warning">
          <Info size={14} />
          <span>
            {t("订阅令牌只在创建或轮换时显示一次：服务端只保存它的 SHA-256 摘要，离开后就无法再次查看。")}
          </span>
        </div>
      </Card>

      <Card className="platform-cards-container exports-table-card">
        <div className="list-card-header">
          <div>
            <h3>{t("导出配置列表")}</h3>
            <p>{t("共 {{count}} 个导出配置", { count: totalProfiles })}</p>
          </div>
        </div>

        {profilesQuery.isPending ? (
          <QueryState loading />
        ) : profilesQuery.error ? (
          <QueryState error={profilesQuery.error} onRetry={() => void profilesQuery.refetch()} />
        ) : profiles.length === 0 ? (
          <QueryState empty emptyText={t("还没有导出配置。创建一个配置即可获得订阅地址，或直接导出当前节点池。")} />
        ) : (
          <DataTable
            data={profiles}
            columns={columns}
            onRowClick={openEdit}
            getRowId={(profile) => profile.id}
            className="data-table-exports"
          />
        )}

        <OffsetPagination
          page={currentPage}
          totalPages={totalPages}
          totalItems={totalProfiles}
          pageSize={pageSize}
          pageSizeOptions={PAGE_SIZE_OPTIONS}
          onPageChange={setPage}
          onPageSizeChange={changePageSize}
        />
      </Card>

      {createOpen ? (
        <DialogSurface title={t("新建导出配置")} variant="modal" onClose={() => setCreateOpen(false)}>
          <Card className="modal-card exports-form-card">
            <div className="modal-header">
              <h3>{t("新建导出配置")}</h3>
              <Button aria-label={t("关闭")} variant="ghost" size="sm" onClick={() => setCreateOpen(false)}>
                <X size={16} />
              </Button>
            </div>
            <ExportProfileFields
              form={createForm}
              idPrefix="create-export"
              platforms={platformOptions}
              subscriptions={subscriptionOptions}
            />
            <div className="detail-actions">
              <Button onClick={() => void onCreateSubmit()} disabled={createMutation.isPending}>
                {createMutation.isPending ? t("创建中") : t("确认创建")}
              </Button>
              <Button variant="secondary" onClick={() => setCreateOpen(false)}>
                {t("取消")}
              </Button>
            </div>
            <p className="platform-op-hint">{t("创建后请立即复制订阅地址。")}</p>
          </Card>
        </DialogSurface>
      ) : null}

      {selectedProfile ? (
        <DialogSurface
          title={t("编辑导出配置 {{name}}", { name: selectedProfile.name })}
          variant="drawer"
          onClose={() => setEditingId("")}
        >
          <Card className="drawer-panel">
            <div className="drawer-header">
              <div>
                <h3>{selectedProfile.name}</h3>
                <p>{selectedProfile.id}</p>
              </div>
              <div className="drawer-header-actions">
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={t("关闭")}
                  onClick={() => setEditingId("")}
                >
                  <X size={16} />
                </Button>
              </div>
            </div>

            <div className="platform-drawer-layout">
              <section className="platform-drawer-section">
                <div className="platform-drawer-section-head">
                  <h4>{t("配置信息")}</h4>
                </div>
                <div className="stats-grid">
                  <div>
                    <span>{t("创建时间")}</span>
                    <p>{formatDateTime(nsToIso(selectedProfile.created_at_ns))}</p>
                  </div>
                  <div>
                    <span>{t("更新时间")}</span>
                    <p>{formatDateTime(nsToIso(selectedProfile.updated_at_ns))}</p>
                  </div>
                  <div>
                    <span>{t("访问次数")}</span>
                    <p>{selectedProfile.access_count}</p>
                  </div>
                  <div>
                    <span>{t("最近访问")}</span>
                    <p>{formatDateTime(nsToIso(selectedProfile.last_access_at_ns))}</p>
                  </div>
                </div>
                {!selectedProfile.enabled ? (
                  <div className="callout callout-error">
                    <AlertTriangle size={14} />
                    <span>{t("该配置已停用，订阅地址会返回错误。")}</span>
                  </div>
                ) : null}
                <ExportProfileFields
                  form={editForm}
                  idPrefix={`edit-export-${selectedProfile.id}`}
                  platforms={platformOptions}
                  subscriptions={subscriptionOptions}
                />
                <div className="detail-actions">
                  <Button onClick={() => void onEditSubmit()} disabled={updateMutation.isPending}>
                    {updateMutation.isPending ? t("保存中") : t("保存配置")}
                  </Button>
                </div>
              </section>

              <section className="platform-drawer-section platform-ops-section">
                <div className="platform-drawer-section-head">
                  <h4>{t("运维操作")}</h4>
                </div>
                <div className="platform-ops-list">
                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>{t("立即导出")}</h5>
                      <p className="platform-op-hint">{t("将沿用该配置的过滤条件。")}</p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() => setExportTarget({ profile: selectedProfile })}
                    >
                      {t("运行导出")}
                    </Button>
                  </div>

                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>{t("轮换令牌")}</h5>
                      <p className="platform-op-hint">
                        {t("服务端只保存令牌的 SHA-256 摘要；关闭本窗口后无法再次查看明文地址。请立即复制并保存到安全的位置。")}
                      </p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() => void handleRotate(selectedProfile)}
                      disabled={rotateMutation.isPending}
                    >
                      {t("轮换令牌")}
                    </Button>
                  </div>

                  <div className="platform-op-item">
                    <div className="platform-op-copy">
                      <h5>{t("删除")}</h5>
                      <p className="platform-op-hint">{t("删除后订阅地址会立即失效，且不可撤销。")}</p>
                    </div>
                    <Button
                      variant="danger"
                      onClick={() => void handleDelete(selectedProfile)}
                      disabled={deleteMutation.isPending}
                    >
                      {t("删除")}
                    </Button>
                  </div>
                </div>
              </section>
            </div>
          </Card>
        </DialogSurface>
      ) : null}

      {exportTarget ? (
        <ExportDialog
          target={exportTarget}
          onClose={() => setExportTarget(null)}
          onMessage={showToast}
        />
      ) : null}

      {reveal ? (
        <SubscriptionUrlDialog
          profileName={reveal.name}
          url={reveal.url}
          onClose={() => setReveal(null)}
          onMessage={showToast}
        />
      ) : null}
    </section>
  );
}
