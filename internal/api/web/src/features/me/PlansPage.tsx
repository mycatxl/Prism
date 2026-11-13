import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, RefreshCw, ShoppingCart } from "lucide-react";
import { useState } from "react";
import {
  ControlButton,
  DetailFact,
  EmptyWorkState,
  InspectorDrawer,
  WorkspacePage,
  QueryBadge,
  StatusMark,
  WorkspaceCard,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/control-plane";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import { newRequestID } from "../../lib/idempotency";
import { formatMicro } from "../../lib/money";
import { BALANCE_LEDGER_LIMIT, getBalance, getPlans, meBalanceQueryKey, purchase } from "./api";
import type { PurchaseResult, PurchasablePlan } from "./types";

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
  const [buying, setBuying] = useState<PurchasablePlan | null>(null);

  const plansQuery = useQuery({
    queryKey: ["me-plans"],
    queryFn: getPlans,
  });
  const balanceQuery = useQuery({
    queryKey: meBalanceQueryKey,
    queryFn: () => getBalance(BALANCE_LEDGER_LIMIT),
  });

  const plans = plansQuery.data?.items ?? [];
  /*
   * A balance that has not loaded is unknown, not zero: a funded account shown
   * "0" is told it cannot buy anything, and the drawer would then render a
   * negative "balance after" out of a number nobody ever read.
   */
  const balance = balanceQuery.data?.balance_micro ?? null;
  const refreshing = plansQuery.isFetching || balanceQuery.isFetching;

  const refresh = () => {
    void plansQuery.refetch();
    void balanceQuery.refetch();
  };

  return (
    <WorkspacePage
      eyebrow="Self Service / billing"
      title={t("套餐与账单")}
      description={t("选择套餐并使用账户余额完成购买或续费。")}
      meta={<StatusMark tone={plansQuery.isError && !plansQuery.data ? "warn" : plansQuery.data ? "live" : "neutral"} label={plansQuery.isError && !plansQuery.data ? t("读取失败") : plansQuery.data ? t("目录已同步") : t("等待目录")} />}
      actions={
        <>
          <QueryBadge
            label={balance === null
              ? `${t("当前余额")} ${balanceQuery.isError ? t("读取失败") : t("未知")}`
              : `${t("当前余额")} ${formatMicro(balance)} USDT`}
            tone="signal"
          />
          <ControlButton variant="secondary" size="sm" onPress={refresh} loading={refreshing}>
            <RefreshCw size={14} aria-hidden />
            {t("刷新")}
          </ControlButton>
        </>
      }
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <WorkspaceCard
        className="pricing-catalog"
        eyebrow="Purchasable catalog"
        title={t("可购买套餐")}
        description={t("价格按 USDT 计，一个周期扣一次。只有服务端返回的 active、可售套餐会显示在这里。")}
        meta={plansQuery.data ? t("共 {{count}} 个套餐", { count: plans.length }) : t("未加载")}
      >
        {plansQuery.isError && !plansQuery.data ? (
          <EmptyWorkState embedded
            title={t("可购买套餐读取失败")}
            detail={formatApiErrorMessage(plansQuery.error, t)}
            action={<ControlButton variant="secondary" size="sm" onPress={() => void plansQuery.refetch()}>{t("重试")}</ControlButton>}
          />
        ) : null}
        {plansQuery.isLoading && !plansQuery.data ? (
          <div className="workspace-register-state"><span className="workspace-spinner" aria-hidden />{t("正在加载可购买套餐...")}</div>
        ) : null}
        {!plansQuery.isLoading && !plansQuery.isError && plans.length === 0 ? (
          <EmptyWorkState embedded title={t("暂无可购买套餐")} detail={t("当前没有对外开放的套餐，请联系运营人员。")} />
        ) : null}
        {plans.length > 0 ? (
          <div className="pricing-grid">
            {plans.map((plan, index) => (
              <article key={plan.id} className="pricing-card" data-featured={index === 1 || (plans.length === 1 && index === 0) ? "true" : undefined}>
                <div className="pricing-card__head">
                  <div><h3>{plan.name}</h3><span>{periodLabel(plan.period, t)}</span></div>
                  <StatusMark tone="signal" label={t("可购买")} />
                </div>
                <div className="pricing-card__price"><strong>{formatMicro(plan.price_micro)}</strong><span>USDT</span></div>
                <dl className="pricing-card__facts">
                  <div><dt>{t("流量配额")}</dt><dd>{plan.traffic_bytes > 0 ? formatBytes(plan.traffic_bytes) : t("不限")}</dd></div>
                  <div><dt>{t("有效期")}</dt><dd>{plan.duration_days > 0 ? t("{{days}} 天", { days: plan.duration_days }) : t("永久有效")}</dd></div>
                  <div><dt>{t("可用平台")}</dt><dd>{plan.platforms?.length ? plan.platforms.join(", ") : t("全部平台")}</dd></div>
                </dl>
                <dl className="workspace-detail-facts"><PlanLimits plan={plan} /></dl>
                <ControlButton variant={index === 1 || plans.length === 1 ? "primary" : "secondary"} onPress={() => setBuying(plan)}>
                  {t("选择套餐")}<ArrowRight size={15} aria-hidden />
                </ControlButton>
              </article>
            ))}
          </div>
        ) : null}
      </WorkspaceCard>
      <WorkspaceCard
        eyebrow="Purchase boundary"
        title={t("购买说明")}
        description={t("购买后当前订阅会被替换，未用完的流量不结转。余额不足时服务端不会扣款。")}
      >
        <div className="workspace-data-grid">
          <div className="workspace-data-grid__cell"><span className="workspace-data-grid__label">{t("余额")}</span><strong className="workspace-data-grid__value">{balance === null ? "—" : formatMicro(balance)}</strong></div>
          <div className="workspace-data-grid__cell"><span className="workspace-data-grid__label">{t("可售套餐")}</span><strong className="workspace-data-grid__value">{plans.length}</strong></div>
          <div className="workspace-data-grid__cell"><span className="workspace-data-grid__label">{t("扣款单位")}</span><strong className="workspace-data-grid__value">USDT</strong></div>
          <div className="workspace-data-grid__cell"><span className="workspace-data-grid__label">{t("购买状态")}</span><strong className="workspace-data-grid__value">{plansQuery.isError ? t("未知") : t("可查询")}</strong></div>
        </div>
      </WorkspaceCard>
      {buying ? (
        <PurchaseDrawer
          key={buying.id}
          plan={buying}
          balanceMicro={balance}
          onClose={() => setBuying(null)}
          onCommitted={(result) => {
            showToast(
              "success",
              result.replayed
                ? t("重复请求：已存在的订阅生效，本次未扣费。")
                : t("已扣费 {{amount}} USDT，套餐已生效。", { amount: formatMicro(result.charged_micro) }),
            );
            void queryClient.invalidateQueries({ queryKey: ["me-plans"] });
            void queryClient.invalidateQueries({ queryKey: ["me-balance"] });
            void queryClient.invalidateQueries({ queryKey: ["me-overview"] });
          }}
        />
      ) : null}
    </WorkspacePage>
  );
}

