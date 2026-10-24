import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Pencil, Plus, Power, RefreshCw, Search, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  ConfirmDrawer,
  ControlButton,
  ControlInput,
  ControlSelect,
  ControlSwitch,
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
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro, formatMicroCompact, parseAmountToMicro } from "../../lib/money";
import { adminPlansQueryKey, createPlan, deletePlan, listPlans, updatePlan } from "./api";
import { BITS_PER_MBPS, BYTES_PER_GB, exactUnitsText, unitsToBase } from "./plan-units";
import type { AdminPlan, PlanPatch, PlanStatus, PlanWrite } from "./types";

const PERIOD_OPTIONS = ["month", "week", "day", "30d", "90d", "365d", "once"] as const;
type StatusFilter = "all" | PlanStatus;

function periodLabel(period: string, t: (text: string, values?: Record<string, unknown>) => string): string {
  switch (period) {
    case "month": return t("按月");
    case "week": return t("按周");
    case "day": return t("按天");
    case "30d": return t("30 天");
    case "90d": return t("90 天");
    case "365d": return t("365 天");
    case "once": return t("不重置");
    default: return period;
  }
}

function trafficReadout(bytes: number): ReactNode {
  const [figure, unit] = formatBytes(bytes).split(" ");
  return <><span>{figure}</span><small>{unit}</small></>;
}

