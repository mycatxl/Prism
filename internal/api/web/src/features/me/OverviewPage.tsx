import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, ArrowUpRight, KeyRound, RefreshCw, ShoppingCart, WalletCards } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import {
  ControlButton,
  ControlInput,
  ControlSwitch,
  DetailFact,
  EmptyWorkState,
  InspectorDrawer,
  ObjectList,
  ObjectRow,
  PageFrame,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatBytes } from "../../lib/bytes";
import { useNow } from "../../lib/clock";
import { formatApiErrorMessage } from "../../lib/error-message";
import { newRequestID } from "../../lib/idempotency";
import { formatMicro } from "../../lib/money";
import { mePaths } from "../../lib/paths";
import { formatGoDuration } from "../../lib/time";
import {
  BALANCE_LEDGER_LIMIT,
  getBalance,
  getOverview,
  getSessions,
  meBalanceQueryKey,
  redeemCode,
  renew,
  revokeSession,
  setAutoRenew,
} from "./api";
import type { BalanceEntry, PurchaseResult, SubscriptionView } from "./types";

const NANOS_PER_MILLI = 1_000_000;
const SUBSCRIPTION_WARN_MS = 48 * 3_600_000;

type StateTone = "neutral" | "signal" | "live" | "warn" | "alert";

function formatNsDate(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, { year: "numeric", month: "2-digit", day: "2-digit" }).format(date);
}

function formatNsDateTime(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(date);
}

function subscriptionState(status: string, t: (text: string) => string): { label: string; tone: StateTone } {
  if (status === "active") return { label: t("生效中"), tone: "signal" };
  if (status === "expired") return { label: t("已过期"), tone: "alert" };
  if (status === "suspended") return { label: t("已停用"), tone: "warn" };
  return { label: status, tone: "neutral" };
}

function remainingTone(remainingMs: number): "signal" | "warn" | "alert" {
  if (remainingMs <= 0) return "alert";
  if (remainingMs <= SUBSCRIPTION_WARN_MS) return "warn";
  return "signal";
}

