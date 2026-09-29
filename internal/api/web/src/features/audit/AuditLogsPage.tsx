import { useQuery } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Table, TableWrap, TBody, TD, TDClip, TH, THead, TR } from "../../components/ui/Table";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatApiErrorMessage } from "../../lib/error-message";
import { listAuditLogs } from "./api";
import type { AuditLogEntry } from "./types";

const PAGE_SIZE_OPTIONS = [20, 50, 100, 200] as const;
const DEFAULT_PAGE_SIZE = 50;
const NANOS_PER_MILLI = 1_000_000;
const MAX_VISIBLE_DETAIL_KEYS = 3;
const EMPTY_ENTRIES: AuditLogEntry[] = [];

const CONTROL_CLASS =
  "h-7 rounded-control border border-rule bg-paper-raised px-1.5 text-xs text-ink disabled:cursor-not-allowed disabled:opacity-50";

// action is "<METHOD> <route pattern>", e.g. "PATCH /api/v1/platforms/{id}".
function splitAuditAction(action: string): { method: string; route: string } {
  const raw = action.trim();
  if (!raw) {
    return { method: "", route: "" };
  }
  const separator = raw.indexOf(" ");
  if (separator < 0) {
    return { method: raw, route: "" };
  }
  return { method: raw.slice(0, separator), route: raw.slice(separator + 1).trim() };
}

