import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Info, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  ConfirmDrawer,
  ControlButton,
  ControlInput,
  ControlPagination,
  ControlSelect,
  ControlSwitch,
  ControlTabs,
  ControlTextarea,
  DetailFact,
  EmptyWorkState,
  FilterChip,
  InspectorDrawer,
  ObjectList,
  ObjectRow,
  PageFrame,
  QueryBadge,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime, formatRelativeTime } from "../../lib/time";
import {
  cleanupSubscriptionCircuitOpenNodes,
  createSubscription,
  deleteSubscription,
  getSubscription,
  getSubscriptionParseReport,
  listSubscriptions,
  refreshSubscription,
  updateSubscription,
} from "./api";
import type { Subscription, SubscriptionCreateInput, SubscriptionParseReportDetail, SubscriptionUpdateInput } from "./types";

type EnabledFilter = "all" | "enabled" | "disabled";
type SourceType = "remote" | "local";
type FormState = {
  name: string;
  source_type: SourceType;
  url: string;
  content: string;
  update_interval: string;
  ephemeral_node_evict_delay: string;
  enabled: boolean;
  ephemeral: boolean;
  incremental_alive_nodes: boolean;
  auto_intel: boolean;
};

type ConfirmAction =
  | { kind: "delete"; subscription: Subscription }
  | { kind: "cleanup"; subscription: Subscription }
  | null;

const PAGE_SIZE_OPTIONS = [10, 20, 50, 100];
const LOCAL_SOURCE_UPDATE_INTERVAL = "12h";
const PARSE_REPORT_HINT = "解析报告按原因分组列出被丢弃的节点，并保留有限的名称样例。被导入但配置被改写的节点会单独标记，不计入丢弃。";

function formFromSubscription(subscription?: Subscription): FormState {
  return {
    name: subscription?.name ?? "",
    source_type: subscription?.source_type ?? "remote",
    url: subscription?.url ?? "",
    content: subscription?.content ?? "",
    update_interval: subscription?.update_interval ?? "12h",
    ephemeral_node_evict_delay: subscription?.ephemeral_node_evict_delay ?? "72h",
    enabled: subscription?.enabled ?? true,
    ephemeral: subscription?.ephemeral ?? false,
    incremental_alive_nodes: subscription?.incremental_alive_nodes ?? false,
    auto_intel: subscription?.auto_intel ?? true,
  };
}


function rowState(subscription: Subscription, pendingRefresh: boolean, enabled: boolean) {
  if (pendingRefresh) return { tone: "neutral" as const, label: "同步中" };
  if (!enabled) return { tone: "neutral" as const, label: "已停用" };
  if (subscription.last_error) return { tone: "alert" as const, label: "错误" };
  if (subscription.last_updated) return { tone: "signal" as const, label: "正常" };
  return { tone: "neutral" as const, label: "未检查" };
}

function hostname(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return url || "-";
  }
}

function ParseReport({ subscription }: { subscription: Subscription }) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const detailQuery = useQuery({
    queryKey: ["subscriptions", "parse-report", subscription.id],
    queryFn: () => getSubscriptionParseReport(subscription.id),
    enabled: expanded,
  });
  const summary = subscription.parse_report;
  const detail = detailQuery.data;
  return (
    <div className="cp-inset-stack">
      <div className="cp-section-line"><strong>{t("解析报告")}</strong><span title={t(PARSE_REPORT_HINT)} aria-label={t(PARSE_REPORT_HINT)}><Info size={13} aria-hidden /></span></div>
      {!summary ? <p className="cp-muted">{t("该订阅尚未解析；刷新一次后会在这里列出被丢弃的节点及原因。")}</p> : <>
        <p className="cp-muted">{t("共 {{total}} 个节点：导入 {{imported}} 个，丢弃 {{skipped}} 个。", { total: summary.total, imported: summary.imported, skipped: summary.skipped })}</p>
        {summary.reasons.length ? <ObjectList>{summary.reasons.map((reason) => <ObjectRow key={reason.reason} leading={<span className="cp-platform-row__mark" aria-hidden>{reason.count}</span>} title={reason.reason} subtitle={reason.detail || t("解析跳过原因")} facts={<span>{reason.sample_names.length ? t("名称样例：{{names}}", { names: reason.sample_names.join("、") }) : t("没有可显示的名称")}</span>} status={reason.samples_truncated ? <QueryBadge label={t("样例已截断")} tone="warn" /> : undefined} />)}</ObjectList> : <p className="cp-muted">{t("没有节点被丢弃。")}</p>}
        {summary.skipped_overflow ? <p className="cp-muted">{t("另有 {{count}} 个被丢弃的节点只计入数量，未记录名称。", { count: summary.skipped_overflow })}</p> : null}
        {summary.notes ? <p className="cp-muted">{t("另有 {{count}} 个节点被导入时改写了配置。", { count: summary.notes })}</p> : null}
        <ControlButton variant="ghost" size="sm" onPress={() => setExpanded((value) => !value)}>{expanded ? t("收起完整列表") : t("查看完整列表")}</ControlButton>
        {expanded ? <ParseReportDetail detail={detail} loading={detailQuery.isLoading} error={detailQuery.error} onRetry={() => void detailQuery.refetch()} /> : null}
      </>}
    </div>
  );
}

