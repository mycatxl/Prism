import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, RefreshCw, ShoppingCart } from "lucide-react";
import { useState } from "react";
import {
  ControlButton,
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
  const balance = balanceQuery.data?.balance_micro ?? 0;
  const refreshing = plansQuery.isFetching || balanceQuery.isFetching;

  const refresh = () => {
    void plansQuery.refetch();
    void balanceQuery.refetch();
  };

  return (
    <PageFrame
      eyebrow="Self Service / billing"
      title={t("购买套餐")}
      description={t("用余额购买或更换套餐，扣款与生效同时完成。")}
      meta={<StatusMark tone={plansQuery.isError && !plansQuery.data ? "warn" : plansQuery.data ? "live" : "neutral"} label={plansQuery.isError && !plansQuery.data ? t("读取失败") : plansQuery.data ? t("目录已同步") : t("等待目录")} />}
      actions={
        <>
          <QueryBadge label={`${t("当前余额")} ${formatMicro(balance)} USDT`} tone="signal" />
          <ControlButton variant="secondary" size="sm" onPress={refresh} loading={refreshing}>
            <RefreshCw size={14} aria-hidden />
            {t("刷新")}
          </ControlButton>
        </>
      }
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <WorkSurface
        eyebrow="Purchasable catalog"
        title={t("可购买套餐")}
        description={t("价格按 USDT 计，一个周期扣一次。只有服务端返回的 active、可售套餐会显示在这里。")}
        meta={plansQuery.data ? t("共 {{count}} 个套餐", { count: plans.length }) : t("未加载")}
      >
        {plansQuery.isError && !plansQuery.data ? (
          <EmptyWorkState
            title={t("可购买套餐读取失败")}
            detail={formatApiErrorMessage(plansQuery.error, t)}
            action={<ControlButton variant="secondary" size="sm" onPress={() => void plansQuery.refetch()}>{t("重试")}</ControlButton>}
          />
        ) : null}
        {plansQuery.isLoading && !plansQuery.data ? (
          <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载可购买套餐...")}</div>
        ) : null}
        {!plansQuery.isLoading && !plansQuery.isError && plans.length === 0 ? (
          <EmptyWorkState title={t("暂无可购买套餐")} detail={t("当前没有对外开放的套餐，请联系运营人员。")} />
        ) : null}
        {plans.length > 0 ? (
          <ObjectList>
            {plans.map((plan) => (
              <ObjectRow
                key={plan.id}
                onClick={() => setBuying(plan)}
                leading={<span aria-hidden>$</span>}
                title={plan.name}
                subtitle={`${periodLabel(plan.period, t)} · ${plan.duration_days > 0 ? t("{{days}} 天", { days: plan.duration_days }) : t("永久有效")}`}
                facts={
                  <>
                    <span>{plan.traffic_bytes > 0 ? formatBytes(plan.traffic_bytes) : t("不限")}</span>
                    <span>{plan.platforms?.length ? plan.platforms.join(", ") : t("全部平台")}</span>
                    <span>{formatMicro(plan.price_micro)} USDT</span>
                  </>
                }
                status={<StatusMark tone="signal" label={t("可购买")} />}
                action={<ArrowRight size={14} aria-hidden />}
              />
            ))}
          </ObjectList>
        ) : null}
      </WorkSurface>
      <WorkSurface
        eyebrow="Purchase boundary"
        title={t("购买说明")}
        description={t("购买后当前订阅会被替换，未用完的流量不结转。余额不足时服务端不会扣款。")}
      >
        <div className="cp-data-grid">
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("余额")}</span><strong className="cp-data-grid__value">{formatMicro(balance)}</strong></div>
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("可售套餐")}</span><strong className="cp-data-grid__value">{plans.length}</strong></div>
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("扣款单位")}</span><strong className="cp-data-grid__value">USDT</strong></div>
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("购买状态")}</span><strong className="cp-data-grid__value">{plansQuery.isError ? t("未知") : t("可查询")}</strong></div>
        </div>
      </WorkSurface>
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
    </PageFrame>
  );
}

function PurchaseDrawer({
  plan,
  balanceMicro,
  onClose,
  onCommitted,
}: {
  plan: PurchasablePlan;
  balanceMicro: number;
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
  const insufficient = balanceMicro < plan.price_micro;

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
          <ControlButton variant="primary" size="sm" onPress={() => buyMutation.mutate()} isDisabled={buyMutation.isPending || insufficient} loading={buyMutation.isPending}>
            <ShoppingCart size={14} aria-hidden />
            {buyMutation.isPending ? t("正在购买...") : insufficient ? t("余额不足") : t("确认购买")}
          </ControlButton>
        </>
      )}
    >
      {result ? (
        <div className="cp-stack">
          <StatusMark tone={result.replayed ? "warn" : "signal"} label={result.replayed ? t("重复请求，已存在的订阅生效") : t("购买成功")} />
          <dl className="cp-detail-facts">
            <DetailFact label={t("套餐")} value={result.subscription.plan_name} />
            <DetailFact label={t("本次扣款")} value={result.replayed ? t("未扣费") : `${formatMicro(result.charged_micro)} USDT`} mono />
            <DetailFact label={t("当前余额")} value={`${formatMicro(result.balance_micro)} USDT`} mono />
            <DetailFact label={t("订阅状态")} value={result.subscription.status === "active" ? t("生效中") : result.subscription.status} />
          </dl>
          <QueryBadge label={t("服务端已返回本次幂等请求的最终结果")} tone={result.replayed ? "warn" : "signal"} />
        </div>
      ) : (
        <div className="cp-stack">
          <p className="cp-muted">{t("扣款与套餐生效同时完成，余额不足时不会扣款。")}</p>
          <dl className="cp-detail-facts">
            <DetailFact label={t("套餐")} value={plan.name} />
            <DetailFact label={t("有效期")} value={plan.duration_days > 0 ? t("{{days}} 天", { days: plan.duration_days }) : t("永久有效")} mono />
            <DetailFact label={t("重置周期")} value={periodLabel(plan.period, t)} />
            <DetailFact label={t("扣款金额")} value={`${formatMicro(plan.price_micro)} USDT`} mono />
            <DetailFact label={t("扣款后余额")} value={`${formatMicro(balanceMicro - plan.price_micro)} USDT`} mono />
          </dl>
          {buyMutation.isError ? <p className="cp-form-error">{formatApiErrorMessage(buyMutation.error, t)}</p> : null}
        </div>
      )}
    </InspectorDrawer>
  );
}
