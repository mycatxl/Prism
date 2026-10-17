import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, ShoppingCart } from "lucide-react";
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
import { PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Table, TableWrap, TBody, TD, TDClip, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import { newRequestID } from "../../lib/idempotency";
import { formatMicro } from "../../lib/money";
import { getBalance, getPlans, purchase } from "./api";
import type { PurchasablePlan } from "./types";

/** periodLabel names a rollover window in the reader's language. */
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

/**
 * PlansPage lists what a customer may buy with their own balance.
 *
 * Only plans that are for sale appear: a plan with no price is granted by an
 * operator, and offering it here would be offering a purchase the server has to
 * refuse.
 */
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
    queryKey: ["me-balance"],
    queryFn: () => getBalance(1),
  });

  const plans = plansQuery.data?.items ?? [];
  const balance = balanceQuery.data?.balance_micro ?? 0;
  const isRefreshing = plansQuery.isFetching || balanceQuery.isFetching;

  const handleRefresh = () => {
    void plansQuery.refetch();
    void balanceQuery.refetch();
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("购买套餐")}
        description={t("用余额购买或更换套餐，扣款与生效同时完成。")}
        actions={
          <div className="flex items-center gap-2">
            <Badge tone={balance > 0 ? "signal" : "neutral"}>
              {t("当前余额")} {formatMicro(balance)}
            </Badge>
            <Button
              variant="secondary"
              onClick={handleRefresh}
              disabled={isRefreshing}
              title={t("刷新")}
            >
              <RefreshCw size={14} className={isRefreshing ? "animate-spin" : undefined} />
              <span>{t("刷新")}</span>
            </Button>
          </div>
        }
      />

      <Panel>
        <PanelHeader
          title={t("可购买套餐")}
          description={t("价格按 USDT 计，一个周期扣一次。")}
          meta={<Badge tone="neutral">{plans.length}</Badge>}
        />
        <PanelBody className="p-0">
          {plansQuery.isLoading ? (
            <LoadingState label={t("正在加载可购买套餐...")} className="py-12" />
          ) : plansQuery.isError ? (
            <ErrorState
              message={formatApiErrorMessage(plansQuery.error, t)}
              onRetry={() => void plansQuery.refetch()}
              className="py-12"
            />
          ) : plans.length === 0 ? (
            <EmptyState
              title={t("暂无可购买套餐")}
              hint={t("当前没有对外开放的套餐，请联系运营人员。")}
              className="py-12"
            />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("套餐名称")}</TH>
                    <TH>{t("适用平台")}</TH>
                    <TH>{t("流量配额")}</TH>
                    <TH>{t("重置周期")}</TH>
                    <TH>{t("有效天数")}</TH>
                    <TH className="text-right">{t("价格 (USDT)")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {plans.map((plan) => (
                    <TR key={plan.id}>
                      <TD className="font-medium text-ink">
                        <TDClip>{plan.name}</TDClip>
                      </TD>
                      <TD className="text-xs text-ink-soft">
                        <TDClip>
                          {plan.platforms && plan.platforms.length > 0
                            ? plan.platforms.join(", ")
                            : t("全部平台")}
                        </TDClip>
                      </TD>
                      <TD className="readout text-xs">
                        {plan.traffic_bytes > 0
                          ? formatBytes(plan.traffic_bytes)
                          : t("不限")}
                      </TD>
                      <TD className="readout text-xs">{periodLabel(plan.period, t)}</TD>
                      <TD className="readout text-xs">
                        {plan.duration_days > 0
                          ? t("{{days}} 天", { days: plan.duration_days })
                          : t("永久有效")}
                      </TD>
                      <TD className="readout text-right text-xs">
                        {formatMicro(plan.price_micro)}
                      </TD>
                      <TD className="text-right">
                        <Button
                          variant="primary"
                          size="sm"
                          onClick={() => setBuying(plan)}
                          title={t("用余额购买此套餐")}
                        >
                          <ShoppingCart size={14} />
                          <span>{t("购买")}</span>
                        </Button>
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
            {t("购买后当前订阅会被替换，未用完的流量不结转。")}
          </span>
        </PanelFooter>
      </Panel>

      {/*
       * The dialog is keyed by the plan it is open for, so opening a different
       * plan remounts it and mints a new idempotency key. Reopening the same
       * one after an error keeps the key it already has.
       */}
      {buying && (
        <PurchaseDialog
          key={buying.id}
          plan={buying}
          balanceMicro={balance}
          onClose={() => setBuying(null)}
          onDone={() => {
            setBuying(null);
            showToast("success", t("购买成功，套餐已生效"));
            void queryClient.invalidateQueries({ queryKey: ["me-plans"] });
            void queryClient.invalidateQueries({ queryKey: ["me-balance"] });
            void queryClient.invalidateQueries({ queryKey: ["me-overview"] });
          }}
        />
      )}

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
  );
}

/**
 * The confirmation step, which is also where the idempotency key is minted.
 *
 * The key is minted once, when this dialog mounts, and kept for its lifetime: a
 * retry after a network error answers with the first call's result instead of
 * charging a second time. The parent keys this component by plan id, so a
 * different plan - or the same one bought again - mounts a new key, which really
 * is a second charge.
 */
function PurchaseDialog({
  plan,
  balanceMicro,
  onClose,
  onDone,
}: {
  plan: PurchasablePlan;
  balanceMicro: number;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [requestID] = useState(newRequestID);

  const buyMutation = useMutation({
    mutationFn: (input: { planID: string; requestID: string }) =>
      purchase(input.planID, input.requestID),
    onSuccess: () => {
      onDone();
    },
  });

  const short = balanceMicro < plan.price_micro;

  return (
    <DialogRoot
      open
      onOpenChange={(open) => {
        if (!open) {
          buyMutation.reset();
          onClose();
        }
      }}
    >
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="w-full max-w-md p-6">
          <DialogTitle className="text-base font-semibold text-ink">
            {t("确认购买")}
          </DialogTitle>
          <p className="mt-1 text-xs text-ink-soft">
            {t("扣款与套餐生效同时完成，余额不足时不会扣款。")}
          </p>

          <div className="mt-4 space-y-2 rounded-control border border-glass-edge bg-glass p-3 text-xs">
            <div className="flex items-center justify-between">
              <span className="text-ink-soft">{t("套餐")}</span>
              <span className="font-medium text-ink">{plan.name}</span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-ink-soft">{t("有效期")}</span>
              <span className="readout text-ink">
                {t("{{days}} 天", { days: plan.duration_days })}
              </span>
            </div>
            <div className="flex items-center justify-between">
              <span className="text-ink-soft">{t("扣款金额")}</span>
              <span className="readout font-mono text-ink">
                {formatMicro(plan.price_micro)} USDT
              </span>
            </div>
            <div className="flex items-center justify-between border-t border-glass-edge pt-2">
              <span className="text-ink-soft">{t("扣款后余额")}</span>
              <span
                className={`readout font-mono ${
                  short ? "text-alert" : "text-signal"
                }`}
              >
                {formatMicro(balanceMicro - plan.price_micro)} USDT
              </span>
            </div>
          </div>

          {buyMutation.isError && (
            <div className="mt-3 rounded-control border border-alert/30 bg-alert-wash p-3 text-xs text-alert">
              {formatApiErrorMessage(buyMutation.error, t)}
            </div>
          )}

          <div className="mt-6 flex items-center justify-end gap-3">
            <Button
              variant="secondary"
              onClick={onClose}
              disabled={buyMutation.isPending}
            >
              {t("取消")}
            </Button>
            <Button
              variant="primary"
              onClick={() => buyMutation.mutate({ planID: plan.id, requestID })}
              disabled={buyMutation.isPending || short}
            >
              {buyMutation.isPending
                ? t("正在购买...")
                : short
                  ? t("余额不足")
                  : t("确认购买")}
            </Button>
          </div>
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}
