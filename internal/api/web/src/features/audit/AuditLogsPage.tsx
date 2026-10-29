import { useQuery } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { useState } from "react";
import {
  ControlButton,
  ControlSelect,
  EmptyWorkState,
  ObjectList,
  ObjectRow,
  WorkspacePage,
  QueryBadge,
  StatusMark,
  WorkspaceCard,
} from "../../components/control-plane";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatApiErrorMessage } from "../../lib/error-message";
import { listAuditLogs } from "./api";
import type { AuditLogEntry } from "./types";

const PAGE_SIZE_OPTIONS = [20, 50, 100, 200] as const;
const DEFAULT_PAGE_SIZE = 50;
const AUDIT_MAX_LIMIT = 200;
const NANOS_PER_MILLI = 1_000_000;
const MAX_VISIBLE_DETAIL_KEYS = 3;
const EMPTY_ENTRIES: AuditLogEntry[] = [];

type Translator = (text: string, values?: Record<string, unknown>) => string;

function splitAuditAction(action: string): { method: string; route: string } {
  const raw = action.trim();
  if (!raw) return { method: "", route: "" };
  const separator = raw.indexOf(" ");
  if (separator < 0) return { method: raw, route: "" };
  return { method: raw.slice(0, separator), route: raw.slice(separator + 1).trim() };
}

function splitAuditTime(atNs: number): { date: string; time: string } {
  if (!Number.isFinite(atNs) || atNs <= 0) return { date: "-", time: "-" };
  const moment = new Date(Math.round(atNs / NANOS_PER_MILLI));
  if (Number.isNaN(moment.getTime())) return { date: "-", time: "-" };
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return {
    date: new Intl.DateTimeFormat(locale, { year: "numeric", month: "2-digit", day: "2-digit" }).format(moment),
    time: new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(moment),
  };
}

function auditDetailKeys(detail: string): string[] {
  const raw = detail.trim();
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return [];
    const keys = (parsed as { keys?: unknown }).keys;
    return Array.isArray(keys) ? keys.filter((key): key is string => typeof key === "string" && key.trim() !== "") : [];
  } catch {
    return [];
  }
}

function AuditPagination({
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
      <span>{hasMore ? t("第 {{page}} 页 · 有更多数据", { page: pageIndex + 1 }) : t("第 {{page}} 页 · 无更多数据", { page: pageIndex + 1 })}</span>
      <div>
        <ControlSelect
          aria-label={t("每页")}
          items={PAGE_SIZE_OPTIONS.map((size) => ({ id: String(size), label: `${size} / ${t("页")}` }))}
          selectedKey={String(pageSize)}
          disabled={disabled}
          onSelectionChange={(key) => {
            if (key !== null) onPageSizeChange(Number(key));
          }}
        />
        <ControlButton variant="ghost" size="sm" onPress={onPrev} isDisabled={disabled || pageIndex <= 0}>{t("上一页")}</ControlButton>
        <ControlButton variant="secondary" size="sm" onPress={onNext} isDisabled={disabled || !hasMore}>{t("下一页")}</ControlButton>
      </div>
    </div>
  );
}

function AuditEntryRow({ entry, t }: { entry: AuditLogEntry; t: Translator }) {
  const parts = splitAuditTime(entry.at_ns);
  const { method, route } = splitAuditAction(entry.action);
  const keys = auditDetailKeys(entry.detail);
  const hidden = Math.max(0, keys.length - MAX_VISIBLE_DETAIL_KEYS);
  const actor = entry.actor.trim();
  const target = entry.target.trim();
  const remote = entry.remote_addr.trim();
  return (
    <ObjectRow
      leading={<span className="workspace-platform-row__mark" aria-hidden>AUD</span>}
      title={`${parts.date} ${parts.time}`}
      subtitle={`${method || t("写操作")} ${route || "-"}`}
      facts={<><span>{t("目标")} <code>{target || "-"}</code></span><span>{t("字段")} {keys.length ? `${keys.slice(0, MAX_VISIBLE_DETAIL_KEYS).join(", ")}${hidden > 0 ? ` +${hidden}` : ""}` : "-"}</span><span>{t("操作者")} <code>{actor || "-"}</code></span><span>{t("来源")} <code>{remote || "-"}</code></span></>}
      status={<StatusMark tone="signal" label={t("成功")} />}
      action={<QueryBadge label={method || t("写操作")} tone="signal" />}
    />
  );
}