function splitAuditTime(atNs: number): { date: string; time: string } {
  if (!Number.isFinite(atNs) || atNs <= 0) {
    return { date: "-", time: "-" };
  }
  const moment = new Date(Math.round(atNs / NANOS_PER_MILLI));
  if (Number.isNaN(moment.getTime())) {
    return { date: "-", time: "-" };
  }

  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  const date = new Intl.DateTimeFormat(locale, { year: "numeric", month: "2-digit", day: "2-digit" }).format(moment);
  const time = new Intl.DateTimeFormat(locale, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(moment);
  return { date, time };
}

// detail is {"keys":[...]}: request body field names only, never their values.
function auditDetailKeys(detail: string): string[] {
  const raw = detail.trim();
  if (!raw) {
    return [];
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return [];
  }
  if (!parsed || typeof parsed !== "object") {
    return [];
  }
  const keys = (parsed as { keys?: unknown }).keys;
  if (!Array.isArray(keys)) {
    return [];
  }
  return keys.filter((key): key is string => typeof key === "string" && key.trim() !== "");
}

/**
 * Cursor pagination for the audit trail.
 *
 * The server pages by `before_id`, so the control is a page cursor rather than a
 * numbered range: the operator moves forward into older records and back again.
 * It is the panel's footer, because that is where a grid keeps its totals.
 */
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
    <PanelFooter className="justify-between">
      <p className="readout text-xs">
        {hasMore
          ? t("第 {{page}} 页 · 有更多数据", { page: pageIndex + 1 })
          : t("第 {{page}} 页 · 无更多数据", { page: pageIndex + 1 })}
      </p>
      <div className="flex items-center gap-2">
        <label className="flex items-center gap-1.5 text-xs text-ink-soft">
          <span>{t("每页")}</span>
          <select
            className={CONTROL_CLASS}
            value={String(pageSize)}
            disabled={disabled}
            onChange={(event) => onPageSizeChange(Number(event.target.value))}
          >
            {PAGE_SIZE_OPTIONS.map((size) => (
              <option key={size} value={size}>
                {size}
              </option>
            ))}
          </select>
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

export function AuditLogsPage() {
  const { t } = useI18n();
  const [pageSize, setPageSize] = useState<number>(DEFAULT_PAGE_SIZE);
  const [pageIndex, setPageIndex] = useState(0);
  // cursorStack[i] is the before_id used by page i; 0 means "start from the newest".
  const [cursorStack, setCursorStack] = useState<number[]>([0]);

  const beforeId = cursorStack[pageIndex] ?? 0;

  const entriesQuery = useQuery({
    queryKey: ["audit-logs", beforeId, pageSize],
    queryFn: () => listAuditLogs({ before_id: beforeId, limit: pageSize }),
    placeholderData: (previous) => previous,
    staleTime: 10_000,
  });

  const entries = entriesQuery.data?.items ?? EMPTY_ENTRIES;
  const isPageTransitioning = entriesQuery.isFetching && entriesQuery.isPlaceholderData;
  const visibleEntries = isPageTransitioning ? EMPTY_ENTRIES : entries;
  const pageLimit = entriesQuery.data?.limit ?? pageSize;
  const hasMore = entries.length > 0 && entries.length >= pageLimit;
  const refreshBusy = entriesQuery.isFetching;

  const resetPagination = () => {
    setCursorStack([0]);
    setPageIndex(0);
  };

  const moveNext = () => {
    const lastEntry = entries[entries.length - 1];
    if (isPageTransitioning || !hasMore || !lastEntry) {
      return;
    }
    const nextIndex = pageIndex + 1;
    setCursorStack((previous) => [...previous.slice(0, nextIndex), lastEntry.id]);
    setPageIndex(nextIndex);
  };

  const movePrev = () => {
    if (isPageTransitioning) {
      return;
    }
    setPageIndex((previous) => Math.max(0, previous - 1));
  };

  const showTable = !entriesQuery.isLoading && !isPageTransitioning && !entriesQuery.isError && visibleEntries.length > 0;

  return (
    <Page bleed>
      <PageHeader
        title={t("审计日志")}
        description={t("记录管理员对配置的写操作，仅成功的写操作会被记录（保留 90 天，最多 100000 条）。")}
        meta={
          <>
            <PageMeta label={t("每页")} value={pageSize} />
            <PageMeta label={t("结果")} value={t("成功")} />
          </>
        }
        actions={
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void entriesQuery.refetch()}
            disabled={refreshBusy}
            title={t("刷新")}
          >
            <RefreshCw size={14} className={refreshBusy ? "animate-spin" : undefined} />
            {t("刷新")}
          </Button>
        }
      />

      <div className="px-4 py-3 lg:px-5 lg:py-4 2xl:px-6 2xl:py-5">
        <Panel className="flex min-w-0 flex-col">
          <PanelHeader title={t("审计日志")} meta={t("仅记录成功的写操作")} />

          {entriesQuery.isLoading || isPageTransitioning ? (
            <PanelBody>
              <LoadingState />
            </PanelBody>
          ) : null}

          {entriesQuery.isError && !entriesQuery.isLoading ? (
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(entriesQuery.error, t)}
                onRetry={() => void entriesQuery.refetch()}
              />
            </PanelBody>
          ) : null}

          {!entriesQuery.isLoading && !isPageTransitioning && !entriesQuery.isError && !visibleEntries.length ? (
            <PanelBody>
              <EmptyState title={t("暂无审计记录")} />
            </PanelBody>
          ) : null}

          {showTable ? (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("时间")}</TH>
                    <TH>{t("动作")}</TH>
                    <TH>{t("目标")}</TH>
                    <TH>{t("变更字段")}</TH>
                    <TH title={t("管理员令牌的指纹前缀（不含令牌本身）")}>{t("操作者")}</TH>
                    <TH title={t("请求的客户端地址")}>{t("来源")}</TH>
                    <TH>{t("结果")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {visibleEntries.map((entry) => {
                    const parts = splitAuditTime(entry.at_ns);
                    const { method, route } = splitAuditAction(entry.action);
                    const keys = auditDetailKeys(entry.detail);
                    const hidden = keys.length - MAX_VISIBLE_DETAIL_KEYS;
                    const stamp = `${parts.date} ${parts.time}`;
                    const actor = entry.actor.trim();
                    const target = entry.target.trim();
                    const remote = entry.remote_addr.trim();
                    return (
                      <TR key={entry.id}>
                        <TDClip className="readout whitespace-nowrap" title={stamp}>
                          {stamp}
                        </TDClip>
                        <TD className="whitespace-nowrap">
                          <span className="flex min-w-0 items-center gap-2">
                            {method ? <Badge tone="outline">{method}</Badge> : <span className="text-ink-faint">-</span>}
                            <span className="readout max-w-[32ch] truncate" title={route || "-"}>
                              {route || "-"}
                            </span>
                          </span>
                        </TD>
                        <TDClip className="readout text-xs">{target || "-"}</TDClip>
                        <TDClip className="text-sm" title={keys.join(", ")}>
                          {keys.length
                            ? `${keys.slice(0, MAX_VISIBLE_DETAIL_KEYS).join(", ")}${hidden > 0 ? ` +${hidden}` : ""}`
                            : "-"}
                        </TDClip>
                        <TDClip className="readout text-xs">{actor || "-"}</TDClip>
                        <TDClip className="readout text-xs">{remote || "-"}</TDClip>
                        <TD>
                          <Badge tone="signal" dot title={t("仅记录成功的写操作")}>
                            {t("成功")}
                          </Badge>
                        </TD>
                      </TR>
                    );
                  })}
                </TBody>
              </Table>
            </TableWrap>
          ) : null}

          <AuditPagination
            pageIndex={pageIndex}
            hasMore={hasMore}
            pageSize={pageSize}
            disabled={isPageTransitioning || entriesQuery.isError}
            onPageSizeChange={(nextPageSize) => {
              setPageSize(nextPageSize);
              resetPagination();
            }}
            onPrev={movePrev}
            onNext={moveNext}
          />
        </Panel>
      </div>
    </Page>
  );
}
