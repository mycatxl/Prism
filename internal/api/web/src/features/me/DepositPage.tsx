import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Copy, Plus, RefreshCw } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import {
  ControlButton,
  ControlInput,
  DetailFact,
  EmptyWorkState,
  ObjectList,
  ObjectRow,
  WorkspacePage,
  QueryBadge,
  StatusMark,
  WorkspaceCard,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/control-plane";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { copyToClipboard } from "../../lib/clipboard";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro, parseAmountToMicro } from "../../lib/money";
import { getDepositInfo, getDepositIntents, openDeposit } from "./api";
import type { DepositIntent } from "./types";

const NANOS_PER_MILLI = 1_000_000;
const NANOS_PER_SECOND = 1_000_000_000;

type DepositTone = "signal" | "live" | "warn" | "alert" | "neutral";

/**
 * What the amount field holds right now. The four rejections are kept apart on
 * purpose: an empty field is not an error yet, but a seventh decimal, an amount
 * below the minimum and one above the ceiling each need their own sentence -
 * and only `valid` may reach the mutation.
 */
type AmountCheck =
  | { state: "empty" }
  | { state: "format" }
  | { state: "below" }
  | { state: "above" }
  | { state: "valid"; micro: number };

function formatNsDateTime(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
}

function formatCountdown(seconds: number): string {
  const mm = Math.floor(seconds / 60);
  const ss = seconds % 60;
  return `${String(mm).padStart(2, "0")}:${String(ss).padStart(2, "0")}`;
}

function statusLabel(status: string, t: (text: string) => string): string {
  if (status === "pending") return t("等待付款");
  if (status === "unpaid") return t("已过期");
  if (status === "paid") return t("已到账");
  if (status === "released") return t("已释放");
  return status;
}

function statusTone(status: string): DepositTone {
  if (status === "pending") return "live";
  if (status === "unpaid") return "warn";
  if (status === "paid") return "signal";
  if (status === "released") return "alert";
  return "neutral";
}

function formatWindow(seconds: number, t: (text: string) => string): string {
  if (seconds > 0 && seconds % 3600 === 0) return `${seconds / 3600} ${t("小时")}`;
  if (seconds > 0 && seconds % 60 === 0) return `${seconds / 60} ${t("分钟")}`;
  return `${seconds} ${t("秒")}`;
}

function pickActive(items: DepositIntent[]): DepositIntent | null {
  for (const item of items) {
    if (item.status === "pending" || item.status === "unpaid") return item;
  }
  return items.length > 0 ? items[0] : null;
}

function DepositStatus({ status, t }: { status: string; t: (text: string) => string }) {
  return <StatusMark tone={statusTone(status)} label={statusLabel(status, t)} />;
}

export function DepositPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const infoQuery = useQuery({ queryKey: ["me", "deposit", "info"], queryFn: getDepositInfo });
  const intentsQuery = useQuery({ queryKey: ["me", "deposit", "intents"], queryFn: getDepositIntents, refetchInterval: 30_000 });
  const [amount, setAmount] = useState("");
  const [opened, setOpened] = useState<DepositIntent | null>(null);
  const [nowNs, setNowNs] = useState(() => Date.now() * NANOS_PER_MILLI);

  useEffect(() => {
    const id = window.setInterval(() => setNowNs(Date.now() * NANOS_PER_MILLI), 1000);
    return () => window.clearInterval(id);
  }, []);

  const minMicro = infoQuery.data?.min_micro ?? 1_000_000;
  // 0 means the server exposes no ceiling; the deposit service uses a positive
  // maximum, so an absent one is read as "no upper bound" rather than "zero".
  const maxMicro = infoQuery.data?.max_micro ?? 0;
  /*
   * The typed amount is classified once. Collapsing every bad input into a -1
   * that only disabled the button hid *why* nothing could be created, and it
   * let an over-ceiling amount look valid; both are decided here instead.
   */
  const amountCheck = useMemo<AmountCheck>(() => {
    const raw = amount.trim();
    if (!raw) return { state: "empty" };
    const micro = parseAmountToMicro(raw);
    if (micro === null) return { state: "format" };
    if (micro < minMicro) return { state: "below" };
    if (maxMicro > 0 && micro > maxMicro) return { state: "above" };
    return { state: "valid", micro };
  }, [amount, maxMicro, minMicro]);
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

  const info = infoQuery.data;
  const activeIntent = opened ?? pickActive(intentsQuery.data?.items ?? []);
  const amountError =
    amountCheck.state === "format" ? t("请输入有效金额，最多六位小数。")
    : amountCheck.state === "below" ? t("低于最小充值额")
    : amountCheck.state === "above" ? t("超过最大充值额 {{max}} USDT", { max: formatMicro(maxMicro) })
    : null;
  const canSubmit = amountCheck.state === "valid" && !openMutation.isPending;

  return (
    <WorkspacePage
      eyebrow="Self Service / billing"
      title={t("链上充值")}
      description={t("转入 USDT (TRC20)，到账后自动入余额")}
      meta={<StatusMark tone={infoQuery.isError ? "warn" : infoQuery.data?.enabled ? "live" : "neutral"} label={infoQuery.isError ? t("读取失败") : infoQuery.isPending ? t("等待数据") : infoQuery.data?.enabled ? t("已启用") : t("暂不可用")} />}
      actions={<ControlButton variant="secondary" size="sm" onPress={() => { void infoQuery.refetch(); void intentsQuery.refetch(); }} loading={infoQuery.isFetching || intentsQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>}
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      {infoQuery.isPending ? <WorkspaceCard headerless eyebrow="Deposit service" title={t("充值服务")}><div className="workspace-register-state"><span className="workspace-spinner" aria-hidden />{t("加载中")}</div></WorkspaceCard> : null}
      {infoQuery.isError ? <WorkspaceCard headerless eyebrow="Deposit service" title={t("充值服务")}><EmptyWorkState title={t("读取失败")} detail={formatApiErrorMessage(infoQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void infoQuery.refetch()}>{t("重试")}</ControlButton>} /></WorkspaceCard> : null}
      {!infoQuery.isPending && !infoQuery.isError && info && !info.enabled ? <WorkspaceCard headerless eyebrow="Deposit service" title={t("充值服务")}><EmptyWorkState title={t("充值暂不可用")} detail={t("管理员尚未配置收款地址，请使用兑换码充值。")} /></WorkspaceCard> : null}
      {!infoQuery.isPending && !infoQuery.isError && info?.enabled ? (
        <>
          <WorkspaceCard eyebrow="Create deposit intent" title={t("生成充值单")} description={t("金额必须准确；充值单会返回本次转账应使用的收款地址和金额。")}>
            <div className="workspace-form-stack">
              <ControlInput
                label={t("充值金额 (USDT)")}
                hint={`${t("最小")} ${formatMicro(minMicro)} USDT`}
                inputMode="decimal"
                value={amount}
                placeholder={formatMicro(minMicro)}
                aria-invalid={amountError !== null || undefined}
                onChange={(event) => {
                  setAmount(event.target.value);
                  // A rejected attempt describes the amount that was just replaced.
                  if (openMutation.isError) openMutation.reset();
                }}
              />
              {amountError ? <p role="alert" className="workspace-form-error">{amountError}</p> : null}
              <div><ControlButton variant="primary" size="sm" onPress={() => { if (amountCheck.state === "valid") openMutation.mutate(amountCheck.micro); }} isDisabled={!canSubmit} loading={openMutation.isPending}><Plus size={14} aria-hidden />{openMutation.isPending ? t("创建中") : t("生成充值单")}</ControlButton></div>
              <dl className="workspace-detail-facts">
                <DetailFact label={t("USDT 合约")} value={info.contract || "-"} mono />
                <DetailFact label={t("金额范围")} value={`${formatMicro(info.min_micro)} – ${formatMicro(info.max_micro)} USDT`} mono />
                <DetailFact label={t("付款窗口")} value={formatWindow(info.window_secs, t)} />
              </dl>
            </div>
          </WorkspaceCard>
          {activeIntent ? <ActiveDeposit intent={activeIntent} info={info} nowNs={nowNs} t={t} showToast={showToast} /> : null}
          <WorkspaceCard eyebrow="Deposit history" title={t("充值记录")} meta={intentsQuery.data ? t("共 {{count}} 条", { count: intentsQuery.data.items.length }) : t("未加载")}>
            {intentsQuery.isPending ? <div className="workspace-register-state"><span className="workspace-spinner" aria-hidden />{t("加载中")}</div> : null}
            {intentsQuery.isError ? <EmptyWorkState title={t("读取失败")} detail={formatApiErrorMessage(intentsQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void intentsQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
            {!intentsQuery.isPending && !intentsQuery.isError && (intentsQuery.data?.items.length ?? 0) === 0 ? <EmptyWorkState title={t("暂无充值记录")} /> : null}
            {intentsQuery.data?.items.length ? <ObjectList>{intentsQuery.data.items.map((intent) => <ObjectRow key={intent.id} leading={<span aria-hidden>₮</span>} title={`${formatMicro(intent.amount_micro)} USDT`} subtitle={formatNsDateTime(intent.created_at_ns)} facts={<><span>{intent.tx_id || t("交易号未知")}</span><span>{intent.id}</span></>} status={<DepositStatus status={intent.status} t={t} />} action={<ArrowRight size={14} aria-hidden />} />)}</ObjectList> : null}
          </WorkspaceCard>
        </>
      ) : null}
    </WorkspacePage>
  );
}

function ActiveDeposit({
  intent,
  info,
  nowNs,
  t,
  showToast,
}: {
  intent: DepositIntent;
  info: { address: string; contract: string; min_micro: number; max_micro: number; window_secs: number };
  nowNs: number;
  t: (text: string, options?: Record<string, unknown>) => string;
  showToast: (tone: "success" | "error", message: string) => void;
}) {
  const secondsLeft = Math.max(0, Math.floor((intent.expires_at_ns - nowNs) / NANOS_PER_SECOND));
  const holdLeft = Math.max(0, Math.floor((intent.hold_until_ns - nowNs) / NANOS_PER_SECOND));
  return (
    <WorkspaceCard eyebrow="Current deposit intent" title={t("本次转账信息")} description={t("必须精确转到下面的金额，多一位少一位都不会入账。")} meta={<DepositStatus status={intent.status} t={t} />}>
      <div className="workspace-stack">
        <dl className="workspace-detail-facts">
          <DetailFact label={t("充值单")} value={intent.id} mono />
          <DetailFact label={t("收款地址")} value={<CopyRow value={info.address} label={t("复制")} onCopied={() => showToast("success", t("已复制"))} />} mono />
          <DetailFact label={t("转账金额 (USDT)")} value={<CopyRow value={formatMicro(intent.amount_micro)} label={t("复制")} onCopied={() => showToast("success", t("已复制"))} />} mono />
          <DetailFact label={t("交易号")} value={intent.tx_id ? <CopyRow value={intent.tx_id} label={t("复制")} onCopied={() => showToast("success", t("已复制"))} /> : "-"} mono />
        </dl>
        {intent.status === "pending" ? <p className="workspace-muted">{t("剩余付款时间")}：<span className="readout">{formatCountdown(secondsLeft)}</span> · {t("付款截止")} {formatNsDateTime(intent.expires_at_ns)}</p> : null}
        {intent.status === "unpaid" ? <p className="workspace-muted">{t("付款窗口已结束，金额仍为你保留")} <span className="readout">{formatCountdown(holdLeft)}</span>{t("，此期间的到账仍会记入余额")} · {t("保留截止")} {formatNsDateTime(intent.hold_until_ns)}</p> : null}
        {intent.status === "paid" ? <p className="workspace-muted">{t("已到账，余额已更新")}</p> : null}
        <QueryBadge label={t("只支持 TRC20 网络的 USDT，转错网络或金额将无法自动入账。")} tone="warn" />
      </div>
    </WorkspaceCard>
  );
}

function CopyRow({ value, label, onCopied }: { value: string; label: string; onCopied: () => void }) {
  const { t } = useI18n();
  const [copyError, setCopyError] = useState(false);
  const copy = () => {
    void copyToClipboard(value).then((ok) => {
      setCopyError(!ok);
      if (ok) onCopied();
    });
  };
  return <span className="flex min-w-0 items-center gap-2"><code className="readout min-w-0 flex-1 break-all text-xs">{value}</code><ControlButton variant="secondary" size="sm" onPress={copy} aria-label={label} isIconOnly><Copy size={14} aria-hidden /></ControlButton>{copyError ? <span className="workspace-form-error">{t("复制失败，请手动选择复制")}</span> : null}</span>;
}
