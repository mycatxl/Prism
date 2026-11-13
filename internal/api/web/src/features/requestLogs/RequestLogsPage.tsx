import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Eraser, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  CommandSurface,
  ControlButton,
  ControlInput,
  ControlSelect,
  ControlTable,
  ControlTableBody,
  ControlTableCell,
  ControlTableClipCell,
  ControlTableColumn,
  ControlTableHead,
  ControlTableNumericCell,
  ControlTableRow,
  ControlTableWrap,
  ControlTabs,
  InspectorDrawer,
  QueryBadge,
  SectionHeading,
  WorkspaceCard,
  WorkspacePage,
} from "../../components/control-plane";
import { ErrorState, EmptyState, LoadingState } from "../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { ToastContainer } from "../../components/control-plane";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatBytes } from "../../lib/bytes";
import { cn } from "../../lib/cn";
import { formatApiErrorMessage } from "../../lib/error-message";
import { opsPaths } from "../../lib/paths";
import { formatDateTime } from "../../lib/time";
import { nodeTagLabel } from "../nodes/detail/nodeMeta";
import { getSystemConfig, SYSTEM_CONFIG_QUERY_KEY, SYSTEM_CONFIG_STALE_MS } from "../systemConfig/api";
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

const PROXY_TYPE_FILTER_OPTIONS: Array<{ id: ProxyTypeFilter; label: string }> = [
  { id: "all", label: "全部" },
  { id: "1", label: "HTTP 正向代理" },
  { id: "2", label: "HTTP 反向代理" },
  { id: "3", label: "SOCKS5 正向代理" },
];

