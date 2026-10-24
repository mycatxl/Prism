import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Check, Copy, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import {
  ConfirmDrawer,
  ControlButton,
  ControlInput,
  ControlPagination,
  ControlSelect,
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
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { copyToClipboard } from "../../lib/clipboard";
import { formatApiErrorMessage } from "../../lib/error-message";
import { adminPlansQueryKey, createInvite, deleteInvite, listInvites, listPlans } from "./api";
import type { AdminInvite, AdminPlan, CreateInviteInput } from "./types";

const PAGE_SIZE = 20;
const NANOS_PER_MILLI = 1_000_000;
type InviteState = "usable" | "exhausted" | "expired" | "disabled";
type InviteFilter = "all" | InviteState;

function formatNsTime(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
}

function classifyInvite(invite: AdminInvite, nowNs: number): InviteState {
  if (invite.expires_at_ns > 0 && nowNs > invite.expires_at_ns) return "expired";
  if (invite.max_uses > 0 && invite.used_count >= invite.max_uses) return "exhausted";
  if (invite.status === "disabled") return "disabled";
  return "usable";
}

export function InvitesPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [page, setPage] = useState(0);
  const [search, setSearch] = useState("");
  const [stateFilter, setStateFilter] = useState<InviteFilter>("all");
  const [createOpen, setCreateOpen] = useState(false);
  const [selected, setSelected] = useState<AdminInvite | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AdminInvite | null>(null);
  const [copiedId, setCopiedId] = useState<string | null>(null);
  const [nowNs] = useState(() => Date.now() * NANOS_PER_MILLI);
  const [formPending, setFormPending] = useState(false);

  const invitesQuery = useQuery({
    queryKey: ["admin-invites", page * PAGE_SIZE],
    queryFn: () => listInvites({ limit: PAGE_SIZE, offset: page * PAGE_SIZE }),
    placeholderData: (previous) => previous,
  });
  const plansQuery = useQuery({ queryKey: adminPlansQueryKey, queryFn: listPlans, staleTime: 60_000 });
  const invites = useMemo(() => invitesQuery.data?.items ?? [], [invitesQuery.data]);
  const total = invitesQuery.data?.total ?? 0;
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return invites.filter((invite) => {
      if (stateFilter !== "all" && classifyInvite(invite, nowNs) !== stateFilter) return false;
      if (!needle) return true;
      return [invite.code, invite.plan_name, invite.note].join(" ").toLowerCase().includes(needle);
    });
  }, [invites, nowNs, search, stateFilter]);
  const filtersActive = search.trim() !== "" || stateFilter !== "all";
  const clearFilters = () => { setSearch(""); setStateFilter("all"); setPage(0); };
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["admin-invites"] });
    void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });
  };
  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteInvite(id),
    onSuccess: () => {
      showToast("success", t("邀请码已删除"));
      setDeleteTarget(null);
      setSelected(null);
      void queryClient.invalidateQueries({ queryKey: ["admin-invites"] });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const handleCopy = (code: string, id: string) => {
    void copyToClipboard(code).then((ok) => {
      if (!ok) {
        showToast("error", t("复制失败，请手动选择复制"));
        return;
      }
      setCopiedId(id);
      showToast("success", t("邀请码已复制到剪贴板"));
      window.setTimeout(() => setCopiedId(null), 2000);
    });
  };
  const statusTone = invitesQuery.isError && !invitesQuery.data ? "warn" : invitesQuery.data ? "live" : "neutral";

  return (
    <PageFrame
      eyebrow="Customers / invitation registry"
      title={t("邀请码")}
      description={t("创建与分发新用户注册邀请码，可绑定默认套餐和有效次数。")}
      meta={<StatusMark tone={statusTone} label={invitesQuery.isError && !invitesQuery.data ? t("读取失败") : invitesQuery.data ? t("目录已同步") : t("等待目录")} />}
      actions={<><ControlButton variant="secondary" size="sm" onPress={refresh} loading={invitesQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton><ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("创建邀请码")}</ControlButton></>}
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <section className="cp-command">
        <div className="cp-command__body">
          <ControlInput label={t("搜索邀请码")} value={search} onChange={(event) => { setSearch(event.target.value); setPage(0); }} placeholder={t("按邀请码、套餐或备注搜索")} />
          <ControlSelect label={t("状态")} value={stateFilter} onChange={(event) => { setStateFilter(event.target.value as InviteFilter); setPage(0); }}>
            <option value="all">{t("全部状态")}</option><option value="usable">{t("有效")}</option><option value="exhausted">{t("已用尽")}</option><option value="expired">{t("已过期")}</option><option value="disabled">{t("已停用")}</option>
          </ControlSelect>
        </div>
        <div className="cp-command__actions"><Search size={14} aria-hidden />{filtersActive ? <ControlButton variant="ghost" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : null}</div>
      </section>
      {filtersActive ? <div className="cp-active-filters" aria-label={t("当前筛选")}>
        {search.trim() ? <FilterChip label={`${t("关键词")}: ${search.trim()}`} onRemove={() => { setSearch(""); setPage(0); }} /> : null}
        {stateFilter !== "all" ? <FilterChip label={`${t("状态")}: ${stateFilter === "usable" ? t("有效") : stateFilter === "exhausted" ? t("已用尽") : stateFilter === "expired" ? t("已过期") : t("已停用")}`} onRemove={() => { setStateFilter("all"); setPage(0); }} /> : null}
      </div> : null}

      <WorkSurface eyebrow="Customer objects" title={t("邀请码目录")} meta={invitesQuery.data ? t("共 {{count}} 个邀请码", { count: total }) : t("未加载")} description={t("邀请码明文可以复制，但状态、绑定套餐和使用次数只来自服务端记录。") }>
        {invitesQuery.isError && !invitesQuery.data ? <EmptyWorkState title={t("邀请码目录读取失败")} detail={formatApiErrorMessage(invitesQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void invitesQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
        {invitesQuery.isLoading && !invitesQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载邀请码列表")}</div> : null}
        {!invitesQuery.isLoading && !invitesQuery.isError && invites.length === 0 ? <EmptyWorkState title={t("暂无邀请码")} detail={t("尚未创建任何注册邀请码，点击右上角创建。") } action={<ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("创建邀请码")}</ControlButton>} /> : null}
        {!invitesQuery.isLoading && !invitesQuery.isError && invites.length > 0 && filtered.length === 0 ? <EmptyWorkState title={t("没有匹配的邀请码")} detail={t("调整搜索或状态筛选，或清除筛选查看全部。") } action={<ControlButton variant="secondary" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton>} /> : null}
        {filtered.length > 0 ? <ObjectList>
          {filtered.map((invite) => {
            const state = classifyInvite(invite, nowNs);
            const stateLabel = state === "usable" ? t("有效") : state === "exhausted" ? t("已用尽") : state === "expired" ? t("已过期") : t("已停用");
            return <ObjectRow
              key={invite.id}
              active={selected?.id === invite.id}
              onClick={() => setSelected(invite)}
              leading={<span className="cp-platform-row__mark" aria-hidden>↗</span>}
              title={invite.code}
              subtitle={`${invite.plan_name || t("未绑定套餐")} · ${invite.used_count} / ${invite.max_uses > 0 ? invite.max_uses : t("不限")}`}
              facts={<><span>{t("状态")} {stateLabel}</span><span>{t("到期")} {invite.expires_at_ns > 0 ? formatNsTime(invite.expires_at_ns) : t("永不过期")}</span><span>{t("创建")} {formatNsTime(invite.created_at_ns)}</span></>}
              status={<StatusMark tone={state === "usable" ? "signal" : state === "exhausted" ? "warn" : "alert"} label={stateLabel} />}
              action={<ArrowRight size={14} aria-hidden />}
            />;
          })}
        </ObjectList> : null}
        {invitesQuery.data ? <ControlPagination
          page={page}
          totalItems={total}
          pageSize={PAGE_SIZE}
          pageSizes={[PAGE_SIZE]}
          disabled={invitesQuery.isFetching}
          labels={{ summary: ({ page: current, pages, start, end, total: count }) => t("第 {{page}} / {{pages}} 页 · {{start}}-{{end}} / {{total}}", { page: current, pages, start, end, total: count }), perPage: t("每页"), pageOption: (size) => `${size} / ${t("页")}`, previous: t("上一页"), next: t("下一页") }}
          onPageChange={setPage}
          onPageSizeChange={() => undefined}
        /> : null}
      </WorkSurface>

      <InspectorDrawer
        open={Boolean(selected) && !createOpen}
        onOpenChange={(open) => { if (!open) setSelected(null); }}
        title={selected?.code ?? t("邀请码")}
        eyebrow={t("Customer / invite inspector")}
        footer={selected ? <><ControlButton variant="ghost" size="sm" onPress={() => setSelected(null)}>{t("关闭")}</ControlButton><ControlButton variant="secondary" size="sm" onPress={() => handleCopy(selected.code, selected.id)}>{copiedId === selected.id ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}{copiedId === selected.id ? t("已复制") : t("复制邀请码")}</ControlButton><ControlButton variant="danger" size="sm" onPress={() => setDeleteTarget(selected)}><Trash2 size={14} aria-hidden />{t("删除")}</ControlButton></> : undefined}
      >
        {selected ? <div className="cp-stack">
          <div className="cp-platform-inspector__identity"><span className="cp-platform-row__mark" aria-hidden>↗</span><div><strong className="mono">{selected.code}</strong><code>{selected.id}</code></div><StatusMark tone={classifyInvite(selected, nowNs) === "usable" ? "signal" : "alert"} label={classifyInvite(selected, nowNs) === "usable" ? t("有效") : t("不可用")} /></div>
          <dl className="cp-detail-facts"><DetailFact label={t("绑定套餐")} value={selected.plan_name || t("未绑定套餐")} /><DetailFact label={t("使用次数")} value={`${selected.used_count} / ${selected.max_uses > 0 ? selected.max_uses : t("不限")}`} mono /><DetailFact label={t("到期时间")} value={selected.expires_at_ns > 0 ? formatNsTime(selected.expires_at_ns) : t("永不过期")} mono /><DetailFact label={t("创建时间")} value={formatNsTime(selected.created_at_ns)} mono /><DetailFact label={t("备注")} value={selected.note || "-"} /></dl>
          <QueryBadge label={t("邀请码明文可由管理员复制分发") } />
        </div> : null}
      </InspectorDrawer>

      <InspectorDrawer
        open={createOpen}
        onOpenChange={(open) => { if (!open && !formPending) setCreateOpen(false); }}
        title={t("创建注册邀请码")}
        eyebrow={t("Administrator action")}
        footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => setCreateOpen(false)} isDisabled={formPending}>{t("取消")}</ControlButton><ControlButton type="submit" form="invite-form" variant="primary" size="sm" isDisabled={formPending} loading={formPending}>{t("生成邀请码")}</ControlButton></div>}
      >
        {createOpen ? <CreateInviteContent plans={plansQuery.data?.items ?? []} onPendingChange={setFormPending} onSuccess={() => { showToast("success", t("邀请码创建成功")); setCreateOpen(false); void queryClient.invalidateQueries({ queryKey: ["admin-invites"] }); }} /> : null}
      </InspectorDrawer>

      <ConfirmDrawer
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}
        title={t("确认删除邀请码")}
        eyebrow={t("Destructive action")}
        message={t("确定要删除邀请码 {{code}} 吗？删除后该邀请码将无法再被用于注册。", { code: deleteTarget?.code ?? "" })}
        cancelLabel={t("取消")}
        confirmLabel={t("确认删除")}
        loading={deleteMutation.isPending}
        onConfirm={() => { if (deleteTarget) deleteMutation.mutate(deleteTarget.id); }}
      />
    </PageFrame>
  );
}