function ParseReportDetail({ detail, loading, error, onRetry }: { detail?: SubscriptionParseReportDetail; loading: boolean; error: Error | null; onRetry: () => void }) {
  const { t } = useI18n();
  if (loading) return <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载解析报告")}</div>;
  if (error) return <p className="cp-form-error">{formatApiErrorMessage(error, t)} <ControlButton variant="ghost" size="sm" onPress={onRetry}>{t("重试")}</ControlButton></p>;
  if (!detail || !detail.parsed) return <p className="cp-muted">{t("该订阅尚未解析。")}</p>;
  if (detail.truncated) return <p className="cp-muted">{t("解析报告超过 64 KiB，只保留了截断标记。")}</p>;
  const notes = detail.notes ?? [];
  return <ObjectList>{[...notes.map((entry) => ({ ...entry, kind: "note" })), ...detail.skipped.map((entry) => ({ ...entry, kind: "skipped" }))].map((entry) => <ObjectRow key={`${entry.kind}|${entry.type}|${entry.name}|${entry.reason}`} leading={<span className="cp-platform-row__mark" aria-hidden>{entry.kind === "note" ? "NOTE" : "DROP"}</span>} title={`${entry.name || t("未命名")} · ${entry.type || t("未知类型")}`} subtitle={entry.kind === "note" ? t("导入时已改写") : entry.reason} facts={<span>{entry.detail}</span>} />)}</ObjectList>;
}

function SubscriptionForm({ mode, initial, onSubmit, pending, error, formId }: { mode: "create" | "edit"; initial?: Subscription; onSubmit: (form: FormState) => void; pending: boolean; error: string; formId: string }) {
  const { t } = useI18n();
  const [form, setForm] = useState<FormState>(() => formFromSubscription(initial));
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => setForm((current) => ({ ...current, [key]: value }));
  const [validationError, setValidationError] = useState("");
  const contentPlaceholder = t("支持 sing-box、Clash/Mihomo、URI 或 base64 内容；本地订阅直接粘贴文本。");
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!form.name.trim()) { setValidationError(t("订阅名称不能为空")); return; }
    if (form.source_type === "remote" && (!form.url.trim() || !/^https?:\/\//i.test(form.url.trim()))) { setValidationError(t("URL 必须是 http/https 地址")); return; }
    if (form.source_type === "local" && !form.content.trim()) { setValidationError(t("订阅内容不能为空")); return; }
    if (form.source_type === "remote" && !form.update_interval.trim()) { setValidationError(t("更新间隔不能为空")); return; }
    if (!form.ephemeral_node_evict_delay.trim()) { setValidationError(t("临时节点驱逐延迟不能为空")); return; }
    setValidationError("");
    onSubmit(form);
  };
  return <form id={formId} className="cp-form-stack" onSubmit={submit}>
    {validationError || error ? <p className="cp-form-error">{validationError || error}</p> : null}
    <ControlInput label={t("订阅名称")} value={form.name} onChange={(event) => set("name", event.target.value)} />
    <ControlTabs label={t("订阅来源类型")} items={[{ id: "remote", label: t("远程") }, { id: "local", label: t("本地") }]} selectedKey={form.source_type} onSelectionChange={(key) => set("source_type", key as SourceType)} />
    {form.source_type === "remote" ? <><ControlInput label={t("更新间隔")} hint={t("例如 12h")} value={form.update_interval} onChange={(event) => set("update_interval", event.target.value)} /><ControlInput label={t("订阅链接")} value={form.url} onChange={(event) => set("url", event.target.value)} /></> : <ControlTextarea label={t("订阅内容")} hint={contentPlaceholder} rows={9} value={form.content} onChange={(event) => set("content", event.target.value)} />}
    <ControlSwitch label={t("启用")} hint={t("禁用订阅后，相关节点不会参与平台路由、健康统计或自动探测。")} isSelected={form.enabled} onChange={(value) => set("enabled", value)} isDisabled={pending} />
    <ControlSwitch label={t("临时订阅")} hint={t("临时订阅的非健康节点会在一段时间后被自动删除。订阅本身不会被删除。")} isSelected={form.ephemeral} onChange={(value) => set("ephemeral", value)} isDisabled={pending} />
    <ControlSwitch label={t("存活节点增量模式")} hint={t("刷新时保留当前仍存活的旧节点，并合并新订阅内容。")} isSelected={form.incremental_alive_nodes} onChange={(value) => set("incremental_alive_nodes", value)} isDisabled={pending} />
    <ControlSwitch label={t("自动 Intel 检测")} hint={t("刷新新增节点后自动进入 Intel 批量检测。")} isSelected={form.auto_intel} onChange={(value) => set("auto_intel", value)} isDisabled={pending} />
    <ControlInput label={t("临时节点驱逐延迟")} hint={t("例如 72h")} value={form.ephemeral_node_evict_delay} onChange={(event) => set("ephemeral_node_evict_delay", event.target.value)} disabled={pending || !form.ephemeral} />
    {mode === "edit" && initial ? <div className="cp-readout-grid"><DetailFact label={t("创建时间")} value={formatDateTime(initial.created_at)} mono /><DetailFact label={t("上次检查")} value={formatDateTime(initial.last_checked || "")} mono /><DetailFact label={t("上次更新")} value={formatDateTime(initial.last_updated || "")} mono /></div> : null}
  </form>;
}

