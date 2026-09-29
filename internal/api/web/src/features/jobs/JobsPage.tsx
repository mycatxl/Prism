import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Ban, LoaderCircle, Plus, RefreshCw, RotateCcw, Wifi, WifiOff } from "lucide-react";
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input, Textarea } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader, PanelToolbar } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { Sheet } from "../../components/ui/Sheet";
import { Switch } from "../../components/ui/Switch";
import { Table, TableWrap, TBody, TD, TDClip, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime } from "../../lib/time";
import {
  JOB_ITEM_PAGE_SIZE_OPTIONS,
  JOB_PAGE_SIZE_OPTIONS,
  cancelIntelJob,
  createIntelJob,
  getIntelJob,
  listIntelJobItems,
  listIntelJobs,
  retryFailedIntelJob,
} from "./api";
import { isTerminalJobStatus, useIntelJobEvents, type JobStreamState } from "./useJobEvents";
import type {
  CreateIntelJobRequest,
  IntelJob,
  IntelJobDetail,
  JobKind,
} from "./types";

// WP08 §3/§7 检测任务工作区：任务列表 + 新建对话框 + 详情抽屉（SSE 实时进度）。
// 后端见 internal/api/handler_intel.go（CRUD 与 SSE）与 internal/intel/jobs。

type BadgeTone = "neutral" | "signal" | "live" | "warn" | "alert" | "outline";
type ShowToast = (tone: "success" | "error", text: string) => void;

const JOB_STATUS_LABELS: Record<string, string> = {
  queued: "排队中",
  running: "运行中",
  succeeded: "已完成",
  partial: "部分完成",
  failed: "失败",
  canceled: "已取消",
};

const JOB_STATUS_TONES: Record<string, BadgeTone> = {
  queued: "neutral",
  running: "live",
  succeeded: "signal",
  partial: "warn",
  failed: "alert",
  canceled: "outline",
};

const JOB_KIND_LABELS: Record<string, string> = {
  intel: "情报检测",
  full: "完整检测",
  egress: "出口探测",
  checks: "解锁检测",
};

const ITEM_STATUS_LABELS: Record<string, string> = {
  queued: "排队中",
  running: "运行中",
  done: "已完成",
  failed: "失败",
  skipped: "已跳过",
  canceled: "已取消",
};

const ITEM_STATUS_TONES: Record<string, BadgeTone> = {
  queued: "neutral",
  running: "live",
  done: "signal",
  failed: "alert",
  skipped: "warn",
  canceled: "outline",
};

// internal/intel/jobs/jobs.go:46 — the numbered pipeline steps of one node.
const STEP_LABELS: Record<string, string> = {
  "0": "等待执行",
  "1": "出口探测",
  "2": "离线判定",
  "3": "在线查询入队",
  "4": "经节点查询",
  "5": "解锁检测",
  "6": "风险评估",
};

const STREAM_LABELS: Record<JobStreamState, string> = {
  idle: "实时连接",
  connecting: "实时连接中",
  live: "实时连接",
  reconnecting: "实时重连中",
  ended: "实时推送已结束",
  error: "实时连接中断",
};

const STREAM_TONES: Record<JobStreamState, BadgeTone> = {
  idle: "outline",
  connecting: "warn",
  live: "live",
  reconnecting: "warn",
  ended: "outline",
  error: "alert",
};

// The bar takes the colour of the outcome it is reporting, so a failed run is
// recognisable from the row without reading the counter beside it.
const PROGRESS_BAR_CLASS: Record<string, string> = {
  queued: "bg-live",
  running: "bg-live",
  succeeded: "bg-signal",
  partial: "bg-warn",
  failed: "bg-alert",
  canceled: "bg-ink-faint",
};

const JOB_STATUS_FILTERS = ["", "queued", "running", "succeeded", "partial", "failed", "canceled"];
const ITEM_STATUS_FILTERS = ["", "queued", "running", "done", "failed", "skipped", "canceled"];
const JOB_KIND_OPTIONS: JobKind[] = ["intel", "full"];

const CONTROL_CLASS =
  "h-7 rounded-control border border-rule bg-paper-raised px-1.5 text-xs text-ink disabled:cursor-not-allowed disabled:opacity-50";
const FORM_CONTROL_CLASS =
  "h-8 w-full rounded-control border border-rule bg-paper-raised px-2 text-sm text-ink disabled:cursor-not-allowed disabled:opacity-60";

