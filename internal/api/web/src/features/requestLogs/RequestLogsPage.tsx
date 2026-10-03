import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Eraser, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader, PanelToolbar, SectionTitle } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { Select } from "../../components/ui/Select";
import { Sheet } from "../../components/ui/Sheet";
import { Table, TableWrap, TBody, TD, TDClip, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../../components/ui/Tabs";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime } from "../../lib/time";
import { getSystemConfig } from "../systemConfig/api";
import { getRequestLog, getRequestLogPayloads, listRequestLogs } from "./api";
import type { RequestLogItem, RequestLogListFilters } from "./types";

type BoolFilter = "all" | "true" | "false";
type ProxyTypeFilter = "all" | "1" | "2" | "3";

type FilterDraft = {
  from_local: string;
  to_local: string;
  platform_name: string;
  account: string;
  target_host: string;
  egress_ip: string;
  proxy_type: ProxyTypeFilter;
  net_ok: BoolFilter;
  http_status: string;
  limit: number;
};

type DebouncedTextFilters = Pick<FilterDraft, "platform_name" | "account" | "target_host" | "egress_ip" | "http_status">;

const defaultFilters: FilterDraft = {
  from_local: "",
  to_local: "",
  platform_name: "",
  account: "",
  target_host: "",
  egress_ip: "",
  proxy_type: "all",
  net_ok: "all",
  http_status: "",
  limit: 100,
};
const FILTER_DEBOUNCE_MS = 100;
const PAGE_SIZE_OPTIONS = [20, 50, 100, 200, 500, 1000, 2000] as const;

const PAYLOAD_TABS = ["request", "response"] as const;
type PayloadTab = (typeof PAYLOAD_TABS)[number];
const EMPTY_LOGS: RequestLogItem[] = [];
const BASE64_DECODE_FAILED = "[Base64 解码失败]";
const UNSUPPORTED_CONTENT_ENCODING_PREFIX = "暂不支持的 Content-Encoding: ";
const CONTENT_ENCODING_DECODE_FAILED_PREFIX = "Content-Encoding=";
const CONTENT_ENCODING_DECODE_FAILED_SUFFIX = " 解压失败";

function toRFC3339(localDateTime: string): string {
  if (!localDateTime) {
    return "";
  }
  const date = new Date(localDateTime);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  return date.toISOString();
}

function boolFromFilter(value: BoolFilter): boolean | undefined {
  if (value === "true") {
    return true;
  }
  if (value === "false") {
    return false;
  }
  return undefined;
}

function formatDurationMs(value: number): string {
  return `${value} ms`;
}

function formatOptionalDurationMs(value: number | undefined): string {
  if (value === undefined || value === null || value <= 0) {
    return "-";
  }
  return formatDurationMs(value);
}

function decodeBase64ToBytes(raw: string): Uint8Array | null {
  if (!raw) {
    return new Uint8Array(0);
  }

  try {
    const binary = atob(raw);
    return Uint8Array.from(binary, (char) => char.charCodeAt(0));
  } catch {
    return null;
  }
}

function decodeBytesToText(bytes: Uint8Array, charset?: string): string {
  if (!bytes.length) {
    return "";
  }

  if (charset) {
    try {
      return new TextDecoder(charset).decode(bytes);
    } catch {
      // Fallback to UTF-8 when charset is unsupported.
    }
  }

  return new TextDecoder().decode(bytes);
}

function decodeBase64ToText(raw: string): string {
  const bytes = decodeBase64ToBytes(raw);
  if (!bytes) {
    return BASE64_DECODE_FAILED;
  }
  return decodeBytesToText(bytes);
}

function parseHeaderMap(headersText: string): Map<string, string> {
  const map = new Map<string, string>();

  for (const line of headersText.split(/\r?\n/)) {
    const separatorIndex = line.indexOf(":");
    if (separatorIndex <= 0) {
      continue;
    }

    const key = line.slice(0, separatorIndex).trim().toLowerCase();
    const value = line.slice(separatorIndex + 1).trim();
    if (!key || !value) {
      continue;
    }

    const current = map.get(key);
    map.set(key, current ? `${current}, ${value}` : value);
  }

  return map;
}

function parseCharset(contentType?: string): string | undefined {
  if (!contentType) {
    return undefined;
  }
  const match = contentType.match(/charset\s*=\s*["']?([^;"'\s]+)/i);
  return match?.[1]?.trim();
}

function parseContentEncodings(contentEncoding?: string): string[] {
  if (!contentEncoding) {
    return [];
  }

  return contentEncoding
    .split(",")
    .map((token) => token.trim().toLowerCase())
    .filter((token) => token && token !== "identity");
}

function normalizeContentEncoding(token: string): "gzip" | "deflate" | "br" | "zstd" | null {
  switch (token) {
    case "gzip":
    case "x-gzip":
      return "gzip";
    case "deflate":
      return "deflate";
    case "br":
      return "br";
    case "zstd":
    case "x-zstd":
      return "zstd";
    default:
      return null;
  }
}

function translatePayloadDecodeErrorMessage(rawMessage: string, t: (text: string, options?: Record<string, unknown>) => string): string {
  if (rawMessage.startsWith(UNSUPPORTED_CONTENT_ENCODING_PREFIX)) {
    const token = rawMessage.slice(UNSUPPORTED_CONTENT_ENCODING_PREFIX.length).trim();
    return t("暂不支持的 Content-Encoding: {{token}}", { token });
  }

  if (rawMessage.startsWith(CONTENT_ENCODING_DECODE_FAILED_PREFIX) && rawMessage.endsWith(CONTENT_ENCODING_DECODE_FAILED_SUFFIX)) {
    const token = rawMessage
      .slice(CONTENT_ENCODING_DECODE_FAILED_PREFIX.length, rawMessage.length - CONTENT_ENCODING_DECODE_FAILED_SUFFIX.length)
      .trim();
    return t("Content-Encoding={{token}} 解压失败", { token });
  }

  return t(rawMessage);
}

async function decompressWithEncoding(bytes: Uint8Array, encoding: "gzip" | "deflate" | "br" | "zstd"): Promise<Uint8Array> {
  if (typeof DecompressionStream === "undefined") {
    throw new Error("当前浏览器不支持 DecompressionStream，无法自动解压");
  }

  const stream = new Blob([new Uint8Array(bytes)]).stream().pipeThrough(new DecompressionStream(encoding as never));
  const arrayBuffer = await new Response(stream).arrayBuffer();
  return new Uint8Array(arrayBuffer);
}

async function decodeContentEncodings(bytes: Uint8Array, encodings: string[]): Promise<Uint8Array> {
  let decoded = bytes;

  // Content-Encoding is listed in the order applied, so decoding must reverse it.
  for (let i = encodings.length - 1; i >= 0; i -= 1) {
    const token = encodings[i];
    const encoding = normalizeContentEncoding(token);
    if (!encoding) {
      throw new Error(`暂不支持的 Content-Encoding: ${token}`);
    }
    try {
      decoded = await decompressWithEncoding(decoded, encoding);
    } catch {
      throw new Error(`Content-Encoding=${token} 解压失败`);
    }
  }

  return decoded;
}

async function decodePayloadBodyForDisplay(rawBodyBase64: string, headersText: string): Promise<string> {
  const bodyBytes = decodeBase64ToBytes(rawBodyBase64);
  if (!bodyBytes) {
    return BASE64_DECODE_FAILED;
  }
  if (!bodyBytes.length) {
    return "";
  }

  const headerMap = parseHeaderMap(headersText);
  const encodings = parseContentEncodings(headerMap.get("content-encoding"));
  const contentType = headerMap.get("content-type");
  let decodedBytes = bodyBytes;
  if (encodings.length) {
    try {
      decodedBytes = await decodeContentEncodings(bodyBytes, encodings);
    } catch {
      // Best-effort: fallback to undecoded bytes when content-encoding decode fails.
    }
  }

  return decodeBytesToText(decodedBytes, parseCharset(contentType));
}

function isFromBeforeTo(fromISO?: string, toISO?: string): boolean {
  if (!fromISO || !toISO) {
    return true;
  }
  return new Date(fromISO).getTime() < new Date(toISO).getTime();
}

function buildActiveFilters(draft: FilterDraft): Omit<RequestLogListFilters, "cursor"> {
  const status = Number(draft.http_status);
  const hasValidStatus = Number.isInteger(status) && status >= 100 && status <= 599;
  const from = toRFC3339(draft.from_local);
  const to = toRFC3339(draft.to_local);
  const validRange = isFromBeforeTo(from, to);

  return {
    from,
    to: validRange ? to : undefined,
    platform_name: draft.platform_name,
    account: draft.account,
    target_host: draft.target_host,
    egress_ip: draft.egress_ip,
    proxy_type: draft.proxy_type === "all" ? undefined : Number(draft.proxy_type),
    net_ok: boolFromFilter(draft.net_ok),
    http_status: hasValidStatus ? status : undefined,
    limit: draft.limit,
    fuzzy: true,
  };
}

function pickDebouncedTextFilters(filters: FilterDraft): DebouncedTextFilters {
  return {
    platform_name: filters.platform_name,
    account: filters.account,
    target_host: filters.target_host,
    egress_ip: filters.egress_ip,
    http_status: filters.http_status,
  };
}

function proxyTypeLabel(proxyType: number): string {
  if (proxyType === 1) {
    return "HTTP 正向代理";
  }
  if (proxyType === 2) {
    return "HTTP 反向代理";
  }
  if (proxyType === 3) {
    return "SOCKS5 正向代理";
  }
  return String(proxyType);
}

function dateLocale(): string {
  return isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
}


function splitDateTime(input: string): { date: string; time: string } {
  if (!input) {
    return { date: "-", time: "-" };
  }

  const value = new Date(input);
  if (Number.isNaN(value.getTime())) {
    return { date: input, time: "-" };
  }

  const date = new Intl.DateTimeFormat(dateLocale(), {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(value);

  const time = new Intl.DateTimeFormat(dateLocale(), {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(value);

  return { date, time };
}

/** One labelled filter with its own validation message, so a bad value is fixed where it sits. */
function FilterField({
  id,
  label,
  warning,
  children,
}: {
  id: string;
  label: string;
  warning?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="min-w-0">
      <label htmlFor={id} className="block text-xs text-ink-soft">
        {label}
      </label>
      <div className="mt-1">{children}</div>
      {warning ? (
        <p className="mt-1 flex items-start gap-1 text-2xs leading-snug text-warn">
          <AlertTriangle size={11} className="mt-px shrink-0" />
          <span>{warning}</span>
        </p>
      ) : null}
    </div>
  );
}

function StatCell({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="label">{label}</div>
      <div className="mt-0.5 min-w-0 text-xs break-all">{children}</div>
    </div>
  );
}

/**
 * Cursor pagination for the request stream: the server hands back a cursor, so the
 * control reads as "page N of a scroll" rather than a numbered range.
 */
function LogsPagination({
  pageIndex,
  hasMore,
  pageSize,
  disabled,
  onPageSizeChange,
  onPrev,
  onNext,
}: {
  pageIndex: number;
  hasMore: boolean;
  pageSize: number;
  disabled: boolean;
  onPageSizeChange: (pageSize: number) => void;
  onPrev: () => void;
  onNext: () => void;
}) {
  const { t } = useI18n();
  return (
    <PanelFooter className="justify-between gap-3">
      <p className="text-xs text-ink-soft">
        {hasMore
          ? t("第 {{page}} 页 · 有更多数据", { page: pageIndex + 1 })
          : t("第 {{page}} 页 · 无更多数据", { page: pageIndex + 1 })}
      </p>
      <div className="flex items-center gap-2">
        <label className="flex items-center gap-1.5 text-xs text-ink-soft">
          <span>{t("每页")}</span>
          <Select
            className="w-auto"
            value={String(pageSize)}
            disabled={disabled}
            onChange={(event) => onPageSizeChange(Number(event.target.value))}
          >
            {PAGE_SIZE_OPTIONS.map((size) => (
              <option key={size} value={size}>
                {size}
              </option>
            ))}
          </Select>
        </label>
        <Button variant="secondary" size="sm" onClick={onPrev} disabled={disabled || pageIndex <= 0}>
          {t("上一页")}
        </Button>
        <Button variant="secondary" size="sm" onClick={onNext} disabled={disabled || !hasMore}>
          {t("下一页")}
        </Button>
      </div>
    </PanelFooter>
  );
}

export function RequestLogsPage() {
  const { t } = useI18n();
  const [filters, setFilters] = useState<FilterDraft>(defaultFilters);
  const [debouncedTextFilters, setDebouncedTextFilters] = useState<DebouncedTextFilters>(() =>
    pickDebouncedTextFilters(defaultFilters),
  );
  const [cursorStack, setCursorStack] = useState<string[]>([""]);
  const [pageIndex, setPageIndex] = useState(0);
  const [selectedLogId, setSelectedLogId] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [payloadTab, setPayloadTab] = useState<PayloadTab>("request");
  const { toasts, dismissToast } = useToast();

  const configQuery = useQuery({
    queryKey: ["system-config"],
    queryFn: getSystemConfig,
    staleTime: 60_000,
  });

  // The text filters are debounced as a unit, so one keystroke is never one request.
  useEffect(() => {
    const timeoutID = window.setTimeout(() => {
      setDebouncedTextFilters({
        platform_name: filters.platform_name,
        account: filters.account,
        target_host: filters.target_host,
        egress_ip: filters.egress_ip,
        http_status: filters.http_status,
      });
    }, FILTER_DEBOUNCE_MS);
    return () => window.clearTimeout(timeoutID);
  }, [filters.account, filters.egress_ip, filters.http_status, filters.platform_name, filters.target_host]);
  const queryFilters = useMemo<FilterDraft>(
    () => ({
      from_local: filters.from_local,
      to_local: filters.to_local,
      proxy_type: filters.proxy_type,
      net_ok: filters.net_ok,
      limit: filters.limit,
      ...debouncedTextFilters,
    }),
    [
      debouncedTextFilters,
      filters.from_local,
      filters.limit,
      filters.net_ok,
      filters.proxy_type,
      filters.to_local,
    ],
  );
  const activeFilters = useMemo(() => buildActiveFilters(queryFilters), [queryFilters]);
  const cursor = cursorStack[pageIndex] || "";

  const rangeInvalid = useMemo(() => {
    const from = toRFC3339(filters.from_local);
    const to = toRFC3339(filters.to_local);
    return Boolean(from && to && !isFromBeforeTo(from, to));
  }, [filters.from_local, filters.to_local]);

  const httpStatusInvalid = useMemo(() => {
    const raw = filters.http_status.trim();
    if (!raw) {
      return false;
    }
    const value = Number(raw);
    return !(Number.isInteger(value) && value >= 100 && value <= 599);
  }, [filters.http_status]);

  const logsQuery = useQuery({
    queryKey: ["request-logs", activeFilters, cursor],
    queryFn: () => listRequestLogs({ ...activeFilters, cursor }),
    refetchInterval: pageIndex === 0 ? 15_000 : false,
    placeholderData: (prev) => prev,
  });

  const logs = logsQuery.data?.items ?? EMPTY_LOGS;
  const isPageTransitioning = logsQuery.isFetching && logsQuery.isPlaceholderData;

  const visibleLogs = isPageTransitioning ? EMPTY_LOGS : logs;

  const selectedLog = useMemo(() => {
    if (!selectedLogId) {
      return null;
    }
    return logs.find((item) => item.id === selectedLogId) ?? null;
  }, [logs, selectedLogId]);

  const detailLogId = selectedLogId;
  const drawerVisible = drawerOpen && Boolean(detailLogId);

  const detailQuery = useQuery({
    queryKey: ["request-log", detailLogId],
    queryFn: () => getRequestLog(detailLogId),
    enabled: drawerVisible,
  });

  const detailLog: RequestLogItem | null = detailQuery.data ?? selectedLog ?? null;

  const payloadQuery = useQuery({
    queryKey: ["request-log-payload", detailLogId],
    queryFn: () => getRequestLogPayloads(detailLogId),
    enabled: drawerVisible && Boolean(detailLog?.payload_present),
    staleTime: 30_000,
  });

  const updateFilter = <K extends keyof FilterDraft>(key: K, value: FilterDraft[K]) => {
    setFilters((prev) => ({ ...prev, [key]: value }));
    setCursorStack([""]);
    setPageIndex(0);
    setSelectedLogId("");
    setDrawerOpen(false);
  };

  const resetFilters = () => {
    setFilters(defaultFilters);
    setDebouncedTextFilters(pickDebouncedTextFilters(defaultFilters));
    setCursorStack([""]);
    setPageIndex(0);
    setSelectedLogId("");
    setDrawerOpen(false);
  };

  const openDrawer = (logId: string) => {
    setSelectedLogId(logId);
    setDrawerOpen(true);
    setPayloadTab("request");
  };

  const moveNext = () => {
    if (isPageTransitioning) {
      return;
    }

    const nextCursor = logsQuery.data?.next_cursor;
    if (!nextCursor) {
      return;
    }

    setCursorStack((prev) => {
      const expectedNextIndex = pageIndex + 1;
      if (prev[expectedNextIndex] === nextCursor) {
        return prev;
      }
      return [...prev.slice(0, expectedNextIndex), nextCursor];
    });
    setPageIndex((prev) => prev + 1);
    setSelectedLogId("");
    setDrawerOpen(false);
  };

  const movePrev = () => {
    if (isPageTransitioning) {
      return;
    }

    setPageIndex((prev) => Math.max(0, prev - 1));
    setSelectedLogId("");
    setDrawerOpen(false);
  };

  const decodedPayload = useQuery({
    queryKey: ["request-payload-decoded", detailLogId, payloadTab, payloadQuery.dataUpdatedAt],
    enabled: drawerVisible && Boolean(payloadQuery.data),
    queryFn: async () => {
      const payload = payloadQuery.data;
      if (!payload) return { headers: "", body: "" };
      const [headersBase64, bodyBase64] = payloadTab === "request"
        ? [payload.req_headers_b64, payload.req_body_b64] : [payload.resp_headers_b64, payload.resp_body_b64];
      const rawHeaders = decodeBase64ToText(headersBase64).trimEnd();
      try {
        const rawBody = (await decodePayloadBodyForDisplay(bodyBase64, rawHeaders)).trimEnd();
        return { headers: rawHeaders === BASE64_DECODE_FAILED ? t(rawHeaders) : rawHeaders, body: rawBody === BASE64_DECODE_FAILED ? t(rawBody) : rawBody };
      } catch (error) {
        const message = error instanceof Error ? translatePayloadDecodeErrorMessage(error.message,t) : t("未知错误");
        return { headers: rawHeaders, body: t("[Body 解码失败：{{message}}]", { message }) };
      }
    },
    gcTime: 60_000,
  });
  const payloadData = decodedPayload.data ?? { headers: "", body: "" };
  const payloadDecodePending = decodedPayload.isFetching;

  const hasMore = Boolean(logsQuery.data?.has_more && logsQuery.data?.next_cursor);

  const renderProxyTypeBadge = useCallback((proxyType: number, context: "table" | "drawer" = "table") => {
    const known = proxyType === 1 || proxyType === 2 || proxyType === 3;
    if (!known) {
      return t(proxyTypeLabel(proxyType));
    }

    let label = "";
    if (proxyType === 1) {
      label = context === "drawer" ? t("HTTP") : t("正向");
    } else if (proxyType === 2) {
      label = t("反向");
    } else {
      label = t("SOCKS5");
    }

    return (
      <Badge tone="outline" title={t(proxyTypeLabel(proxyType))}>
        {label}
      </Badge>
    );
  }, [t]);

  const truncatedFor = (tab: PayloadTab): boolean => {
    if (!payloadQuery.data) {
      return false;
    }
    return tab === "request"
      ? payloadQuery.data.truncated.req_headers || payloadQuery.data.truncated.req_body
      : payloadQuery.data.truncated.resp_headers || payloadQuery.data.truncated.resp_body;
  };

  const payloadTabLabels: Record<PayloadTab, string> = {
    request: t("请求"),
    response: t("响应"),
  };

  const hasDiagnostics = Boolean(
    detailLog &&
      (detailLog.prism_error ||
        detailLog.upstream_stage ||
        detailLog.upstream_err_kind ||
        detailLog.upstream_errno ||
        detailLog.upstream_err_msg),
  );

  const showList = !logsQuery.isLoading && !isPageTransitioning && !logsQuery.isError;

  return (
    <Page bleed>
      <PageHeader
        title={t("请求日志")}
        description={t("按条件检索请求记录，快速定位问题。")}
        meta={
          <>
            <PageMeta label={t("每页")} value={filters.limit} />
            {!configQuery.isLoading && configQuery.data && (
              <Link
                to="/system-config"
                className="flex shrink-0 items-center transition-opacity hover:opacity-80"
              >
                <Badge tone={configQuery.data.request_log_enabled ? "signal" : "warn"} dot>
                  {configQuery.data.request_log_enabled
                    ? t("当前实时日志记录已开启")
                    : t("当前实时日志记录未开启")}
                </Badge>
              </Link>
            )}
          </>
        }
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="page-content page-content--fill">
        <Panel className="flex min-w-0 flex-col">
          <PanelHeader title={t("请求日志")} />

          <PanelToolbar className="filter-toolbar filter-toolbar--dense grid gap-2 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5">
            <FilterField id="logs-from" label={t("开始时间")} warning={rangeInvalid ? t("时间范围错误：开始时间必须早于结束时间，已暂不应用结束时间筛选。") : undefined}>
              <Input
                id="logs-from"
                className="readout"
                type="datetime-local"
                value={filters.from_local}
                onChange={(event) => updateFilter("from_local", event.target.value)}
              />
            </FilterField>

            <FilterField id="logs-to" label={t("结束时间")}>
              <Input
                id="logs-to"
                className="readout"
                type="datetime-local"
                value={filters.to_local}
                onChange={(event) => updateFilter("to_local", event.target.value)}
              />
            </FilterField>

            <FilterField id="logs-platform-name" label={t("平台")}>
              <Input
                id="logs-platform-name"
                value={filters.platform_name}
                onChange={(event) => updateFilter("platform_name", event.target.value)}
              />
            </FilterField>

            <FilterField id="logs-account" label={t("账号")}>
              <Input
                id="logs-account"
                className="readout text-xs"
                value={filters.account}
                onChange={(event) => updateFilter("account", event.target.value)}
              />
            </FilterField>

            <FilterField id="logs-target-host" label={t("目标主机")}>
              <Input
                id="logs-target-host"
                className="readout text-xs"
                value={filters.target_host}
                onChange={(event) => updateFilter("target_host", event.target.value)}
              />
            </FilterField>

            <FilterField id="logs-egress-ip" label={t("出口 IP")}>
              <Input
                id="logs-egress-ip"
                className="readout text-xs"
                inputMode="numeric"
                value={filters.egress_ip}
                onChange={(event) => updateFilter("egress_ip", event.target.value)}
              />
            </FilterField>

            <FilterField id="logs-proxy-type" label={t("代理类型")}>
              <Select
                id="logs-proxy-type"
                value={filters.proxy_type}
                onChange={(event) => updateFilter("proxy_type", event.target.value as ProxyTypeFilter)}
              >
                <option value="all">{t("全部")}</option>
                <option value="1">{t("HTTP 正向代理")}</option>
                <option value="2">{t("HTTP 反向代理")}</option>
                <option value="3">{t("SOCKS5 正向代理")}</option>
              </Select>
            </FilterField>

            <FilterField id="logs-net-ok" label={t("网络状态")}>
              <Select
                id="logs-net-ok"
                value={filters.net_ok}
                onChange={(event) => updateFilter("net_ok", event.target.value as BoolFilter)}
              >
                <option value="all">{t("全部")}</option>
                <option value="true">{t("成功")}</option>
                <option value="false">{t("失败")}</option>
              </Select>
            </FilterField>

            <FilterField
              id="logs-http-status"
              label={t("HTTP 状态")}
              warning={httpStatusInvalid ? t("HTTP 状态码需为 100-599 的整数，当前输入暂不应用。") : undefined}
            >
              <Input
                id="logs-http-status"
                className="readout"
                inputMode="numeric"
                placeholder="100-599"
                value={filters.http_status}
                onChange={(event) => updateFilter("http_status", event.target.value)}
              />
            </FilterField>

            <div className="flex items-end gap-2 sm:col-span-2 lg:col-span-1 lg:justify-end">
              <Button
                size="sm"
                variant="secondary"
                onClick={() => void logsQuery.refetch()}
                disabled={logsQuery.isFetching}
              >
                <RefreshCw size={14} className={logsQuery.isFetching ? "animate-spin" : undefined} />
                {t("刷新")}
              </Button>
              <Button size="sm" variant="secondary" onClick={resetFilters}>
                <Eraser size={14} />
                {t("重置")}
              </Button>
            </div>
          </PanelToolbar>

          {logsQuery.isLoading || isPageTransitioning ? (
            <PanelBody>
              <LoadingState label={t("正在加载日志...")} />
            </PanelBody>
          ) : null}

          {logsQuery.isError ? (
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(logsQuery.error, t)}
                onRetry={() => void logsQuery.refetch()}
              />
            </PanelBody>
          ) : null}

          {showList && !visibleLogs.length ? (
            <PanelBody>
              <EmptyState title={t("没有匹配日志")} />
            </PanelBody>
          ) : null}

          {visibleLogs.length ? (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("时间")}</TH>
                    <TH>{t("代理")}</TH>
                    <TH>{t("平台 / 账号")}</TH>
                    <TH>{t("目标")}</TH>
                    <TH>{t("HTTP")}</TH>
                    <TH>{t("网络")}</TH>
                    <TH className="text-right">{t("首字耗时")}</TH>
                    <TH className="text-right">{t("总耗时")}</TH>
                    <TH className="text-right">{t("流量")}</TH>
                    <TH>{t("节点")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {visibleLogs.map((log) => {
                    const timeParts = splitDateTime(log.ts);
                    const stamp = `${timeParts.date} ${timeParts.time}`;
                    const platform = log.platform_name || "-";
                    const account = log.account || "-";
                    const egress = log.egress_ip || "-";
                    const http = `${log.http_method || "-"} ${log.http_status || "-"}`;
                    return (
                      <TR
                        key={log.id}
                        tabIndex={0}
                        className="cursor-pointer"
                        selected={drawerVisible && detailLogId === log.id}
                        aria-selected={drawerVisible && detailLogId === log.id}
                        onClick={() => openDrawer(log.id)}
                        onKeyDown={(event) => {
                          if (event.target === event.currentTarget && (event.key === "Enter" || event.key === " ")) {
                            event.preventDefault();
                            openDrawer(log.id);
                          }
                        }}
                      >
                        <TDClip className="readout whitespace-nowrap text-xs" title={log.ts}>
                          {stamp}
                        </TDClip>
                        <TD>{renderProxyTypeBadge(log.proxy_type)}</TD>
                        <TDClip className="text-xs" title={`${platform} · ${account}`}>
                          {platform}
                          <span className="readout text-ink-faint"> · {account}</span>
                        </TDClip>
                        <TDClip className="readout text-xs" title={log.target_url || log.target_host}>
                          {log.target_host || "-"}
                        </TDClip>
                        <TDClip className="readout text-xs text-ink-soft" title={http}>
                          {http}
                        </TDClip>
                        <TD>
                          <Badge tone={log.net_ok ? "signal" : "warn"} dot>
                            {log.net_ok ? t("成功") : t("失败")}
                          </Badge>
                        </TD>
                        <TDNum className="text-xs text-ink-soft">
                          {formatOptionalDurationMs(log.first_byte_duration_ms)}
                        </TDNum>
                        <TDNum className="text-xs">{formatDurationMs(log.duration_ms)}</TDNum>
                        <TDNum className="text-xs text-ink-soft">
                          {formatBytes((log.ingress_bytes || 0) + (log.egress_bytes || 0))}
                        </TDNum>
                        <TDClip className="text-xs" title={`${log.node_tag || "-"} · ${egress}`}>
                          {log.node_tag ? (
                            <Link
                              to={`/nodes?tag_keyword=${encodeURIComponent(log.node_tag)}`}
                              title={t("在节点池搜索 {{tag}}", { tag: log.node_tag })}
                              className="text-signal-deep hover:underline"
                              onClick={(event) => event.stopPropagation()}
                            >
                              {log.node_tag}
                            </Link>
                          ) : (
                            <span className="text-ink-faint">-</span>
                          )}
                          <span className="readout text-ink-faint"> · {egress}</span>
                        </TDClip>
                      </TR>
                    );
                  })}
                </TBody>
              </Table>
            </TableWrap>
          ) : null}

          <LogsPagination
            pageIndex={pageIndex}
            hasMore={hasMore}
            pageSize={filters.limit}
            disabled={isPageTransitioning}
            onPageSizeChange={(limit) => updateFilter("limit", limit)}
            onPrev={movePrev}
            onNext={moveNext}
          />
        </Panel>
      </div>

      <Sheet
        open={drawerVisible}
        onOpenChange={(open) => {
          if (!open) {
            setDrawerOpen(false);
          }
        }}
        title={detailLog ? detailLog.target_host || detailLog.account || t("请求日志详情") : t("请求日志详情")}
        description={detailLog ? <span className="readout text-xs text-ink-faint">{detailLog.id}</span> : undefined}
        width="lg"
      >
        {detailLog ? (
          <div className="flex flex-col gap-5">
            <section>
              <SectionTitle>{t("日志摘要")}</SectionTitle>
              <p className="text-xs leading-relaxed text-ink-soft">{t("请求时间、协议结果与平台路由信息。")}</p>

              {detailQuery.isError ? (
                <div className="mt-3">
                  <ErrorState
                    message={formatApiErrorMessage(detailQuery.error, t)}
                    onRetry={() => void detailQuery.refetch()}
                  />
                </div>
              ) : null}

              <ReadoutStrip className="mt-3 grid-cols-2 sm:grid-cols-3">
                <ReadoutCell>
                  <Readout label={t("时间")} value={formatDateTime(detailLog.ts)} size="sm" />
                </ReadoutCell>
                <ReadoutCell>
                  <Readout label={t("代理类型")} value={renderProxyTypeBadge(detailLog.proxy_type, "drawer")} size="sm" />
                </ReadoutCell>
                <ReadoutCell>
                  <Readout
                    label={t("HTTP")}
                    value={`${detailLog.http_method || "-"} ${detailLog.http_status || "-"}`}
                    size="sm"
                  />
                </ReadoutCell>
                <ReadoutCell>
                  <Readout
                    label={t("首字耗时")}
                    value={formatOptionalDurationMs(detailLog.first_byte_duration_ms)}
                    size="sm"
                  />
                </ReadoutCell>
                <ReadoutCell>
                  <Readout label={t("总耗时")} value={formatDurationMs(detailLog.duration_ms)} size="sm" />
                </ReadoutCell>
                <ReadoutCell>
                  <Readout label={t("流量")} value={formatBytes((detailLog.ingress_bytes || 0) + (detailLog.egress_bytes || 0))} size="sm" />
                </ReadoutCell>
                <ReadoutCell>
                  <Readout label={t("平台")} value={detailLog.platform_name || "-"} size="sm" />
                </ReadoutCell>
                <ReadoutCell>
                  <Readout label={t("账号")} value={detailLog.account || "-"} size="sm" />
                </ReadoutCell>
                <ReadoutCell>
                  <Readout label={t("出口 IP")} value={detailLog.egress_ip || "-"} size="sm" />
                </ReadoutCell>
                <ReadoutCell>
                  <Readout label={t("客户端 IP")} value={detailLog.client_ip || "-"} size="sm" />
                </ReadoutCell>
              </ReadoutStrip>
            </section>

            <section className="border-t border-rule pt-4">
              <SectionTitle>{t("诊断")}</SectionTitle>
              <p className="text-xs leading-relaxed text-ink-soft">{t("异常排查与连接状态分析。")}</p>

              {hasDiagnostics ? (
                <dl className="mt-3 flex flex-col gap-2 text-xs">
                  {detailLog.prism_error ? (
                    <div className="grid grid-cols-[minmax(5rem,auto)_1fr] gap-x-4">
                      <dt className="font-medium text-alert">{t("Prism 错误:")}</dt>
                      <dd className="readout min-w-0 break-all">{detailLog.prism_error}</dd>
                    </div>
                  ) : null}
                  {detailLog.upstream_stage ? (
                    <div className="grid grid-cols-[minmax(5rem,auto)_1fr] gap-x-4">
                      <dt className="font-medium text-warn">{t("失败阶段:")}</dt>
                      <dd className="readout min-w-0 break-all">{detailLog.upstream_stage}</dd>
                    </div>
                  ) : null}
                  {detailLog.upstream_err_kind ? (
                    <div className="grid grid-cols-[minmax(5rem,auto)_1fr] gap-x-4">
                      <dt className="font-medium text-ink-soft">{t("错误类型:")}</dt>
                      <dd className="readout min-w-0 break-all">{detailLog.upstream_err_kind}</dd>
                    </div>
                  ) : null}
                  {detailLog.upstream_errno ? (
                    <div className="grid grid-cols-[minmax(5rem,auto)_1fr] gap-x-4">
                      <dt className="font-medium text-ink-soft">Errno:</dt>
                      <dd className="readout min-w-0 break-all">{detailLog.upstream_errno}</dd>
                    </div>
                  ) : null}
                  {detailLog.upstream_err_msg ? (
                    <div className="grid grid-cols-[minmax(5rem,auto)_1fr] gap-x-4">
                      <dt className="font-medium text-ink-soft">{t("错误详情:")}</dt>
                      <dd className="min-w-0 text-ink break-all">{detailLog.upstream_err_msg}</dd>
                    </div>
                  ) : null}
                </dl>
              ) : null}

              {!detailLog.prism_error && !detailLog.upstream_stage && !detailLog.upstream_err_kind && !detailLog.upstream_err_msg ? (
                <div className="mt-3">
                  <Badge tone="signal" dot>
                    {t("当前请求未产生异常诊断信息")}
                  </Badge>
                </div>
              ) : null}
            </section>

            <section className="border-t border-rule pt-4">
              <SectionTitle>{t("目标与节点")}</SectionTitle>
              <p className="text-xs leading-relaxed text-ink-soft">{t("请求目标与命中节点信息。")}</p>

              <div className="mt-3 grid gap-x-6 gap-y-3 text-xs sm:grid-cols-3">
                <StatCell label={t("目标地址")}>
                  <div className="readout">{detailLog.target_host || "-"}</div>
                  <div className="readout mt-0.5 text-2xs break-all text-ink-faint">{detailLog.target_url || "-"}</div>
                </StatCell>
                <StatCell label={t("流量")}>
                  <div className="readout text-sm">
                    {formatBytes((detailLog.ingress_bytes || 0) + (detailLog.egress_bytes || 0))}
                  </div>
                  <div className="readout mt-0.5 flex gap-3 text-2xs text-ink-faint">
                    <span>
                      {t("入站")} {formatBytes(detailLog.ingress_bytes || 0)}
                    </span>
                    <span>
                      {t("出站")} {formatBytes(detailLog.egress_bytes || 0)}
                    </span>
                  </div>
                </StatCell>
                <StatCell label={t("节点")}>
                  <div className="text-xs">{detailLog.node_tag || "-"}</div>
                  <div className="readout mt-0.5 text-2xs break-all text-ink-faint">{detailLog.node_hash || "-"}</div>
                </StatCell>
              </div>
            </section>

            <section className="border-t border-rule pt-4">
              <SectionTitle>{t("报文内容")}</SectionTitle>
              <p className="text-xs leading-relaxed text-ink-soft">{t("查看请求/响应内容。")}</p>

              {!detailLog.payload_present ? (
                <p className="mt-3 text-xs text-ink-soft">{t("该条日志未记录报文内容。")}</p>
              ) : (
                <Tabs
                  value={payloadTab}
                  className="mt-3"
                  onValueChange={(next) => setPayloadTab(next === "response" ? "response" : "request")}
                >
                  <TabsList>
                    {PAYLOAD_TABS.map((tab) => (
                      <TabsTrigger key={tab} value={tab}>
                        {payloadTabLabels[tab]}
                        {truncatedFor(tab) ? <Badge tone="warn">{t("已截断")}</Badge> : null}
                      </TabsTrigger>
                    ))}
                  </TabsList>

                  <TabsContent value={payloadTab}>
                    {payloadQuery.isError ? (
                      <div className="mt-3">
                        <ErrorState
                          message={formatApiErrorMessage(payloadQuery.error, t)}
                          onRetry={() => void payloadQuery.refetch()}
                        />
                      </div>
                    ) : null}

                    {(payloadQuery.isFetching || payloadDecodePending) && !(payloadData.headers || payloadData.body) ? (
                      <LoadingState label={t("加载报文内容中...")} />
                    ) : (
                      <div className="mt-3 flex flex-col gap-2">
                        <pre className="readout max-h-72 overflow-auto rounded-control border border-rule bg-paper-sunk px-3 py-2 text-2xs leading-relaxed break-words whitespace-pre-wrap">
                          {payloadData.headers || t("（空 Headers）")}
                        </pre>
                        <pre className="readout max-h-96 overflow-auto rounded-control border border-rule bg-paper-sunk px-3 py-2 text-2xs leading-relaxed break-words whitespace-pre-wrap">
                          {payloadData.body || t("（空 Body）")}
                        </pre>
                      </div>
                    )}
                  </TabsContent>
                </Tabs>
              )}
            </section>
          </div>
        ) : null}
      </Sheet>
    </Page>
  );
}