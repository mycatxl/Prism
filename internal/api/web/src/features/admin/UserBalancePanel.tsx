import { ControlTable, ControlTableBody, ControlTableCell, ControlTableColumn, ControlTableNumericCell, ControlTableRow, ControlTableHead, ControlTableWrap, QueryBadge, WorkspaceCard } from "../../components/control-plane";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro } from "../../lib/money";
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

  // A balance that has not loaded is unknown, not zero: the header must not
  // report "0.000000 USDT" for an account whose ledger simply has not arrived.
  const balance = balanceQuery.data?.balance_micro;
  const ledger: AdminBalanceEntry[] = balanceQuery.data?.ledger ?? [];

  return (
    <WorkspaceCard
      title={t("余额流水")}
      description={t("余额用于购买与续费套餐，每一笔变动都记在流水里。")}
      meta={
        balance === undefined ? (
          <QueryBadge label={balanceQuery.isError ? t("读取失败") : t("未知")} tone="neutral" />
        ) : (
          <QueryBadge label={`${formatMicro(balance)} USDT`} tone={balance > 0 ? "signal" : "neutral"} />
        )
      }
    >
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
        <ControlTableWrap>
          <ControlTable>
            <ControlTableHead>
              <ControlTableColumn isRowHeader>{t("时间")}</ControlTableColumn>
              <ControlTableColumn>{t("来源")}</ControlTableColumn>
              <ControlTableColumn className="text-right">{t("变动金额")}</ControlTableColumn>
              <ControlTableColumn className="text-right">{t("变动后余额")}</ControlTableColumn>
              <ControlTableColumn>{t("备注")}</ControlTableColumn>
            </ControlTableHead>
            <ControlTableBody>
              {ledger.map((entry) => (
                <ControlTableRow key={entry.id}>
                  <ControlTableCell className="readout whitespace-nowrap text-xs text-ink-soft">
                    {formatNsTime(entry.created_at_ns)}
                  </ControlTableCell>
                  <ControlTableCell className="text-xs text-ink">{reasonLabel(entry.reason, t)}</ControlTableCell>
                  <ControlTableNumericCell
                    className={`whitespace-nowrap text-xs ${
                      entry.delta_micro >= 0 ? "text-signal" : "text-alert"
                    }`}
                  >
                    <span className="readout">
                      {entry.delta_micro >= 0 ? "+" : ""}
                      {formatMicro(entry.delta_micro)}
                    </span>
                  </ControlTableNumericCell>
                  <ControlTableNumericCell className="whitespace-nowrap text-xs">
                    <span className="readout">{formatMicro(entry.balance_after_micro)}</span>
                  </ControlTableNumericCell>
                  <ControlTableCell
                    className="max-w-xs truncate text-xs text-ink-faint"
                    title={entry.note}
                  >
                    {entry.note || entry.actor || "-"}
                  </ControlTableCell>
                </ControlTableRow>
              ))}
            </ControlTableBody>
          </ControlTable>
        </ControlTableWrap>
      )}
    </WorkspaceCard>
  );
}
