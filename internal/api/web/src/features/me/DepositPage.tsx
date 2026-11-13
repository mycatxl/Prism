import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { QRCodeSVG } from "qrcode.react";
import { Coins, Copy, ExternalLink, Plus, RefreshCw } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import {
  ControlButton,
  ControlInput,
  ControlTabs,
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
import { convertPaymentMicro, formatMicro, parseAmountToMicro } from "../../lib/money";
import { meBalanceQueryKey, createLDCPay, getDepositInfo, getDepositIntents, getLDCInfo, openDeposit } from "./api";
import type { DepositIntent } from "./types";

const NANOS_PER_MILLI = 1_000_000;
const NANOS_PER_SECOND = 1_000_000_000;

type DepositTone = "signal" | "live" | "warn" | "alert" | "neutral";

type AmountCheck =
  | { state: "empty" }
  | { state: "format" }
  | { state: "below" }
  | { state: "above" }
  | { state: "credit" }
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
  if (status === "pending") return t("等待付款或结算验证");
  if (status === "failed") return t("结算失败");
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
  const [method, setMethod] = useState("ldc");

  const ldcInfoQuery = useQuery({ queryKey: ["me", "ldc", "info"], queryFn: getLDCInfo });
  const infoQuery = useQuery({ queryKey: ["me", "deposit", "info"], queryFn: getDepositInfo });
  const intentsQuery = useQuery({ queryKey: ["me", "deposit", "intents"], queryFn: getDepositIntents, refetchInterval: 5_000 });

  const [ldcPoints, setLdcPoints] = useState("10");
  const [amount, setAmount] = useState("");
  const [opened, setOpened] = useState<DepositIntent | null>(null);
  const [nowNs, setNowNs] = useState(() => Date.now() * NANOS_PER_MILLI);

  useEffect(() => {
    const id = window.setInterval(() => setNowNs(Date.now() * NANOS_PER_MILLI), 1000);
    return () => window.clearInterval(id);
  }, []);

  // LDC payment mutation
  const ldcPayMutation = useMutation({
    mutationFn: (pts: number) => createLDCPay(pts),
    onSuccess: (data) => {
      showToast("success", t("已发起充值，正在跳转收银台..."));
      if (data.redirect_url) {
        window.location.href = data.redirect_url;
      }
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const ldcPointsMicro = parseAmountToMicro(ldcPoints);
  const parsedLdcPoints = (ldcPointsMicro ?? 0) / 1_000_000;
  const ldcRate = ldcInfoQuery.data?.rate ?? 1;
  const ldcCreditMicro = convertPaymentMicro(ldcPointsMicro ?? 0, ldcRate);
  const isLdcPointsValid = ldcCreditMicro !== null && /^\d+(\.\d{1,2})?$/.test(ldcPoints.trim()) && parsedLdcPoints > 0 && parsedLdcPoints <= 1_000_000;
  const ldcPointsError = ldcPoints.trim() !== "" && !isLdcPointsValid ? t("请输入 0.01 至 1000000 的积分数量，最多两位小数，到账余额不得超过 1000000 USDT。") : null;
  const currency = method === "trx" ? "trx" : "usdt";
  const unit = currency.toUpperCase();

  const minMicro = infoQuery.data?.min_micro ?? 1_000_000;
  const maxMicro = infoQuery.data?.max_micro ?? 0;
  const conversionRate = currency === "trx" ? (infoQuery.data?.trx_ratio ?? 0) : 1;

  const amountCheck = useMemo<AmountCheck>(() => {
    const raw = amount.trim();
    if (!raw) return { state: "empty" };
    const micro = parseAmountToMicro(raw);
    if (micro === null) return { state: "format" };
    if (micro < minMicro) return { state: "below" };
    if (maxMicro > 0 && micro > maxMicro) return { state: "above" };
    if (convertPaymentMicro(micro + 100, conversionRate) === null) return { state: "credit" };
    return { state: "valid", micro };
  }, [amount, maxMicro, minMicro, conversionRate]);

  const openMutation = useMutation({
    mutationFn: (amountMicro: number) => openDeposit(amountMicro, currency),
    onSuccess: (intent) => {
      setOpened(intent);
      setAmount("");
      void queryClient.invalidateQueries({ queryKey: ["me", "deposit"] });
      showToast("success", t("充值单已创建"));
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const info = infoQuery.data;
  const matchingIntents = (intentsQuery.data?.items ?? []).filter((intent) => (intent.currency ?? "usdt") === currency);
  const activeIntent = (opened && (opened.currency ?? "usdt") === currency ? matchingIntents.find((intent) => intent.id === opened.id) ?? opened : null) ?? pickActive(matchingIntents);
  useEffect(() => {
    if (activeIntent?.status === "paid") void queryClient.invalidateQueries({ queryKey: meBalanceQueryKey });
  }, [activeIntent?.id, activeIntent?.status, queryClient]);
  const amountError =
    amountCheck.state === "format" ? t("请输入有效金额，最多六位小数。")
    : amountCheck.state === "below" ? t("低于最小充值额")
    : amountCheck.state === "above" ? t("超过最大充值额 {{max}} {{currency}}", { max: formatMicro(maxMicro), currency: unit })
    : amountCheck.state === "credit" ? t("到账余额必须至少为 0.000001 USDT，且不得超过 1000000 USDT。")
    : null;
  const canSubmit = amountCheck.state === "valid" && !openMutation.isPending;

  return (
    <WorkspacePage
      eyebrow="Self Service / billing"
      title={t("充值")}
      description={t("选择 LinuxDO 积分、USDT 或 TRX 充值账户余额")}
      actions={
        <ControlButton
          variant="secondary"
          size="sm"
          onPress={() => {
            void ldcInfoQuery.refetch();
            void infoQuery.refetch();
            void intentsQuery.refetch();
          }}
          loading={infoQuery.isFetching || intentsQuery.isFetching || ldcInfoQuery.isFetching}
        >
          <RefreshCw size={14} aria-hidden />
          {t("刷新")}
        </ControlButton>
      }
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <WorkspaceCard title={t("结算与到账")}>
        <p className="workspace-muted">{t("付款后需等待结算验证；返回本站或支付方显示成功不代表余额已入账。请刷新余额和充值记录，勿重复付款。")}</p>
        <p className="workspace-muted">{t("若结算失败或付款后仍未入账，请保留订单号和交易号联系运营核对。验证通过前不会增加余额，不承诺到账时长。")}</p>
      </WorkspaceCard>

      <WorkspaceCard eyebrow="Method" title={t("充值方式")}>
        <ControlTabs
          items={[
            { id: "ldc", label: t("Linux.do 积分 (EasyPay)") },
            { id: "usdt", label: t("USDT (TRC20 链上)") },
            { id: "trx", label: t("TRX (波场主币)") },
          ]}
          selectedKey={method}
          onSelectionChange={setMethod}
          label={t("充值方式")}
        />
      </WorkspaceCard>

      {method === "ldc" ? (
        <WorkspaceCard
          eyebrow="Linux.do Credit"
          title={t("快捷积分充值")}
          description={t("通过 Linux.do 论坛积分兑换账户余额，结算验证通过后入账。")}
          meta={
            <StatusMark
              tone={ldcInfoQuery.data?.enabled ? "signal" : "neutral"}
              label={ldcInfoQuery.data?.enabled ? t("已启用") : t("暂不可用")}
            />
          }
        >
          {ldcInfoQuery.isPending ? <p className="workspace-muted">{t("加载中")}</p> : ldcInfoQuery.isError ? <EmptyWorkState embedded title={t("读取失败")} detail={formatApiErrorMessage(ldcInfoQuery.error,t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void ldcInfoQuery.refetch()}>{t("重试")}</ControlButton>} /> : !ldcInfoQuery.data?.enabled ? (
            <EmptyWorkState embedded
              title={t("Linux.do 充值服务未开启")}
              detail={t("管理员尚未配置 Linux.do Credit PID 或 Key，请使用其他方式充值。")}
            />
          ) : (
            <div className="workspace-form-stack">
              <ControlInput
                label={t("充值积分数量")}
                hint={t("1 积分 = {{rate}} USDT 账户余额", { rate: ldcRate })}
                inputMode="decimal"
                value={ldcPoints}
                aria-invalid={ldcPointsError !== null || undefined}
                onChange={(event) => setLdcPoints(event.target.value)}
              />
              {ldcPointsError ? <p role="alert" className="workspace-form-error">{ldcPointsError}</p> : null}

              <div className="flex flex-wrap items-center gap-2">
                <span className="text-xs text-ink-faint">{t("快捷选择")}:</span>
                {[5, 10, 20, 50, 100].map((pts) => (
                  <ControlButton
                    key={pts}
                    variant={ldcPoints === String(pts) ? "primary" : "secondary"}
                    size="sm"
                    onPress={() => setLdcPoints(String(pts))}
                  >
                    {pts} {t("积分")}
                  </ControlButton>
                ))}
              </div>

              <div>
                <ControlButton
                  variant="primary"
                  size="sm"
                  onPress={() => {
                    if (isLdcPointsValid) {
                      ldcPayMutation.mutate(parsedLdcPoints);
                    }
                  }}
                  isDisabled={!isLdcPointsValid || !ldcInfoQuery.data?.enabled || ldcPayMutation.isPending}
                  loading={ldcPayMutation.isPending}
                >
                  <Coins size={14} aria-hidden />
                  {ldcPayMutation.isPending ? t("充值单创建中") : t("前往收银台支付")}
                  <ExternalLink size={12} className="ml-1" aria-hidden />
                </ControlButton>
              </div>

              <dl className="workspace-detail-facts">
                <DetailFact label={t("兑换比例")} value={`1 LDC = ${ldcRate} USDT`} mono />
                <DetailFact label={t("预计到账余额")} value={`${isLdcPointsValid ? formatMicro(ldcCreditMicro ?? 0) : "—"} USDT`} mono />
                <DetailFact label={t("到账方式")} value={t("支付回调验证通过后入账")} />
              </dl>
            </div>
          )}
        </WorkspaceCard>
      ) : null}

      {method === "usdt" || method === "trx" ? (
        <>
          {infoQuery.isPending ? (
            <WorkspaceCard headerless eyebrow="Deposit service" title={t("充值服务")}>
              <div className="workspace-register-state">
                <span className="workspace-spinner" aria-hidden />
                {t("加载中")}
              </div>
            </WorkspaceCard>
          ) : null}
          {infoQuery.isError ? (
            <WorkspaceCard headerless eyebrow="Deposit service" title={t("充值服务")}>
              <EmptyWorkState embedded
                title={t("读取失败")}
                detail={formatApiErrorMessage(infoQuery.error, t)}
                action={
                  <ControlButton variant="secondary" size="sm" onPress={() => void infoQuery.refetch()}>
                    {t("重试")}
                  </ControlButton>
                }
              />
            </WorkspaceCard>
          ) : null}
          {!infoQuery.isPending && !infoQuery.isError && info && (!info.enabled || (currency === "trx" && !info.trx_enabled)) ? (
            <WorkspaceCard headerless eyebrow="Deposit service" title={t("充值服务")}>
              <EmptyWorkState embedded title={t("充值暂不可用")} detail={t("管理员尚未配置收款地址，请使用兑换码充值。")} />
            </WorkspaceCard>
          ) : null}
          {!infoQuery.isPending && !infoQuery.isError && info?.enabled && (currency !== "trx" || info.trx_enabled) ? (
            <>
              <WorkspaceCard
                eyebrow="Create deposit intent"
                title={t("生成充值单")}
                description={t("金额必须准确；充值单会返回本次转账应使用的收款地址和金额。")}
              >
                <div className="workspace-form-stack">
                  <ControlInput
                    label={currency === "trx" ? t("充值金额 (TRX)") : t("充值金额 (USDT)")}
                    hint={`${t("最小")} ${formatMicro(minMicro)} ${unit}`}
                    inputMode="decimal"
                    value={amount}
                    placeholder={formatMicro(minMicro)}
                    aria-invalid={amountError !== null || undefined}
                    onChange={(event) => {
                      setAmount(event.target.value);
                      if (openMutation.isError) openMutation.reset();
                    }}
                  />
                  {amountError ? <p role="alert" className="workspace-form-error">{amountError}</p> : null}
                  <div>
                    <ControlButton
                      variant="primary"
                      size="sm"
                      onPress={() => {
                        if (amountCheck.state === "valid") openMutation.mutate(amountCheck.micro);
                      }}
                      isDisabled={!canSubmit}
                      loading={openMutation.isPending}
                    >
                      <Plus size={14} aria-hidden />
                      {openMutation.isPending ? t("创建中") : t("生成充值单")}
                    </ControlButton>
                  </div>
                  <dl className="workspace-detail-facts">
                    {currency === "usdt" ? <DetailFact label={t("USDT 合约")} value={info.contract || "-"} mono /> : <DetailFact label={t("兑换比例")} value={`1 TRX = ${info.trx_ratio} USDT`} mono />}
                    <DetailFact label={t("金额范围")} value={`${formatMicro(info.min_micro)} – ${formatMicro(info.max_micro)} ${unit}`} mono />
                    <DetailFact label={t("付款窗口")} value={formatWindow(info.window_secs, t)} />
                  </dl>
                </div>
              </WorkspaceCard>

            </>
          ) : null}
          {activeIntent ? <ActiveDeposit intent={activeIntent} nowNs={nowNs} t={t} showToast={showToast} /> : null}
        </>
      ) : null}

      <WorkspaceCard
        eyebrow="Deposit history"
        title={t("充值记录")}
        meta={intentsQuery.data ? t("共 {{count}} 条", { count: intentsQuery.data.items.length }) : t("未加载")}
      >
        {intentsQuery.isPending ? (
          <div className="workspace-register-state">
            <span className="workspace-spinner" aria-hidden />
            {t("加载中")}
          </div>
        ) : null}
        {intentsQuery.isError ? (
          <EmptyWorkState embedded
            title={t("读取失败")}
            detail={formatApiErrorMessage(intentsQuery.error, t)}
            action={
              <ControlButton variant="secondary" size="sm" onPress={() => void intentsQuery.refetch()}>
                {t("重试")}
              </ControlButton>
            }
          />
        ) : null}
        {!intentsQuery.isPending && !intentsQuery.isError && (intentsQuery.data?.items.length ?? 0) === 0 ? (
          <EmptyWorkState embedded title={t("暂无充值记录")} />
        ) : null}
        {intentsQuery.data?.items.length ? (
          <ObjectList>
            {intentsQuery.data.items.map((intent) => (
              <ObjectRow
                key={intent.id}
                leading={<span aria-hidden>₮</span>}
                title={`${formatMicro(intent.amount_micro)} ${(intent.currency ?? "usdt").toUpperCase()}`}
                subtitle={formatNsDateTime(intent.created_at_ns)}
                facts={
                  <>
                    <span>{intent.tx_id || t("交易号未知")}</span>
                    <span>{intent.id}</span>
                    {intent.status === "pending" ? <span>{t("等待结算验证，请勿重复付款。")}</span> : null}
                    {["failed", "unpaid", "released"].includes(intent.status) ? <span>{t("尚未完成结算；如已付款，请保留交易凭证联系运营核对。")}</span> : null}
                  </>
                }
                status={<DepositStatus status={intent.status} t={t} />}
                onClick={() => { setOpened(intent); setMethod(intent.currency ?? "usdt"); }}
              />
            ))}
          </ObjectList>
        ) : null}
      </WorkspaceCard>
    </WorkspacePage>
  );
}

function ActiveDeposit({
  intent,
  nowNs,
  t,
  showToast,
}: {
  intent: DepositIntent;
  nowNs: number;
  t: (text: string, options?: Record<string, unknown>) => string;
  showToast: (tone: "success" | "error", message: string) => void;
}) {
  const secondsLeft = Math.max(0, Math.floor((intent.expires_at_ns - nowNs) / NANOS_PER_SECOND));
  const holdLeft = Math.max(0, Math.floor((intent.hold_until_ns - nowNs) / NANOS_PER_SECOND));
  return (
    <WorkspaceCard eyebrow="Current deposit intent" title={t("本次转账信息")} description={t("必须精确转到下面的金额，多一位少一位都不会入账。")} meta={<DepositStatus status={intent.status} t={t} />}>
      <div className="workspace-stack">
        {intent.status === "pending" && secondsLeft > 0 ? <figure className="workspace-payment-qr"><QRCodeSVG value={intent.address} size={160} marginSize={4} title={t("收款地址二维码")} /><figcaption className="workspace-muted">{t("扫码填写地址后，请核对币种和精确金额。")}</figcaption></figure> : null}
        <dl className="workspace-detail-facts">
          <DetailFact label={t("到账余额")} value={`${formatMicro(intent.credit_micro ?? intent.amount_micro)} USDT`} mono />
          <DetailFact label={t("兑换比例")} value={`1 ${(intent.currency ?? "usdt").toUpperCase()} = ${intent.rate ?? 1} USDT`} mono />
          <DetailFact label={t("充值单")} value={intent.id} mono />
          <DetailFact label={t("收款地址")} value={<CopyRow value={intent.address} label={t("复制")} onCopied={() => showToast("success", t("已复制"))} />} mono />
          <DetailFact label={(intent.currency ?? "usdt") === "trx" ? t("转账金额 (TRX)") : t("转账金额 (USDT)")} value={<CopyRow value={formatMicro(intent.amount_micro)} label={t("复制")} onCopied={() => showToast("success", t("已复制"))} />} mono />
          <DetailFact label={t("交易号")} value={intent.tx_id ? <CopyRow value={intent.tx_id} label={t("复制")} onCopied={() => showToast("success", t("已复制"))} /> : "-"} mono />
        </dl>
        {intent.status === "pending" && secondsLeft === 0 ? <p className="workspace-form-error">{t("付款窗口已结束，请勿继续转账。")}</p> : null}
        {intent.status === "pending" && secondsLeft > 0 ? <p className="workspace-muted">{t("剩余付款时间")}：<span className="readout">{formatCountdown(secondsLeft)}</span> · {t("付款截止")} {formatNsDateTime(intent.expires_at_ns)}</p> : null}
        {intent.status === "unpaid" ? <p className="workspace-muted">{t("付款窗口已结束，金额仍为你保留")} <span className="readout">{formatCountdown(holdLeft)}</span>{t("，此期间的到账仍会记入余额")} · {t("保留截止")} {formatNsDateTime(intent.hold_until_ns)}</p> : null}
        {intent.status === "paid" ? <p className="workspace-muted">{t("已到账，余额已更新")}</p> : null}
        <QueryBadge label={intent.currency === "trx" ? t("只支持 TRON 网络的原生 TRX，请勿发送其他币种。") : t("只支持 TRC20 网络的 USDT，转错网络或金额将无法自动入账。")} tone="warn" />
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
