import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, LockKeyhole, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
import {
  ConfirmDrawer,
  ControlButton,
  ControlInput,
  ControlPagination,
  ControlSwitch,
  DetailFact,
  EmptyWorkState,
  InspectorDrawer,
  ObjectList,
  ObjectRow,
  PageFrame,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { createEndpoint, deleteEndpoint, listEndpoints, updateEndpoint } from "./api";
import type { Endpoint, EndpointInput } from "./types";

type EndpointFormState = {
  port: string;
  enabled: boolean;
  allow_management: boolean;
  require_proxy_auth_info: boolean;
  allow_http_forward: boolean;
  allow_http_reverse: boolean;
  allow_socks5: boolean;
};

type EndpointConfirm = Endpoint | null;
const PAGE_SIZE_OPTIONS = [10, 20, 50, 100];
const DEFAULT_FORM: EndpointFormState = { port: "", enabled: true, allow_management: false, require_proxy_auth_info: false, allow_http_forward: true, allow_http_reverse: true, allow_socks5: true };
const AUTH_HINT = "当代理令牌未配置时，部分应用只有在代理服务器强制要求认证时才会发送认证字段。开启后，没有认证信息的请求会被拒绝。";

function formFromEndpoint(endpoint?: Endpoint | null): EndpointFormState {
  if (!endpoint) return DEFAULT_FORM;
  return { port: String(endpoint.port), enabled: endpoint.enabled, allow_management: endpoint.allow_management, require_proxy_auth_info: endpoint.require_proxy_auth_info && (endpoint.allow_http_forward || endpoint.allow_socks5), allow_http_forward: endpoint.allow_http_forward, allow_http_reverse: endpoint.allow_http_reverse, allow_socks5: endpoint.allow_socks5 };
}

function statusView(status: string, t: (value: string, values?: Record<string, unknown>) => string) {
  if (status === "active") return { tone: "signal" as const, label: t("运行中") };
  if (status === "starting") return { tone: "warn" as const, label: t("启动中") };
  if (status === "error") return { tone: "alert" as const, label: t("异常") };
  return { tone: "neutral" as const, label: t("未运行") };
}

function parseEndpoint(form: EndpointFormState, endpoints: Endpoint[], editingId: string | null, t: (value: string, values?: Record<string, unknown>) => string): EndpointInput {
  const port = Number(form.port.trim());
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error(t("端口必须是 1 到 65535 之间的整数"));
  if (endpoints.some((endpoint) => endpoint.id !== editingId && endpoint.port === port)) throw new Error(t("端口 {{port}} 已被其他接入点使用", { port }));
  const allowProxy = form.allow_http_forward || form.allow_http_reverse || form.allow_socks5;
  if (!form.allow_management && !allowProxy) throw new Error(t("至少启用管理页面或一种代理能力"));
  if (form.require_proxy_auth_info && !form.allow_http_forward && !form.allow_socks5) throw new Error(t("强制客户端发送认证信息需要启用 HTTP 正向代理或 SOCKS5 代理"));
  return { port, enabled: form.enabled, allow_management: form.allow_management, allow_proxy: allowProxy, require_proxy_auth_info: form.require_proxy_auth_info, allow_http_forward: form.allow_http_forward, allow_http_reverse: form.allow_http_reverse, allow_socks5: form.allow_socks5 };
}

function EndpointForm({ endpoint, endpoints, formId, pending, onSubmit }: { endpoint?: Endpoint | null; endpoints: Endpoint[]; formId: string; pending: boolean; onSubmit: (input: EndpointInput) => void }) {
  const { t } = useI18n();
  const [form, setForm] = useState<EndpointFormState>(() => formFromEndpoint(endpoint));
  const [error, setError] = useState("");
  const set = <K extends keyof EndpointFormState>(key: K, value: EndpointFormState[K]) => setForm((current) => ({ ...current, [key]: value }));
  const setProtocol = (key: "allow_http_forward" | "allow_http_reverse" | "allow_socks5", value: boolean) => setForm((current) => ({ ...current, [key]: value, require_proxy_auth_info: key !== "allow_http_forward" && key !== "allow_socks5" ? current.require_proxy_auth_info : value || (key === "allow_http_forward" ? current.allow_socks5 : current.allow_http_forward) ? current.require_proxy_auth_info : false }));
  const readOnly = endpoint?.read_only ?? false;
  return <form id={formId} className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); if (readOnly) return; try { setError(""); onSubmit(parseEndpoint(form, endpoints, endpoint?.id ?? null, t)); } catch (reason) { setError(reason instanceof Error ? reason.message : t("未知错误")); } }}>
    {readOnly ? <p className="cp-notice">{t("默认接入点由环境端口定义，不可修改或删除。")}</p> : null}
    {error ? <p className="cp-form-error"><AlertTriangle size={13} aria-hidden /> {error}</p> : null}
    <ControlInput label={t("监听端口")} type="number" min={1} max={65535} value={form.port} disabled={readOnly} onChange={(event) => { set("port", event.target.value); setError(""); }} />
    <ControlSwitch label={t("启用") } isSelected={form.enabled} isDisabled={readOnly || pending} onChange={(value) => set("enabled", value)} />
    <div className="cp-inset-stack"><strong>{t("接入能力")}</strong><ControlSwitch label={t("登录管理页面")} isSelected={form.allow_management} isDisabled={readOnly || pending} onChange={(value) => set("allow_management", value)} /><ControlSwitch label={t("HTTP 正向代理")} isSelected={form.allow_http_forward} isDisabled={readOnly || pending} onChange={(value) => setProtocol("allow_http_forward", value)} /><ControlSwitch label={t("HTTP 反向代理")} isSelected={form.allow_http_reverse} isDisabled={readOnly || pending} onChange={(value) => setProtocol("allow_http_reverse", value)} /><ControlSwitch label={t("SOCKS5 代理")} isSelected={form.allow_socks5} isDisabled={readOnly || pending} onChange={(value) => setProtocol("allow_socks5", value)} /></div>
    <div className="cp-inset-stack"><strong>{t("认证策略")}</strong><ControlSwitch label={t("强制客户端认证")} hint={t(AUTH_HINT)} isSelected={form.require_proxy_auth_info} isDisabled={readOnly || pending || (!form.allow_http_forward && !form.allow_socks5)} onChange={(value) => set("require_proxy_auth_info", value)} /></div>
  </form>;
}