export function AuditLogsPage() {
  const { t } = useI18n();
  const [pageSize, setPageSize] = useState<number>(DEFAULT_PAGE_SIZE);
  const [pageIndex, setPageIndex] = useState(0);
  const [cursorStack, setCursorStack] = useState<number[]>([0]);
  const beforeId = cursorStack[pageIndex] ?? 0;

  const entriesQuery = useQuery({
    queryKey: ["audit-logs", beforeId, pageSize],
    queryFn: async () => ({
      ...(await listAuditLogs({ before_id: beforeId, limit: Math.min(pageSize + 1, AUDIT_MAX_LIMIT) })),
      pageIndex,
      pageSize,
    }),
    placeholderData: (previous) => previous,
    staleTime: 10_000,
  });

  const [lastPage, setLastPage] = useState<typeof entriesQuery.data>(undefined);
  if (entriesQuery.data !== undefined && !entriesQuery.isPlaceholderData && entriesQuery.data !== lastPage) {
    setLastPage(entriesQuery.data);
  }
  const pageData = entriesQuery.data ?? lastPage;
  const settledPageSize = pageData?.pageSize ?? pageSize;
  const entries = pageData?.items ?? EMPTY_ENTRIES;
  const hasData = pageData !== undefined;
  const isPageTransitioning = entriesQuery.isFetching && (entriesQuery.isPlaceholderData || (hasData && entriesQuery.data === undefined));
  const visibleEntries = entries.slice(0, settledPageSize);
  const isInitialLoading = entriesQuery.isLoading && !hasData;
  const hasStaleError = entriesQuery.isError && hasData;
  const hasMore = settledPageSize < AUDIT_MAX_LIMIT ? entries.length > settledPageSize : entries.length >= settledPageSize;
  const settledPageIndex = pageData?.pageIndex ?? pageIndex;

  const resetPagination = () => {
    setCursorStack([0]);
    setPageIndex(0);
  };
  const moveNext = () => {
    const lastEntry = visibleEntries[visibleEntries.length - 1];
    if (isPageTransitioning || !hasMore || !lastEntry) return;
    const nextIndex = pageIndex + 1;
    setCursorStack((previous) => [...previous.slice(0, nextIndex), lastEntry.id]);
    setPageIndex(nextIndex);
  };
  const movePrev = () => {
    if (!isPageTransitioning) setPageIndex((previous) => Math.max(0, previous - 1));
  };
  const ready = hasData && !isInitialLoading;

  return (
    <WorkspacePage
      eyebrow="Governance / audit"
      title={t("审计日志")}
      description={t("查看服务端记录的成功写操作、目标对象、字段摘要和来源地址。")}
      meta={<StatusMark tone={entriesQuery.isError ? "warn" : entriesQuery.isFetching ? "warn" : hasData ? "live" : "neutral"} label={entriesQuery.isError ? (hasData ? t("数据可能已过期") : t("读取失败")) : entriesQuery.isFetching ? t("更新中...") : hasData ? t("审计已同步") : t("等待审计记录")} />}
      actions={<ControlButton variant="secondary" size="sm" onPress={() => void entriesQuery.refetch()} loading={entriesQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>}
    >
      <WorkspaceCard headerless eyebrow="Governance / write evidence" title={t("写操作时间线")} meta={hasData ? t("第 {{page}} 页", { page: settledPageIndex + 1 }) : t("未加载")} description={t("仅记录已成功应用的管理写操作；凭据本身不会进入审计详情。") }>
        {entriesQuery.isError && !hasData ? <EmptyWorkState title={t("审计日志读取失败")} detail={formatApiErrorMessage(entriesQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void entriesQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
        {isInitialLoading ? <div className="workspace-register-state"><span className="workspace-spinner" aria-hidden />{t("正在加载审计日志")}</div> : null}
        {isPageTransitioning ? <p className="workspace-muted"><span className="workspace-spinner" aria-hidden />{t("更新中...")}</p> : null}
        {hasStaleError ? <div className="workspace-notice workspace-notice--warn" role="status"><span>{t("数据可能已过期")} · {formatApiErrorMessage(entriesQuery.error, t)}</span><ControlButton variant="ghost" size="sm" onPress={() => void entriesQuery.refetch()}>{t("重试")}</ControlButton></div> : null}
        {ready && !isPageTransitioning && !entriesQuery.isError && visibleEntries.length === 0 ? <EmptyWorkState title={t("暂无审计记录")} detail={t("成功的管理写操作会在这里按时间倒序显示。")} /> : null}
        {ready && visibleEntries.length ? <ObjectList>{visibleEntries.map((entry) => <AuditEntryRow key={entry.id} entry={entry} t={t} />)}</ObjectList> : null}
        <AuditPagination pageIndex={settledPageIndex} hasMore={hasMore} pageSize={pageSize} disabled={isPageTransitioning || entriesQuery.isError} onPageSizeChange={(next) => { setPageSize(next); resetPagination(); }} onPrev={movePrev} onNext={moveNext} />
      </WorkspaceCard>
    </WorkspacePage>
  );
}

export default AuditLogsPage;