const NET_OK_FILTER_OPTIONS: Array<{ id: BoolFilter; label: string }> = [
  { id: "all", label: "全部" },
  { id: "true", label: "成功" },
  { id: "false", label: "失败" },
];

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
    <div className="workspace-pagination">
      <p>
        {hasMore
          ? t("第 {{page}} 页 · 有更多数据", { page: pageIndex + 1 })
          : t("第 {{page}} 页 · 无更多数据", { page: pageIndex + 1 })}
      </p>
      <div className="workspace-pagination__actions">
        <div className="flex items-center gap-1.5 text-xs text-ink-soft">
          <span aria-hidden>{t("每页")}</span>
          <ControlSelect
            aria-label={t("每页")}
            className="w-auto"
            selectedKey={String(pageSize)}
            disabled={disabled}
            items={PAGE_SIZE_OPTIONS.map((size) => ({ id: String(size), label: String(size) }))}
            onSelectionChange={(key) => {
              if (key !== null) onPageSizeChange(Number(key));
            }}
          />
        </div>
        <ControlButton variant="secondary" size="sm" onPress={onPrev} isDisabled={disabled || pageIndex <= 0}>
          {t("上一页")}
        </ControlButton>
        <ControlButton variant="secondary" size="sm" onPress={onNext} isDisabled={disabled || !hasMore}>
          {t("下一页")}
        </ControlButton>
      </div>
    </div>
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
    // The shared key: this page reads one flag off the config, and a page-local
    // key meant the same url was fetched twice and invalidated twice.
    queryKey: SYSTEM_CONFIG_QUERY_KEY,
    queryFn: getSystemConfig,
    staleTime: SYSTEM_CONFIG_STALE_MS,
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
    queryFn: async () => ({
      ...(await listRequestLogs({ ...activeFilters, cursor })),
      pageIndex,
    }),
    refetchInterval: pageIndex === 0 ? 15_000 : false,
    placeholderData: (prev) => prev,
  });

  const [lastPage, setLastPage] = useState<typeof logsQuery.data>(undefined);
  if (logsQuery.data !== undefined && !logsQuery.isPlaceholderData && logsQuery.data !== lastPage) {
    setLastPage(logsQuery.data);
  }
  const pageData = logsQuery.data ?? lastPage;
  const logs = pageData?.items ?? EMPTY_LOGS;
  const hasData = pageData !== undefined;
  const isPageTransitioning = logsQuery.isFetching && (logsQuery.isPlaceholderData || (hasData && logsQuery.data === undefined));
  const visibleLogs = logs;
  const isInitialLoading = logsQuery.isLoading && !hasData;
  const hasStaleError = logsQuery.isError && hasData;
  const settledPageIndex = pageData?.pageIndex ?? pageIndex;

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

  const hasMore = Boolean(pageData?.has_more && pageData?.next_cursor);

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
      <span title={t(proxyTypeLabel(proxyType))}>
        <QueryBadge label={label} />
      </span>
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

  const showList = hasData && !isInitialLoading;

  return (
    <WorkspacePage
      eyebrow="Governance / request logs"
      title={t("请求日志")}
      description={t("按时间、平台、目标和网络结果筛选保留请求日志。")}
      meta={
        !configQuery.isLoading && configQuery.data ? (
          <Link
            to={opsPaths.config}
            className="flex shrink-0 items-center transition-opacity hover:opacity-80"
            aria-label={
              configQuery.data.request_log_enabled
                ? t("请求日志已开启，前往系统配置")
                : t("请求日志未开启，前往系统配置")
            }
          >
            <span className="flex items-center gap-2">
              <span
                aria-hidden
                className={cn(
                  "workspace-status-dot",
                  configQuery.data.request_log_enabled ? "workspace-status-dot--signal" : "workspace-status-dot--warn",
                )}
              />
              <span className="workspace-status-text">{configQuery.data.request_log_enabled ? t("已开启") : t("未开启")}</span>
            </span>
          </Link>
        ) : undefined
      }
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <WorkspaceCard headerless eyebrow="Governance / request logs" title={t("请求日志")} className="min-w-0">
        <CommandSurface className="workspace-filter-toolbar workspace-filter-toolbar--dense">
          <div className="grid w-full gap-2 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5">
            <FilterField id="logs-from" label={t("开始时间")} warning={rangeInvalid ? t("时间范围错误：开始时间必须早于结束时间，已暂不应用结束时间筛选。") : undefined}>
              <ControlInput
                id="logs-from"
                className="readout"
                type="datetime-local"
                value={filters.from_local}
                onChange={(event) => updateFilter("from_local", event.target.value)}
              />
            </FilterField>

            <FilterField id="logs-to" label={t("结束时间")}>
              <ControlInput
                id="logs-to"
                className="readout"
                type="datetime-local"
                value={filters.to_local}
                onChange={(event) => updateFilter("to_local", event.target.value)}
              />
            </FilterField>

            <FilterField id="logs-platform-name" label={t("平台")}>
              <ControlInput
                id="logs-platform-name"
                value={filters.platform_name}
                onChange={(event) => updateFilter("platform_name", event.target.value)}
              />
            </FilterField>

            <FilterField id="logs-account" label={t("账号")}>
              <ControlInput
                id="logs-account"
                className="readout text-xs"
                value={filters.account}
                onChange={(event) => updateFilter("account", event.target.value)}
              />
            </FilterField>

            <FilterField id="logs-target-host" label={t("目标主机")}>
              <ControlInput
                id="logs-target-host"
                className="readout text-xs"
                value={filters.target_host}
                onChange={(event) => updateFilter("target_host", event.target.value)}
              />
            </FilterField>

            <FilterField id="logs-egress-ip" label={t("出口 IP")}>
              <ControlInput
                id="logs-egress-ip"
                className="readout text-xs"
                inputMode="numeric"
                value={filters.egress_ip}
                onChange={(event) => updateFilter("egress_ip", event.target.value)}
              />
            </FilterField>

            <ControlSelect
              label={t("代理类型")}
              className="min-w-0"
              selectedKey={filters.proxy_type}
              items={PROXY_TYPE_FILTER_OPTIONS.map((option) => ({ id: option.id, label: t(option.label) }))}
              onSelectionChange={(key) => {
                const next = PROXY_TYPE_FILTER_OPTIONS.find((option) => option.id === key)?.id;
                if (next) updateFilter("proxy_type", next);
              }}
            />

            <ControlSelect
              label={t("网络状态")}
              className="min-w-0"
              selectedKey={filters.net_ok}
              items={NET_OK_FILTER_OPTIONS.map((option) => ({ id: option.id, label: t(option.label) }))}
              onSelectionChange={(key) => {
                const next = NET_OK_FILTER_OPTIONS.find((option) => option.id === key)?.id;
                if (next) updateFilter("net_ok", next);
              }}
            />

            <FilterField
              id="logs-http-status"
              label={t("HTTP 状态")}
              warning={httpStatusInvalid ? t("HTTP 状态码需为 100-599 的整数，当前输入暂不应用。") : undefined}
            >
              <ControlInput
                id="logs-http-status"
                className="readout"
                inputMode="numeric"
                placeholder="100-599"
                value={filters.http_status}
                onChange={(event) => updateFilter("http_status", event.target.value)}
              />
            </FilterField>

            <div className="flex items-end gap-2 sm:col-span-2 lg:col-span-1 lg:justify-end">
              <ControlButton
                size="sm"
                variant="secondary"
                onPress={() => void logsQuery.refetch()}
                isDisabled={logsQuery.isFetching}
              >
                <RefreshCw size={14} className={logsQuery.isFetching ? "animate-spin" : undefined} />
                {t("刷新")}
              </ControlButton>
              <ControlButton size="sm" variant="secondary" onPress={resetFilters}>
                <Eraser size={14} />
                {t("重置")}
              </ControlButton>
            </div>
          </div>
        </CommandSurface>

          {isInitialLoading ? (
            <div>
              <LoadingState label={t("正在加载日志...")} />
            </div>
          ) : null}

          {isPageTransitioning ? <p className="workspace-muted"><span className="workspace-spinner" aria-hidden />{t("更新中...")}</p> : null}

          {logsQuery.isError && !hasData ? (
            <div>
              <ErrorState
                message={formatApiErrorMessage(logsQuery.error, t)}
                onRetry={() => void logsQuery.refetch()}
              />
            </div>
          ) : null}

          {hasStaleError ? <div className="workspace-notice workspace-notice--warn"><span>{t("数据可能已过期")} · {formatApiErrorMessage(logsQuery.error, t)}</span><ControlButton variant="ghost" size="sm" onPress={() => void logsQuery.refetch()}>{t("重试")}</ControlButton></div> : null}

          {showList && !isPageTransitioning && !logsQuery.isError && !visibleLogs.length ? (
            <div>
              <EmptyState title={t("没有匹配日志")} action={<ControlButton variant="secondary" size="sm" onPress={resetFilters}>{t("重置")}</ControlButton>} />
            </div>
          ) : null}

          {visibleLogs.length ? (
            <ControlTableWrap>
              <ControlTable>
                <ControlTableHead>
                  <ControlTableColumn>{t("时间")}</ControlTableColumn>
                  <ControlTableColumn>{t("代理")}</ControlTableColumn>
                  <ControlTableColumn>{t("平台 / 账号")}</ControlTableColumn>
                  <ControlTableColumn isRowHeader>{t("目标")}</ControlTableColumn>
                  <ControlTableColumn>{t("HTTP")}</ControlTableColumn>
                  <ControlTableColumn>{t("网络")}</ControlTableColumn>
                  <ControlTableColumn className="text-right">{t("首字耗时")}</ControlTableColumn>
                  <ControlTableColumn className="text-right">{t("总耗时")}</ControlTableColumn>
                  <ControlTableColumn className="text-right">{t("流量")}</ControlTableColumn>
                  <ControlTableColumn>{t("节点")}</ControlTableColumn>
                </ControlTableHead>
                <ControlTableBody>
                  {visibleLogs.map((log) => {
                    const timeParts = splitDateTime(log.ts);
                    const stamp = `${timeParts.date} ${timeParts.time}`;
                    const platform = log.platform_name || "-";
                    const account = log.account || "-";
                    const egress = log.egress_ip || "-";
                    const http = `${log.http_method || "-"} ${log.http_status || "-"}`;
                    return (
                      <ControlTableRow
                        key={log.id}
                        className="cursor-pointer"
                        selected={drawerVisible && detailLogId === log.id}
                        onAction={() => openDrawer(log.id)}
                      >
                        <ControlTableClipCell className="readout whitespace-nowrap text-xs" title={log.ts}>
                          {stamp}
                        </ControlTableClipCell>
                        <ControlTableCell>{renderProxyTypeBadge(log.proxy_type)}</ControlTableCell>
                        <ControlTableClipCell className="text-xs" title={`${platform} · ${account}`}>
                          {platform}
                          <span className="readout text-ink-faint"> · {account}</span>
                        </ControlTableClipCell>
                        <ControlTableClipCell className="readout text-xs" title={log.target_url || log.target_host}>
                          {log.target_host || "-"}
                        </ControlTableClipCell>
                        <ControlTableClipCell className="readout text-xs text-ink-soft" title={http}>
                          {http}
                        </ControlTableClipCell>
                        <ControlTableCell>
                          <span className="flex items-center gap-2">
                            <span
                              aria-hidden
                              className={cn("workspace-status-dot", log.net_ok ? "workspace-status-dot--signal" : "workspace-status-dot--warn")}
                            />
                            <span className="workspace-status-text">{log.net_ok ? t("成功") : t("失败")}</span>
                          </span>
                        </ControlTableCell>
                        <ControlTableNumericCell className="text-xs text-ink-soft">
                          {formatOptionalDurationMs(log.first_byte_duration_ms)}
                        </ControlTableNumericCell>
                        <ControlTableNumericCell className="text-xs">{formatDurationMs(log.duration_ms)}</ControlTableNumericCell>
                        <ControlTableNumericCell className="text-xs text-ink-soft">
                          {formatBytes((log.ingress_bytes || 0) + (log.egress_bytes || 0))}
                        </ControlTableNumericCell>
                        <ControlTableClipCell className="text-xs" title={`${nodeTagLabel(log.node_tag) || "-"} · ${egress}`}>
                          {nodeTagLabel(log.node_tag) ? (
                            <Link
                              to={`${opsPaths.nodes}?tag_keyword=${encodeURIComponent(nodeTagLabel(log.node_tag))}`}
                              title={t("在节点池搜索 {{tag}}", { tag: nodeTagLabel(log.node_tag) })}
                              className="text-signal-deep hover:underline"
                              onPointerDown={(event) => event.stopPropagation()}
                              onKeyDown={(event) => event.stopPropagation()}
                              onClick={(event) => event.stopPropagation()}
                            >
                              {nodeTagLabel(log.node_tag)}
                            </Link>
                          ) : (
                            <span className="text-ink-faint">-</span>
                          )}
                          <span className="readout text-ink-faint"> · {egress}</span>
                        </ControlTableClipCell>
                      </ControlTableRow>
                    );
                  })}
                </ControlTableBody>
              </ControlTable>
            </ControlTableWrap>
          ) : null}

          <LogsPagination
            pageIndex={settledPageIndex}
            hasMore={hasMore}
            pageSize={filters.limit}
            disabled={isPageTransitioning || logsQuery.isError}
            onPageSizeChange={(limit) => updateFilter("limit", limit)}
            onPrev={movePrev}
            onNext={moveNext}
          />
        </WorkspaceCard>

      <InspectorDrawer
        open={drawerVisible}
        onOpenChange={(open) => {
          if (!open) {
            setDrawerOpen(false);
          }
        }}
        title={detailLog ? detailLog.target_host || detailLog.account || t("请求日志详情") : t("请求日志详情")}
        eyebrow={detailLog ? <span className="workspace-mono text-2xs text-ink-faint">{detailLog.id}</span> : t("请求日志")}
      >
        {detailLog ? (
          <div className="flex flex-col gap-5">
            <section>
              <SectionHeading>{t("日志摘要")}</SectionHeading>
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
              <SectionHeading>{t("诊断")}</SectionHeading>
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
                  <QueryBadge label={t("当前请求未产生异常诊断信息")} tone="signal" />
                </div>
              ) : null}
            </section>

            <section className="border-t border-rule pt-4">
              <SectionHeading>{t("目标与节点")}</SectionHeading>
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
                  <div className="text-xs">{nodeTagLabel(detailLog.node_tag) || "-"}</div>
                  <div className="readout mt-0.5 text-2xs break-all text-ink-faint">{detailLog.node_hash || "-"}</div>
                </StatCell>
              </div>
            </section>

            <section className="border-t border-rule pt-4">
              <SectionHeading>{t("报文内容")}</SectionHeading>
              <p className="text-xs leading-relaxed text-ink-soft">{t("查看请求/响应内容。")}</p>

              {!detailLog.payload_present ? (
                <p className="mt-3 text-xs text-ink-soft">{t("该条日志未记录报文内容。")}</p>
              ) : (
                <>
                  <ControlTabs
                    label={t("报文内容")}
                    selectedKey={payloadTab}
                    onSelectionChange={(next) => setPayloadTab(next === "response" ? "response" : "request")}
                    items={PAYLOAD_TABS.map((tab) => ({
                      id: tab,
                      label: (
                        <>
                          {payloadTabLabels[tab]}
                          {truncatedFor(tab) ? <QueryBadge label={t("已截断")} tone="warn" /> : null}
                        </>
                      ),
                    }))}
                  />

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
                    <pre className="workspace-code-block max-h-72 overflow-auto">
                      {payloadData.headers || t("（空 Headers）")}
                    </pre>
                    <pre className="workspace-code-block max-h-96 overflow-auto">
                      {payloadData.body || t("（空 Body）")}
                    </pre>
                  </div>
                )}
                </>
              )}
            </section>
          </div>
        ) : null}
      </InspectorDrawer>
    </WorkspacePage>
  );
}