function textOf(map: Record<string, string>, value: string): string {
  return map[value] ?? value;
}

function formatNs(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return "-";
  }
  const date = new Date(Math.round(value / 1_000_000));
  if (Number.isNaN(date.getTime())) {
    return "-";
  }
  return formatDateTime(date.toISOString());
}

function settledCount(job: IntelJob): number {
  return job.done + job.failed + job.skipped;
}

function parseNodeHashes(raw: string): string[] {
  const seen = new Set<string>();
  const hashes: string[] = [];
  for (const token of raw.split(/[\s,;]+/)) {
    const hash = token.trim();
    if (!hash || seen.has(hash)) {
      continue;
    }
    seen.add(hash);
    hashes.push(hash);
  }
  return hashes;
}

function inlineValue(value: unknown): string {
  if (value === null || value === undefined) {
    return "-";
  }
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return JSON.stringify(value);
}

// job_items.result_json is the bounded step summary written by the pipeline
// (internal/intel/jobs/jobs.go:184, truncated to 4 KiB server side). One step
// result per line keeps the dump readable when it is long.
function formatResultSummary(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed || trimmed === "{}") {
    return "";
  }
  try {
    const parsed: unknown = JSON.parse(trimmed);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return trimmed;
    }
    return Object.entries(parsed as Record<string, unknown>)
      .map(([key, value]) => `${key}=${inlineValue(value)}`)
      .join("\n");
  } catch {
    return trimmed;
  }
}

function JobStatusBadge({ status }: { status: string }) {
  const { t } = useI18n();
  const tone = JOB_STATUS_TONES[status] ?? "neutral";
  return (
    <Badge tone={tone} dot={tone === "live"} title={status}>
      {t(textOf(JOB_STATUS_LABELS, status))}
    </Badge>
  );
}

function ItemStatusBadge({ status }: { status: string }) {
  const { t } = useI18n();
  return (
    <Badge tone={ITEM_STATUS_TONES[status] ?? "neutral"} title={status}>
      {t(textOf(ITEM_STATUS_LABELS, status))}
    </Badge>
  );
}

function JobKindBadge({ kind }: { kind: string }) {
  const { t } = useI18n();
  return (
    <Badge tone="outline" title={kind}>
      {t(textOf(JOB_KIND_LABELS, kind))}
    </Badge>
  );
}

/** The progress meter: a counter, a hairline-thin bar, and the failure count when there is one. */
function ProgressMeter({
  job,
  size = "sm",
  inline = false,
}: {
  job: IntelJob;
  size?: "sm" | "lg";
  inline?: boolean;
}) {
  const { t } = useI18n();
  const settled = settledCount(job);
  const max = Math.max(job.total, settled);
  const percent = max > 0 ? Math.min(100, Math.round((settled / max) * 100)) : 0;
  // 列表行只有一行高：失败计数挤不进单元格，退到 title；详情抽屉里仍然单独成行。
  const failure = job.failed > 0 ? t("失败 {{count}}", { count: job.failed }) : undefined;
  const bar = (
    <div
      role="progressbar"
      aria-label={t("进度")}
      aria-valuemin={0}
      aria-valuemax={max}
      aria-valuenow={settled}
      title={failure}
      className={cn(
        "overflow-hidden rounded-full bg-paper-sunk",
        inline ? "h-1 min-w-0 flex-1" : cn("h-1 w-full", size === "lg" ? "mt-2" : "mt-1"),
      )}
    >
      <span
        className={cn("block h-full", PROGRESS_BAR_CLASS[job.status] ?? "bg-ink-faint")}
        style={{ width: `${percent}%` }}
      />
    </div>
  );
  if (inline) {
    return bar;
  }
  return (
    <div className="min-w-0">
      {bar}
      {job.failed > 0 ? <p className="mt-1 text-2xs text-alert">{failure}</p> : null}
    </div>
  );
}

/**
 * Offset pagination. The list table and the per-node result list both page this
 * way, so the readout, the page size and the jump box are built once.
 */