function toCreateInput(form: FormState): SubscriptionCreateInput {
  return { name: form.name.trim(), source_type: form.source_type, update_interval: form.source_type === "local" ? LOCAL_SOURCE_UPDATE_INTERVAL : form.update_interval.trim(), ephemeral_node_evict_delay: form.ephemeral_node_evict_delay.trim(), enabled: form.enabled, ephemeral: form.ephemeral, incremental_alive_nodes: form.incremental_alive_nodes, auto_intel: form.auto_intel, ...(form.source_type === "remote" ? { url: form.url.trim() } : { content: form.content }) };
}

function toUpdateInput(form: FormState): SubscriptionUpdateInput {
  return { name: form.name.trim(), update_interval: form.source_type === "local" ? LOCAL_SOURCE_UPDATE_INTERVAL : form.update_interval.trim(), ephemeral_node_evict_delay: form.ephemeral_node_evict_delay.trim(), enabled: form.enabled, ephemeral: form.ephemeral, incremental_alive_nodes: form.incremental_alive_nodes, auto_intel: form.auto_intel, ...(form.source_type === "remote" ? { url: form.url.trim() } : { content: form.content }) };
}

export function SubscriptionPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [params, setParams] = useSearchParams();
  const [filter, setFilter] = useState<EnabledFilter>("all");
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebouncedValue(search);
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(20);
  const [confirmAction, setConfirmAction] = useState<ConfirmAction>(null);
  const [pendingRefresh, setPendingRefresh] = useState<string | null>(null);
  const [pendingToggle, setPendingToggle] = useState<string | null>(null);
  const selectedId = params.get("selected") ?? "";
  const createOpen = params.get("create") === "1";
  const setSelectedId = (id: string) => setParams((current) => { const next = new URLSearchParams(current); if (id) next.set("selected", id); else next.delete("selected"); return next; }, { replace: true });
  const setCreateOpen = (open: boolean) => setParams((current) => { const next = new URLSearchParams(current); if (open) next.set("create", "1"); else next.delete("create"); return next; }, { replace: true });
  const enabled = filter === "all" ? undefined : filter === "enabled";
  const listQuery = useQuery({ queryKey: ["subscriptions", filter, debouncedSearch, page, pageSize], queryFn: () => listSubscriptions({ enabled, keyword: debouncedSearch, limit: pageSize, offset: page * pageSize }), placeholderData: (previous) => previous, refetchInterval: 30_000 });
  const selectedQuery = useQuery({ queryKey: ["subscriptions", "detail", selectedId], queryFn: () => getSubscription(selectedId), enabled: Boolean(selectedId), refetchInterval: 30_000 });
  const subscriptions = listQuery.data?.items ?? [];
  const selected = selectedQuery.data ?? subscriptions.find((item) => item.id === selectedId) ?? null;
  const total = listQuery.data?.total ?? 0;
  const filtersActive = filter !== "all" || Boolean(search.trim());
  const invalidate = () => { void queryClient.invalidateQueries({ queryKey: ["subscriptions"] }); void queryClient.invalidateQueries({ queryKey: ["nodes"] }); };

  const createMutation = useMutation({ mutationFn: (form: FormState) => createSubscription(toCreateInput(form)), onSuccess: (created) => { setCreateOpen(false); invalidate(); showToast("success", t("订阅 {{name}} 创建成功", { name: created.name })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const updateMutation = useMutation({ mutationFn: ({ id, form }: { id: string; form: FormState }) => updateSubscription(id, toUpdateInput(form)), onSuccess: (updated) => { invalidate(); showToast("success", t("订阅 {{name}} 已更新", { name: updated.name })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const refreshMutation = useMutation({ mutationFn: (subscription: Subscription) => refreshSubscription(subscription.id), onSuccess: (_result, subscription) => { setPendingRefresh(null); invalidate(); showToast("success", t("订阅 {{name}} 已手动刷新", { name: subscription.name })); }, onError: (error) => { setPendingRefresh(null); showToast("error", formatApiErrorMessage(error, t)); } });
  const toggleMutation = useMutation({ mutationFn: ({ subscription, enabled: next }: { subscription: Subscription; enabled: boolean }) => updateSubscription(subscription.id, { enabled: next }), onSuccess: (_result, variables) => { setPendingToggle(null); invalidate(); showToast("success", variables.enabled ? t("订阅已启用") : t("订阅已禁用")); }, onError: (error) => { setPendingToggle(null); showToast("error", formatApiErrorMessage(error, t)); } });
  const cleanupMutation = useMutation({ mutationFn: (subscription: Subscription) => cleanupSubscriptionCircuitOpenNodes(subscription.id), onSuccess: (count, subscription) => { setConfirmAction(null); invalidate(); showToast("success", count > 0 ? t("订阅 {{name}} 已清理 {{count}} 个节点", { name: subscription.name, count }) : t("订阅 {{name}} 没有可清理的熔断或异常节点", { name: subscription.name })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const deleteMutation = useMutation({ mutationFn: (subscription: Subscription) => deleteSubscription(subscription.id), onSuccess: (_result, subscription) => { setConfirmAction(null); if (selectedId === subscription.id) setSelectedId(""); invalidate(); showToast("success", t("订阅 {{name}} 已删除", { name: subscription.name })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });

  const clearFilters = () => { setFilter("all"); setSearch(""); setPage(0); };
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const currentPage = Math.min(page, pages - 1);
  const selectedStatus = selected ? rowState(selected, pendingRefresh === selected.id, selected.enabled) : null;

  const actionDrawer = selected ? <InspectorDrawer open={Boolean(selectedId)} onOpenChange={(open) => { if (!open) setSelectedId(""); }} title={selected.name} eyebrow="Inventory / subscription" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => setSelectedId("")}>{t("关闭")}</ControlButton><ControlButton variant="secondary" size="sm" onPress={() => { setPendingRefresh(selected.id); refreshMutation.mutate(selected); }} loading={pendingRefresh === selected.id}><RefreshCw size={14} aria-hidden />{t("立即刷新")}</ControlButton><ControlButton variant="danger" size="sm" onPress={() => setConfirmAction({ kind: "delete", subscription: selected })}><Trash2 size={14} aria-hidden />{t("删除")}</ControlButton></div>}>
    <div className="cp-stack"><div className="cp-platform-inspector__identity"><span className="cp-platform-row__mark" aria-hidden>{selected.source_type === "local" ? "LOC" : "URL"}</span><div><strong>{selected.name}</strong><code>{selected.id}</code></div>{selectedStatus ? <StatusMark tone={selectedStatus.tone === "signal" ? "signal" : selectedStatus.tone === "alert" ? "alert" : "neutral"} label={t(selectedStatus.label)} /> : null}</div><div className="cp-readout-grid"><DetailFact label={t("来源") } value={selected.source_type === "local" ? t("本地订阅") : hostname(selected.url)} /><DetailFact label={t("节点") } value={`${selected.healthy_node_count} / ${selected.node_count}`} mono /><DetailFact label={t("最近检查") } value={formatDateTime(selected.last_checked || "")} mono /><DetailFact label={t("最近更新") } value={formatDateTime(selected.last_updated || "")} mono /></div><SubscriptionForm mode="edit" initial={selected} formId="edit-subscription-form" pending={updateMutation.isPending} error={updateMutation.error ? formatApiErrorMessage(updateMutation.error, t) : ""} onSubmit={(form) => updateMutation.mutate({ id: selected.id, form })}/><div className="cp-drawer-actions"><ControlButton type="submit" form="edit-subscription-form" variant="primary" size="sm" loading={updateMutation.isPending}>{t("保存配置")}</ControlButton><ControlButton variant="secondary" size="sm" onPress={() => { setPendingToggle(selected.id); toggleMutation.mutate({ subscription: selected, enabled: !selected.enabled }); }} loading={pendingToggle === selected.id}>{selected.enabled ? t("停用") : t("启用")}</ControlButton></div><ParseReport subscription={selected}/><div className="cp-danger-list"><div className="cp-danger-row cp-danger-row--warn"><div><strong>{t("清理失效节点")}</strong><p>{t("立即清理当前熔断或出错的节点。")} </p></div><ControlButton variant="secondary" size="sm" onPress={() => setConfirmAction({ kind: "cleanup", subscription: selected })}>{t("立即清理")}</ControlButton></div></div></div>
  </InspectorDrawer> : null;

  return <PageFrame eyebrow="Inventory / subscriptions" title={t("订阅源")} description={t("管理远程与本地订阅、刷新策略、解析结果和节点池影响。")} meta={<StatusMark tone={listQuery.isError && !listQuery.data ? "warn" : listQuery.data ? "live" : "neutral"} label={listQuery.isError && !listQuery.data ? t("读取失败") : listQuery.data ? t("目录已同步") : t("等待目录")} />} actions={<ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("新建订阅")}</ControlButton>}>
    <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    <section className="cp-command"><div className="cp-command__body"><ControlSelect label={t("状态")} value={filter} onChange={(event) => { setFilter(event.target.value as EnabledFilter); setPage(0); }}><option value="all">{t("全部状态")}</option><option value="enabled">{t("仅启用")}</option><option value="disabled">{t("仅禁用")}</option></ControlSelect><ControlInput label={t("搜索订阅")} value={search} onChange={(event) => { setSearch(event.target.value); setPage(0); }} placeholder={t("按名称或 URL 搜索")}/></div><div className="cp-command__actions"><Search size={14} aria-hidden />{filtersActive ? <ControlButton variant="ghost" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : null}</div></section>
    {filtersActive ? <div className="cp-active-filters" aria-label={t("当前筛选")}><FilterChip label={`${t("状态")}: ${filter === "enabled" ? t("仅启用") : t("仅禁用")}`} onRemove={() => { setFilter("all"); setPage(0); }}/>{search.trim() ? <FilterChip label={`${t("关键词")}: ${search.trim()}`} onRemove={() => { setSearch(""); setPage(0); }}/> : null}</div> : null}
    <WorkSurface eyebrow="Subscription objects" title={t("订阅目录")} meta={listQuery.data ? t("共 {{count}} 个订阅", { count: total }) : t("未加载")} description={t("选择一项查看配置、解析报告和运维动作；列表只呈现服务端返回的节点与同步事实。")}>
      {listQuery.isError && !listQuery.data ? <EmptyWorkState title={t("订阅目录读取失败")} detail={formatApiErrorMessage(listQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void listQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
      {listQuery.isLoading && !listQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载订阅列表")}</div> : null}
      {!listQuery.isLoading && !listQuery.isError && subscriptions.length === 0 ? <EmptyWorkState title={t(filtersActive ? "没有匹配的订阅" : "尚未添加任何订阅源")} detail={t(filtersActive ? "调整筛选后重试。" : "创建第一个订阅源后，节点池关系会显示在这里。")} action={filtersActive ? <ControlButton variant="secondary" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : <ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("新建订阅")}</ControlButton>} /> : null}
      {subscriptions.length ? <ObjectList>{subscriptions.map((subscription) => { const state = rowState(subscription, pendingRefresh === subscription.id, subscription.enabled); return <ObjectRow key={subscription.id} active={selectedId === subscription.id} onClick={() => setSelectedId(subscription.id)} leading={<span className="cp-platform-row__mark" aria-hidden>{subscription.source_type === "local" ? "LOC" : "URL"}</span>} title={subscription.name} subtitle={`${subscription.source_type === "local" ? t("本地订阅") : hostname(subscription.url)} · ${formatRelativeTime(subscription.last_updated || subscription.created_at)}`} facts={<><span>{t("健康节点")} <span className="readout">{subscription.healthy_node_count}</span> / {subscription.node_count}</span><span>{t("更新间隔")} <span className="readout">{subscription.update_interval || "-"}</span></span></>} status={<StatusMark tone={state.tone === "signal" ? "signal" : state.tone === "alert" ? "alert" : "neutral"} label={t(state.label)} />} action={<ControlButton variant="ghost" size="sm" isIconOnly aria-label={subscription.enabled ? t("停用") : t("启用")} onPress={() => { setPendingToggle(subscription.id); toggleMutation.mutate({ subscription, enabled: !subscription.enabled }); }} loading={pendingToggle === subscription.id}>{subscription.enabled ? "−" : "+"}</ControlButton>} />; })}</ObjectList> : null}
      {listQuery.data ? <ControlPagination page={currentPage} totalItems={total} pageSize={pageSize} pageSizes={PAGE_SIZE_OPTIONS} disabled={listQuery.isFetching} labels={{ summary: ({ page: current, pages: totalPages, start, end, total: count }) => t("第 {{page}} / {{pages}} 页 · {{start}}-{{end}} / {{total}}", { page: current, pages: totalPages, start, end, total: count }), perPage: t("每页"), pageOption: (size) => `${size} / ${t("页")}`, previous: t("上一页"), next: t("下一页") }} onPageChange={setPage} onPageSizeChange={(size) => { setPageSize(size); setPage(0); }}/>: null}
    </WorkSurface>
    {selectedId && !selected ? <InspectorDrawer open onOpenChange={(open) => { if (!open) setSelectedId(""); }} title={t("订阅详情")} eyebrow="Inventory / subscription" footer={<ControlButton variant="ghost" size="sm" onPress={() => setSelectedId("")}>{t("关闭")}</ControlButton>}><div className="cp-register-state"><span className="cp-spinner" aria-hidden />{selectedQuery.isError ? formatApiErrorMessage(selectedQuery.error, t) : t("正在加载订阅详情")}</div></InspectorDrawer> : null}
    {actionDrawer}
    <InspectorDrawer open={createOpen} onOpenChange={setCreateOpen} title={t("新建订阅")} eyebrow="Inventory / subscription" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => setCreateOpen(false)}>{t("取消")}</ControlButton><ControlButton type="submit" form="create-subscription-form" variant="primary" size="sm" loading={createMutation.isPending}>{t("确认创建")}</ControlButton></div>}><SubscriptionForm key={createOpen ? "create" : "closed"} mode="create" formId="create-subscription-form" pending={createMutation.isPending} error={createMutation.error ? formatApiErrorMessage(createMutation.error, t) : ""} onSubmit={(form) => createMutation.mutate(form)}/></InspectorDrawer>
    <ConfirmDrawer open={Boolean(confirmAction)} onOpenChange={(open) => { if (!open) setConfirmAction(null); }} title={confirmAction?.kind === "delete" ? t("确认删除订阅") : t("确认清理失效节点")} eyebrow="Destructive action" message={confirmAction?.kind === "delete" ? t("确认删除订阅 {{name}}？关联节点会被清理，操作不可撤销。", { name: confirmAction.subscription.name }) : t("确认立即清理订阅 {{name}} 中的熔断或异常节点？", { name: confirmAction?.subscription.name ?? "" })} cancelLabel={t("取消")} confirmLabel={confirmAction?.kind === "delete" ? t("确认删除") : t("确认清理")} loading={deleteMutation.isPending || cleanupMutation.isPending} onConfirm={() => { if (!confirmAction) return; if (confirmAction.kind === "delete") deleteMutation.mutate(confirmAction.subscription); else cleanupMutation.mutate(confirmAction.subscription); }}/>
  </PageFrame>;
}

export default SubscriptionPage;
