import { useQuery } from "@tanstack/react-query";
import { createColumnHelper } from "@tanstack/react-table";
import { AlertTriangle, RefreshCw } from "lucide-react";
import { useMemo, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { CursorPagination } from "../../components/ui/CursorPagination";
import { DataTable } from "../../components/ui/DataTable";
import { QueryState } from "../../components/ui/QueryState";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatApiErrorMessage } from "../../lib/error-message";
import { listAuditLogs } from "./api";
import type { AuditLogEntry } from "./types";

type BadgeVariantName = "neutral" | "success" | "warning" | "danger" | "info" | "accent" | "muted";

const PAGE_SIZE_OPTIONS = [20, 50, 100, 200] as const;
const DEFAULT_PAGE_SIZE = 50;
const NANOS_PER_MILLI = 1_000_000;
const MAX_VISIBLE_DETAIL_KEYS = 3;
const EMPTY_ENTRIES: AuditLogEntry[] = [];

function methodBadgeVariant(method: string): BadgeVariantName {
  switch (method) {
    case "POST":
      return "success";
    case "PUT":
      return "info";
    case "PATCH":
      return "warning";
    case "DELETE":
      return "danger";
    default:
      return "neutral";
  }
}

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

function AuditValue({ value, mono = false }: { value: string; mono?: boolean }) {
  const text = value.trim();
  if (!text) {
    return <span>-</span>;
  }
  return (
    <span className={mono ? "audit-log-value audit-log-value-mono" : "audit-log-value"} title={text}>
      {text}
    </span>
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

  const col = useMemo(() => createColumnHelper<AuditLogEntry>(), []);

  const auditColumns = useMemo(
    () => [
      col.accessor("at_ns", {
        header: t("时间"),
        cell: (info) => {
          const parts = splitAuditTime(info.getValue());
          return (
            <div className="logs-cell-stack logs-time-cell">
              <span>{parts.time}</span>
              <small>{parts.date}</small>
            </div>
          );
        },
      }),
      col.accessor("action", {
        header: t("动作"),
        cell: (info) => {
          const { method, route } = splitAuditAction(info.getValue());
          return (
            <div className="audit-log-action">
              {method ? <Badge variant={methodBadgeVariant(method)}>{method}</Badge> : <span>-</span>}
              <code title={route}>{route || "-"}</code>
            </div>
          );
        },
      }),
      col.accessor("target", {
        header: t("目标"),
        cell: (info) => <AuditValue value={info.getValue()} mono />,
      }),
      col.accessor("detail", {
        header: t("变更字段"),
        cell: (info) => {
          const keys = auditDetailKeys(info.getValue());
          if (!keys.length) {
            return <span>-</span>;
          }
          const hidden = keys.length - MAX_VISIBLE_DETAIL_KEYS;
          return (
            <span title={keys.join(", ")}>
              {keys.slice(0, MAX_VISIBLE_DETAIL_KEYS).join(", ")}
              {hidden > 0 ? ` +${hidden}` : ""}
            </span>
          );
        },
      }),
      col.display({
        id: "result",
        header: t("结果"),
        cell: () => (
          <Badge variant="success" title={t("仅记录成功的写操作")}>
            {t("成功")}
          </Badge>
        ),
      }),
      col.accessor("actor", {
        header: () => <span title={t("管理员令牌的指纹前缀（不含令牌本身）")}>{t("操作者")}</span>,
        cell: (info) => <AuditValue value={info.getValue()} mono />,
      }),
      col.accessor("remote_addr", {
        header: () => <span title={t("请求的客户端地址")}>{t("来源")}</span>,
        cell: (info) => <AuditValue value={info.getValue()} mono />,
      }),
    ],
    [col, t]
  );

  return (
    <section className="logs-page">
      <header className="module-header">
        <div>
          <h2>{t("审计日志")}</h2>
          <p className="module-description">
            {t("记录管理员对配置的写操作，仅成功的写操作会被记录（保留 90 天，最多 100000 条）。")}
          </p>
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => void entriesQuery.refetch()}
          disabled={refreshBusy}
          title={t("刷新")}
        >
          <RefreshCw size={14} className={refreshBusy ? "spin" : undefined} />
          {t("刷新")}
        </Button>
      </header>

      <Card className="logs-table-card">
        {entriesQuery.isLoading || isPageTransitioning ? <QueryState loading /> : null}

        {entriesQuery.isError && !entriesQuery.isLoading ? (
          <>
            <QueryState error={entriesQuery.error} onRetry={() => void entriesQuery.refetch()} />
            <div className="callout callout-error">
              <AlertTriangle size={14} />
              <span>{formatApiErrorMessage(entriesQuery.error, t)}</span>
            </div>
          </>
        ) : null}

        {!entriesQuery.isLoading && !isPageTransitioning && !entriesQuery.isError && !visibleEntries.length ? (
          <QueryState empty emptyText={t("暂无审计记录")} />
        ) : null}

        {visibleEntries.length ? (
          <DataTable
            data={visibleEntries}
            columns={auditColumns}
            getRowId={(entry) => String(entry.id)}
            className="data-table-audit"
          />
        ) : null}

        <CursorPagination
          pageIndex={pageIndex}
          hasMore={hasMore}
          pageSize={pageSize}
          pageSizeOptions={PAGE_SIZE_OPTIONS}
          disabled={isPageTransitioning || entriesQuery.isError}
          onPageSizeChange={(nextPageSize) => {
            setPageSize(nextPageSize);
            resetPagination();
          }}
          onPrev={movePrev}
          onNext={moveNext}
        />
      </Card>
    </section>
  );
}
