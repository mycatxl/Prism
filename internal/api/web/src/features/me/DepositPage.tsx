import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Table, TableWrap, TBody, TD, TDClip, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { cn } from "../../lib/cn";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro, parseAmountToMicro } from "../../lib/money";
import { getDepositInfo, getDepositIntents, openDeposit } from "./api";
import type { DepositIntent } from "./types";

const NANOS_PER_MILLI = 1_000_000;
const NANOS_PER_SECOND = 1_000_000_000;

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

function formatCountdown(seconds: number): string {
  const mm = Math.floor(seconds / 60);
  const ss = seconds % 60;
  return `${String(mm).padStart(2, "0")}:${String(ss).padStart(2, "0")}`;
}

function statusLabel(status: string, t: (text: string) => string): string {
  switch (status) {
    case "pending":
      return t("等待付款");
    case "unpaid":
      return t("已过期");
    case "paid":
      return t("已到账");
    case "released":
      return t("已释放");
    default:
      return status;
  }
}

/*
 * The status vocabulary is the server's (internal/model/user.go: pending,
 * unpaid, paid, released). Tone follows the same reading the labels do: a
 * payment still running is live, a closed window with the amount on hold is a
 * warning, a credited deposit is the success ink, a released reservation is the
 * one state that needs a person to look at it. Anything else is unknown, and
 * unknown is its own state - it renders the bare dot, not failure.
 */
type DepositTone = "signal" | "live" | "warn" | "alert" | "neutral";

function statusTone(status: string): DepositTone {
  switch (status) {
    case "pending":
      return "live";
    case "unpaid":
      return "warn";
    case "paid":
      return "signal";
    case "released":
      return "alert";
    default:
      return "neutral";
  }
}

/**
 * The standard state rendering: the dot carries the hue, the word carries the
 * meaning, so the state survives a screenshot into a ticket in greyscale.
 */
function DepositStatus({ status, t }: { status: string; t: (text: string) => string }) {
  const tone = statusTone(status);
  return (
    <span className="flex items-center gap-2">
      <span aria-hidden className={cn("state-dot", tone !== "neutral" && `state-dot--${tone}`)} />
      <span className="state-text">{statusLabel(status, t)}</span>
    </span>
  );
}

/** formatWindow reads the server's window_secs the way a person reads a span. */
function formatWindow(secs: number, t: (text: string) => string): string {
  if (secs > 0 && secs % 3600 === 0) return `${secs / 3600} ${t("小时")}`;
  if (secs > 0 && secs % 60 === 0) return `${secs / 60} ${t("分钟")}`;
  return `${secs} ${t("秒")}`;
}

/** pickActive returns the newest intent that is still worth showing. */
function pickActive(items: DepositIntent[]): DepositIntent | null {
  for (const item of items) {
    if (item.status === "pending" || item.status === "unpaid") return item;
  }
  return items.length > 0 ? items[0] : null;
}

/**
 * DepositPage is the on-chain top-up flow: ask for an amount, pay exactly the
 * amount the server hands back, and wait for the listener to credit it.
 *
 * The amount is what identifies the payer - a bare TRC20 transfer carries no
 * field for an identifier - so the page is emphatic that it has to be exact.
 * The address is shown and copied as plain text: the URI form with an amount is
 * rejected by TronLink, so nothing here builds one.
 */
