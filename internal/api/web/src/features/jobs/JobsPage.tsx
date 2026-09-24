import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createColumnHelper } from "@tanstack/react-table";
import { AlertTriangle, Ban, LoaderCircle, Plus, RefreshCw, RotateCcw, Wifi, WifiOff, X } from "lucide-react";
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Card } from "../../components/ui/Card";
import { DataTable } from "../../components/ui/DataTable";
import { DialogSurface } from "../../components/ui/DialogSurface";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
import { QueryState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import { Switch } from "../../components/ui/Switch";
import { Textarea } from "../../components/ui/Textarea";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
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
  IntelJobItem,
  JobKind,
} from "./types";

// WP08 §3/§7 检测任务工作区：任务列表 + 新建对话框 + 详情抽屉（SSE 实时进度）。
// 后端见 internal/api/handler_intel.go（CRUD 与 SSE）与 internal/intel/jobs。

type BadgeVariant = "neutral" | "success" | "warning" | "danger" | "info" | "accent" | "muted";
type ShowToast = (tone: "success" | "error", text: string) => void;

const JOB_STATUS_LABELS: Record<string, string> = {
  queued: "排队中",
  running: "运行中",
  succeeded: "已完成",
  partial: "部分完成",
  failed: "失败",
  canceled: "已取消",
};

const JOB_STATUS_VARIANTS: Record<string, BadgeVariant> = {
  queued: "neutral",
  running: "info",
  succeeded: "success",
  partial: "warning",
  failed: "danger",
  canceled: "muted",
};

const JOB_KIND_LABELS: Record<string, string> = {
  intel: "情报检测",
  full: "完整检测",
  egress: "出口探测",
  checks: "解锁检测",
};

const JOB_KIND_VARIANTS: Record<string, BadgeVariant> = {
  intel: "accent",
  full: "info",
  egress: "neutral",
  checks: "neutral",
};

const ITEM_STATUS_LABELS: Record<string, string> = {
  queued: "排队中",
  running: "运行中",
  done: "已完成",
  failed: "失败",
  skipped: "已跳过",
  canceled: "已取消",
};

const ITEM_STATUS_VARIANTS: Record<string, BadgeVariant> = {
  queued: "neutral",
  running: "info",
  done: "success",
  failed: "danger",
  skipped: "warning",
  canceled: "muted",
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

const STREAM_VARIANTS: Record<JobStreamState, BadgeVariant> = {
  idle: "muted",
  connecting: "warning",
  live: "success",
  reconnecting: "warning",
  ended: "muted",
  error: "danger",
};

const JOB_STATUS_FILTERS = ["", "queued", "running", "succeeded", "partial", "failed", "canceled"];
const ITEM_STATUS_FILTERS = ["", "queued", "running", "done", "failed", "skipped", "canceled"];
const JOB_KIND_OPTIONS: JobKind[] = ["intel", "full"];

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
// (internal/intel/jobs/jobs.go:184, truncated to 4 KiB server side).
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
      .join(" · ");
  } catch {
    return trimmed;
  }
}

function JobStatusBadge({ status }: { status: string }) {
  const { t } = useI18n();
  return (
    <Badge variant={JOB_STATUS_VARIANTS[status] ?? "neutral"} title={status}>
      {t(textOf(JOB_STATUS_LABELS, status))}
    </Badge>
  );
}

function ItemStatusBadge({ status }: { status: string }) {
  const { t } = useI18n();
  return (
    <Badge variant={ITEM_STATUS_VARIANTS[status] ?? "neutral"} title={status}>
      {t(textOf(ITEM_STATUS_LABELS, status))}
    </Badge>
  );
}

function JobKindBadge({ kind }: { kind: string }) {
  const { t } = useI18n();
  return (
    <Badge variant={JOB_KIND_VARIANTS[kind] ?? "neutral"} title={kind}>
      {t(textOf(JOB_KIND_LABELS, kind))}
    </Badge>
  );
}

