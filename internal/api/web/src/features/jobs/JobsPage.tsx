import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Ban, Plus, RefreshCw, RotateCcw } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import {
  ConfirmDrawer,
  ControlButton,
  ControlPagination,
  ControlSelect,
  ControlSwitch,
  ControlTextarea,
  DetailFact,
  EmptyWorkState,
  InspectorDrawer,
  ObjectList,
  ObjectRow,
  PageFrame,
  QueryBadge,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
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
import { isTerminalJobStatus, useIntelJobEvents } from "./useJobEvents";
import type { CreateIntelJobRequest, IntelJob, IntelJobDetail, IntelJobItem, JobKind } from "./types";

const JOB_STATUS_FILTERS = ["", "queued", "running", "succeeded", "partial", "failed", "canceled"];
const JOB_ITEM_STATUS_FILTERS = ["", "queued", "running", "done", "failed", "skipped", "canceled"];
const JOB_STATUS_LABELS: Record<string, string> = { queued: "排队中", running: "运行中", succeeded: "已完成", partial: "部分完成", failed: "失败", canceled: "已取消" };
const JOB_KIND_LABELS: Record<string, string> = { intel: "情报检测", full: "完整检测", egress: "出口探测", checks: "解锁检测" };
const ITEM_STATUS_LABELS: Record<string, string> = { queued: "排队中", running: "运行中", done: "已完成", failed: "失败", skipped: "已跳过", canceled: "已取消" };
const STEP_LABELS: Record<string, string> = { "0": "等待执行", "1": "出口探测", "2": "离线判定", "3": "在线查询入队", "4": "经节点查询", "5": "解锁检测", "6": "风险评估" };
const JOB_KINDS: JobKind[] = ["intel", "full"];

type ToastFn = (tone: "success" | "error", text: string) => void;

function labelOf(map: Record<string, string>, value: string, t: (value: string, values?: Record<string, unknown>) => string) {
  return t(map[value] ?? value);
}

function toneForStatus(status: string): "neutral" | "signal" | "warn" | "alert" {
  if (status === "running" || status === "done" || status === "succeeded") return "signal";
  if (status === "failed") return "alert";
  if (status === "partial" || status === "skipped") return "warn";
  return "neutral";
}

function formatNs(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "-";
  const date = new Date(Math.round(value / 1_000_000));
  return Number.isNaN(date.getTime()) ? "-" : formatDateTime(date.toISOString());
}

function parseHashes(raw: string): string[] {
  return [...new Set(raw.split(/[\s,;]+/).map((value) => value.trim()).filter(Boolean))];
}

function settled(job: IntelJob) {
  return job.done + job.failed + job.skipped;
}

function progressLabel(job: IntelJob, t: (value: string, values?: Record<string, unknown>) => string) {
  const finished = settled(job);
  return `${finished} / ${job.total}${job.failed > 0 ? ` · ${t("失败 {{count}}", { count: job.failed })}` : ""}`;
}

function formatRequest(raw: string) {
  const value = raw.trim();
  if (!value || value === "{}") return "";
  try { return JSON.stringify(JSON.parse(value), null, 2); } catch { return value; }
}

function formatResult(raw: string) {
  const value = raw.trim();
  if (!value || value === "{}") return "";
  try {
    const parsed = JSON.parse(value) as Record<string, unknown>;
    return Object.entries(parsed).map(([key, item]) => `${key}=${typeof item === "object" ? JSON.stringify(item) : String(item)}`).join("\n");
  } catch { return value; }
}

function ProgressBar({ job }: { job: IntelJob }) {
  const finished = settled(job);
  const total = Math.max(job.total, finished);
  const percent = total > 0 ? Math.min(100, Math.round((finished / total) * 100)) : 0;
  return <span className="cp-progress-track" role="progressbar" aria-valuemin={0} aria-valuemax={total} aria-valuenow={finished}><span className={`cp-progress-fill cp-progress-fill--${toneForStatus(job.status)}`} style={{ width: `${percent}%` }} /></span>;
}

function JobDetailDrawer({ jobId, onClose, showToast }: { jobId: string; onClose: () => void; showToast: ToastFn }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [itemStatus, setItemStatus] = useState("");
  const [itemPage, setItemPage] = useState(0);
  const [itemPageSize, setItemPageSize] = useState<number>(JOB_ITEM_PAGE_SIZE_OPTIONS[0]);
  const [cancelOpen, setCancelOpen] = useState(false);
  const detailQuery = useQuery({ queryKey: ["intel-job", jobId], queryFn: () => getIntelJob(jobId), refetchInterval: (query) => isTerminalJobStatus(query.state.data?.job.status) ? false : 10_000 });
  const job = detailQuery.data?.job;
  const running = job ? !isTerminalJobStatus(job.status) : true;
  const stream = useIntelJobEvents(jobId, running);
  const streamState = stream.state === "ended" && running ? "connecting" : stream.state;
  const itemsQuery = useQuery({ queryKey: ["intel-job-items", jobId, itemStatus, itemPage, itemPageSize], queryFn: () => listIntelJobItems(jobId, itemStatus, itemPageSize, itemPage * itemPageSize), refetchInterval: running ? 15_000 : false });

  useEffect(() => {
    if (!stream.progress) return;
    const frame = stream.progress;
    const status = frame.status && frame.status !== "unknown" ? frame.status : undefined;
    queryClient.setQueryData<IntelJobDetail>(["intel-job", jobId], (current) => current ? { job: { ...current.job, status: status ?? current.job.status, done: frame.done ?? current.job.done, failed: frame.failed ?? current.job.failed, skipped: frame.skipped ?? current.job.skipped, total: frame.total ?? current.job.total }, progress: { ...current.progress, status: status ?? current.progress.status, done: frame.done ?? current.progress.done, failed: frame.failed ?? current.progress.failed, skipped: frame.skipped ?? current.progress.skipped, total: frame.total ?? current.progress.total } } : current);
    void queryClient.invalidateQueries({ queryKey: ["intel-job-items", jobId] });
  }, [jobId, queryClient, stream.progress]);

  useEffect(() => {
    const ended = stream.state === "ended" || (stream.progress?.status ? isTerminalJobStatus(stream.progress.status) : false);
    if (!ended) return;
    void queryClient.invalidateQueries({ queryKey: ["intel-job", jobId] });
    void queryClient.invalidateQueries({ queryKey: ["intel-job-items", jobId] });
    void queryClient.invalidateQueries({ queryKey: ["intel-jobs"] });
  }, [jobId, queryClient, stream.progress?.status, stream.state]);

  const refreshAll = async () => {
    await queryClient.invalidateQueries({ queryKey: ["intel-job", jobId] });
    await queryClient.invalidateQueries({ queryKey: ["intel-job-items", jobId] });
    await queryClient.invalidateQueries({ queryKey: ["intel-jobs"] });
  };
  const cancelMutation = useMutation({ mutationFn: () => cancelIntelJob(jobId), onSuccess: async () => { setCancelOpen(false); await refreshAll(); showToast("success", t("任务已取消")); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const retryMutation = useMutation({ mutationFn: () => retryFailedIntelJob(jobId), onSuccess: async (result) => { await refreshAll(); showToast("success", result.retried > 0 ? t("已重新排队 {{count}} 个失败项", { count: result.retried }) : t("没有可重试的失败项")); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const items: IntelJobItem[] = itemsQuery.data?.items ?? [];
  const total = itemsQuery.data?.total ?? 0;
  const request = job ? formatRequest(job.request_json) : "";

  return <InspectorDrawer open onOpenChange={(open) => { if (!open) onClose(); }} title={t("任务详情")} eyebrow="Inventory / detection job" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={onClose}>{t("关闭")}</ControlButton>{job && !isTerminalJobStatus(job.status) ? <ControlButton variant="danger" size="sm" onPress={() => setCancelOpen(true)}><Ban size={14} aria-hidden />{t("取消任务")}</ControlButton> : null}<ControlButton variant="secondary" size="sm" onPress={() => retryMutation.mutate()} isDisabled={!job || job.failed <= 0} loading={retryMutation.isPending}><RotateCcw size={14} aria-hidden />{t("重试失败项")}</ControlButton></div>}>
    {detailQuery.isError && !job ? <EmptyWorkState title={t("任务详情读取失败")} detail={formatApiErrorMessage(detailQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void detailQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
    {detailQuery.isLoading && !job ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载任务详情")}</div> : null}
    {job ? <div className="cp-stack"><div className="cp-platform-inspector__identity"><span className="cp-platform-row__mark" aria-hidden>JOB</span><div><strong>{job.id}</strong><code>{job.created_by}</code></div><StatusMark tone={toneForStatus(job.status)} label={labelOf(JOB_STATUS_LABELS, job.status, t)} /></div><div className="cp-readout-grid"><DetailFact label={t("任务种类")} value={labelOf(JOB_KIND_LABELS, job.kind, t)} /><DetailFact label={t("进度")} value={<span className="readout">{progressLabel(job, t)}</span>} mono /><DetailFact label={t("优先级")} value={job.priority} mono /><DetailFact label={t("创建时间")} value={formatNs(job.created_at_ns)} mono /><DetailFact label={t("开始时间")} value={formatNs(job.started_at_ns)} mono /><DetailFact label={t("完成时间")} value={formatNs(job.finished_at_ns)} mono /></div><ProgressBar job={job}/><div className="cp-status-line"><StatusMark tone={streamState === "live" ? "signal" : streamState === "error" ? "alert" : "warn"} label={t({ idle: "实时连接", connecting: "实时连接中", live: "实时连接", reconnecting: "实时重连中", ended: "实时推送已结束", error: "实时连接中断" }[streamState])}/>{streamState === "error" ? <ControlButton variant="ghost" size="sm" onPress={stream.reconnect}><RefreshCw size={13} aria-hidden />{t("重新连接")}</ControlButton> : null}</div>{job.error ? <p className="cp-notice cp-notice--alert"><AlertTriangle size={13} aria-hidden />{job.error}</p> : null}{streamState === "error" && running ? <p className="cp-notice cp-notice--warn">{t("实时推送不可用，已降级为每 10 秒轮询一次。")}</p> : null}{request ? <pre className="cp-code-block">{request}</pre> : null}<WorkSurface eyebrow="Inventory / job items" title={t("节点结果")} meta={t("共 {{count}} 个节点", { count: total })}><div className="cp-command"><div className="cp-command__body"><ControlSelect label={t("节点状态")} value={itemStatus} onChange={(event) => { setItemStatus(event.target.value); setItemPage(0); }}><option value="">{t("全部结果")}</option>{JOB_ITEM_STATUS_FILTERS.slice(1).map((value) => <option key={value} value={value}>{labelOf(ITEM_STATUS_LABELS, value, t)}</option>)}</ControlSelect></div></div>{itemsQuery.isError && !itemsQuery.data ? <EmptyWorkState title={t("节点结果读取失败")} detail={formatApiErrorMessage(itemsQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void itemsQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}{itemsQuery.isLoading && !itemsQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载节点结果")}</div> : null}{!itemsQuery.isLoading && !itemsQuery.isError && !items.length ? <EmptyWorkState title={t("暂无节点结果")} /> : null}{items.length ? <ObjectList>{items.map((item) => <ObjectRow key={item.node_hash} leading={<span className="cp-platform-row__mark" aria-hidden>{item.status === "done" ? "OK" : "NODE"}</span>} title={item.node_hash} subtitle={`${labelOf(ITEM_STATUS_LABELS, item.status, t)} · ${t("第 {{step}} 步", { step: item.step_index })} ${labelOf(STEP_LABELS, String(item.step_index), t)}`} facts={<><span>{t("尝试") } <span className="readout">{item.attempts}</span></span><span>{item.error_code || t("无错误")}</span><span>{formatNs(item.updated_at_ns)}</span></>} status={<StatusMark tone={toneForStatus(item.status)} label={labelOf(ITEM_STATUS_LABELS, item.status, t)} />} action={formatResult(item.result_json) ? <QueryBadge label={t("有结果摘要")} /> : undefined} />)}</ObjectList> : null}{total ? <ControlPagination page={Math.min(itemPage, Math.max(0, Math.ceil(total / itemPageSize) - 1))} totalItems={total} pageSize={itemPageSize} pageSizes={JOB_ITEM_PAGE_SIZE_OPTIONS} disabled={itemsQuery.isFetching} labels={{ summary: ({ page: current, pages, start, end, total: count }) => t("第 {{page}} / {{pages}} 页 · {{start}}-{{end}} / {{total}}", { page: current, pages, start, end, total: count }), perPage: t("每页"), pageOption: (size) => `${size} / ${t("页")}`, previous: t("上一页"), next: t("下一页") }} onPageChange={setItemPage} onPageSizeChange={(size) => { setItemPageSize(size); setItemPage(0); }}/>: null}</WorkSurface></div> : null}
    <ConfirmDrawer open={cancelOpen} onOpenChange={setCancelOpen} title={t("确认取消任务")} eyebrow="Destructive action" message={t("确认取消任务 {{id}} 吗？正在处理的节点会停止。", { id: jobId })} cancelLabel={t("取消") } confirmLabel={t("确认取消")} loading={cancelMutation.isPending} onConfirm={() => cancelMutation.mutate()}/>
  </InspectorDrawer>;
}

function CreateJobDrawer({ open, onOpenChange, showToast, onCreated }: { open: boolean; onOpenChange: (open: boolean) => void; showToast: ToastFn; onCreated: (job: IntelJob) => void }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<JobKind>("intel");
  const [allNodes, setAllNodes] = useState(true);
  const [hashesText, setHashesText] = useState("");
  const [force, setForce] = useState(false);
  const [error, setError] = useState("");
  const hashes = useMemo(() => parseHashes(hashesText), [hashesText]);
  const mutation = useMutation({ mutationFn: (request: CreateIntelJobRequest) => createIntelJob(request), onSuccess: (result) => { void queryClient.invalidateQueries({ queryKey: ["intel-jobs"] }); setError(""); onCreated(result.job); onOpenChange(false); showToast("success", t("任务已创建")); }, onError: (mutationError) => setError(formatApiErrorMessage(mutationError, t)) });
  return <InspectorDrawer open={open} onOpenChange={onOpenChange} title={t("新建检测任务")} eyebrow="Inventory / detection job" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => onOpenChange(false)}>{t("取消")}</ControlButton><ControlButton type="submit" form="create-job-form" variant="primary" size="sm" loading={mutation.isPending}>{t("创建任务")}</ControlButton></div>}><form id="create-job-form" className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); if (!allNodes && hashes.length === 0) { setError(t("请选择全部节点，或至少填写一个节点哈希。")); return; } setError(""); mutation.mutate({ kind, scope: allNodes ? { all: true } : { node_hashes: hashes }, force }); }}><ControlSelect label={t("检测种类")} value={kind} onChange={(event) => setKind(event.target.value as JobKind)}>{JOB_KINDS.map((value) => <option key={value} value={value}>{labelOf(JOB_KIND_LABELS, value, t)}</option>)}</ControlSelect><p className="cp-muted">{t("手动任务优先级固定为 100。")}</p><ControlSwitch label={t("全部节点")} hint={allNodes ? t("对节点池中的所有节点执行。") : t("仅对下方列出的节点执行。")} isSelected={allNodes} onChange={setAllNodes}/>{!allNodes ? <ControlTextarea label={t("节点哈希")} hint={t("每行一个节点哈希，也支持空格或逗号分隔；重复项会自动去除。") + (hashes.length ? ` ${t("已识别 {{count}} 个节点", { count: hashes.length })}` : "")} rows={6} value={hashesText} onChange={(event) => setHashesText(event.target.value)} /> : null}<ControlSwitch label={t("忽略缓存证据")} hint={t("开启后会重新查询未过期的在线证据，而不是复用它们。")} isSelected={force} onChange={setForce}/>{error ? <p className="cp-form-error">{error}</p> : null}</form></InspectorDrawer>;
}

export function JobsPage() {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const [status, setStatus] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(JOB_PAGE_SIZE_OPTIONS[1]);
  const [selectedId, setSelectedId] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const query = useQuery({ queryKey: ["intel-jobs", status, page, pageSize], queryFn: () => listIntelJobs(status, pageSize, page * pageSize), placeholderData: (previous) => previous, refetchInterval: (current) => (current.state.data?.items ?? []).some((job) => !isTerminalJobStatus(job.status)) ? 15_000 : false });
  const jobs = query.data?.items ?? [];
  const total = query.data?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const currentPage = Math.min(page, pages - 1);
  return <PageFrame eyebrow="Inventory / jobs" title={t("检测任务")} description={t("管理批量情报、出口和解锁检测；任务详情保留实时进度、节点结果与重试入口。")} meta={<StatusMark tone={query.isError && !query.data ? "warn" : query.data ? "live" : "neutral"} label={query.isError && !query.data ? t("读取失败") : query.data ? t("队列已同步") : t("等待队列")} />} actions={<><ControlButton variant="secondary" size="sm" onPress={() => void query.refetch()} loading={query.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton><ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("新建任务")}</ControlButton></>}>
    <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    <section className="cp-command"><div className="cp-command__body"><ControlSelect label={t("任务状态")} value={status} onChange={(event) => { setStatus(event.target.value); setPage(0); }}><option value="">{t("全部状态")}</option>{JOB_STATUS_FILTERS.slice(1).map((value) => <option key={value} value={value}>{labelOf(JOB_STATUS_LABELS, value, t)}</option>)}</ControlSelect></div></section>
    <WorkSurface eyebrow="Inventory / job queue" title={t("任务队列")} meta={query.data ? t("共 {{count}} 个任务", { count: total }) : t("未加载")} description={t("实时流只在任务详情打开时建立；列表使用慢轮询发现新状态。")}>
      {query.isError && !query.data ? <EmptyWorkState title={t("任务队列读取失败")} detail={formatApiErrorMessage(query.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void query.refetch()}>{t("重试")}</ControlButton>} /> : null}
      {query.isLoading && !query.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载任务队列")}</div> : null}
      {!query.isLoading && !query.isError && !jobs.length ? <EmptyWorkState title={t("暂无检测任务")} detail={t("创建一次批量检测后，实时进度和终态证据会显示在这里。")} action={<ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("新建任务")}</ControlButton>} /> : null}
      {jobs.length ? <ObjectList>{jobs.map((job) => <ObjectRow key={job.id} active={selectedId === job.id} onClick={() => setSelectedId(job.id)} leading={<span className="cp-platform-row__mark" aria-hidden>JOB</span>} title={job.id.slice(0, 12)} subtitle={`${labelOf(JOB_KIND_LABELS, job.kind, t)} · ${job.created_by}`} facts={<><span>{progressLabel(job, t)}</span><span>{t("创建")} {formatNs(job.created_at_ns)}</span><span>{job.finished_at_ns ? `${t("完成")} ${formatNs(job.finished_at_ns)}` : t("仍在运行")}</span></>} status={<StatusMark tone={toneForStatus(job.status)} label={labelOf(JOB_STATUS_LABELS, job.status, t)} />} action={<ProgressBar job={job}/>} />)}</ObjectList> : null}
      {query.data ? <ControlPagination page={currentPage} totalItems={total} pageSize={pageSize} pageSizes={JOB_PAGE_SIZE_OPTIONS} disabled={query.isFetching} labels={{ summary: ({ page: current, pages: totalPages, start, end, total: count }) => t("第 {{page}} / {{pages}} 页 · {{start}}-{{end}} / {{total}}", { page: current, pages: totalPages, start, end, total: count }), perPage: t("每页"), pageOption: (size) => `${size} / ${t("页")}`, previous: t("上一页"), next: t("下一页") }} onPageChange={setPage} onPageSizeChange={(size) => { setPageSize(size); setPage(0); }} /> : null}
    </WorkSurface>
    {selectedId ? <JobDetailDrawer key={selectedId} jobId={selectedId} onClose={() => setSelectedId("")} showToast={showToast}/> : null}
    <CreateJobDrawer open={createOpen} onOpenChange={setCreateOpen} showToast={showToast} onCreated={(job) => setSelectedId(job.id)}/>
  </PageFrame>;
}

export default JobsPage;
