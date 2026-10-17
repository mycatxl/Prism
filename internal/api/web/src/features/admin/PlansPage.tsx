import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import {
  DialogContent,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "../../components/ui/Dialog";
import { Fieldset, Input } from "../../components/ui/Input";
import { PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import { Switch } from "../../components/ui/Switch";
import { Table, TableWrap, TBody, TD, TDClip, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro, formatMicroCompact, parseAmountToMicro } from "../../lib/money";
import { createPlan, deletePlan, listPlans, updatePlan } from "./api";
import {
  BITS_PER_MBPS,
  BYTES_PER_GB,
  exactUnitsText,
  unitsToBase,
} from "./plan-units";
import type { AdminPlan, PlanPatch, PlanStatus, PlanWrite } from "./types";

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

export function PlansPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  const [formOpen, setFormOpen] = useState(false);
  const [editingPlan, setEditingPlan] = useState<AdminPlan | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AdminPlan | null>(null);

  const {
    data: plansData,
    isLoading,
    isError,
    error,
    refetch,
    isFetching,
  } = useQuery({
    queryKey: ["admin-plans"],
    queryFn: () => listPlans(),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deletePlan(id),
    onSuccess: () => {
      showToast("success", t("套餐已删除"));
      setDeleteTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["admin-plans"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  const plans = plansData?.items ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("套餐管理")}
        description={t("管理订阅套餐规则、流量配额与并发限制。")}
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="secondary"
              onClick={() => void refetch()}
              disabled={isFetching}
              title={t("刷新")}
            >
              <RefreshCw
                size={14}
                className={isFetching ? "animate-spin" : undefined}
              />
              <span>{t("刷新")}</span>
            </Button>
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
          </div>
        }
      />

      <Panel>
        <PanelHeader
          title={t("全部套餐")}
          meta={<Badge tone="neutral">{plans.length}</Badge>}
        />

        <PanelBody className="p-0">
          {isLoading ? (
            <LoadingState label={t("正在加载套餐列表...")} />
          ) : isError ? (
            <ErrorState
              message={formatApiErrorMessage(error, t)}
              onRetry={() => void refetch()}
            />
          ) : plans.length === 0 ? (
            <EmptyState
              title={t("暂无套餐")}
              hint={t("尚未创建任何订阅套餐，点击右上角新建。")}
              action={
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
              }
            />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("套餐名称")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH>{t("适用平台")}</TH>
                    <TH>{t("IP 类型与纯净度")}</TH>
                    <TH>{t("流量配额")}</TH>
                    <TH>{t("重置周期")}</TH>
                    <TH>{t("最大并发")}</TH>
                    <TH>{t("最大密钥数")}</TH>
                    <TH>{t("有效天数")}</TH>
                    <TH className="text-right">{t("价格 (USDT)")}</TH>
                    <TH>{t("活跃用户")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {plans.map((plan) => (
                    <TR key={plan.id}>
                      <TD className="font-medium text-ink">
                        <TDClip>{plan.name}</TDClip>
                      </TD>
                      <TD>
                        <Badge
                          tone={plan.status === "active" ? "live" : "warn"}
                        >
                          {plan.status === "active" ? t("正常") : t("已停用")}
                        </Badge>
                      </TD>
                      <TD className="text-xs text-ink-soft">
                        <TDClip>
                          {plan.platforms && plan.platforms.length > 0
                            ? plan.platforms.join(", ")
                            : t("全部平台")}
                        </TDClip>
                      </TD>
                      <TD className="text-xs text-ink-soft">
                        <TDClip>
                          {plan.ip_tiers && plan.ip_tiers.length > 0
                            ? plan.ip_tiers.join(", ")
                            : t("不限")}
                        </TDClip>
                      </TD>
                      <TD className="readout text-xs">
                        {plan.traffic_bytes > 0
                          ? formatBytes(plan.traffic_bytes)
                          : t("不限")}
                      </TD>
                      <TD className="readout text-xs">
                        {plan.period || t("按月")}
                      </TD>
                      <TD className="readout text-xs">
                        {plan.max_concurrent > 0 ? plan.max_concurrent : t("不限")}
                      </TD>
                      <TD className="readout text-xs">
                        {plan.max_keys > 0 ? plan.max_keys : t("不限")}
                      </TD>
                      <TD className="readout text-xs">
                        {plan.duration_days > 0
                          ? t("{{days}} 天", { days: plan.duration_days })
                          : t("永久有效")}
                      </TD>
                      <TD className="readout text-right text-xs">
                        {plan.price_micro > 0 ? (
                          formatMicro(plan.price_micro)
                        ) : (
                          <span className="text-ink-faint">{t("非卖品")}</span>
                        )}
                      </TD>
                      <TD className="readout text-xs">
                        {plan.active_user_count ?? 0}
                      </TD>
                      <TD className="text-right">
                        <div className="flex items-center justify-end gap-1.5">
                          <Button
                            variant="ghost"
                            onClick={() => {
                              setEditingPlan(plan);
                              setFormOpen(true);
                            }}
                            title={t("编辑套餐")}
                          >
                            <Pencil size={14} />
                            <span>{t("编辑")}</span>
                          </Button>
                          <Button
                            variant="ghost"
                            onClick={() => setDeleteTarget(plan)}
                            title={t("删除套餐")}
                          >
                            <Trash2 size={14} className="text-alert" />
                            <span>{t("删除")}</span>
                          </Button>
                        </div>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          )}
        </PanelBody>

        <PanelFooter className="flex items-center justify-between">
          <span className="readout text-xs text-ink-soft">
            {t("共 {{count}} 个套餐", { count: plans.length })}
          </span>
        </PanelFooter>
      </Panel>

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
          void queryClient.invalidateQueries({ queryKey: ["admin-plans"] });
        }}
      />

      <DialogRoot
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md p-6">
            <DialogTitle className="text-base font-semibold text-ink">
              {t("确认删除套餐")}
            </DialogTitle>
            <p className="mt-3 text-sm text-ink-soft">
              {t(
                "确定要删除套餐 {{name}} 吗？若有用户正在使用该套餐，此操作将被服务器拒绝。",
                {
                  name: deleteTarget?.name ?? "",
                },
              )}
            </p>
            <div className="mt-6 flex items-center justify-end gap-3">
              <Button
                variant="secondary"
                onClick={() => setDeleteTarget(null)}
                disabled={deleteMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                variant="primary"
                onClick={() => {
                  if (deleteTarget) deleteMutation.mutate(deleteTarget.id);
                }}
                disabled={deleteMutation.isPending}
              >
                {deleteMutation.isPending ? t("正在删除...") : t("确认删除")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
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
          <DialogTitle className="text-base font-semibold text-ink">
            {initialPlan ? t("编辑套餐") : t("新建套餐")}
          </DialogTitle>
          <p className="mt-1 text-xs text-ink-soft">
            {t("配置套餐的适用平台、网络质量约束与用量配额。")}
          </p>

          <div className="mt-5 space-y-4">
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

            {initialPlan && (
              <div className="flex items-center justify-between rounded-control border border-glass-edge bg-glass p-3">
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

          <div className="mt-6 flex items-center justify-end gap-3">
            <Button
              variant="secondary"
              onClick={() => onOpenChange(false)}
              disabled={saveMutation.isPending}
            >
              {t("取消")}
            </Button>
            <Button
              variant="primary"
              onClick={() => saveMutation.mutate()}
              disabled={saveMutation.isPending}
            >
              {saveMutation.isPending
                ? t("正在保存...")
                : initialPlan
                  ? t("保存修改")
                  : t("立即创建")}
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
        <DialogContent className="max-h-[85vh] w-full max-w-xl overflow-y-auto p-6">
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