function formatRemainingDuration(remainingMs: number): string {
  const totalSeconds = Math.max(0, Math.floor(remainingMs / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const parts: string[] = [];
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0) parts.push(`${minutes}m`);
  if (hours === 0 && seconds > 0) parts.push(`${seconds}s`);
  return formatGoDuration(parts.join("") || "0s");
}

function SubscriptionRemaining({ subscription }: { subscription: SubscriptionView }) {
  const { t } = useI18n();
  const now = useNow();
  const startsMs = Math.round(subscription.starts_at_ns / NANOS_PER_MILLI);
  const expiresMs = Math.round(subscription.expires_at_ns / NANOS_PER_MILLI);
  const remainingMs = expiresMs - now;
  const windowMs = expiresMs - startsMs;
  if (windowMs <= 0) return <StatusMark tone={remainingMs <= 0 ? "alert" : "neutral"} label={remainingMs <= 0 ? t("已过期") : "-"} />;
  return <StatusMark tone={remainingTone(remainingMs)} label={remainingMs <= 0 ? t("已过期") : formatRemainingDuration(remainingMs)} />;
}

function BalanceSurface({
  balanceQuery,
  onBalanceChanged,
}: {
  balanceQuery: ReturnType<typeof useQuery<Awaited<ReturnType<typeof getBalance>>>>;
  onBalanceChanged: () => void;
}) {
  const { t } = useI18n();
  const { showToast } = useToast();
  const [code, setCode] = useState("");
  const [invalid, setInvalid] = useState(false);
  const redeemMutation = useMutation({
    mutationFn: (value: string) => redeemCode(value),
    onSuccess: () => {
      setCode("");
      setInvalid(false);
      showToast("success", t("充值成功，余额已更新"));
      onBalanceChanged();
    },
    onError: (error) => {
      setInvalid(true);
      showToast("error", formatApiErrorMessage(error, t));
    },
  });
  const balance = balanceQuery.data?.balance_micro ?? 0;
  const ledger: BalanceEntry[] = balanceQuery.data?.ledger ?? [];
  const submit = () => {
    const value = code.trim();
    if (!value) {
      setInvalid(true);
      return;
    }
    redeemMutation.mutate(value);
  };

  return (
    <WorkSurface
      eyebrow="Balance ledger"
      title={t("余额")}
      description={t("用兑换码或链上转账充值，余额用于购买与续费套餐。")}
      meta={<StatusMark tone={balance > 0 ? "signal" : "neutral"} label={balance > 0 ? t("可用") : t("暂无余额")} />}
      actions={<Link className="link-quiet text-xs text-accent" to={mePaths.deposit}><WalletCards size={14} aria-hidden />{t("链上充值")} <ArrowUpRight size={13} aria-hidden /></Link>}
    >
      <div className="cp-stack">
        <div className="cp-data-grid">
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("当前余额")}</span><strong className="cp-data-grid__value">{balanceQuery.data ? formatMicro(balance) : "—"}</strong><span className="cp-data-grid__hint">USDT</span></div>
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("流水记录")}</span><strong className="cp-data-grid__value">{balanceQuery.data ? ledger.length : "—"}</strong></div>
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("兑换方式")}</span><strong className="cp-data-grid__value cp-data-grid__value--text">{t("兑换码")}</strong></div>
          <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("金额单位")}</span><strong className="cp-data-grid__value cp-data-grid__value--text">USDT</strong></div>
        </div>
        <form className="flex flex-wrap items-end gap-3" onSubmit={(event) => { event.preventDefault(); submit(); }}>
          <ControlInput label={t("兑换码")} hint={t("输入充值码即可到账，码不区分大小写与分隔符。")} value={code} aria-invalid={invalid || undefined} onChange={(event) => { setCode(event.target.value); setInvalid(false); }} placeholder="XXXX-XXXX-XXXX-XXXX-XXXX" autoComplete="off" spellCheck={false} className="min-w-[16rem] flex-1" />
          <ControlButton type="submit" variant="primary" size="sm" loading={redeemMutation.isPending}><WalletCards size={14} aria-hidden />{t("兑换")}</ControlButton>
        </form>
        {balanceQuery.isLoading ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载余额...")}</div> : null}
        {balanceQuery.isError ? <EmptyWorkState title={t("读取失败")} detail={formatApiErrorMessage(balanceQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void balanceQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
        {!balanceQuery.isLoading && !balanceQuery.isError && ledger.length === 0 ? <EmptyWorkState title={t("暂无余额变动")} detail={t("充值或消费后，流水会显示在这里。")} /> : null}
        {ledger.length > 0 ? <ObjectList>{ledger.map((entry) => <ObjectRow key={entry.id} leading={<span aria-hidden>{entry.delta_micro >= 0 ? "+" : "−"}</span>} title={entry.reason || t("余额变动")} subtitle={formatNsDateTime(entry.created_at_ns)} facts={<><span>{entry.delta_micro >= 0 ? "+" : ""}{formatMicro(entry.delta_micro)} USDT</span><span>{entry.note || entry.actor || "-"}</span></>} status={<StatusMark tone={entry.delta_micro >= 0 ? "signal" : "alert"} label={formatMicro(entry.balance_after_micro)} />} action={<span className="readout text-2xs">{entry.request_id || entry.ref_id || "-"}</span>} />)}</ObjectList> : null}
      </div>
    </WorkSurface>
  );
}

export function OverviewPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [renewing, setRenewing] = useState(false);
  const overviewQuery = useQuery({ queryKey: ["me-overview"], queryFn: getOverview });
  const sessionsQuery = useQuery({ queryKey: ["me-sessions"], queryFn: getSessions });
  const balanceQuery = useQuery({ queryKey: meBalanceQueryKey, queryFn: () => getBalance(BALANCE_LEDGER_LIMIT), staleTime: 30_000 });
  const autoRenewMutation = useMutation({
    mutationFn: (input: { subscriptionID: string; autoRenew: boolean }) => setAutoRenew(input.subscriptionID, input.autoRenew),
    onSuccess: (_result, input) => {
      showToast("success", input.autoRenew ? t("已开启自动续费") : t("已关闭自动续费"));
      void queryClient.invalidateQueries({ queryKey: ["me-overview"] });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const revokeMutation = useMutation({
    mutationFn: (sessionID: string) => revokeSession(sessionID),
    onSuccess: () => {
      showToast("success", t("会话已撤销"));
      void queryClient.invalidateQueries({ queryKey: ["me-sessions"] });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const overview = overviewQuery.data;
  const subscription = overview?.subscription ?? null;
  const sessions = sessionsQuery.data?.items ?? [];
  const dailyItems = overview?.usage_daily ?? [];
  const refreshing = overviewQuery.isFetching || sessionsQuery.isFetching || balanceQuery.isFetching;
  const refresh = () => {
    void overviewQuery.refetch();
    void sessionsQuery.refetch();
    void balanceQuery.refetch();
  };
  const subscriptionTone = subscription ? subscriptionState(subscription.status, t) : null;

  return (
    <PageFrame
      eyebrow="Self Service / overview"
      title={t("概览")}
      description={t("先确认套餐余量，再管理接入密钥、平台范围和当前会话。")}
      meta={<StatusMark tone={overviewQuery.isFetching ? "warn" : overviewQuery.isSuccess ? "live" : "neutral"} label={overviewQuery.isFetching ? t("更新中") : overviewQuery.isSuccess ? t("已同步") : t("未加载")} />}
      actions={<ControlButton variant="secondary" size="sm" onPress={refresh} loading={refreshing}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>}
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      {overviewQuery.isLoading ? <WorkSurface eyebrow="Tenant workspace" title={t("概览")}><div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载概览数据...")}</div></WorkSurface> : null}
      {overviewQuery.isError ? <WorkSurface eyebrow="Tenant workspace" title={t("概览")}><EmptyWorkState title={t("读取失败")} detail={formatApiErrorMessage(overviewQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void overviewQuery.refetch()}>{t("重试")}</ControlButton>} /></WorkSurface> : null}
      {overview ? (
        <>
          <div className="cp-data-grid">
            <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("当前套餐")}</span><strong className="cp-data-grid__value cp-data-grid__value--text">{subscription?.plan_name || t("暂无有效订阅")}</strong><span className="cp-data-grid__hint">{subscriptionTone ? <StatusMark tone={subscriptionTone.tone} label={subscriptionTone.label} /> : t("需要购买")}</span></div>
            <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("周期流量")}</span><strong className="cp-data-grid__value">{formatBytes(overview.period_used_bytes)}</strong><span className="cp-data-grid__hint">{overview.remaining_bytes === null ? t("无限制") : t("剩余 {{value}}", { value: formatBytes(Math.max(0, overview.remaining_bytes)) })}</span></div>
            <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("当前余额")}</span><strong className="cp-data-grid__value">{balanceQuery.data ? formatMicro(balanceQuery.data.balance_micro) : "—"}</strong><span className="cp-data-grid__hint">{balanceQuery.isError ? t("读取失败") : t("USDT，可用于购买和续费")}</span></div>
            <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("访问额度")}</span><strong className="cp-data-grid__value">{overview.max_keys > 0 ? `${overview.active_keys} / ${overview.max_keys}` : `${overview.active_keys} / ${t("无限制")}`}</strong><span className="cp-data-grid__hint">{t("实时连接 {{count}} 路", { count: overview.live_concurrent })}</span></div>
          </div>

          <WorkSurface eyebrow="Access workspace" title={t("接入工作台")} description={t("密钥是访问平台代理的主入口，平台授权和连接状态在这里快速确认。")} meta={t("{{count}} 个平台", { count: overview.platforms?.length ?? 0 })} actions={<><Link className="link-quiet text-xs text-accent" to={mePaths.keys}><KeyRound size={14} aria-hidden />{t("管理密钥")}</Link><Link className="link-quiet text-xs text-accent" to={mePaths.guide}>{t("接入指南")} <ArrowUpRight size={13} aria-hidden /></Link></>}>
            <ObjectList>
              <ObjectRow leading={<KeyRound size={14} aria-hidden />} title={t("访问密钥")} subtitle={overview.max_keys > 0 ? t("已使用 {{active}} / {{max}}", { active: overview.active_keys, max: overview.max_keys }) : t("已创建 {{active}} 个", { active: overview.active_keys })} facts={<span>{t("打开密钥工作面管理凭据")}</span>} status={<StatusMark tone={overview.max_keys > 0 && overview.active_keys >= overview.max_keys ? "warn" : "signal"} label={overview.max_keys > 0 && overview.active_keys >= overview.max_keys ? t("配额已满") : t("可用")} />} action={<Link className="link-quiet" to={mePaths.keys}><ArrowUpRight size={14} aria-hidden /></Link>} />
              <ObjectRow leading={<Activity size={14} aria-hidden />} title={t("授权平台")} subtitle={overview.platforms?.length ? overview.platforms.map((platform) => platform.platform_name || platform.platform_id).join(", ") : t("暂无限制或未授权")} facts={<span>{t("当前套餐允许的平台代理范围")}</span>} status={<StatusMark tone={overview.platforms?.length ? "signal" : "neutral"} label={overview.platforms?.length ? t("可用") : t("未知")} />} action={<Link className="link-quiet" to={mePaths.guide}><ArrowUpRight size={14} aria-hidden /></Link>} />
              <ObjectRow leading={<Activity size={14} aria-hidden />} title={t("当前连接")} subtitle={overview.live_concurrent > 0 ? t("正在使用 {{count}} 路", { count: overview.live_concurrent }) : t("当前没有活跃连接")} facts={<span>{t("最大并发")} {overview.max_concurrent > 0 ? overview.max_concurrent : t("无限制")}</span>} status={<StatusMark tone={overview.live_concurrent > 0 ? "live" : "neutral"} label={overview.live_concurrent > 0 ? t("活跃") : t("空闲")} />} />
            </ObjectList>
          </WorkSurface>

          <div className="workbench-layout">
            <WorkSurface eyebrow="Quota and usage" title={t("配额与实时使用")} description={t("剩余量、周期窗口和实时连接分别反映可用空间与当前负载。")} actions={subscription && subscription.price_micro > 0 ? <ControlButton variant="primary" size="sm" onPress={() => setRenewing(true)}><RefreshCw size={14} aria-hidden />{t("续费当前套餐")}</ControlButton> : <Link className="link-quiet text-xs text-accent" to={mePaths.plans}><ShoppingCart size={14} aria-hidden />{t("查看套餐")}</Link>}>
              <div className="cp-stack">
                {subscription ? <div className="flex items-center justify-between gap-3 border-b border-rule py-3"><DetailFact label={t("到期时间")} value={subscription.expires_at_ns > 0 ? formatNsDate(subscription.expires_at_ns) : t("永不过期")} mono /><SubscriptionRemaining subscription={subscription} /></div> : <EmptyWorkState title={t("暂无有效订阅")} detail={t("先购买套餐，再创建访问密钥。")} action={<Link className="link-quiet text-xs text-accent" to={mePaths.plans}><ShoppingCart size={14} aria-hidden />{t("查看套餐")}</Link>} />}
                <dl className="cp-detail-facts">
                  <DetailFact label={t("周期已用")} value={formatBytes(overview.period_used_bytes)} mono />
                  <DetailFact label={t("周期剩余")} value={overview.remaining_bytes === null ? t("无限制") : formatBytes(Math.max(0, overview.remaining_bytes))} mono />
                  <DetailFact label={t("最大并发")} value={overview.max_concurrent > 0 ? `${overview.max_concurrent} ${t("路")}` : t("无限制")} mono />
                  <DetailFact label={t("实时连接")} value={`${overview.live_concurrent} ${t("路")}`} mono />
                </dl>
                {subscription ? <ControlSwitch isSelected={subscription.auto_renew} onChange={(checked) => autoRenewMutation.mutate({ subscriptionID: subscription.id, autoRenew: checked })} isDisabled={autoRenewMutation.isPending || subscription.price_micro <= 0} label={t("自动续费")} hint={subscription.price_micro > 0 ? t("到期前 7 天尝试从余额扣 {{price}} USDT。", { price: formatMicro(subscription.price_micro) }) : t("该套餐未定价，无法自动续费。")} /> : null}
              </div>
            </WorkSurface>
            <WorkSurface eyebrow="Authorized platforms" title={t("可访问平台")} description={t("当前套餐和密钥允许访问的平台代理范围。")} meta={t("{{count}} 个", { count: overview.platforms?.length ?? 0 })}>
              {overview.platforms?.length ? <ObjectList>{overview.platforms.map((platform) => <ObjectRow key={platform.platform_id} leading={<Activity size={14} aria-hidden />} title={platform.platform_name || platform.platform_id} subtitle={platform.platform_id} status={<StatusMark tone="signal" label={t("可用")} />} />)}</ObjectList> : <EmptyWorkState title={t("暂无可路由平台")} detail={t("当前套餐没有配置平台限制，或运营侧尚未配置可用平台。")} />}
            </WorkSurface>
          </div>

          <WorkSurface eyebrow="Traffic evidence" title={t("近 30 天用量")} description={t("按天统计的历史入站与出站流量，只展示接口返回的记录。")} meta={dailyItems.length ? t("{{count}} 天", { count: dailyItems.length }) : t("暂无记录")}>
            {dailyItems.length === 0 ? <EmptyWorkState title={t("暂无每日流量数据")} detail={t("最近 30 天内未记录到流量消耗。")} /> : <ObjectList>{dailyItems.map((day) => <ObjectRow key={day.day_ns} leading={<span aria-hidden>▦</span>} title={formatNsDate(day.day_ns)} subtitle={t("每日流量")} facts={<><span>{t("入站流量")} {formatBytes(day.ingress_bytes)}</span><span>{t("出站流量")} {formatBytes(day.egress_bytes)}</span><span>{t("合计流量")} {formatBytes(day.total_bytes)}</span></>} />)}</ObjectList>}
          </WorkSurface>

          <WorkSurface eyebrow="Session control" title={t("登录会话")} description={t("撤销不再使用的设备，当前会话不能从这里撤销。")} meta={sessionsQuery.data ? t("{{count}} 个会话", { count: sessions.length }) : t("未加载")}>
            {sessionsQuery.isLoading ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载会话列表...")}</div> : null}
            {sessionsQuery.isError ? <EmptyWorkState title={t("读取失败")} detail={formatApiErrorMessage(sessionsQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void sessionsQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
            {!sessionsQuery.isLoading && !sessionsQuery.isError && sessions.length === 0 ? <EmptyWorkState title={t("暂无活跃会话")} detail={t("未查询到任何活动的登录会话。")} /> : null}
            {sessions.length > 0 ? <ObjectList>{sessions.map((session) => <ObjectRow key={session.id} leading={<span aria-hidden>◉</span>} title={session.ip || "-"} subtitle={session.user_agent || "-"} facts={<><span>{formatNsDateTime(session.created_at_ns)}</span><span>{t("会话到期")} {formatNsDateTime(session.expires_at_ns)}</span></>} status={<StatusMark tone={session.current ? "signal" : "neutral"} label={session.current ? t("当前设备") : t("可撤销")} />} action={session.current ? <span className="cp-muted">{t("当前会话不可撤销")}</span> : <ControlButton variant="ghost" size="sm" onPress={() => revokeMutation.mutate(session.id)} isDisabled={revokeMutation.isPending}>{t("撤销")}</ControlButton>} />)}</ObjectList> : null}
          </WorkSurface>

          <BalanceSurface balanceQuery={balanceQuery} onBalanceChanged={() => { void queryClient.invalidateQueries({ queryKey: ["me-balance"] }); void queryClient.invalidateQueries({ queryKey: ["me-overview"] }); }} />
        </>
      ) : null}
      {!overviewQuery.isLoading && !overviewQuery.isError && !overview ? <EmptyWorkState title={t("概览暂无数据")} detail={t("服务没有返回可用的概览记录，请稍后重试。")} /> : null}
      {renewing && subscription ? <RenewDrawer key={subscription.id} subscription={subscription} onClose={() => setRenewing(false)} onCommitted={(result) => { showToast("success", result.replayed ? t("重复请求：已存在的订阅生效，本次未扣费。") : t("已扣费 {{amount}} USDT，已续费一期。", { amount: formatMicro(result.charged_micro) })); void queryClient.invalidateQueries({ queryKey: ["me-overview"] }); void queryClient.invalidateQueries({ queryKey: ["me-balance"] }); }} /> : null}
    </PageFrame>
  );
}

function RenewDrawer({ subscription, onClose, onCommitted }: { subscription: SubscriptionView; onClose: () => void; onCommitted: (result: PurchaseResult) => void }) {
  const { t } = useI18n();
  const [requestID] = useState(newRequestID);
  const [result, setResult] = useState<PurchaseResult | null>(null);
  const renewMutation = useMutation({
    mutationFn: () => renew(subscription.id, requestID),
    onSuccess: (next) => { setResult(next); onCommitted(next); },
  });
  return (
    <InspectorDrawer
      open
      onOpenChange={(open) => { if (!open) { renewMutation.reset(); onClose(); } }}
      title={result ? t("续费完成") : t("确认续费")}
      eyebrow={result ? t("Renewal result") : t("Renewal confirmation")}
      footer={result ? <ControlButton variant="primary" size="sm" onPress={onClose}>{t("完成")}</ControlButton> : <><ControlButton variant="ghost" size="sm" onPress={onClose} isDisabled={renewMutation.isPending}>{t("取消")}</ControlButton><ControlButton variant="primary" size="sm" onPress={() => renewMutation.mutate()} loading={renewMutation.isPending}><RefreshCw size={14} aria-hidden />{t("确认续费")}</ControlButton></>}
    >
      {result ? <div className="cp-stack"><StatusMark tone={result.replayed ? "warn" : "signal"} label={result.replayed ? t("重复请求，已存在的订阅生效") : t("续费成功")} /><dl className="cp-detail-facts"><DetailFact label={t("本次扣款")} value={result.replayed ? t("未扣费") : `${formatMicro(result.charged_micro)} USDT`} mono /><DetailFact label={t("当前余额")} value={`${formatMicro(result.balance_micro)} USDT`} mono /><DetailFact label={t("当前到期")} value={result.subscription.expires_at_ns > 0 ? formatNsDate(result.subscription.expires_at_ns) : t("永不过期")} mono /></dl></div> : <div className="cp-stack"><p className="cp-muted">{t("扣款与延长同时完成，余额不足时不会扣款。")}</p><dl className="cp-detail-facts"><DetailFact label={t("套餐")} value={subscription.plan_name} /><DetailFact label={t("当前到期")} value={subscription.expires_at_ns > 0 ? formatNsDate(subscription.expires_at_ns) : t("永不过期")} mono /><DetailFact label={t("扣款金额")} value={`${formatMicro(subscription.price_micro)} USDT`} mono /></dl>{renewMutation.isError ? <p role="alert" className="cp-form-error">{formatApiErrorMessage(renewMutation.error, t)}</p> : null}</div>}
    </InspectorDrawer>
  );
}