function JobsPagination({
  page,
  totalPages,
  totalItems,
  pageSize,
  pageSizeOptions,
  disabled,
  onPageChange,
  onPageSizeChange,
}: {
  page: number;
  totalPages: number;
  totalItems: number;
  pageSize: number;
  pageSizeOptions: readonly number[];
  disabled?: boolean;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
}) {
  const { t } = useI18n();
  const pages = Math.max(1, totalPages);
  const current = Math.min(Math.max(0, page), pages - 1);
  const jump = (raw: string) => {
    const value = Number(raw);
    if (Number.isInteger(value) && value > 0) onPageChange(Math.max(0, Math.min(pages - 1, value - 1)));
  };
  return (
    <PanelFooter className="justify-between">
      <p className="text-xs text-ink-soft">
        {t("第 {{page}} / {{pages}} 页 · 显示 {{start}}-{{end}} / {{total}}", {
          page: current + 1,
          pages,
          start: totalItems ? current * pageSize + 1 : 0,
          end: Math.min((current + 1) * pageSize, totalItems),
          total: totalItems,
        })}
      </p>
      <div className="flex items-center gap-2">
        <label className="flex items-center gap-1.5 text-xs text-ink-soft">
          <span>{t("每页")}</span>
          <select
            className={CONTROL_CLASS}
            value={pageSize}
            disabled={disabled}
            onChange={(event) => onPageSizeChange(Number(event.target.value))}
          >
            {pageSizeOptions.map((size) => (
              <option key={size} value={size}>
                {size}
              </option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-1.5 text-xs text-ink-soft">
          <span>{t("跳至")}</span>
          <Input
            key={current}
            className="readout h-7 w-14 px-1.5 text-xs"
            type="number"
            inputMode="numeric"
            min={1}
            max={pages}
            defaultValue={current + 1}
            aria-label={t("选择页码")}
            disabled={disabled}
            onKeyDown={(event) => {
              if (event.key === "Enter") jump(event.currentTarget.value);
            }}
            onBlur={(event) => {
              jump(event.currentTarget.value);
            }}
          />
        </label>
        <Button
          variant="secondary"
          size="sm"
          aria-label={t("上一页")}
          title={t("上一页")}
          disabled={disabled || current === 0}
          onClick={() => onPageChange(current - 1)}
        >
          {t("上一页")}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          aria-label={t("下一页")}
          title={t("下一页")}
          disabled={disabled || current >= pages - 1}
          onClick={() => onPageChange(current + 1)}
        >
          {t("下一页")}
        </Button>
      </div>
    </PanelFooter>
  );
}

function JobDetailDrawer({ jobID, onClose, showToast }: { jobID: string; onClose: () => void; showToast: ShowToast }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [itemStatus, setItemStatus] = useState("");
  const [itemPage, setItemPage] = useState(0);
  const [itemPageSize, setItemPageSize] = useState<number>(JOB_ITEM_PAGE_SIZE_OPTIONS[0]);

  const detailQuery = useQuery({
    queryKey: ["intel-job", jobID],
    queryFn: () => getIntelJob(jobID),
    // 兜底轮询：SSE 不可用时仍能看到进度（SSE 正常时由下方补丁即时更新）。
    refetchInterval: (query) => (isTerminalJobStatus(query.state.data?.job.status) ? false : 10_000),
  });
  const job = detailQuery.data?.job;
  const running = job ? !isTerminalJobStatus(job.status) : true;

  const stream = useIntelJobEvents(jobID, running);
  const streamState: JobStreamState = stream.state === "ended" && running ? "connecting" : stream.state;

  const itemsQuery = useQuery({
    queryKey: ["intel-job-items", jobID, itemStatus, itemPage, itemPageSize],
    queryFn: () => listIntelJobItems(jobID, itemStatus, itemPageSize, itemPage * itemPageSize),
    refetchInterval: running ? 15_000 : false,
  });

  // SSE 帧只带计数器，这里把它们补进详情缓存；计数器变化时顺带刷新节点结果。
  const previousFrameRef = useRef("");
  useEffect(() => {
    const frame = stream.progress;
    if (!frame) {
      return;
    }
    const status = frame.status && frame.status !== "unknown" ? frame.status : undefined;
    queryClient.setQueryData<IntelJobDetail>(["intel-job", jobID], (current) => {
      if (!current) {
        return current;
      }
      const nextStatus = status ?? current.job.status;
      return {
        job: {
          ...current.job,
          status: nextStatus,
          done: frame.done ?? current.job.done,
          failed: frame.failed ?? current.job.failed,
          skipped: frame.skipped ?? current.job.skipped,
          total: frame.total ?? current.job.total,
        },
        progress: {
          ...current.progress,
          status: nextStatus,
          done: frame.done ?? current.progress.done,
          failed: frame.failed ?? current.progress.failed,
          skipped: frame.skipped ?? current.progress.skipped,
          total: frame.total ?? current.progress.total,
        },
      };
    });

    const signature = `${frame.done ?? "-"}/${frame.failed ?? "-"}/${frame.skipped ?? "-"}/${frame.total ?? "-"}`;
    if (previousFrameRef.current !== signature) {
      previousFrameRef.current = signature;
      void queryClient.invalidateQueries({ queryKey: ["intel-job-items", jobID] });
    }
  }, [stream.progress, jobID, queryClient]);

  // `end` 帧（或终态帧）后做一次最终刷新，让列表与节点结果与服务端对齐。
  const ended = stream.state === "ended" || (stream.progress?.status ? isTerminalJobStatus(stream.progress.status) : false);
  useEffect(() => {
    if (!ended) {
      return;
    }
    void queryClient.invalidateQueries({ queryKey: ["intel-job", jobID] });
    void queryClient.invalidateQueries({ queryKey: ["intel-job-items", jobID] });
    void queryClient.invalidateQueries({ queryKey: ["intel-jobs"] });
  }, [ended, jobID, queryClient]);

  const refreshAll = async () => {
    await queryClient.invalidateQueries({ queryKey: ["intel-job", jobID] });
    await queryClient.invalidateQueries({ queryKey: ["intel-job-items", jobID] });
    await queryClient.invalidateQueries({ queryKey: ["intel-jobs"] });
  };

  const cancelMutation = useMutation({
    mutationFn: () => cancelIntelJob(jobID),
    onSuccess: async () => {
      await refreshAll();
      showToast("success", t("任务已取消"));
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const retryMutation = useMutation({
    mutationFn: () => retryFailedIntelJob(jobID),
    onSuccess: async (result) => {
      await refreshAll();
      showToast(
        "success",
        result.retried > 0
          ? t("已重新排队 {{count}} 个失败项", { count: result.retried })
          : t("没有可重试的失败项"),
      );
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const items = itemsQuery.data?.items ?? [];
  const itemTotal = itemsQuery.data?.total ?? 0;
  const failedCount = job?.failed ?? 0;
  const busy = cancelMutation.isPending || retryMutation.isPending;

  const handleCancel = () => {
    if (!running || !window.confirm(t("确认取消任务 {{id}} 吗？正在处理的节点会停止。", { id: jobID }))) {
      return;
    }
    cancelMutation.mutate();
  };

  return (
    <Sheet
      open
      onOpenChange={(open) => {
        if (!open) {
          onClose();
        }
      }}
      title={t("任务详情")}
      description={<span className="readout text-xs text-ink-faint">{jobID}</span>}
      width="lg"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          {job ? (
            <>
              <JobStatusBadge status={job.status} />
              <JobKindBadge kind={job.kind} />
              <span className="text-xs text-ink-soft">{t("优先级 {{priority}}", { priority: job.priority })}</span>
              <span className="text-xs text-ink-soft">{t("创建者 {{by}}", { by: job.created_by })}</span>
            </>
          ) : null}
        </div>

        <div className="flex items-center gap-2">
          <Badge tone={STREAM_TONES[streamState]} dot={streamState === "live"}>
            {streamState === "live" ? <Wifi size={11} /> : streamState === "error" ? <WifiOff size={11} /> : null}
            {t(STREAM_LABELS[streamState])}
          </Badge>
          {streamState === "error" ? (
            <Button size="sm" variant="secondary" onClick={stream.reconnect}>
              <RefreshCw size={13} />
              {t("重新连接")}
            </Button>
          ) : null}
        </div>
      </div>

      {detailQuery.isLoading && !detailQuery.data ? <LoadingState /> : null}

      {detailQuery.error ? (
        <div className="mt-3">
          <ErrorState
            message={formatApiErrorMessage(detailQuery.error, t)}
            onRetry={() => void detailQuery.refetch()}
          />
        </div>
      ) : null}

      {job ? (
        <>
          <ReadoutStrip className="mt-4 grid-cols-2 sm:grid-cols-4">
            <ReadoutCell>
              <Readout
                label={t("进度")}
                value={`${job.done} / ${job.total}`}
                size="lg"
                tone={job.status === "running" ? "live" : job.status === "failed" ? "alert" : "ink"}
              />
              <ProgressMeter job={job} size="lg" />
            </ReadoutCell>
            <ReadoutCell>
              <Readout label={t("已完成")} value={job.done} size="sm" tone="signal" />
            </ReadoutCell>
            <ReadoutCell>
              <Readout
                label={t("失败")}
                value={job.failed}
                size="sm"
                tone={job.failed > 0 ? "alert" : "muted"}
              />
            </ReadoutCell>
            <ReadoutCell>
              <Readout label={t("已跳过")} value={job.skipped} size="sm" tone="muted" />
            </ReadoutCell>
          </ReadoutStrip>

          {detailQuery.data ? (
            <div className="mt-2 flex flex-wrap gap-x-4 text-xs text-ink-soft">
              <span>{t("待处理 {{count}}", { count: detailQuery.data.progress.pending })}</span>
              {detailQuery.data.progress.pending_online_lookups > 0 ? (
                <span>{t("在线查询待完成 {{count}}", { count: detailQuery.data.progress.pending_online_lookups })}</span>
              ) : null}
            </div>
          ) : null}

          <div className="mt-4 grid gap-x-6 gap-y-2 border-t border-rule pt-3 text-xs sm:grid-cols-3">
            <div className="min-w-0">
              <div className="label">{t("创建时间")}</div>
              <div className="readout mt-0.5">{formatNs(job.created_at_ns)}</div>
            </div>
            <div className="min-w-0">
              <div className="label">{t("开始时间")}</div>
              <div className="readout mt-0.5">{formatNs(job.started_at_ns)}</div>
            </div>
            <div className="min-w-0">
              <div className="label">{t("完成时间")}</div>
              <div className="readout mt-0.5">{formatNs(job.finished_at_ns)}</div>
            </div>
          </div>

          {job.error ? (
            <p
              className="mt-3 flex items-start gap-1.5 rounded-control border border-alert/30 bg-alert-wash px-3 py-2 text-xs leading-relaxed text-alert"
              role="alert"
            >
              <AlertTriangle size={13} className="mt-px shrink-0" />
              <span className="min-w-0 break-words">{job.error}</span>
            </p>
          ) : null}

          {streamState === "error" && running ? (
            <p className="mt-3 flex items-start gap-1.5 text-xs leading-relaxed text-warn">
              <AlertTriangle size={13} className="mt-px shrink-0" />
              <span>{t("实时推送不可用，已降级为每 10 秒轮询一次。")}</span>
            </p>
          ) : null}

          <div className="mt-4 flex flex-wrap items-center gap-2">
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void detailQuery.refetch()}
              disabled={detailQuery.isFetching}
            >
              <RefreshCw size={13} className={detailQuery.isFetching ? "animate-spin" : undefined} />
              {t("刷新")}
            </Button>
            <Button variant="danger" size="sm" onClick={handleCancel} disabled={!running || busy}>
              <Ban size={13} />
              {cancelMutation.isPending ? t("取消中...") : t("取消任务")}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => retryMutation.mutate()}
              disabled={failedCount <= 0 || retryMutation.isPending}
              title={failedCount <= 0 ? t("没有可重试的失败项") : t("重试失败项")}
            >
              <RotateCcw size={13} />
              {retryMutation.isPending ? t("重试中...") : t("重试失败项")}
            </Button>
          </div>

          <Panel className="mt-6 flex min-w-0 flex-col">
            <PanelHeader
              title={t("节点结果")}
              meta={t("共 {{count}} 个节点", { count: itemTotal })}
            />
            <PanelToolbar>
              <select
                className={CONTROL_CLASS}
                value={itemStatus}
                aria-label={t("按节点状态筛选")}
                onChange={(event) => {
                  setItemStatus(event.target.value);
                  setItemPage(0);
                }}
              >
                {ITEM_STATUS_FILTERS.map((value) => (
                  <option key={value || "all"} value={value}>
                    {value ? t(textOf(ITEM_STATUS_LABELS, value)) : t("全部结果")}
                  </option>
                ))}
              </select>
            </PanelToolbar>

            {itemsQuery.isLoading && !itemsQuery.data ? (
              <PanelBody>
                <LoadingState />
              </PanelBody>
            ) : null}

            {itemsQuery.error ? (
              <PanelBody>
                <ErrorState
                  message={formatApiErrorMessage(itemsQuery.error, t)}
                  onRetry={() => void itemsQuery.refetch()}
                />
              </PanelBody>
            ) : null}

            {!itemsQuery.isLoading && !itemsQuery.error && !items.length ? (
              <PanelBody>
                <EmptyState title={t("暂无节点结果")} />
              </PanelBody>
            ) : null}

            {items.length ? (
              <TableWrap>
                <Table>
                  <THead>
                    <TR>
                      <TH>{t("节点")}</TH>
                      <TH>{t("状态")}</TH>
                      <TH>{t("进度")}</TH>
                      <TH>{t("错误")}</TH>
                      <TH className="text-right">{t("更新时间")}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {/* 每个节点一行：尝试次数与步骤结果 JSON 退到 title，行高不再被
                        服务端截断到 4 KiB 的结果摘要撑开。 */}
                    {items.map((item) => (
                      <TR
                        key={item.node_hash}
                        title={[
                          t("尝试 {{count}} 次", { count: item.attempts }),
                          formatResultSummary(item.result_json),
                        ]
                          .filter(Boolean)
                          .join(" · ")}
                      >
                        <TDClip className="readout text-xs">{item.node_hash}</TDClip>
                        <TD>
                          <ItemStatusBadge status={item.status} />
                        </TD>
                        <TDClip>
                          {t("第 {{step}} 步：{{name}}", {
                            step: item.step_index,
                            name: t(textOf(STEP_LABELS, String(item.step_index))),
                          })}
                        </TDClip>
                        <TDClip className="readout text-xs text-alert">{item.error_code}</TDClip>
                        <TDNum className="text-xs text-ink-soft">
                          {formatNs(item.updated_at_ns)}
                        </TDNum>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              </TableWrap>
            ) : null}

            {itemTotal > 0 ? (
              <JobsPagination
                page={itemPage}
                totalPages={Math.max(1, Math.ceil(itemTotal / itemPageSize))}
                totalItems={itemTotal}
                pageSize={itemPageSize}
                pageSizeOptions={JOB_ITEM_PAGE_SIZE_OPTIONS}
                disabled={itemsQuery.isFetching}
                onPageChange={setItemPage}
                onPageSizeChange={(size) => {
                  setItemPageSize(size);
                  setItemPage(0);
                }}
              />
            ) : null}
          </Panel>
        </>
      ) : null}
    </Sheet>
  );
}

function CreateJobDialog({
  onClose,
  showToast,
  onCreated,
}: {
  onClose: () => void;
  showToast: ShowToast;
  onCreated: (job: IntelJob) => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<JobKind>("intel");
  const [allNodes, setAllNodes] = useState(true);
  const [hashesText, setHashesText] = useState("");
  const [force, setForce] = useState(false);
  const [error, setError] = useState("");

  const hashes = useMemo(() => parseNodeHashes(hashesText), [hashesText]);

  const createMutation = useMutation({
    mutationFn: (request: CreateIntelJobRequest) => createIntelJob(request),
    onSuccess: (response) => {
      void queryClient.invalidateQueries({ queryKey: ["intel-jobs"] });
      showToast("success", t("任务已创建"));
      onCreated(response.job);
      onClose();
    },
    onError: (mutationError) => setError(formatApiErrorMessage(mutationError, t)),
  });

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!allNodes && hashes.length === 0) {
      setError(t("请选择全部节点，或至少填写一个节点哈希。"));
      return;
    }
    setError("");
    createMutation.mutate({
      kind,
      scope: allNodes ? { all: true } : { node_hashes: hashes },
      force,
    });
  };

  return (
    <Sheet
      open
      onOpenChange={(open) => {
        if (!open && !createMutation.isPending) {
          onClose();
        }
      }}
      title={t("新建检测任务")}
      width="sm"
      footer={
        <div className="flex items-center justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose} disabled={createMutation.isPending}>
            {t("取消")}
          </Button>
          <Button type="submit" form="create-job-form" disabled={createMutation.isPending}>
            {createMutation.isPending ? <LoaderCircle size={14} className="animate-spin" /> : <Plus size={14} />}
            {createMutation.isPending ? t("创建中...") : t("创建任务")}
          </Button>
        </div>
      }
    >
      <form id="create-job-form" className="flex flex-col gap-4" onSubmit={submit}>
        <Fieldset
          label={t("检测种类")}
          htmlFor="job-kind"
          hint={t("情报检测覆盖出口与风险评估；完整检测会额外运行解锁检测规则。")}
        >
          <select
            id="job-kind"
            className={FORM_CONTROL_CLASS}
            value={kind}
            onChange={(event) => setKind(event.target.value as JobKind)}
          >
            {JOB_KIND_OPTIONS.map((option) => (
              <option key={option} value={option}>
                {t(textOf(JOB_KIND_LABELS, option))} ({option})
              </option>
            ))}
          </select>
        </Fieldset>

        <Fieldset
          label={t("优先级")}
          htmlFor="job-priority"
          hint={t("新建任务固定使用手动优先级 100；订阅与定时刷新任务由系统排队。")}
        >
          {/* 优先级由服务端固定：POST /intel/jobs 的请求体不接受 priority
              字段（internal/service/control_plane_intel.go:113 固定为手动 100），
              解码器还启用了 DisallowUnknownFields。 */}
          <select id="job-priority" className={FORM_CONTROL_CLASS} value="manual" disabled>
            <option value="manual">{t("手动 (100)")}</option>
            <option value="subscription">{t("订阅 (50)")}</option>
            <option value="refresh">{t("定时刷新 (10)")}</option>
          </select>
        </Fieldset>

        <fieldset className="min-w-0">
          <legend className="label px-0">{t("作用范围")}</legend>
          <div className="mt-1 flex items-center gap-3 border-y border-rule py-2">
            <Switch
              id="job-scope-all"
              checked={allNodes}
              onCheckedChange={(checked: boolean) => setAllNodes(checked)}
              aria-label={t("全部节点")}
            />
            <label className="text-sm" htmlFor="job-scope-all">
              {t("全部节点")}
            </label>
            <span className="text-xs text-ink-faint">
              {allNodes ? t("对节点池中的所有节点执行。") : t("仅对下方列出的节点执行。")}
            </span>
          </div>
        </fieldset>

        {allNodes ? null : (
          <Fieldset
            label={t("节点哈希")}
            htmlFor="job-hashes"
            hint={`${t("每行一个节点哈希，也支持空格或逗号分隔；重复项会自动去除。")}${
              hashes.length > 0 ? ` ${t("已识别 {{count}} 个节点", { count: hashes.length })}` : ""
            }`}
          >
            <Textarea
              id="job-hashes"
              className="readout text-xs"
              rows={5}
              value={hashesText}
              placeholder={"3f2a9c...\n8b71d0..."}
              onChange={(event) => setHashesText(event.target.value)}
            />
          </Fieldset>
        )}

        <div className="flex items-center gap-3">
          <Switch
            id="job-force"
            checked={force}
            onCheckedChange={(checked: boolean) => setForce(checked)}
            aria-label={t("忽略缓存证据")}
          />
          <label className="text-sm" htmlFor="job-force">
            {t("忽略缓存证据")}
          </label>
        </div>
        <p className="-mt-2 text-xs text-ink-faint">{t("开启后会重新查询未过期的在线证据，而不是复用它们。")}</p>

        {error ? (
          <p
            className="flex items-start gap-1.5 rounded-control border border-alert/30 bg-alert-wash px-3 py-2 text-xs leading-relaxed text-alert"
            role="alert"
          >
            <AlertTriangle size={13} className="mt-px shrink-0" />
            <span className="min-w-0 break-words">{error}</span>
          </p>
        ) : null}
      </form>
    </Sheet>
  );
}

export function JobsPage() {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const [statusFilter, setStatusFilter] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(JOB_PAGE_SIZE_OPTIONS[1]);
  const [selectedJobID, setSelectedJobID] = useState("");
  const [createOpen, setCreateOpen] = useState(false);

  const jobsQuery = useQuery({
    queryKey: ["intel-jobs", statusFilter, page, pageSize],
    queryFn: () => listIntelJobs(statusFilter, pageSize, page * pageSize),
    // 列表本身不做实时推送；有活动任务时用慢轮询兜底，进度条由详情抽屉的 SSE 驱动。
    refetchInterval: (query) =>
      (query.state.data?.items ?? []).some((job) => !isTerminalJobStatus(job.status)) ? 15_000 : false,
  });

  const jobs = jobsQuery.data?.items ?? [];
  const total = jobsQuery.data?.total ?? 0;
  const showList = !jobsQuery.isLoading && !jobsQuery.isError;

  const openJob = (job: IntelJob) => setSelectedJobID(job.id);

  return (
    <Page bleed>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <PageHeader
        title={t("检测任务")}
        description={t("批量检测任务的排队、进度与逐节点结果。")}
        meta={<PageMeta label={t("任务")} value={total.toLocaleString()} />}
        actions={
          <>
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus size={15} />
              {t("新建任务")}
            </Button>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => void jobsQuery.refetch()}
              disabled={jobsQuery.isFetching}
            >
              <RefreshCw size={15} className={jobsQuery.isFetching ? "animate-spin" : undefined} />
              {t("刷新")}
            </Button>
          </>
        }
      />

      <div className="flex min-w-0 flex-1 flex-col gap-3 px-4 py-3 lg:px-5 lg:py-4 2xl:px-6 2xl:py-5">
        <Panel className="flex min-w-0 flex-col">
          <PanelHeader title={t("任务列表")} />
          <PanelToolbar>
            <select
              className={CONTROL_CLASS}
              value={statusFilter}
              aria-label={t("按状态筛选")}
              onChange={(event) => {
                setStatusFilter(event.target.value);
                setPage(0);
              }}
            >
              {JOB_STATUS_FILTERS.map((value) => (
                <option key={value || "all"} value={value}>
                  {value ? t(textOf(JOB_STATUS_LABELS, value)) : t("全部状态")}
                </option>
              ))}
            </select>
          </PanelToolbar>

          {jobsQuery.isLoading ? (
            <PanelBody>
              <LoadingState />
            </PanelBody>
          ) : null}

          {jobsQuery.isError ? (
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(jobsQuery.error, t)}
                onRetry={() => void jobsQuery.refetch()}
              />
            </PanelBody>
          ) : null}

          {showList && !jobs.length ? (
            <PanelBody>
              <EmptyState
                title={t("暂无检测任务，点击“新建任务”开始一次批量检测。")}
                action={
                  <Button size="sm" onClick={() => setCreateOpen(true)}>
                    <Plus size={15} />
                    {t("新建任务")}
                  </Button>
                }
              />
            </PanelBody>
          ) : null}

          {/* 每行一个任务且只有一行高：计数与进度条并排，时间不再包内层元素。 */}
          {jobs.length ? (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("任务")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH>{t("种类")}</TH>
                    <TH>{t("创建者")}</TH>
                    <TH>{t("进度")}</TH>
                    <TH className="text-right">{t("创建时间")}</TH>
                    <TH className="text-right">{t("完成时间")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {jobs.map((job) => (
                    <TR
                      key={job.id}
                      tabIndex={0}
                      className="cursor-pointer"
                      selected={selectedJobID === job.id}
                      aria-selected={selectedJobID === job.id}
                      onClick={() => openJob(job)}
                      onKeyDown={(event) => {
                        if (event.target === event.currentTarget && (event.key === "Enter" || event.key === " ")) {
                          event.preventDefault();
                          openJob(job);
                        }
                      }}
                    >
                      <TDClip className="readout text-xs" title={job.id}>
                        {job.id.slice(0, 8)}
                      </TDClip>
                      <TD>
                        <JobStatusBadge status={job.status} />
                      </TD>
                      <TD>
                        <JobKindBadge kind={job.kind} />
                      </TD>
                      <TDClip className="text-xs text-ink-soft" title={job.created_by}>
                        {job.created_by}
                      </TDClip>
                      <TD>
                        <span className="flex min-w-40 items-center gap-2">
                          <span className="readout shrink-0 text-xs">
                            {job.done} / {job.total}
                          </span>
                          <ProgressMeter job={job} inline />
                        </span>
                      </TD>
                      <TDNum className="text-xs text-ink-soft">{formatNs(job.created_at_ns)}</TDNum>
                      <TDNum className="text-xs text-ink-soft">{formatNs(job.finished_at_ns)}</TDNum>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          ) : null}

          {total > 0 ? (
            <JobsPagination
              page={page}
              totalPages={Math.max(1, Math.ceil(total / pageSize))}
              totalItems={total}
              pageSize={pageSize}
              pageSizeOptions={JOB_PAGE_SIZE_OPTIONS}
              disabled={jobsQuery.isFetching}
              onPageChange={setPage}
              onPageSizeChange={(size) => {
                setPageSize(size);
                setPage(0);
              }}
            />
          ) : null}
        </Panel>
      </div>

      {selectedJobID ? (
        <JobDetailDrawer
          key={selectedJobID}
          jobID={selectedJobID}
          onClose={() => setSelectedJobID("")}
          showToast={showToast}
        />
      ) : null}

      {createOpen ? (
        <CreateJobDialog
          onClose={() => setCreateOpen(false)}
          showToast={showToast}
          onCreated={(job) => setSelectedJobID(job.id)}
        />
      ) : null}
    </Page>
  );
}