export function DepositPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  const infoQuery = useQuery({ queryKey: ["me", "deposit", "info"], queryFn: getDepositInfo });
  const intentsQuery = useQuery({
    queryKey: ["me", "deposit", "intents"],
    queryFn: getDepositIntents,
    refetchInterval: 30_000,
  });

  const [amount, setAmount] = useState("");
  const [opened, setOpened] = useState<DepositIntent | null>(null);
  const [nowNs, setNowNs] = useState(() => Date.now() * NANOS_PER_MILLI);

  useEffect(() => {
    const id = window.setInterval(() => setNowNs(Date.now() * NANOS_PER_MILLI), 1000);
    return () => window.clearInterval(id);
  }, []);

  const minMicro = infoQuery.data?.min_micro ?? 1_000_000;
  const parsedMicro = useMemo(() => {
    if (!amount.trim()) return 0;
    return parseAmountToMicro(amount) ?? -1;
  }, [amount]);

  const openMutation = useMutation({
    mutationFn: (amountMicro: number) => openDeposit(amountMicro),
    onSuccess: (intent) => {
      setOpened(intent);
      setAmount("");
      void queryClient.invalidateQueries({ queryKey: ["me", "deposit"] });
      showToast("success", t("充值单已创建"));
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  if (infoQuery.isPending) {
    return <LoadingState label={t("加载中")} />;
  }
  if (infoQuery.isError) {
    return <ErrorState message={formatApiErrorMessage(infoQuery.error, t)} />;
  }
  if (!infoQuery.data?.enabled) {
    return (
      <div className="space-y-6">
        <PageHeader title={t("链上充值")} description={t("转入 USDT (TRC20)，到账后自动入余额")} />
        <Panel>
          <PanelBody>
            <EmptyState
              title={t("充值暂不可用")}
              hint={t("管理员尚未配置收款地址，请使用兑换码充值。")}
            />
          </PanelBody>
        </Panel>
      </div>
    );
  }

  const info = infoQuery.data;
  const activeIntent = opened ?? pickActive(intentsQuery.data?.items ?? []);
  const secondsLeft = activeIntent
    ? Math.max(0, Math.floor((activeIntent.expires_at_ns - nowNs) / NANOS_PER_SECOND))
    : 0;
  const holdLeft = activeIntent
    ? Math.max(0, Math.floor((activeIntent.hold_until_ns - nowNs) / NANOS_PER_SECOND))
    : 0;

  const amountTooSmall = parsedMicro > 0 && parsedMicro < minMicro;
  const canSubmit = parsedMicro >= minMicro && !openMutation.isPending;

  return (
    <div className="space-y-6">
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <PageHeader title={t("链上充值")} description={t("转入 USDT (TRC20)，到账后自动入余额")} />

      <Panel>
        <PanelBody className="flex flex-col gap-4">
          <Fieldset
            label={t("充值金额 (USDT)")}
            htmlFor="deposit-amount"
            hint={`${t("最小")} ${formatMicro(minMicro)} USDT`}
          >
            <Input
              id="deposit-amount"
              inputMode="decimal"
              value={amount}
              placeholder={formatMicro(minMicro)}
              invalid={amountTooSmall || undefined}
              onChange={(event) => setAmount(event.target.value)}
            />
          </Fieldset>
          {amountTooSmall ? (
            <p role="alert" className="text-sm text-alert">
              {t("低于最小充值额")}
            </p>
          ) : null}
          <div>
            <Button
              variant="primary"
              disabled={!canSubmit}
              onClick={() => openMutation.mutate(parsedMicro)}
            >
              {openMutation.isPending ? t("创建中") : t("生成充值单")}
            </Button>
          </div>
          <div className="flex flex-col gap-1.5 border-t border-rule pt-3 text-xs">
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-ink-faint">{t("USDT 合约")}</span>
              <code className="readout break-all text-ink-soft">{info.contract || "-"}</code>
            </div>
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-ink-faint">{t("金额范围")}</span>
              <span className="readout text-ink-soft">
                {formatMicro(info.min_micro)} – {formatMicro(info.max_micro)} USDT
              </span>
            </div>
            <div className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-ink-faint">{t("付款窗口")}</span>
              <span className="readout text-ink-soft">{formatWindow(info.window_secs, t)}</span>
            </div>
          </div>
        </PanelBody>
      </Panel>

      {activeIntent ? (
        <Panel>
          <PanelHeader
            title={t("本次转账信息")}
            description={t("必须精确转到下面的金额，多一位少一位都不会入账")}
            meta={<DepositStatus status={activeIntent.status} t={t} />}
          />
          <PanelBody className="flex flex-col gap-4">
            <div className="flex flex-col gap-1">
              <span className="text-xs text-ink-faint">{t("收款地址")}</span>
              <CopyRow
                value={info.address}
                label={t("复制")}
                onCopied={() => showToast("success", t("已复制"))}
              />
            </div>
            <div className="flex flex-col gap-1">
              <span className="text-xs text-ink-faint">{t("转账金额 (USDT)")}</span>
              <CopyRow
                value={formatMicro(activeIntent.amount_micro)}
                label={t("复制")}
                large
                onCopied={() => showToast("success", t("已复制"))}
              />
            </div>

            {activeIntent.tx_id ? (
              <div className="flex flex-col gap-1">
                <span className="text-xs text-ink-faint">{t("交易号")}</span>
                <CopyRow
                  value={activeIntent.tx_id}
                  label={t("复制")}
                  onCopied={() => showToast("success", t("已复制"))}
                />
              </div>
            ) : null}
            {activeIntent.status === "pending" ? (
              <>
                <p className="text-sm">
                  {t("剩余付款时间")}：
                  <span className="readout">{formatCountdown(secondsLeft)}</span>
                </p>
                <p className="text-xs text-ink-faint">
                  {t("付款截止")}{" "}
                  <span className="readout text-ink-soft">
                    {formatNsDateTime(activeIntent.expires_at_ns)}
                  </span>
                </p>
              </>
            ) : null}
            {activeIntent.status === "unpaid" ? (
              <>
                <p className="text-sm">
                  {t("付款窗口已结束，金额仍为你保留")}{" "}
                  <span className="readout">{formatCountdown(holdLeft)}</span>
                  {t("，此期间的到账仍会记入余额")}
                </p>
                <p className="text-xs text-ink-faint">
                  {t("保留截止")}{" "}
                  <span className="readout text-ink-soft">
                    {formatNsDateTime(activeIntent.hold_until_ns)}
                  </span>
                </p>
              </>
            ) : null}
            {activeIntent.status === "paid" ? <p className="text-sm">{t("已到账，余额已更新")}</p> : null}

            <p className="text-xs text-ink-faint">
              {t("只支持 TRC20 网络的 USDT，转错网络或金额将无法自动入账。")}
            </p>
          </PanelBody>
        </Panel>
      ) : null}

      <Panel>
        <PanelHeader title={t("充值记录")} />
        <PanelBody>
          {intentsQuery.isPending ? (
            <LoadingState label={t("加载中")} />
          ) : intentsQuery.isError ? (
            <ErrorState message={formatApiErrorMessage(intentsQuery.error, t)} />
          ) : (intentsQuery.data?.items.length ?? 0) === 0 ? (
            <EmptyState title={t("暂无充值记录")} />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("金额")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH>{t("创建时间")}</TH>
                    <TH>{t("交易号")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {intentsQuery.data?.items.map((intent) => (
                    <TR key={intent.id}>
                      <TDNum className="whitespace-nowrap">
                        <span className="readout">{formatMicro(intent.amount_micro)}</span>
                      </TDNum>
                      <TD>
                        <DepositStatus status={intent.status} t={t} />
                      </TD>
                      <TD className="readout whitespace-nowrap text-xs text-ink-soft">
                        {formatNsDateTime(intent.created_at_ns)}
                      </TD>
                      <TDClip className="readout text-xs">{intent.tx_id || "-"}</TDClip>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          )}
        </PanelBody>
      </Panel>
    </div>
  );
}

function CopyRow({
  value,
  label,
  large,
  onCopied,
}: {
  value: string;
  label: string;
  large?: boolean;
  onCopied: () => void;
}) {
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      onCopied();
    } catch {
      // Clipboard access needs a secure context. The value stays on screen so it
      // can still be selected by hand, which is why nothing is hidden behind it.
    }
  };
  return (
    <div className="flex items-center gap-2">
      <code className={cn("readout flex-1 break-all", large ? "text-lg" : "text-sm")}>{value}</code>
      <Button variant="secondary" size="sm" onClick={copy} title={label}>
        <Copy className="size-4" />
        {label}
      </Button>
    </div>
  );
}
