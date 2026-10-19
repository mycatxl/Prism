import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Coins, Ticket, Wallet } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout } from "../../components/ui/Readout";
import { Table, TableWrap, TBody, TD, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatApiErrorMessage } from "../../lib/error-message";
import { mePaths } from "../../lib/paths";
import { formatMicro } from "../../lib/money";
import { BALANCE_LEDGER_LIMIT, getBalance, meBalanceQueryKey, redeemCode } from "./api";
import type { BalanceEntry } from "./types";

const NANOS_PER_MILLI = 1_000_000;

function formatNsDateTime(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}

/** reasonLabel names where a movement came from, in the reader's language. */
function reasonLabel(reason: string, t: (text: string) => string): string {
  switch (reason) {
    case "redeem":
      return t("兑换码充值");
    case "purchase":
      return t("购买套餐");
    case "renew":
      return t("订阅续费");
    case "admin_adjust":
      return t("运营调整");
    case "refund":
      return t("退款");
    case "deposit":
      return t("链上充值");
    default:
      return reason;
  }
}

/**
 * BalancePanel is the user's own money: what is left, how to add more, and where
 * the last movements came from.
 *
 * The amount is never rendered from a float. It arrives as micro-units and goes
 * through formatMicro, which is the single place the division by 1e6 happens.
 */
export function BalancePanel() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [code, setCode] = useState("");
  const [invalid, setInvalid] = useState(false);

  // The limit is in the key: this panel needs the movements, the plans page only
  // needs the total, and one shared entry would hand one of them the other's
  // list length.
  const balanceQuery = useQuery({
    queryKey: meBalanceQueryKey,
    queryFn: () => getBalance(BALANCE_LEDGER_LIMIT),
  });

  const redeemMutation = useMutation({
    mutationFn: (value: string) => redeemCode(value),
    onSuccess: () => {
      setCode("");
      setInvalid(false);
      showToast("success", t("充值成功，余额已更新"));
      void queryClient.invalidateQueries({ queryKey: ["me-balance"] });
    },
    onError: (err) => {
      setInvalid(true);
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  const balance = balanceQuery.data?.balance_micro ?? 0;
  const ledger: BalanceEntry[] = balanceQuery.data?.ledger ?? [];

  const handleRedeem = () => {
    const trimmed = code.trim();
    if (trimmed === "") {
      setInvalid(true);
      return;
    }
    redeemMutation.mutate(trimmed);
  };

  return (
    <>
      <Panel>
        <PanelHeader
          title={t("余额")}
          description={t("用兑换码或链上转账充值，余额用于购买与续费套餐。")}
          meta={
            <Badge tone={balance > 0 ? "signal" : "neutral"}>
              {balance > 0 ? t("可用") : t("暂无余额")}
            </Badge>
          }
        />
        <PanelBody className="space-y-6">
          <div className="flex flex-wrap items-end justify-between gap-4 border-b border-rule pb-4">
            <Readout
              label={t("当前余额")}
              value={formatMicro(balance)}
              unit="USDT"
              tone={balance > 0 ? "signal" : "muted"}
              hint={t("金额以 USDT 计，保留六位小数。")}
            />
            <div className="flex items-center gap-2 text-xs text-ink-faint">
              <Coins size={14} />
              <span>{t("余额不可自助退款，如需退款请联系运营人员。")}</span>
            </div>
          </div>

          <div className="flex flex-wrap items-end gap-3">
            <Fieldset
              label={t("兑换码")}
              hint={t("输入充值码即可到账，码不区分大小写与分隔符。")}
              htmlFor="redeem-code"
              className="min-w-[16rem] flex-1"
            >
              <Input
                id="redeem-code"
                value={code}
                invalid={invalid}
                onChange={(event) => {
                  setCode(event.target.value);
                  setInvalid(false);
                }}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    handleRedeem();
                  }
                }}
                placeholder="XXXX-XXXX-XXXX-XXXX-XXXX"
                autoComplete="off"
                spellCheck={false}
                className="font-mono uppercase tracking-wider"
              />
            </Fieldset>
            <Button
              onClick={handleRedeem}
              disabled={redeemMutation.isPending}
              title={t("使用兑换码充值")}
            >
              <Ticket size={14} />
              <span>{redeemMutation.isPending ? t("兑换中...") : t("兑换")}</span>
            </Button>
            <Button variant="secondary" asChild title={t("转入 USDT (TRC20) 充值")}>
              <Link to={mePaths.deposit}>
                <Wallet size={14} />
                <span>{t("链上充值")}</span>
              </Link>
            </Button>
          </div>
        </PanelBody>
      </Panel>

      <Panel>
        <PanelHeader
          title={t("余额流水")}
          description={t("每一笔余额变动都有记录，可按来源追溯。")}
          meta={<Badge tone="neutral">{ledger.length}</Badge>}
        />
        <PanelBody className="p-0">
          {balanceQuery.isLoading ? (
            <LoadingState label={t("正在加载余额...")} className="py-8" />
          ) : balanceQuery.isError ? (
            <ErrorState
              message={formatApiErrorMessage(balanceQuery.error, t)}
              onRetry={() => void balanceQuery.refetch()}
              className="py-8"
            />
          ) : ledger.length === 0 ? (
            <EmptyState
              title={t("暂无余额变动")}
              hint={t("充值或消费后，流水会显示在这里。")}
              className="py-8"
            />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("时间")}</TH>
                    <TH>{t("来源")}</TH>
                    <TH className="text-right">{t("变动金额")}</TH>
                    <TH className="text-right">{t("变动后余额")}</TH>
                    <TH>{t("备注")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {ledger.map((entry) => (
                    <TR key={entry.id}>
                      <TD className="readout font-mono text-xs text-ink-soft">
                        {formatNsDateTime(entry.created_at_ns)}
                      </TD>
                      <TD className="text-xs text-ink">
                        {reasonLabel(entry.reason, t)}
                      </TD>
                      <TD
                        className={`readout text-right font-mono text-xs ${
                          entry.delta_micro >= 0 ? "text-signal" : "text-alert"
                        }`}
                      >
                        {entry.delta_micro >= 0 ? "+" : ""}
                        {formatMicro(entry.delta_micro)}
                      </TD>
                      <TD className="readout text-right font-mono text-xs text-ink">
                        {formatMicro(entry.balance_after_micro)}
                      </TD>
                      <TD className="max-w-xs truncate text-xs text-ink-faint" title={entry.note}>
                        {entry.note || entry.actor || "-"}
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          )}
        </PanelBody>
      </Panel>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </>
  );
}