function CreateInviteContent({ plans, onPendingChange, onSuccess }: { plans: AdminPlan[]; onPendingChange: (pending: boolean) => void; onSuccess: () => void }) {
  const { t } = useI18n();
  const [planId, setPlanId] = useState("");
  const [maxUses, setMaxUses] = useState("1");
  const [expiresInDays, setExpiresInDays] = useState("7");
  const [note, setNote] = useState("");
  const { toasts, showToast, dismissToast } = useToast();
  const activePlans = plans.filter((plan) => plan.status === "active");
  const createMutation = useMutation({
    mutationFn: () => {
      const payload: CreateInviteInput = {
        plan_id: planId.trim() || undefined,
        max_uses: Math.max(0, Number(maxUses) || 0),
        expires_in_days: expiresInDays.trim() !== "" ? Math.max(0, Number(expiresInDays) || 0) : undefined,
        note: note.trim() || undefined,
      };
      return createInvite(payload);
    },
    onMutate: () => onPendingChange(true),
    onSuccess,
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
    onSettled: () => onPendingChange(false),
  });
  return <>
    <form id="invite-form" className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); createMutation.mutate(); }}>
      <ControlSelect label={t("绑定套餐 (可选)")} hint={t("用户使用此邀请码注册后将自动获得该套餐")} value={planId} onChange={(event) => setPlanId(event.target.value)}><option value="">{t("暂不绑定套餐")}</option>{activePlans.map((plan) => <option key={plan.id} value={plan.id}>{plan.name}</option>)}</ControlSelect>
      <ControlInput label={t("最大可用次数")} hint={t("填 0 为不限制使用次数")} type="number" min="0" value={maxUses} onChange={(event) => setMaxUses(event.target.value)} />
      <ControlInput label={t("有效天数 (可选)")} hint={t("留空或填 0 表示永不过期")} type="number" min="0" value={expiresInDays} onChange={(event) => setExpiresInDays(event.target.value)} placeholder={t("例如: 7")} />
      <ControlInput label={t("备注信息 (可选)")} hint={t("仅管理员可见，便于追踪邀请来源")} value={note} onChange={(event) => setNote(event.target.value)} placeholder={t("例如: 发给合作团队测试")} />
      {createMutation.isError ? <p className="cp-form-error">{formatApiErrorMessage(createMutation.error, t)}</p> : null}
    </form>
    <ToastContainer toasts={toasts} onDismiss={dismissToast} />
  </>;
}