function PurchaseDrawer({
  plan,
  balanceMicro,
  onClose,
  onCommitted,
}: {
  plan: PurchasablePlan;
  /** `null` when the balance read has not produced a number. */
  balanceMicro: number | null;
  onClose: () => void;
  onCommitted: (result: PurchaseResult) => void;
}) {
  const { t } = useI18n();
  const [requestID] = useState(newRequestID);
  const [result, setResult] = useState<PurchaseResult | null>(null);
  const buyMutation = useMutation({
    mutationFn: () => purchase(plan.id, requestID),
    onSuccess: (next) => {
      setResult(next);
      onCommitted(next);
    },
  });
  // A missing balance is not a comparison: the purchase is blocked because the
  // number is absent, and the drawer says exactly that instead of guessing.
  const balanceUnknown = balanceMicro === null;
  const insufficient = balanceMicro !== null && balanceMicro < plan.price_micro;

  return (
    <InspectorDrawer
      open
      onOpenChange={(open) => {
        if (!open) {
          buyMutation.reset();
          onClose();
        }
      }}
      title={result ? t("购买完成") : t("确认购买")}
      eyebrow={result ? t("Purchase result") : t("Purchase confirmation")}
      footer={result ? (
        <ControlButton variant="primary" size="sm" onPress={onClose}>{t("完成")}</ControlButton>
      ) : (
        <>
          <ControlButton variant="ghost" size="sm" onPress={onClose} isDisabled={buyMutation.isPending}>{t("取消")}</ControlButton>
          <ControlButton variant="primary" size="sm" onPress={() => buyMutation.mutate()} isDisabled={buyMutation.isPending || balanceUnknown || insufficient} loading={buyMutation.isPending}>
            <ShoppingCart size={14} aria-hidden />
            {buyMutation.isPending ? t("正在购买...") : insufficient ? t("余额不足") : t("确认购买")}
          </ControlButton>
        </>
      )}
    >
      {result ? (
        <div className="workspace-stack">
          <StatusMark tone={result.replayed ? "warn" : "signal"} label={result.replayed ? t("重复请求，已存在的订阅生效") : t("购买成功")} />
          <dl className="workspace-detail-facts">
            <DetailFact label={t("套餐")} value={result.subscription.plan_name} />
            <DetailFact label={t("本次扣款")} value={result.replayed ? t("未扣费") : `${formatMicro(result.charged_micro)} USDT`} mono />
            <DetailFact label={t("当前余额")} value={`${formatMicro(result.balance_micro)} USDT`} mono />
            <DetailFact label={t("订阅状态")} value={result.subscription.status === "active" ? t("生效中") : result.subscription.status} />
          </dl>
          <QueryBadge label={t("服务端已返回本次幂等请求的最终结果")} tone={result.replayed ? "warn" : "signal"} />
        </div>
      ) : (
        <div className="workspace-stack">
          <p className="workspace-muted">{t("扣款与套餐生效同时完成，余额不足时不会扣款。")}</p>
          <dl className="workspace-detail-facts">
            <DetailFact label={t("套餐")} value={plan.name} />
            <DetailFact label={t("有效期")} value={plan.duration_days > 0 ? t("{{days}} 天", { days: plan.duration_days }) : t("永久有效")} mono />
            <DetailFact label={t("重置周期")} value={periodLabel(plan.period, t)} />
            <DetailFact label={t("扣款金额")} value={`${formatMicro(plan.price_micro)} USDT`} mono />
            <DetailFact label={t("扣款后余额")} value={balanceMicro === null ? "—" : insufficient ? t("余额不足") : `${formatMicro(balanceMicro - plan.price_micro)} USDT`} mono />
            <PlanLimits plan={plan} />
          </dl>
          {balanceUnknown ? <p role="alert" className="workspace-form-error">{t("余额未知，无法确认购买")}</p> : null}
          {buyMutation.isError ? <p className="workspace-form-error">{formatApiErrorMessage(buyMutation.error, t)}</p> : null}
        </div>
      )}
    </InspectorDrawer>
  );
}

export function PlanLimits({ plan }: { plan: PurchasablePlan }) {
  const { t } = useI18n();
  const limit = (value: number | undefined, unit = "") => value == null ? t("未知") : value > 0 ? `${value}${unit}` : t("不限");
  return <>
    <DetailFact label={t("每密钥请求上限")} value={limit(plan.max_rps, " RPS")} mono />
    <DetailFact label={t("每密钥双向合计带宽")} value={limit(plan.max_bandwidth_bps, " B/s")} mono />
    <DetailFact label={t("每密钥并发连接上限")} value={limit(plan.max_concurrent)} mono />
    <DetailFact label={t("活跃密钥上限")} value={limit(plan.max_keys)} mono />
  </>;
}