function ProgressTrack({ job }: { job: IntelJob }) {
  const { t } = useI18n();
  const settled = settledCount(job);
  const max = Math.max(job.total, settled);
  const percent = max > 0 ? Math.min(100, Math.round((settled / max) * 100)) : 0;
  return (
    <div style={{ display: "grid", gap: "5px", minWidth: "110px" }}>
      <span style={{ fontSize: "12px" }}>
        {job.done} / {job.total}
      </span>
      <div
        role="progressbar"
        aria-label={t("进度")}
        aria-valuemin={0}
        aria-valuemax={max}
        aria-valuenow={settled}
        style={{ height: "5px", borderRadius: "999px", background: "var(--bg-soft)", overflow: "hidden" }}
      >
        <span style={{ display: "block", height: "100%", width: `${percent}%`, background: "var(--primary)" }} />
      </div>
      {job.failed > 0 ? (
        <span style={{ color: "var(--danger)", fontSize: "11px" }}>{t("失败 {{count}}", { count: job.failed })}</span>
      ) : null}
    </div>
  );
}

function JobItemRow({ item }: { item: IntelJobItem }) {
  const { t } = useI18n();
  const summary = formatResultSummary(item.result_json);
  return (
    <article
      style={{
        border: "1px solid var(--border)",
        borderRadius: "6px",
        padding: "10px 12px",
        display: "grid",
        gap: "6px",
        minWidth: 0,
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: "8px", flexWrap: "wrap", minWidth: 0 }}>
        <span
          style={{
            fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
            fontSize: "12px",
            overflowWrap: "anywhere",
            minWidth: 0,
          }}
          title={item.node_hash}
        >
          {item.node_hash}
        </span>
        <ItemStatusBadge status={item.status} />
        <span className="muted" style={{ fontSize: "11px" }}>
          {t("第 {{step}} 步：{{name}}", {
            step: item.step_index,
            name: t(textOf(STEP_LABELS, String(item.step_index))),
          })}
        </span>
      </div>

      <div className="muted" style={{ fontSize: "11px", display: "flex", gap: "10px", flexWrap: "wrap" }}>
        <span>{t("尝试 {{count}} 次", { count: item.attempts })}</span>
        <span>{t("更新时间 {{time}}", { time: formatNs(item.updated_at_ns) })}</span>
        {item.error_code ? (
          <span style={{ color: "var(--danger)" }}>{t("错误 {{code}}", { code: item.error_code })}</span>
        ) : null}
      </div>

      {summary ? (
        <div
          style={{
            fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
            fontSize: "11px",
            color: "var(--text-secondary)",
            background: "var(--bg-soft)",
            border: "1px solid var(--border)",
            borderRadius: "6px",
            padding: "6px 8px",
            maxHeight: "120px",
            overflow: "auto",
            overflowWrap: "anywhere",
            whiteSpace: "pre-wrap",
          }}
        >
          {summary}
        </div>
      ) : null}
    </article>
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
    <DialogSurface title={t("任务详情 {{id}}", { id: jobID })} variant="drawer" onClose={onClose}>
      <Card className="drawer-panel">
        <div className="drawer-header">
          <div style={{ minWidth: 0 }}>
            <h3>{t("任务详情")}</h3>
            <p className="muted" style={{ overflowWrap: "anywhere" }}>
              {jobID}
            </p>
          </div>
          <div className="drawer-header-actions" style={{ flexWrap: "wrap", justifyContent: "flex-end" }}>
            <Badge variant={STREAM_VARIANTS[streamState]}>
              {streamState === "live" ? <Wifi size={11} /> : streamState === "error" ? <WifiOff size={11} /> : null}{" "}
              {t(STREAM_LABELS[streamState])}
            </Badge>
            {streamState === "error" ? (
              <Button size="sm" variant="secondary" onClick={stream.reconnect}>
                <RefreshCw size={13} /> {t("重新连接")}
              </Button>
            ) : null}
            <Button aria-label={t("关闭")} title={t("关闭")} variant="ghost" size="sm" onClick={onClose}>
              <X size={16} />
            </Button>
          </div>
        </div>

        <QueryState
          loading={detailQuery.isLoading && !detailQuery.data}
          error={detailQuery.error}
          onRetry={() => void detailQuery.refetch()}
        />

        {job ? (
          <>
            <div style={{ display: "flex", gap: "8px", flexWrap: "wrap", alignItems: "center", marginTop: "12px" }}>
              <JobStatusBadge status={job.status} />
              <JobKindBadge kind={job.kind} />
              <span className="muted" style={{ fontSize: "11px" }}>
                {t("优先级 {{priority}}", { priority: job.priority })}
              </span>
              <span className="muted" style={{ fontSize: "11px" }}>
                {t("创建者 {{by}}", { by: job.created_by })}
              </span>
            </div>

            <div className="form-grid" style={{ marginTop: "12px" }}>
              <div className="field-group">
                <span className="field-label">{t("进度")}</span>
                <ProgressTrack job={job} />
              </div>
              <div className="field-group">
                <span className="field-label">{t("时间")}</span>
                <p className="muted" style={{ fontSize: "11px" }}>
                  {t("创建时间")} {formatNs(job.created_at_ns)}
                </p>
                <p className="muted" style={{ fontSize: "11px" }}>
                  {t("开始时间")} {formatNs(job.started_at_ns)} · {t("完成时间")} {formatNs(job.finished_at_ns)}
                </p>
              </div>
            </div>

            <div style={{ display: "flex", gap: "8px", flexWrap: "wrap", marginTop: "8px", fontSize: "11px" }}>
              <span className="muted">{t("已完成 {{count}}", { count: job.done })}</span>
              {job.failed > 0 ? (
                <span style={{ color: "var(--danger)" }}>{t("失败 {{count}}", { count: job.failed })}</span>
              ) : null}
              {job.skipped > 0 ? <span className="muted">{t("已跳过 {{count}}", { count: job.skipped })}</span> : null}
              {detailQuery.data ? (
                <span className="muted">{t("待处理 {{count}}", { count: detailQuery.data.progress.pending })}</span>
              ) : null}
              {detailQuery.data && detailQuery.data.progress.pending_online_lookups > 0 ? (
                <span className="muted">
                  {t("在线查询待完成 {{count}}", { count: detailQuery.data.progress.pending_online_lookups })}
                </span>
              ) : null}
            </div>

            {job.error ? (
              <div className="callout callout-error" style={{ marginTop: "12px" }}>
                <AlertTriangle size={14} />
                <span style={{ overflowWrap: "anywhere" }}>{job.error}</span>
              </div>
            ) : null}

            {streamState === "error" && running ? (
              <div className="callout callout-warning" style={{ marginTop: "12px" }}>
                <AlertTriangle size={14} />
                <span>{t("实时推送不可用，已降级为每 10 秒轮询一次。")}</span>
              </div>
            ) : null}

            <div className="detail-actions" style={{ marginTop: "14px" }}>
              <Button variant="secondary" size="sm" onClick={() => void detailQuery.refetch()} disabled={detailQuery.isFetching}>
                <RefreshCw size={13} className={detailQuery.isFetching ? "spin" : undefined} /> {t("刷新")}
              </Button>
              <Button variant="danger" size="sm" onClick={handleCancel} disabled={!running || busy}>
                <Ban size={13} /> {cancelMutation.isPending ? t("取消中...") : t("取消任务")}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => retryMutation.mutate()}
                disabled={failedCount <= 0 || retryMutation.isPending}
                title={failedCount <= 0 ? t("没有可重试的失败项") : t("重试失败项")}
              >
                <RotateCcw size={13} /> {retryMutation.isPending ? t("重试中...") : t("重试失败项")}
              </Button>
            </div>

            <div className="list-card-header" style={{ marginTop: "20px", flexWrap: "wrap" }}>
              <div>
                <h3>{t("节点结果")}</h3>
                <p>{t("共 {{count}} 个节点", { count: itemTotal })}</p>
              </div>
              <Select
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
              </Select>
            </div>

            <QueryState
              loading={itemsQuery.isLoading && !itemsQuery.data}
              error={itemsQuery.error}
              onRetry={() => void itemsQuery.refetch()}
            />
            <QueryState empty={!itemsQuery.isLoading && !itemsQuery.error && items.length === 0} emptyText={t("暂无节点结果")} />

            <div style={{ display: "grid", gap: "8px", marginTop: "10px" }}>
              {items.map((item) => (
                <JobItemRow key={item.node_hash} item={item} />
              ))}
            </div>

            {itemTotal > 0 ? (
              <OffsetPagination
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
          </>
        ) : null}
      </Card>
    </DialogSurface>
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
    <DialogSurface title={t("新建检测任务")} variant="modal" onClose={onClose}>
      <Card className="modal-card">
        <div className="modal-header">
          <h3>{t("新建检测任务")}</h3>
          <Button aria-label={t("关闭")} title={t("关闭")} variant="ghost" size="sm" onClick={onClose}>
            <X size={16} />
          </Button>
        </div>

        <form className="form-grid single-column" onSubmit={submit}>
          <div className="field-group">
            <label className="field-label" htmlFor="job-kind">
              {t("检测种类")}
            </label>
            <Select id="job-kind" value={kind} onChange={(event) => setKind(event.target.value as JobKind)}>
              {JOB_KIND_OPTIONS.map((option) => (
                <option key={option} value={option}>
                  {t(textOf(JOB_KIND_LABELS, option))} ({option})
                </option>
              ))}
            </Select>
            <p className="field-hint">
              {t("情报检测覆盖出口与风险评估；完整检测会额外运行解锁检测规则。")}
            </p>
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="job-priority">
              {t("优先级")}
            </label>
            {/* 优先级由服务端固定：POST /intel/jobs 的请求体不接受 priority
                字段（internal/service/control_plane_intel.go:113 固定为手动 100），
                解码器还启用了 DisallowUnknownFields。 */}
            <Select id="job-priority" value="manual" disabled>
              <option value="manual">{t("手动 (100)")}</option>
              <option value="subscription">{t("订阅 (50)")}</option>
              <option value="refresh">{t("定时刷新 (10)")}</option>
            </Select>
            <p className="field-hint">{t("新建任务固定使用手动优先级 100；订阅与定时刷新任务由系统排队。")}</p>
          </div>

          <div className="field-group">
            <label className="field-label" htmlFor="job-scope-all">
              {t("作用范围")}
            </label>
            <div style={{ display: "flex", alignItems: "center", gap: "10px", flexWrap: "wrap" }}>
              <Switch
                id="job-scope-all"
                checked={allNodes}
                onChange={(event) => setAllNodes(event.target.checked)}
                aria-label={t("全部节点")}
              />
              <span>{t("全部节点")}</span>
              <span className="muted" style={{ fontSize: "11px" }}>
                {allNodes ? t("对节点池中的所有节点执行。") : t("仅对下方列出的节点执行。")}
              </span>
            </div>
          </div>

          {allNodes ? null : (
            <div className="field-group">
              <label className="field-label" htmlFor="job-hashes">
                {t("节点哈希")}
              </label>
              <Textarea
                id="job-hashes"
                rows={5}
                value={hashesText}
                placeholder={"3f2a9c...\n8b71d0..."}
                onChange={(event) => setHashesText(event.target.value)}
              />
              <p className="field-hint">
                {t("每行一个节点哈希，也支持空格或逗号分隔；重复项会自动去除。")}
                {hashes.length > 0 ? ` ${t("已识别 {{count}} 个节点", { count: hashes.length })}` : ""}
              </p>
            </div>
          )}

          <div className="field-group">
            <div style={{ display: "flex", alignItems: "center", gap: "10px", flexWrap: "wrap" }}>
              <Switch
                checked={force}
                onChange={(event) => setForce(event.target.checked)}
                aria-label={t("忽略缓存证据")}
              />
              <span>{t("忽略缓存证据")}</span>
            </div>
            <p className="field-hint">{t("开启后会重新查询未过期的在线证据，而不是复用它们。")}</p>
          </div>

          {error ? (
            <div className="callout callout-error">
              <AlertTriangle size={14} />
              <span style={{ overflowWrap: "anywhere" }}>{error}</span>
            </div>
          ) : null}

          <div className="detail-actions">
            <Button type="submit" disabled={createMutation.isPending}>
              {createMutation.isPending ? <LoaderCircle size={14} className="spin" /> : <Plus size={14} />}
              {createMutation.isPending ? t("创建中...") : t("创建任务")}
            </Button>
            <Button type="button" variant="secondary" onClick={onClose} disabled={createMutation.isPending}>
              {t("取消")}
            </Button>
          </div>
        </form>
      </Card>
    </DialogSurface>
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
  const columnHelper = useMemo(() => createColumnHelper<IntelJob>(), []);
  const columns = useMemo(
    () => [
      columnHelper.accessor("id", {
        header: t("任务"),
        cell: (info) => (
          <span
            style={{ fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace", fontSize: "12px" }}
            title={info.getValue()}
          >
            {info.getValue().slice(0, 8)}
          </span>
        ),
      }),
      columnHelper.accessor("status", {
        header: t("状态"),
        cell: (info) => <JobStatusBadge status={info.getValue()} />,
      }),
      columnHelper.accessor("kind", {
        header: t("种类"),
        cell: (info) => <JobKindBadge kind={info.getValue()} />,
      }),
      columnHelper.accessor("created_by", {
        header: t("创建者"),
        cell: (info) => <span style={{ overflowWrap: "anywhere" }}>{info.getValue()}</span>,
      }),
      columnHelper.display({
        id: "progress",
        header: t("进度"),
        cell: (info) => <ProgressTrack job={info.row.original} />,
      }),
      columnHelper.accessor("created_at_ns", {
        header: t("创建时间"),
        cell: (info) => <span className="muted">{formatNs(info.getValue())}</span>,
      }),
      columnHelper.display({
        id: "finished",
        header: t("完成时间"),
        cell: (info) => <span className="muted">{formatNs(info.row.original.finished_at_ns)}</span>,
      }),
    ],
    [columnHelper, t],
  );

  return (
    <section className="jobs-page">
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <header className="module-header">
        <div>
          <h2>{t("检测任务")}</h2>
          <p className="page-context">{t("批量检测任务的排队、进度与逐节点结果。")}</p>
        </div>
      </header>

      <Card className="platform-list-card platform-directory-card">
        <div className="list-card-header" style={{ flexWrap: "wrap" }}>
          <div>
            <h3>{t("任务列表")}</h3>
            <p>{t("共 {{count}} 个任务", { count: total })}</p>
          </div>
          <div style={{ display: "flex", gap: "8px", flexWrap: "wrap", alignItems: "center" }}>
            <Select
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
            </Select>
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus size={16} /> {t("新建任务")}
            </Button>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => void jobsQuery.refetch()}
              disabled={jobsQuery.isFetching}
            >
              <RefreshCw size={16} className={jobsQuery.isFetching ? "spin" : undefined} /> {t("刷新")}
            </Button>
          </div>
        </div>
      </Card>

      <Card className="platform-cards-container jobs-table-card">
        <QueryState
          loading={jobsQuery.isLoading}
          error={jobsQuery.error}
          empty={!jobsQuery.isLoading && !jobsQuery.isError && jobs.length === 0}
          emptyText={t("暂无检测任务，点击“新建任务”开始一次批量检测。")}
          onRetry={() => void jobsQuery.refetch()}
        />

        {jobs.length > 0 ? (
          <DataTable
            data={jobs}
            columns={columns}
            getRowId={(job) => job.id}
            selectedRowId={selectedJobID || undefined}
            onRowClick={(job) => setSelectedJobID(job.id)}
          />
        ) : null}

        {total > 0 ? (
          <OffsetPagination
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
      </Card>

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
    </section>
  );
}
