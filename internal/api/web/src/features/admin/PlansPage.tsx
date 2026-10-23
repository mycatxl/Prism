import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, Power, RefreshCw, Search, Trash2 } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { Button } from "../../components/ui/Button";
import {
  DialogContent,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "../../components/ui/Dialog";
import { Fieldset, Input } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import {
  Panel,
  PanelBody,
  PanelFooter,
  PanelHeader,
  PanelToolbar,
} from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import { Switch } from "../../components/ui/Switch";
import { Table, TableWrap, TBody, TD, TDClip, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro, formatMicroCompact, parseAmountToMicro } from "../../lib/money";
import { adminPlansQueryKey, createPlan, deletePlan, listPlans, updatePlan } from "./api";
import {
  BITS_PER_MBPS,
  BYTES_PER_GB,
  exactUnitsText,
  unitsToBase,
} from "./plan-units";
import type { AdminPlan, PlanPatch, PlanStatus, PlanWrite } from "./types";

/*
 * The operator's plan catalogue, on the Signal Console rhythm: the page header
 * states the page and carries the one action that creates, one panel carries a
 * toolbar of search + status + refresh over a single data grid, and the footer
 * carries the count. The status column is the dot-and-word pair; the figures a
 * reader compares — price, quota, duration — are readouts with their units set
 * small. The create/edit dialog keeps every field and validation it had; only
 * the arrangement changed.
 */

/*
 * The periods a plan may name, in the order the form offers them.
 *
 * These are the values `validPlanPeriod` accepts on the server: the named
 * periods `month` / `week` / `day` / `once`, plus the "<n>d" form. `year` used to
 * be in this list and the server refused it, so choosing it made every save fail
 * with a 400 that named a field the operator had not touched.
 */
const PERIOD_OPTIONS = ["month", "week", "day", "30d", "90d", "365d", "once"] as const;

/** periodLabel names a period in the reader's language. */
function periodLabel(period: string, t: (text: string) => string): string {
  switch (period) {
    case "month":
      return t("按月");
    case "week":
      return t("按周");
    case "day":
      return t("按天");
    case "30d":
      return t("30 天");
    case "90d":
      return t("90 天");
    case "365d":
      return t("365 天");
    case "once":
      return t("不重置");
    default:
      return period;
  }
}

/** `formatBytes` keeps figure and unit apart; here the unit drops into a `<small>`. */
function trafficReadout(bytes: number): ReactNode {
  const [figure, unit] = formatBytes(bytes).split(" ");
  return (
    <>
      {figure}
      <small>{unit}</small>
    </>
  );
}

type StatusFilter = "all" | PlanStatus;

export function PlansPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [formOpen, setFormOpen] = useState(false);
  const [editingPlan, setEditingPlan] = useState<AdminPlan | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AdminPlan | null>(null);
  const [delistTarget, setDelistTarget] = useState<AdminPlan | null>(null);

  const {
    data: plansData,
    isLoading,
    isError,
    error,
    isFetching,
  } = useQuery({
    queryKey: adminPlansQueryKey,
    queryFn: () => listPlans(),
  });

  const plans = useMemo(() => plansData?.items ?? [], [plansData]);

  /*
   * The list endpoint takes no filters, so search and status narrow the page
   * itself: the same rows the cache holds, viewed through the toolbar. The
   * needle matches the name and the bound platforms and IP tiers, the three
   * things an operator scans a catalogue by.
   */
  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return plans.filter((plan) => {
      if (statusFilter !== "all" && plan.status !== statusFilter) return false;
      if (!needle) return true;
      const haystack = [
        plan.name,
        (plan.platforms ?? []).join(" "),
        (plan.ip_tiers ?? []).join(" "),
      ]
        .join(" ")
        .toLowerCase();
      return haystack.includes(needle);
    });
  }, [plans, search, statusFilter]);
  const filtersActive = search.trim() !== "" || statusFilter !== "all";
  const clearFilters = () => {
    setSearch("");
    setStatusFilter("all");
  };

  // One action, the page's own cache entry: the list this panel reads and the
  // plan pickers on the user, invite and user-detail pages share.
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });
  };

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deletePlan(id),
    onSuccess: () => {
      showToast("success", t("套餐已删除"));
      setDeleteTarget(null);
      void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  /*
   * Listing and delisting are one field on the merge patch, so the row action
   * sends only the status. Delisting is the destructive direction — the server
   * stops every subscription on the plan at once and listing again does not
   * restore them — so it confirms before it sends; listing does not.
   */
  const statusMutation = useMutation({
    mutationFn: ({ id, status }: { id: string; status: PlanStatus }) =>
      updatePlan(id, { status }),
    onSuccess: (_plan, variables) => {
      showToast("success", t(variables.status === "active" ? "套餐已上架" : "套餐已下架"));
      setDelistTarget(null);
      void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  return (
    <Page bleed>
      <PageHeader
        title={t("套餐")}
        description={t("管理订阅套餐规则、流量配额与并发限制。")}
        meta={<PageMeta label={t("套餐总数")} value={plansData ? plans.length : "--"} />}
        actions={
          <Button
            variant="primary"
            onClick={() => {
              setEditingPlan(null);
              setFormOpen(true);
            }}
          >
            <Plus size={16} />
            <span>{t("新建套餐")}</span>
          </Button>
        }
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="page-content page-content--fill">
        <Panel className="flex min-w-0 flex-col">
          <PanelHeader
            title={t("全部套餐")}
            meta={t("共 {{count}} 个套餐", { count: filtered.length })}
          />

          <PanelToolbar>
            <label htmlFor="plans-search" className="relative block min-w-48 sm:w-64">
              <Search
                size={13}
                aria-hidden
                className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-ink-faint"
              />
              <Input
                id="plans-search"
                className="pl-7 text-xs"
                placeholder={t("搜索套餐")}
                aria-label={t("搜索套餐")}
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
            </label>
            <Select
              className="w-32"
              aria-label={t("状态")}
              value={statusFilter}
              onChange={(event) => setStatusFilter(event.target.value as StatusFilter)}
            >
              <option value="all">{t("全部状态")}</option>
              <option value="active">{t("上架")}</option>
              <option value="disabled">{t("下架")}</option>
            </Select>
            {filtersActive && (
              <Button variant="ghost" size="sm" onClick={clearFilters}>
                {t("清除筛选")}
              </Button>
            )}
            <div className="ml-auto flex items-center gap-1.5">
              <Button
                variant="secondary"
                size="sm"
                onClick={refresh}
                disabled={isFetching}
                title={t("刷新")}
              >
                <RefreshCw
                  size={15}
                  className={isFetching ? "animate-spin" : undefined}
                />
                <span>{t("刷新")}</span>
              </Button>
            </div>
          </PanelToolbar>

          {isLoading ? (
            <PanelBody>
              <LoadingState label={t("正在加载套餐列表...")} />
            </PanelBody>
          ) : isError ? (
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(error, t)}
                onRetry={refresh}
              />
            </PanelBody>
          ) : filtered.length === 0 ? (
            <PanelBody>
              <EmptyState
                title={filtersActive ? t("没有匹配的套餐") : t("暂无套餐")}
                hint={
                  filtersActive
                    ? t("调整搜索或状态筛选，或清除筛选查看全部。")
                    : t("尚未创建任何订阅套餐，点击右上角新建。")
                }
                action={
                  filtersActive ? (
                    <Button variant="secondary" onClick={clearFilters}>
                      {t("清除筛选")}
                    </Button>
                  ) : (
                    <Button
                      variant="primary"
                      onClick={() => {
                        setEditingPlan(null);
                        setFormOpen(true);
                      }}
                    >
                      <Plus size={14} />
                      <span>{t("新建套餐")}</span>
                    </Button>
                  )
                }
              />
            </PanelBody>
          ) : (
            <TableWrap aria-busy={isFetching}>
              <Table className="min-w-[960px]">
                <THead>
                  <TR>
                    <TH>{t("套餐名称")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH className="text-right">{t("价格 (USDT)")}</TH>
                    <TH className="text-right">{t("流量配额")}</TH>
                    <TH>{t("重置周期")}</TH>
                    <TH className="text-right">{t("时长")}</TH>
                    <TH>{t("适用平台")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {filtered.map((plan) => {
                    const active = plan.status === "active";
                    const busy =
                      statusMutation.isPending && statusMutation.variables?.id === plan.id;
                    const platformText =
                      plan.platforms && plan.platforms.length > 0
                        ? plan.platforms.join(", ")
                        : t("全部平台");
                    return (
                      <TR key={plan.id}>
                        <TDClip className="font-medium text-ink">{plan.name}</TDClip>
                        <TD className="whitespace-nowrap">
                          <span className="inline-flex items-center gap-2">
                            <span
                              aria-hidden
                              className={active ? "state-dot state-dot--signal" : "state-dot"}
                            />
                            <span className="state-text">
                              {active ? t("上架") : t("下架")}
                            </span>
                          </span>
                        </TD>
                        <TDNum className="whitespace-nowrap">
                          {plan.price_micro > 0 ? (
                            <span className="readout">
                              {formatMicro(plan.price_micro)}
                              <small>USDT</small>
                            </span>
                          ) : (
                            <span className="text-ink-faint">{t("非卖品")}</span>
                          )}
                        </TDNum>
                        <TDNum className="whitespace-nowrap">
                          {plan.traffic_bytes > 0 ? (
                            <span className="readout">
                              {trafficReadout(plan.traffic_bytes)}
                            </span>
                          ) : (
                            <span className="text-ink-faint">{t("不限")}</span>
                          )}
                        </TDNum>
                        <TD className="whitespace-nowrap text-xs text-ink-soft">
                          {periodLabel(plan.period || "month", t)}
                        </TD>
                        <TDNum className="whitespace-nowrap">
                          {plan.duration_days > 0 ? (
                            <span className="readout">
                              {plan.duration_days}
                              <small>{t("天")}</small>
                            </span>
                          ) : (
                            <span className="text-ink-faint">{t("永久有效")}</span>
                          )}
                        </TDNum>
                        <TDClip className="text-xs text-ink-soft" title={platformText}>
                          {platformText}
                        </TDClip>
                        <TD className="text-right">
                          <div className="flex items-center justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="icon"
                              onClick={() => {
                                setEditingPlan(plan);
                                setFormOpen(true);
                              }}
                              title={t("编辑套餐")}
                              aria-label={t("编辑套餐")}
                            >
                              <Pencil size={14} />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              disabled={busy}
                              onClick={() => {
                                if (active) setDelistTarget(plan);
                                else statusMutation.mutate({ id: plan.id, status: "active" });
                              }}
                              title={active ? t("下架套餐") : t("上架套餐")}
                              aria-label={active ? t("下架套餐") : t("上架套餐")}
                            >
                              <Power size={14} />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="text-ink-faint hover:bg-alert-wash hover:text-alert"
                              onClick={() => setDeleteTarget(plan)}
                              title={t("删除套餐")}
                              aria-label={t("删除套餐")}
                            >
                              <Trash2 size={14} />
                            </Button>
                          </div>
                        </TD>
                      </TR>
                    );
                  })}
                </TBody>
              </Table>
            </TableWrap>
          )}

          <PanelFooter>
            <p className="text-xs text-ink-soft">
              {filtersActive
                ? t("显示 {{shown}} / {{total}} 个套餐", {
                    shown: filtered.length,
                    total: plans.length,
                  })
                : t("共 {{count}} 个套餐", { count: plans.length })}
            </p>
          </PanelFooter>
        </Panel>
      </div>

      <PlanFormDialog
        open={formOpen}
        onOpenChange={setFormOpen}
        initialPlan={editingPlan}
        onSuccess={() => {
          showToast(
            "success",
            editingPlan ? t("套餐已更新") : t("套餐创建成功"),
          );
          setFormOpen(false);
          setEditingPlan(null);
          void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });
        }}
      />

      <DialogRoot
        open={Boolean(delistTarget)}
        onOpenChange={(open) => !open && setDelistTarget(null)}
      >
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md">
            <div className="border-b border-rule px-5 py-4">
              <DialogTitle>{t("确认下架")}</DialogTitle>
            </div>
            <div className="px-5 py-4 text-sm leading-relaxed text-ink-soft">
              {t(
                "下架会立即停止该套餐现有用户的订阅，重新上架也不会自动恢复。确定要下架 {{name}} 吗？",
                { name: delistTarget?.name ?? "" },
              )}
            </div>
            <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
              <Button
                variant="secondary"
                onClick={() => setDelistTarget(null)}
                disabled={statusMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                variant="danger"
                loading={statusMutation.isPending}
                onClick={() => {
                  if (delistTarget) {
                    statusMutation.mutate({ id: delistTarget.id, status: "disabled" });
                  }
                }}
              >
                {t("确认下架")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>

      <DialogRoot
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md">
            <div className="border-b border-rule px-5 py-4">
              <DialogTitle>{t("确认删除套餐")}</DialogTitle>
            </div>
            <div className="px-5 py-4 text-sm leading-relaxed text-ink-soft">
              {t(
                "确定要删除套餐 {{name}} 吗？若有用户正在使用该套餐，此操作将被服务器拒绝。",
                {
                  name: deleteTarget?.name ?? "",
                },
              )}
            </div>
            <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
              <Button
                variant="secondary"
                onClick={() => setDeleteTarget(null)}
                disabled={deleteMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                variant="danger"
                loading={deleteMutation.isPending}
                onClick={() => {
                  if (deleteTarget) deleteMutation.mutate(deleteTarget.id);
                }}
              >
                {t("确认删除")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>
    </Page>
  );
}

function PlanFormContent({
  initialPlan,
  onOpenChange,
  onSuccess,
}: {
  initialPlan: AdminPlan | null;
  onOpenChange: (open: boolean) => void;
  onSuccess: () => void;
}) {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();

  const [name, setName] = useState(initialPlan?.name ?? "");
  const [platformsRaw, setPlatformsRaw] = useState(
    initialPlan?.platforms?.join(", ") ?? "",
  );
  const [ipTiersRaw, setIpTiersRaw] = useState(
    initialPlan?.ip_tiers?.join(", ") ?? "",
  );
  const [trafficGB, setTrafficGB] = useState(() =>
    exactUnitsText(initialPlan?.traffic_bytes ?? 0, BYTES_PER_GB),
  );
  /*
   * The stored byte count, kept until the operator edits the field. Without it the
   * form only ever had the rounded GB text, so saving a *name* change rewrote the
   * quota: 1.5 GiB was displayed as "2" and multiplied back into 2 GiB.
   */
  const [trafficBytesExact, setTrafficBytesExact] = useState<number | null>(
    initialPlan ? initialPlan.traffic_bytes : null,
  );
  const [period, setPeriod] = useState(initialPlan?.period || "month");
  /*
   * A stored period the list does not carry still gets an entry: a select
   * whose value matches no option renders blank, which reads as "unset" on
   * a plan that has one.
   */
  const periodOptions = (PERIOD_OPTIONS as readonly string[]).includes(period)
    ? [...PERIOD_OPTIONS]
    : [period, ...PERIOD_OPTIONS];
  const [maxConcurrent, setMaxConcurrent] = useState(
    String(initialPlan?.max_concurrent ?? 0),
  );
  const [maxRps, setMaxRps] = useState(String(initialPlan?.max_rps ?? 0));
  const [maxBandwidthMbps, setMaxBandwidthMbps] = useState(() =>
    exactUnitsText(initialPlan?.max_bandwidth_bps ?? 0, BITS_PER_MBPS),
  );
  const [bandwidthBpsExact, setBandwidthBpsExact] = useState<number | null>(
    initialPlan ? initialPlan.max_bandwidth_bps : null,
  );
  const [maxKeys, setMaxKeys] = useState(String(initialPlan?.max_keys ?? 5));
  const [durationDays, setDurationDays] = useState(
    String(initialPlan?.duration_days ?? 30),
  );
  const [price, setPrice] = useState(() =>
    initialPlan && initialPlan.price_micro > 0
      ? formatMicroCompact(initialPlan.price_micro)
      : "",
  );
  const [status, setStatus] = useState<PlanStatus>(
    initialPlan?.status || "active",
  );
  const [applyToExisting, setApplyToExisting] = useState(false);

  const saveMutation = useMutation({
    mutationFn: async () => {
      const trimmedName = name.trim();
      if (!trimmedName) {
        throw new Error(t("套餐名称不能为空"));
      }

      const platforms = platformsRaw
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);

      const ipTiers = ipTiersRaw
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);

      // An untouched field submits the exact stored number; an edited one is the
      // operator's own figure, converted once.
      const trafficBytes =
        trafficBytesExact ?? unitsToBase(trafficGB, BYTES_PER_GB);
      const bandwidthBps =
        bandwidthBpsExact ?? unitsToBase(maxBandwidthMbps, BITS_PER_MBPS);

      // An empty field means the plan is not for sale, which the server spells
      // as price_micro = 0. Anything typed has to be an amount it can store.
      const priceMicro = price.trim() === "" ? 0 : parseAmountToMicro(price);
      if (priceMicro === null) {
        throw new Error(t("价格最多六位小数，留空表示不对外出售。"));
      }

      const payload: PlanWrite = {
        name: trimmedName,
        platforms,
        ip_tiers: ipTiers,
        traffic_bytes: trafficBytes,
        period: period.trim() || "month",
        max_concurrent: Math.max(0, Number(maxConcurrent) || 0),
        max_rps: Math.max(0, Number(maxRps) || 0),
        max_bandwidth_bps: bandwidthBps,
        max_keys: Math.max(0, Number(maxKeys) || 0),
        duration_days: Math.max(0, Number(durationDays) || 0),
        price_micro: priceMicro,
        status,
      };

      if (initialPlan) {
        const patch: PlanPatch = {
          ...payload,
          apply_to_existing: applyToExisting,
        };
        return updatePlan(initialPlan.id, patch);
      }

      return createPlan(payload);
    },
    onSuccess: () => {
      onSuccess();
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  return (
    <>
      <div className="border-b border-rule px-5 py-4">
        <DialogTitle>{initialPlan ? t("编辑套餐") : t("新建套餐")}</DialogTitle>
        <p className="mt-1 text-xs text-ink-faint">
          {t("配置套餐的适用平台、网络质量约束与用量配额。")}
        </p>
      </div>

      <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-5 py-4">
        <section className="space-y-4">
          <div className="micro">{t("基本信息")}</div>
          <Fieldset label={t("套餐名称")} htmlFor="plan-name">
            <Input
              id="plan-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("输入套餐名称，如 标准订阅")}
            />
          </Fieldset>
          <div className="grid grid-cols-2 gap-4">
            <Fieldset label={t("状态")} htmlFor="plan-status">
              <Select
                id="plan-status"
                value={status}
                onChange={(e) => setStatus(e.target.value as PlanStatus)}
              >
                <option value="active">{t("正常启用")}</option>
                <option value="disabled">{t("已停用")}</option>
              </Select>
            </Fieldset>

            <Fieldset label={t("重置周期")} htmlFor="plan-period">
              <Select
                id="plan-period"
                value={period}
                onChange={(e) => setPeriod(e.target.value)}
              >
                {periodOptions.map((option) => (
                  <option key={option} value={option}>
                    {periodLabel(option, t)}
                  </option>
                ))}
              </Select>
            </Fieldset>
          </div>
        </section>

        <section className="space-y-4 border-t border-rule-faint pt-4">
          <div className="micro">{t("平台与网络")}</div>
          <Fieldset
            label={t("适用平台 (可选，多个逗号分隔)")}
            hint={t("留空允许所有平台，或填: openai, anthropic")}
            htmlFor="plan-platforms"
          >
            <Input
              id="plan-platforms"
              value={platformsRaw}
              onChange={(e) => setPlatformsRaw(e.target.value)}
              placeholder={t("留空不限制，或填: openai, anthropic")}
            />
          </Fieldset>

          <Fieldset
            label={t("IP 类型与纯净度 (可选，多个逗号分隔)")}
            hint={t("格式为 类型:纯净度，如 residential:clean，留空不限制")}
            htmlFor="plan-ip-tiers"
          >
            <Input
              id="plan-ip-tiers"
              value={ipTiersRaw}
              onChange={(e) => setIpTiersRaw(e.target.value)}
              placeholder={t("例如: residential:clean, datacenter:fair")}
            />
          </Fieldset>
        </section>

        <section className="space-y-4 border-t border-rule-faint pt-4">
          <div className="micro">{t("配额与限制")}</div>
          <div className="grid grid-cols-2 gap-4">
            <Fieldset
              label={t("周期流量配额 (GB)")}
              hint={t("填 0 为不限制流量")}
              htmlFor="plan-traffic"
            >
              <Input
                id="plan-traffic"
                type="number"
                min="0"
                value={trafficGB}
                onChange={(e) => {
                  // The field now owns the number: the stored one no longer applies.
                  setTrafficGB(e.target.value);
                  setTrafficBytesExact(null);
                }}
              />
            </Fieldset>

            <Fieldset
              label={t("默认有效天数")}
              hint={t("填 0 为永久有效")}
              htmlFor="plan-duration"
            >
              <Input
                id="plan-duration"
                type="number"
                min="0"
                value={durationDays}
                onChange={(e) => setDurationDays(e.target.value)}
              />
            </Fieldset>
          </div>

          <div className="grid grid-cols-3 gap-3">
            <Fieldset
              label={t("最大并发数")}
              hint={t("0 为不限制")}
              htmlFor="plan-concurrent"
            >
              <Input
                id="plan-concurrent"
                type="number"
                min="0"
                value={maxConcurrent}
                onChange={(e) => setMaxConcurrent(e.target.value)}
              />
            </Fieldset>

            <Fieldset
              label={t("最大 RPS")}
              hint={t("0 为不限制")}
              htmlFor="plan-rps"
            >
              <Input
                id="plan-rps"
                type="number"
                min="0"
                value={maxRps}
                onChange={(e) => setMaxRps(e.target.value)}
              />
            </Fieldset>

            <Fieldset
              label={t("最大密钥数")}
              hint={t("0 为不限制")}
              htmlFor="plan-keys"
            >
              <Input
                id="plan-keys"
                type="number"
                min="0"
                value={maxKeys}
                onChange={(e) => setMaxKeys(e.target.value)}
              />
            </Fieldset>
          </div>

          <Fieldset
            label={t("最大带宽 (Mbps)")}
            hint={t("填 0 为不限制带宽")}
            htmlFor="plan-bandwidth"
          >
            <Input
              id="plan-bandwidth"
              type="number"
              min="0"
              value={maxBandwidthMbps}
              onChange={(e) => {
                setMaxBandwidthMbps(e.target.value);
                setBandwidthBpsExact(null);
              }}
            />
          </Fieldset>
        </section>

        <section className="space-y-4 border-t border-rule-faint pt-4">
          <div className="micro">{t("定价")}</div>
          <Fieldset
            label={t("价格 (USDT)")}
            hint={t("一个周期的售价，留空表示非卖品，由运营方手动分配。")}
            htmlFor="plan-price"
          >
            <Input
              id="plan-price"
              inputMode="decimal"
              value={price}
              onChange={(e) => setPrice(e.target.value)}
              placeholder="0.000000"
              className="font-mono"
            />
          </Fieldset>
        </section>

        {initialPlan && (
          <div className="flex items-center justify-between rounded-control border border-rule bg-paper-inset p-3">
            <div className="space-y-0.5">
              <span className="text-xs font-medium text-ink">
                {t("同步应用到存量订阅")}
              </span>
              <p className="text-xs text-ink-soft">
                {t("开启后，该套餐已有的用户订阅将立即同步应用最新配置")}
              </p>
            </div>
            <Switch
              checked={applyToExisting}
              onCheckedChange={setApplyToExisting}
            />
          </div>
        )}
      </div>

      <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
        <Button
          variant="secondary"
          onClick={() => onOpenChange(false)}
          disabled={saveMutation.isPending}
        >
          {t("取消")}
        </Button>
        <Button
          variant="primary"
          loading={saveMutation.isPending}
          onClick={() => saveMutation.mutate()}
        >
          {initialPlan ? t("保存修改") : t("立即创建")}
        </Button>
      </div>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </>
  );
}

function PlanFormDialog({
  open,
  onOpenChange,
  initialPlan,
  onSuccess,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  initialPlan: AdminPlan | null;
  onSuccess: () => void;
}) {
  return (
    <DialogRoot open={open} onOpenChange={onOpenChange}>
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="flex w-full max-w-xl flex-col">
          {open && (
            <PlanFormContent
              key={initialPlan?.id ?? "new"}
              initialPlan={initialPlan}
              onOpenChange={onOpenChange}
              onSuccess={onSuccess}
            />
          )}
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}