export function PlansPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [formOpen, setFormOpen] = useState(false);
  const [editingPlan, setEditingPlan] = useState<AdminPlan | null>(null);
  const [selected, setSelected] = useState<AdminPlan | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AdminPlan | null>(null);
  const [delistTarget, setDelistTarget] = useState<AdminPlan | null>(null);
  const [formPending, setFormPending] = useState(false);

  const plansQuery = useQuery({ queryKey: adminPlansQueryKey, queryFn: listPlans, placeholderData: (previous) => previous });
  const plans = useMemo(() => plansQuery.data?.items ?? [], [plansQuery.data]);
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return plans.filter((plan) => {
      if (statusFilter !== "all" && plan.status !== statusFilter) return false;
      if (!needle) return true;
      return [plan.name, ...(plan.platforms ?? []), ...(plan.ip_tiers ?? [])].join(" ").toLowerCase().includes(needle);
    });
  }, [plans, search, statusFilter]);
  const filtersActive = search.trim() !== "" || statusFilter !== "all";
  const clearFilters = () => {
    setSearch("");
    setStatusFilter("all");
  };
  const refresh = () => void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deletePlan(id),
    onSuccess: () => {
      showToast("success", t("套餐已删除"));
      setDeleteTarget(null);
      setSelected(null);
      void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const statusMutation = useMutation({
    mutationFn: ({ id, status }: { id: string; status: PlanStatus }) => updatePlan(id, { status }),
    onSuccess: (updated, variables) => {
      showToast("success", t(variables.status === "active" ? "套餐已上架" : "套餐已下架"));
      setDelistTarget(null);
      if (selected?.id === updated.id) setSelected(updated);
      void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const openCreate = () => {
    setEditingPlan(null);
    setFormOpen(true);
  };
  const openEdit = (plan: AdminPlan) => {
    setEditingPlan(plan);
    setFormOpen(true);
    setSelected(null);
  };
  const statusTone = plansQuery.isError && !plansQuery.data ? "warn" : plansQuery.data ? "live" : "neutral";

  return (
    <PageFrame
      eyebrow="Customers / plan catalogue"
      title={t("套餐")}
      description={t("管理订阅套餐规则、流量配额与并发限制。套餐所有权属于管理员，租户只能购买可售目录中的套餐。")}
      meta={<StatusMark tone={statusTone} label={plansQuery.isError && !plansQuery.data ? t("读取失败") : plansQuery.data ? t("目录已同步") : t("等待目录")} />}
      actions={
        <>
          <ControlButton variant="secondary" size="sm" onPress={refresh} loading={plansQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>
          <ControlButton variant="primary" size="sm" onPress={openCreate}><Plus size={14} aria-hidden />{t("新建套餐")}</ControlButton>
        </>
      }
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <section className="cp-command">
        <div className="cp-command__body">
          <ControlInput label={t("搜索套餐")} value={search} onChange={(event) => setSearch(event.target.value)} placeholder={t("按名称、平台或网络层级搜索")} />
          <ControlSelect label={t("状态")} value={statusFilter} onChange={(event) => setStatusFilter(event.target.value as StatusFilter)}>
            <option value="all">{t("全部状态")}</option>
            <option value="active">{t("上架")}</option>
            <option value="disabled">{t("下架")}</option>
          </ControlSelect>
        </div>
        <div className="cp-command__actions">
          <Search size={14} aria-hidden />
          {filtersActive ? <ControlButton variant="ghost" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : null}
        </div>
      </section>
      {filtersActive ? <div className="cp-active-filters" aria-label={t("当前筛选")}>
        {search.trim() ? <FilterChip label={`${t("关键词")}: ${search.trim()}`} onRemove={() => setSearch("")} /> : null}
        {statusFilter !== "all" ? <FilterChip label={`${t("状态")}: ${statusFilter === "active" ? t("上架") : t("下架")}`} onRemove={() => setStatusFilter("all")} /> : null}
      </div> : null}

      <WorkSurface
        eyebrow="Customer objects"
        title={t("套餐目录")}
        meta={plansQuery.data ? t("显示 {{shown}} / {{total}} 个套餐", { shown: filtered.length, total: plans.length }) : t("未加载")}
        description={t("价格、配额、周期、平台关系和活跃订阅数来自管理员套餐 API；选择一行查看编辑与上架动作。")}
      >
        {plansQuery.isError && !plansQuery.data ? <EmptyWorkState title={t("套餐目录读取失败")} detail={formatApiErrorMessage(plansQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void plansQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
        {plansQuery.isLoading && !plansQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载套餐列表")}</div> : null}
        {!plansQuery.isLoading && !plansQuery.isError && filtered.length === 0 ? <EmptyWorkState title={t(filtersActive ? "没有匹配的套餐" : "暂无套餐")} detail={t(filtersActive ? "调整搜索或状态筛选后重试。" : "创建第一个订阅套餐后，可在用户和邀请码流程中使用。")} action={filtersActive ? <ControlButton variant="secondary" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : <ControlButton variant="primary" size="sm" onPress={openCreate}><Plus size={14} aria-hidden />{t("新建套餐")}</ControlButton>} /> : null}
        {filtered.length > 0 ? <ObjectList>
          {filtered.map((plan) => {
            const active = plan.status === "active";
            const platformText = plan.platforms?.length ? plan.platforms.join(", ") : t("全部平台");
            return <ObjectRow
              key={plan.id}
              active={selected?.id === plan.id}
              onClick={() => setSelected(plan)}
              leading={<span className="cp-platform-row__mark" aria-hidden>{plan.name.slice(0, 1).toUpperCase()}</span>}
              title={plan.name}
              subtitle={`${active ? t("上架") : t("下架")} · ${t("{{count}} 个活跃用户", { count: plan.active_user_count })}`}
              facts={<><span>{t("价格")} {plan.price_micro > 0 ? <span className="readout">{formatMicro(plan.price_micro)}<small>USDT</small></span> : t("非卖品")}</span><span>{t("流量")} {plan.traffic_bytes > 0 ? <span className="readout">{trafficReadout(plan.traffic_bytes)}</span> : t("不限")}</span><span>{t("周期")} {periodLabel(plan.period || "month", t)}</span><span>{t("平台")} {platformText}</span></>}
              status={<StatusMark tone={active ? "signal" : "alert"} label={active ? t("上架") : t("下架")} />}
              action={<ArrowRight size={14} aria-hidden />}
            />;
          })}
        </ObjectList> : null}
      </WorkSurface>

      <InspectorDrawer
        open={Boolean(selected) && !formOpen}
        onOpenChange={(open) => { if (!open) setSelected(null); }}
        title={selected?.name ?? t("套餐")}
        eyebrow={t("Customer / plan inspector")}
        footer={selected ? <>
          <ControlButton variant="ghost" size="sm" onPress={() => setSelected(null)}>{t("关闭")}</ControlButton>
          <ControlButton variant="secondary" size="sm" onPress={() => openEdit(selected)}><Pencil size={14} aria-hidden />{t("编辑")}</ControlButton>
          <ControlButton
            variant="secondary"
            size="sm"
            isDisabled={statusMutation.isPending}
            onPress={() => selected.status === "active" ? setDelistTarget(selected) : statusMutation.mutate({ id: selected.id, status: "active" })}
            loading={statusMutation.isPending && statusMutation.variables?.id === selected.id}
          >
            <Power size={14} aria-hidden />{selected.status === "active" ? t("下架") : t("上架")}
          </ControlButton>
          <ControlButton variant="danger" size="sm" onPress={() => setDeleteTarget(selected)}><Trash2 size={14} aria-hidden />{t("删除")}</ControlButton>
        </> : undefined}
      >
        {selected ? <div className="cp-stack">
          <div className="cp-platform-inspector__identity"><span className="cp-platform-row__mark" aria-hidden>{selected.name.slice(0, 1).toUpperCase()}</span><div><strong>{selected.name}</strong><code>{selected.id}</code></div><StatusMark tone={selected.status === "active" ? "signal" : "alert"} label={selected.status === "active" ? t("上架") : t("下架")} /></div>
          <dl className="cp-detail-facts">
            <DetailFact label={t("价格")} value={selected.price_micro > 0 ? <span className="readout">{formatMicro(selected.price_micro)}<small>USDT</small></span> : t("非卖品")} mono />
            <DetailFact label={t("流量配额")} value={selected.traffic_bytes > 0 ? <span className="readout">{trafficReadout(selected.traffic_bytes)}</span> : t("不限")} mono />
            <DetailFact label={t("重置周期")} value={periodLabel(selected.period || "month", t)} />
            <DetailFact label={t("有效天数")} value={selected.duration_days > 0 ? `${selected.duration_days} ${t("天")}` : t("永久有效")} mono />
            <DetailFact label={t("活跃订阅")} value={selected.active_user_count} mono />
            <DetailFact label={t("最大并发")} value={selected.max_concurrent || t("不限")} mono />
            <DetailFact label={t("最大 RPS")} value={selected.max_rps || t("不限")} mono />
            <DetailFact label={t("最大密钥数")} value={selected.max_keys || t("不限")} mono />
          </dl>
          <WorkSurface eyebrow="Routing" title={t("适用范围")}>
            <dl className="cp-detail-facts"><DetailFact label={t("平台")} value={selected.platforms?.length ? selected.platforms.join(", ") : t("全部平台")} /><DetailFact label={t("IP 类型与纯净度")} value={selected.ip_tiers?.length ? selected.ip_tiers.join(", ") : t("不限制")} /></dl>
          </WorkSurface>
          <QueryBadge label={t("创建于 {{time}}", { time: new Date(Math.round(selected.created_at_ns / 1_000_000)).toLocaleString() })} />
        </div> : null}
      </InspectorDrawer>

      <InspectorDrawer
        open={formOpen}
        onOpenChange={(open) => { if (!open && !formPending) { setFormOpen(false); setEditingPlan(null); } }}
        title={editingPlan ? t("编辑套餐") : t("新建套餐")}
        eyebrow={t("Administrator action")}
        footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => { setFormOpen(false); setEditingPlan(null); }} isDisabled={formPending}>{t("取消")}</ControlButton><ControlButton type="submit" form="plan-form" variant="primary" size="sm" isDisabled={formPending} loading={formPending}>{editingPlan ? t("保存修改") : t("立即创建")}</ControlButton></div>}
      >
        {formOpen ? <PlanFormContent initialPlan={editingPlan} onPendingChange={setFormPending} onSuccess={() => { showToast("success", editingPlan ? t("套餐已更新") : t("套餐创建成功")); setFormOpen(false); setEditingPlan(null); void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey }); }} /> : null}
      </InspectorDrawer>

      <ConfirmDrawer
        open={Boolean(delistTarget)}
        onOpenChange={(open) => { if (!open) setDelistTarget(null); }}
        title={t("确认下架")}
        eyebrow={t("Destructive action")}
        message={t("下架会立即停止该套餐现有用户的订阅，重新上架也不会自动恢复。确定要下架 {{name}} 吗？", { name: delistTarget?.name ?? "" })}
        cancelLabel={t("取消")}
        confirmLabel={t("确认下架")}
        loading={statusMutation.isPending}
        onConfirm={() => { if (delistTarget) statusMutation.mutate({ id: delistTarget.id, status: "disabled" }); }}
      />
      <ConfirmDrawer
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}
        title={t("确认删除套餐")}
        eyebrow={t("Destructive action")}
        message={t("确定要删除套餐 {{name}} 吗？若有用户正在使用该套餐，此操作将被服务器拒绝。", { name: deleteTarget?.name ?? "" })}
        cancelLabel={t("取消")}
        confirmLabel={t("确认删除")}
        loading={deleteMutation.isPending}
        onConfirm={() => { if (deleteTarget) deleteMutation.mutate(deleteTarget.id); }}
      />
    </PageFrame>
  );
}

function PlanFormContent({ initialPlan, onPendingChange, onSuccess }: { initialPlan: AdminPlan | null; onPendingChange: (pending: boolean) => void; onSuccess: () => void }) {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const [name, setName] = useState(initialPlan?.name ?? "");
  const [platformsRaw, setPlatformsRaw] = useState(initialPlan?.platforms?.join(", ") ?? "");
  const [ipTiersRaw, setIpTiersRaw] = useState(initialPlan?.ip_tiers?.join(", ") ?? "");
  const [trafficGB, setTrafficGB] = useState(() => exactUnitsText(initialPlan?.traffic_bytes ?? 0, BYTES_PER_GB));
  const [trafficBytesExact, setTrafficBytesExact] = useState<number | null>(initialPlan ? initialPlan.traffic_bytes : null);
  const [period, setPeriod] = useState(initialPlan?.period || "month");
  const periodOptions = (PERIOD_OPTIONS as readonly string[]).includes(period) ? [...PERIOD_OPTIONS] : [period, ...PERIOD_OPTIONS];
  const [maxConcurrent, setMaxConcurrent] = useState(String(initialPlan?.max_concurrent ?? 0));
  const [maxRps, setMaxRps] = useState(String(initialPlan?.max_rps ?? 0));
  const [maxBandwidthMbps, setMaxBandwidthMbps] = useState(() => exactUnitsText(initialPlan?.max_bandwidth_bps ?? 0, BITS_PER_MBPS));
  const [bandwidthBpsExact, setBandwidthBpsExact] = useState<number | null>(initialPlan ? initialPlan.max_bandwidth_bps : null);
  const [maxKeys, setMaxKeys] = useState(String(initialPlan?.max_keys ?? 5));
  const [durationDays, setDurationDays] = useState(String(initialPlan?.duration_days ?? 30));
  const [price, setPrice] = useState(initialPlan && initialPlan.price_micro > 0 ? formatMicroCompact(initialPlan.price_micro) : "");
  const [status, setStatus] = useState<PlanStatus>(initialPlan?.status || "active");
  const [applyToExisting, setApplyToExisting] = useState(false);

  const saveMutation = useMutation({
    mutationFn: async () => {
      const trimmedName = name.trim();
      if (!trimmedName) throw new Error(t("套餐名称不能为空"));
      const platforms = platformsRaw.split(",").map((value) => value.trim()).filter(Boolean);
      const ipTiers = ipTiersRaw.split(",").map((value) => value.trim()).filter(Boolean);
      const priceMicro = price.trim() === "" ? 0 : parseAmountToMicro(price);
      if (priceMicro === null) throw new Error(t("价格最多六位小数，留空表示不对外出售。"));
      const payload: PlanWrite = {
        name: trimmedName,
        platforms,
        ip_tiers: ipTiers,
        traffic_bytes: trafficBytesExact ?? unitsToBase(trafficGB, BYTES_PER_GB),
        period: period.trim() || "month",
        max_concurrent: Math.max(0, Number(maxConcurrent) || 0),
        max_rps: Math.max(0, Number(maxRps) || 0),
        max_bandwidth_bps: bandwidthBpsExact ?? unitsToBase(maxBandwidthMbps, BITS_PER_MBPS),
        max_keys: Math.max(0, Number(maxKeys) || 0),
        duration_days: Math.max(0, Number(durationDays) || 0),
        price_micro: priceMicro,
        status,
      };
      if (initialPlan) {
        const patch: PlanPatch = { ...payload, apply_to_existing: applyToExisting };
        return updatePlan(initialPlan.id, patch);
      }
      return createPlan(payload);
    },
    onMutate: () => onPendingChange(true),
    onSuccess,
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
    onSettled: () => onPendingChange(false),
  });

  useEffect(() => () => onPendingChange(false), [onPendingChange]);

  return (
    <>
      <form id="plan-form" className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); saveMutation.mutate(); }}>
        <div className="cp-form-grid">
          <ControlInput label={t("套餐名称")} value={name} onChange={(event) => setName(event.target.value)} placeholder={t("输入套餐名称，如 标准订阅")} />
          <ControlSelect label={t("状态")} value={status} onChange={(event) => setStatus(event.target.value as PlanStatus)}><option value="active">{t("正常启用")}</option><option value="disabled">{t("已停用")}</option></ControlSelect>
          <ControlSelect label={t("重置周期")} value={period} onChange={(event) => setPeriod(event.target.value)}>{periodOptions.map((option) => <option key={option} value={option}>{periodLabel(option, t)}</option>)}</ControlSelect>
        </div>
        <section className="cp-form-section"><span className="cp-eyebrow">{t("平台与网络")}</span><ControlInput label={t("适用平台 (可选，多个逗号分隔)")} hint={t("留空允许所有平台") } value={platformsRaw} onChange={(event) => setPlatformsRaw(event.target.value)} placeholder={t("例如 openai, anthropic")} /><ControlInput label={t("IP 类型与纯净度 (可选，多个逗号分隔)")} hint={t("格式为 类型:纯净度") } value={ipTiersRaw} onChange={(event) => setIpTiersRaw(event.target.value)} placeholder={t("例如 residential:clean")} /></section>
        <section className="cp-form-section"><span className="cp-eyebrow">{t("配额与限制")}</span><div className="cp-form-grid"><ControlInput label={t("周期流量配额 (GB)")} hint={t("填 0 为不限制流量")} type="number" min="0" value={trafficGB} onChange={(event) => { setTrafficGB(event.target.value); setTrafficBytesExact(null); }} /><ControlInput label={t("默认有效天数")} hint={t("填 0 为永久有效")} type="number" min="0" value={durationDays} onChange={(event) => setDurationDays(event.target.value)} /><ControlInput label={t("最大并发数")} hint={t("0 为不限制")} type="number" min="0" value={maxConcurrent} onChange={(event) => setMaxConcurrent(event.target.value)} /><ControlInput label={t("最大 RPS")} hint={t("0 为不限制")} type="number" min="0" value={maxRps} onChange={(event) => setMaxRps(event.target.value)} /><ControlInput label={t("最大密钥数")} hint={t("0 为不限制")} type="number" min="0" value={maxKeys} onChange={(event) => setMaxKeys(event.target.value)} /><ControlInput label={t("最大带宽 (Mbps)")} hint={t("填 0 为不限制带宽")} type="number" min="0" value={maxBandwidthMbps} onChange={(event) => { setMaxBandwidthMbps(event.target.value); setBandwidthBpsExact(null); }} /></div></section>
        <section className="cp-form-section"><span className="cp-eyebrow">{t("定价")}</span><ControlInput label={t("价格 (USDT)")} hint={t("留空表示非卖品，由运营方手动分配") } inputMode="decimal" value={price} onChange={(event) => setPrice(event.target.value)} placeholder="0.000000" className="font-mono" /></section>
        {initialPlan ? <ControlSwitch isSelected={applyToExisting} onChange={setApplyToExisting} label={t("同步应用到存量订阅")} hint={t("开启后，该套餐已有的用户订阅将立即同步应用最新配置")} /> : null}
        {saveMutation.isError ? <p className="cp-form-error">{formatApiErrorMessage(saveMutation.error, t)}</p> : null}
      </form>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </>
  );
}