export function EndpointsPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(20);
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<Endpoint | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<EndpointConfirm>(null);
  const [togglePending, setTogglePending] = useState<string | null>(null);
  const query = useQuery({ queryKey: ["endpoints", page, pageSize], queryFn: () => listEndpoints({ limit: pageSize, offset: page * pageSize }), placeholderData: (previous) => previous, refetchInterval: 15_000 });
  const endpoints = query.data?.items ?? [];
  const total = query.data?.total ?? 0;
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const currentPage = Math.min(page, pages - 1);
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["endpoints"] });
  const createMutation = useMutation({ mutationFn: createEndpoint, onSuccess: (endpoint) => { setCreateOpen(false); invalidate(); showToast("success", t("接入点 :{{port}} 已创建", { port: endpoint.port })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const updateMutation = useMutation({ mutationFn: ({ id, input }: { id: string; input: EndpointInput }) => updateEndpoint(id, input), onSuccess: (endpoint) => { setEditing(null); invalidate(); showToast("success", t("接入点 :{{port}} 已更新", { port: endpoint.port })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const toggleMutation = useMutation({ mutationFn: ({ endpoint, enabled }: { endpoint: Endpoint; enabled: boolean }) => updateEndpoint(endpoint.id, { enabled }), onSuccess: (endpoint) => { setTogglePending(null); invalidate(); showToast("success", endpoint.enabled ? t("接入点 :{{port}} 已启用", { port: endpoint.port }) : t("接入点 :{{port}} 已禁用", { port: endpoint.port })); }, onError: (error) => { setTogglePending(null); showToast("error", formatApiErrorMessage(error, t)); } });
  const deleteMutation = useMutation({ mutationFn: (endpoint: Endpoint) => deleteEndpoint(endpoint.id), onSuccess: (_result, endpoint) => { setDeleteTarget(null); if (editing?.id === endpoint.id) setEditing(null); if (endpoints.length === 1 && page > 0) setPage(page - 1); invalidate(); showToast("success", t("接入点 :{{port}} 已删除", { port: endpoint.port })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });

  return <PageFrame eyebrow="Delivery / endpoints" title={t("接入点")} description={t("管理监听端口、管理页面入口和代理协议能力；环境定义的默认接入点保持只读。")} meta={<StatusMark tone={query.isError && !query.data ? "warn" : query.data ? "live" : "neutral"} label={query.isError && !query.data ? t("读取失败") : query.data ? t("配置已同步") : t("等待配置")} />} actions={<><ControlButton variant="secondary" size="sm" onPress={() => void query.refetch()} loading={query.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton><ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("新建接入点")}</ControlButton></>}>
    <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    <WorkSurface eyebrow="Delivery / listener registry" title={t("接入点目录")} meta={query.data ? t("共 {{count}} 个接入点", { count: total }) : t("未加载")} description={t("状态与能力由接入点接口返回；选择编辑打开配置抽屉。")}>
      {query.isError && !query.data ? <EmptyWorkState title={t("接入点读取失败")} detail={formatApiErrorMessage(query.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void query.refetch()}>{t("重试")}</ControlButton>} /> : null}
      {query.isLoading && !query.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载接入点")}</div> : null}
      {!query.isLoading && !query.isError && !endpoints.length ? <EmptyWorkState title={t("暂无接入点")} detail={t("创建一个接入点后，监听和代理能力会显示在这里。")} action={<ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("新建接入点")}</ControlButton>} /> : null}
      {endpoints.length ? <ObjectList>{endpoints.map((endpoint) => { const status = statusView(endpoint.status, t); const title = <span className="readout">:{endpoint.port}</span>; return <ObjectRow key={endpoint.id} leading={<span className="cp-platform-row__mark" aria-hidden>{endpoint.read_only ? <LockKeyhole size={13} aria-hidden /> : "IN"}</span>} title={title} subtitle={endpoint.read_only ? t("默认 · 只读") : endpoint.id} facts={<><span>{t("管理") } {endpoint.allow_management ? t("开启") : t("关闭")}</span><span>{t("HTTP") } {endpoint.allow_http_forward ? t("正向") : "-"} / {endpoint.allow_http_reverse ? t("反向") : "-"}</span><span>{t("SOCKS5") } {endpoint.allow_socks5 ? t("开启") : t("关闭")}</span></>} status={<StatusMark tone={status.tone} label={status.label} />} action={<div className="cp-drawer-actions"><ControlSwitch aria-label={endpoint.enabled ? t("禁用接入点") : t("启用接入点")} isSelected={endpoint.enabled} isDisabled={endpoint.read_only || togglePending === endpoint.id} onChange={(value) => { setTogglePending(endpoint.id); toggleMutation.mutate({ endpoint, enabled: value }); }} /><ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("编辑接入点")} isDisabled={endpoint.read_only} onPress={() => setEditing(endpoint)}><Pencil size={14} aria-hidden /></ControlButton><ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("删除接入点")} isDisabled={endpoint.read_only} onPress={() => setDeleteTarget(endpoint)}><Trash2 size={14} aria-hidden /></ControlButton></div>} />; })}</ObjectList> : null}
      {query.data ? <ControlPagination page={currentPage} totalItems={total} pageSize={pageSize} pageSizes={PAGE_SIZE_OPTIONS} disabled={query.isFetching} labels={{ summary: ({ page: current, pages: totalPages, start, end, total: count }) => t("第 {{page}} / {{pages}} 页 · {{start}}-{{end}} / {{total}}", { page: current, pages: totalPages, start, end, total: count }), perPage: t("每页"), pageOption: (size) => `${size} / ${t("页")}`, previous: t("上一页"), next: t("下一页") }} onPageChange={setPage} onPageSizeChange={(size) => { setPageSize(size); setPage(0); }} /> : null}
    </WorkSurface>
    <InspectorDrawer open={createOpen} onOpenChange={setCreateOpen} title={t("新建接入点")} eyebrow="Delivery / endpoint" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => setCreateOpen(false)}>{t("取消")}</ControlButton><ControlButton type="submit" form="endpoint-create-form" variant="primary" size="sm" loading={createMutation.isPending}>{t("确认创建")}</ControlButton></div>}><EndpointForm key={createOpen ? "create" : "closed"} endpoints={endpoints} formId="endpoint-create-form" pending={createMutation.isPending} onSubmit={(input) => createMutation.mutate(input)} /></InspectorDrawer>
    <InspectorDrawer open={Boolean(editing)} onOpenChange={(open) => { if (!open) setEditing(null); }} title={editing ? `:${editing.port}` : t("接入点配置")} eyebrow="Delivery / endpoint" footer={editing && !editing.read_only ? <div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => setEditing(null)}>{t("取消")}</ControlButton><ControlButton type="submit" form="endpoint-edit-form" variant="primary" size="sm" loading={updateMutation.isPending}>{t("保存配置")}</ControlButton></div> : undefined}>{editing ? <div className="cp-stack"><EndpointForm key={editing.id} endpoint={editing} endpoints={endpoints} formId="endpoint-edit-form" pending={updateMutation.isPending} onSubmit={(input) => updateMutation.mutate({ id: editing.id, input })}/><dl className="cp-detail-facts"><DetailFact label={t("接入点 ID")} value={editing.id} mono/><DetailFact label={t("来源")} value={editing.source === "environment" ? t("环境") : t("数据库")}/><DetailFact label={t("最近状态")} value={editing.last_error || t("未返回错误")} /></dl></div> : null}</InspectorDrawer>
    <ConfirmDrawer open={Boolean(deleteTarget)} onOpenChange={(open) => { if (!open) setDeleteTarget(null); }} title={t("确认删除接入点")} eyebrow="Destructive action" message={t("确认删除接入点 :{{port}}？该端口将立即停止监听。", { port: deleteTarget?.port ?? "" })} cancelLabel={t("取消")} confirmLabel={t("确认删除")} loading={deleteMutation.isPending} onConfirm={() => { if (deleteTarget) deleteMutation.mutate(deleteTarget); }}/>
  </PageFrame>;
}

export default EndpointsPage;
