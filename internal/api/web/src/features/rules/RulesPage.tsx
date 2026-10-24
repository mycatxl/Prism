import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bug, Pencil, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useState } from "react";
import {
  ConfirmDrawer,
  ControlButton,
  ControlInput,
  ControlTextarea,
  EmptyWorkState,
  FilterChip,
  InspectorDrawer,
  ObjectList,
  ObjectRow,
  PageFrame,
  DetailFact,
  QueryBadge,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime } from "../../lib/time";
import { deleteRule, listRules, resolveRule, upsertRule } from "./api";
import type { ResolveResult, Rule } from "./types";

const HEADERS_PREVIEW_LIMIT = 3;

function parseHeaders(raw: string): string[] {
  return raw.split(/[\n,]/).map((value) => value.trim()).filter(Boolean);
}

function previewHeaders(rule: Rule): string {
  const shown = rule.headers.slice(0, HEADERS_PREVIEW_LIMIT).join(", ");
  const extra = rule.headers.length - HEADERS_PREVIEW_LIMIT;
  return extra > 0 ? `${shown} +${extra}` : shown;
}

function isFallback(rule: Rule) {
  return rule.url_prefix === "*";
}

export function RulesPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [search, setSearch] = useState("");
  const settledSearch = useDebouncedValue(search);
  const [selected, setSelected] = useState<Rule | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [resolveOpen, setResolveOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Rule | null>(null);
  const [prefix, setPrefix] = useState("");
  const [headers, setHeaders] = useState("");
  const [createPrefix, setCreatePrefix] = useState("");
  const [createHeaders, setCreateHeaders] = useState("");
  const [resolveURL, setResolveURL] = useState("");
  const [resolveResult, setResolveResult] = useState<ResolveResult | null>(null);
  const query = useQuery({ queryKey: ["header-rules", settledSearch], queryFn: () => listRules(settledSearch), refetchInterval: 30_000 });
  const rules = query.data ?? [];
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["header-rules"] });
  const selectRule = (rule: Rule) => { setSelected(rule); setPrefix(rule.url_prefix); setHeaders(rule.headers.join("\n")); };
  const createMutation = useMutation({ mutationFn: () => { const nextPrefix = createPrefix.trim(); const nextHeaders = parseHeaders(createHeaders); if (!nextPrefix) throw new Error(t("地址前缀不能为空")); if (!nextHeaders.length) throw new Error(t("请求头不能为空")); return upsertRule(nextPrefix, nextHeaders); }, onSuccess: (rule) => { setCreateOpen(false); setCreatePrefix(""); setCreateHeaders(""); invalidate(); showToast("success", t("规则 {{prefix}} 已创建", { prefix: rule.url_prefix })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const updateMutation = useMutation({ mutationFn: () => { const nextPrefix = prefix.trim(); const nextHeaders = parseHeaders(headers); if (!nextPrefix) throw new Error(t("地址前缀不能为空")); if (!nextHeaders.length) throw new Error(t("请求头不能为空")); return upsertRule(nextPrefix, nextHeaders); }, onSuccess: (rule) => { setSelected(rule); setPrefix(rule.url_prefix); setHeaders(rule.headers.join("\n")); invalidate(); showToast("success", t("规则 {{prefix}} 已保存", { prefix: rule.url_prefix })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const resolveMutation = useMutation({ mutationFn: () => { if (!resolveURL.trim()) throw new Error(t("请输入 URL")); return resolveRule(resolveURL.trim()); }, onSuccess: setResolveResult, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const deleteMutation = useMutation({ mutationFn: (rule: Rule) => deleteRule(rule.url_prefix), onSuccess: (_result, rule) => { setDeleteTarget(null); if (selected?.url_prefix === rule.url_prefix) setSelected(null); invalidate(); showToast("success", t("规则 {{prefix}} 已删除", { prefix: rule.url_prefix })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const activeFilters = Boolean(search.trim());
  const clearFilters = () => setSearch("");

  return <PageFrame eyebrow="Delivery / header rules" title={t("请求头规则")} description={t("为匹配的 URL 注入请求头，并用解析测试确认实际命中结果。")} meta={<StatusMark tone={query.isError && !query.data ? "warn" : query.data ? "live" : "neutral"} label={query.isError && !query.data ? t("读取失败") : query.data ? t("规则已同步") : t("等待规则")} />} actions={<><ControlButton variant="secondary" size="sm" onPress={() => setResolveOpen(true)}><Bug size={14} aria-hidden />{t("规则测试")}</ControlButton><ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("新建规则")}</ControlButton></>}>
    <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    <section className="cp-command"><div className="cp-command__body"><ControlInput label={t("搜索规则")} value={search} onChange={(event) => setSearch(event.target.value)} placeholder={t("按 URL 前缀搜索")}/></div><div className="cp-command__actions"><Search size={14} aria-hidden />{activeFilters ? <ControlButton variant="ghost" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : null}<ControlButton variant="ghost" size="sm" onPress={() => void query.refetch()} loading={query.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton></div></section>
    {activeFilters ? <div className="cp-active-filters"><FilterChip label={`${t("关键词")}: ${search.trim()}`} onRemove={clearFilters}/></div> : null}
    <WorkSurface eyebrow="Delivery / request headers" title={t("规则目录")} meta={query.data ? t("共 {{count}} 条规则", { count: rules.length }) : t("未加载")} description={t("兜底规则可以编辑但不能删除；选择规则打开编辑和运维抽屉。")}>
      {query.isError && !query.data ? <EmptyWorkState title={t("规则读取失败")} detail={formatApiErrorMessage(query.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void query.refetch()}>{t("重试")}</ControlButton>} /> : null}
      {query.isLoading && !query.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载规则")}</div> : null}
      {!query.isLoading && !query.isError && !rules.length ? <EmptyWorkState title={t("没有匹配规则")} detail={t("创建第一条规则后，命中的请求头会显示在这里。")} action={<ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("新建规则")}</ControlButton>} /> : null}
      {rules.length ? <ObjectList>{rules.map((rule) => <ObjectRow key={rule.url_prefix} active={selected?.url_prefix === rule.url_prefix} leading={<span className="cp-platform-row__mark" aria-hidden>{isFallback(rule) ? "*" : "HDR"}</span>} title={rule.url_prefix} subtitle={`${formatDateTime(rule.updated_at)} · ${rule.headers.length} ${t("个请求头")}`} facts={<span className="readout">{previewHeaders(rule) || t("无请求头")}</span>} status={isFallback(rule) ? <QueryBadge label={t("兜底")} /> : <StatusMark tone="signal" label={t("已配置")} />} action={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("编辑")} onPress={() => selectRule(rule)}><Pencil size={14} aria-hidden /></ControlButton><ControlButton variant="ghost" size="sm" isIconOnly aria-label={isFallback(rule) ? t('兜底规则 "*" 不可删除') : t("删除")} isDisabled={isFallback(rule)} onPress={() => setDeleteTarget(rule)}><Trash2 size={14} aria-hidden /></ControlButton></div>} />)}</ObjectList> : null}
    </WorkSurface>
    <InspectorDrawer open={Boolean(selected)} onOpenChange={(open) => { if (!open) setSelected(null); }} title={selected?.url_prefix ?? t("规则编辑")} eyebrow="Delivery / header rule" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => setSelected(null)}>{t("关闭")}</ControlButton><ControlButton type="submit" form="edit-rule-form" variant="primary" size="sm" loading={updateMutation.isPending}>{t("保存规则")}</ControlButton></div>}>
      {selected ? <div className="cp-stack"><ControlInput label={t("地址前缀")} value={prefix} disabled hint={t("已有规则的地址前缀不可直接改名。")}/><form id="edit-rule-form" className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); updateMutation.mutate(); }}><ControlTextarea label={t("请求头")} hint={t("每行一个 header，例如 Authorization")} rows={8} value={headers} onChange={(event) => setHeaders(event.target.value)}/>{updateMutation.error ? <p className="cp-form-error">{formatApiErrorMessage(updateMutation.error, t)}</p> : null}</form>{selected ? <div className="cp-danger-list"><div className="cp-danger-row cp-danger-row--alert"><div><strong>{t("删除规则")}</strong><p>{isFallback(selected) ? t('兜底规则 "*" 仅允许编辑，不允许删除。') : t("删除后该规则将不再生效。")}</p></div><ControlButton variant="danger" size="sm" isDisabled={isFallback(selected)} onPress={() => setDeleteTarget(selected)}><Trash2 size={14} aria-hidden />{t("删除")}</ControlButton></div></div> : null}</div> : null}
    </InspectorDrawer>
    <InspectorDrawer open={createOpen} onOpenChange={setCreateOpen} title={t("新建规则")} eyebrow="Delivery / header rule" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => setCreateOpen(false)}>{t("取消")}</ControlButton><ControlButton type="submit" form="create-rule-form" variant="primary" size="sm" loading={createMutation.isPending}>{t("确认创建")}</ControlButton></div>}><form id="create-rule-form" className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); createMutation.mutate(); }}><ControlInput label={t("地址前缀")} value={createPrefix} onChange={(event) => setCreatePrefix(event.target.value)} placeholder={t("例如 api.example.com/v1")}/><ControlTextarea label={t("请求头")} rows={8} value={createHeaders} onChange={(event) => setCreateHeaders(event.target.value)} placeholder={t("每行一个 header，例如 Authorization")}/>{createMutation.error ? <p className="cp-form-error">{formatApiErrorMessage(createMutation.error, t)}</p> : null}</form></InspectorDrawer>
    <InspectorDrawer open={resolveOpen} onOpenChange={(open) => { setResolveOpen(open); if (!open) setResolveResult(null); }} title={t("规则测试")} eyebrow="Delivery / resolution" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => setResolveOpen(false)}>{t("关闭")}</ControlButton><ControlButton variant="primary" size="sm" onPress={() => resolveMutation.mutate()} loading={resolveMutation.isPending}>{t("开始测试")}</ControlButton></div>}><div className="cp-form-stack"><ControlInput label={t("目标地址")} value={resolveURL} onChange={(event) => setResolveURL(event.target.value)} placeholder="https://api.example.com/v1/orders/123"/>{resolveResult ? <WorkSurface eyebrow="Resolution result" title={t("命中结果")}><dl className="cp-detail-facts"><DetailFact label={t("命中前缀")} value={resolveResult.matched_url_prefix || t("无")} mono/><DetailFact label={t("命中请求头")} value={resolveResult.headers?.length ? resolveResult.headers.join(", ") : t("无")} /></dl></WorkSurface> : null}</div></InspectorDrawer>
    <ConfirmDrawer open={Boolean(deleteTarget)} onOpenChange={(open) => { if (!open) setDeleteTarget(null); }} title={t("确认删除规则")} eyebrow="Destructive action" message={t("确认删除规则 {{prefix}} 吗？", { prefix: deleteTarget?.url_prefix ?? "" })} cancelLabel={t("取消")} confirmLabel={t("确认删除")} loading={deleteMutation.isPending} onConfirm={() => { if (deleteTarget) deleteMutation.mutate(deleteTarget); }}/>
  </PageFrame>;
}

export default RulesPage;
