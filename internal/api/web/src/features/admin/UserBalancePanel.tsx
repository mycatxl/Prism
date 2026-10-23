import { useMutation } from "@tanstack/react-query";
import { useId, useState } from "react";
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
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Table, TableWrap, TBody, TD, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro, parseAmountToMicro } from "../../lib/money";
import { adjustUserBalance } from "./api";
import { useUserBalance } from "./userBalance";
import type { AdminBalanceEntry } from "./types";

/*
 * One user's money, from the operator's side, split the way the detail page
 * reads it: the ledger panel sits in the main column and the adjust dialog is
 * triggered from the operations column, both over the one shared cache entry
 * (`adminUserBalanceQueryKey`), so an adjustment lands in every reader at once.
 *
 * A manual movement is the only one with no external record behind it, so the
 * server refuses it without a note; the dialog asks for one and the ledger then
 * shows the note and the actor beside the amount.
 */

const NANOS_PER_MILLI = 1_000_000;

function formatNsTime(ns?: number | null): string {
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
 * The ledger: every movement of one account, newest first, with the balance it
 * left behind. The header carries the current balance as its one fact.
 */
export function UserBalanceLedgerPanel({ userId }: { userId: string }) {
  const { t } = useI18n();
  const balanceQuery = useUserBalance(userId);

  const balance = balanceQuery.data?.balance_micro ?? 0;
  const ledger: AdminBalanceEntry[] = balanceQuery.data?.ledger ?? [];

  return (
    <Panel>
      <PanelHeader
        title={t("余额流水")}
        description={t("余额用于购买与续费套餐，每一笔变动都记在流水里。")}
        meta={
          <Badge tone={balance > 0 ? "signal" : "neutral"}>
            {formatMicro(balance)} USDT
          </Badge>
        }
      />

      <PanelBody className="p-0">
        {balanceQuery.isLoading ? (
          <LoadingState label={t("正在加载余额...")} className="py-10" />
        ) : balanceQuery.isError ? (
          <ErrorState
            message={formatApiErrorMessage(balanceQuery.error, t)}
            onRetry={() => void balanceQuery.refetch()}
            className="m-4"
          />
        ) : ledger.length === 0 ? (
          <EmptyState
            title={t("暂无余额变动")}
            hint={t("兑换码充值或运营调整后，流水会显示在这里。")}
            className="py-10"
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
                    <TD className="readout whitespace-nowrap text-xs text-ink-soft">
                      {formatNsTime(entry.created_at_ns)}
                    </TD>
                    <TD className="text-xs text-ink">{reasonLabel(entry.reason, t)}</TD>
                    <TDNum
                      className={`whitespace-nowrap text-xs ${
                        entry.delta_micro >= 0 ? "text-signal" : "text-alert"
                      }`}
                    >
                      <span className="readout">
                        {entry.delta_micro >= 0 ? "+" : ""}
                        {formatMicro(entry.delta_micro)}
                      </span>
                    </TDNum>
                    <TDNum className="whitespace-nowrap text-xs">
                      <span className="readout">{formatMicro(entry.balance_after_micro)}</span>
                    </TDNum>
                    <TD
                      className="max-w-xs truncate text-xs text-ink-faint"
                      title={entry.note}
                    >
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
  );
}

/**
 * The operator's manual movement: a signed amount plus the note the audit trail
 * requires. The dialog validates what the server will refuse and reports what
 * it refused anyway; invalidation belongs to the caller's `onSuccess`.
 */
export function AdjustBalanceDialog({
  open,
  onOpenChange,
  userId,
  onSuccess,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  userId: string;
  onSuccess: () => void;
}) {
  const { t } = useI18n();
  const amountField = useId();
  const noteField = useId();
  const [amount, setAmount] = useState("");
  const [note, setNote] = useState("");
  const [errorMsg, setErrorMsg] = useState("");

  const mutation = useMutation({
    mutationFn: (deltaMicro: number) => adjustUserBalance(userId, deltaMicro, note.trim()),
    onSuccess: () => {
      setAmount("");
      setNote("");
      setErrorMsg("");
      onSuccess();
    },
    onError: (err) => setErrorMsg(formatApiErrorMessage(err, t)),
  });

  const handleSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    const trimmed = amount.trim();
    const negative = trimmed.startsWith("-");
    const parsed = parseAmountToMicro(negative ? trimmed.slice(1) : trimmed);
    if (parsed === null || parsed === 0) {
      setErrorMsg(t("请输入非零金额，最多六位小数。"));
      return;
    }
    if (note.trim() === "") {
      setErrorMsg(t("手动调整必须填写备注。"));
      return;
    }
    setErrorMsg("");
    mutation.mutate(negative ? -parsed : parsed);
  };

  return (
    <DialogRoot open={open} onOpenChange={onOpenChange}>
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="w-full max-w-md">
          <div className="border-b border-rule px-5 py-4">
            <DialogTitle>{t("调整余额")}</DialogTitle>
          </div>

          <form onSubmit={handleSubmit}>
            <div className="space-y-4 px-5 py-4">
              {errorMsg && (
                <div className="rounded-control border border-alert/30 bg-alert-wash p-3 text-xs text-alert">
                  {errorMsg}
                </div>
              )}

              <Fieldset
                label={t("变动金额 (USDT)")}
                hint={t("正数增加余额，负数扣减；最多六位小数。")}
                htmlFor={amountField}
              >
                <Input
                  id={amountField}
                  value={amount}
                  onChange={(event) => setAmount(event.target.value)}
                  inputMode="decimal"
                  placeholder="10.000000"
                  className="mono"
                />
              </Fieldset>

              <Fieldset
                label={t("备注")}
                hint={t("手动调整没有外部凭据，备注与操作人是唯一的审计线索。")}
                htmlFor={noteField}
              >
                <Input
                  id={noteField}
                  value={note}
                  onChange={(event) => setNote(event.target.value)}
                  placeholder={t("例如：工单 #123 补偿")}
                />
              </Fieldset>
            </div>

            <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
              <Button
                type="button"
                variant="secondary"
                onClick={() => onOpenChange(false)}
                disabled={mutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button type="submit" variant="primary" loading={mutation.isPending}>
                {t("确定")}
              </Button>
            </div>
          </form>
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}
