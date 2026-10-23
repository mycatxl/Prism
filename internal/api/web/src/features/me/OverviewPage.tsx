import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, ArrowUpRight, KeyRound, RefreshCw, ShoppingCart, WalletCards } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Button } from "../../components/ui/Button";
import {
  DialogContent,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "../../components/ui/Dialog";
import { DataFreshness } from "../../components/ui/DataFreshness";
import { PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { Sparkline } from "../../components/ui/Sparkline";
import { Switch } from "../../components/ui/Switch";
import { Table, TableWrap, TBody, TD, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { WorkSection } from "../../components/ui/WorkSection";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatBytes } from "../../lib/bytes";
import { cn } from "../../lib/cn";
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
  renew,
  revokeSession,
  setAutoRenew,
} from "./api";
import { BalancePanel } from "./BalancePanel";
import type { PurchaseResult, SelfOverview, SubscriptionView } from "./types";

const NANOS_PER_MILLI = 1_000_000;

function formatNsDate(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(date);
}

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
    second: "2-digit",
    hour12: false,
  }).format(date);
}

/** The console-wide "soon" line: the last two days of a window's life warn. */
const SUBSCRIPTION_WARN_MS = 48 * 3_600_000;

/** The state vocabulary the dot renders; `neutral` is the bare state-dot. */
type StateTone = "neutral" | "signal" | "live" | "warn" | "alert";

/** The system's state language: the dot carries the hue, the word carries the meaning. */
function StateLine({ tone, label }: { tone: StateTone; label: ReactNode }) {
  return (
    <span className="flex items-center gap-2">
      <span aria-hidden className={cn("state-dot", tone !== "neutral" && `state-dot--${tone}`)} />
      <span className="state-text">{label}</span>
    </span>
  );
}

/**
 * The subscription's state, graded with the same four inks as everything else:
 * active is healthy, an expired or suspended plan is respectively failure and
 * degradation, and anything the server adds later stays its own unknown state —
 * a bare dot and the raw token, never read as healthy or as failure.
 */
function subscriptionState(status: string): { label: string; tone: StateTone } {
  if (status === "active") return { label: "生效中", tone: "signal" };
  if (status === "expired") return { label: "已过期", tone: "alert" };
  if (status === "suspended") return { label: "已停用", tone: "warn" };
  return { label: status, tone: "neutral" };
}

/** The tone of a remaining span: signal while calm, warn inside two days, alert when gone. */
function remainingTone(remainingMs: number): "signal" | "warn" | "alert" {
  if (remainingMs <= 0) return "alert";
  if (remainingMs <= SUBSCRIPTION_WARN_MS) return "warn";
  return "signal";
}

/** The share of a window that is still ahead, 0..100. */
function remainingPercent(remainingMs: number, windowMs: number): number {
  if (windowMs <= 0 || remainingMs <= 0) return 0;
  return Math.max(0, Math.min(100, (remainingMs / windowMs) * 100));
}

/** A remaining span in the house duration voice, re-encoded through formatGoDuration. */
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

/** The fill class of the remaining-life bar, one per state tone. */
const BAR_CLASS = {
  signal: "lease-fill",
  warn: "lease-fill lease-fill--warn",
  alert: "lease-fill lease-fill--alert",
} as const;

/**
 * The remaining-life bar: the length is what is still usable, the tone is how
 * urgent that is, and the figure beside it says the same thing in words.
 */
function LeaseBar({
  label,
  detail,
  percent,
  tone,
  expired,
}: {
  label: string;
  detail: string;
  percent: number;
  tone: "signal" | "warn" | "alert";
  expired: boolean;
}) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between gap-3">
        <span className="micro">{label}</span>
        <span
          className={cn(
            "readout whitespace-nowrap text-xs",
            expired ? "text-alert" : "text-ink-soft",
          )}
        >
          {detail}
        </span>
      </div>
      <span className="lease-track block" aria-hidden>
        <span
          className={cn("block", BAR_CLASS[tone])}
          style={{ width: `${expired ? 100 : percent}%` }}
        />
      </span>
    </div>
  );
}

/** The subscription's remaining life against its own window. */
function SubscriptionTimeRow({ subscription }: { subscription: SubscriptionView }) {
  const { t } = useI18n();
  const now = useNow();
  const startsMs = Math.round(subscription.starts_at_ns / NANOS_PER_MILLI);
  const expiresMs = Math.round(subscription.expires_at_ns / NANOS_PER_MILLI);
  const windowMs = expiresMs - startsMs;
  const remainingMs = expiresMs - now;
  const expired = remainingMs <= 0;

  // A window the record cannot produce is a bar that would guess at its
  // denominator, so it is left out rather than drawn.
  if (windowMs <= 0) {
    return (
      <div className="flex items-baseline justify-between gap-3">
        <span className="micro">{t("剩余时间")}</span>
        <span className="readout text-xs text-ink-soft">{expired ? t("已过期") : "-"}</span>
      </div>
    );
  }

  return (
    <LeaseBar
      label={t("剩余时间")}
      detail={expired ? t("已过期") : formatRemainingDuration(remainingMs)}
      percent={remainingPercent(remainingMs, windowMs)}
      tone={remainingTone(remainingMs)}
      expired={expired}
    />
  );
}

/** The period quota's remaining share; an unlimited quota draws no bar. */
function QuotaRow({ overview }: { overview: SelfOverview }) {
  const { t } = useI18n();
  const remaining = overview.remaining_bytes ?? overview.traffic_bytes - overview.period_used_bytes;
  const ratio = remaining / overview.traffic_bytes;
  const expired = remaining <= 0;

  return (
    <LeaseBar
      label={t("周期剩余流量")}
      detail={`${formatBytes(Math.max(0, remaining))} / ${formatBytes(overview.traffic_bytes)}`}
      percent={Math.max(0, Math.min(100, ratio * 100))}
      tone={ratio <= 0.05 ? "alert" : ratio <= 0.25 ? "warn" : "signal"}
      expired={expired}
    />
  );
}

export function OverviewPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [renewing, setRenewing] = useState(false);

  const overviewQuery = useQuery({
    queryKey: ["me-overview"],
    queryFn: getOverview,
  });

  const sessionsQuery = useQuery({
    queryKey: ["me-sessions"],
    queryFn: getSessions,
  });

  const balanceQuery = useQuery({
    queryKey: meBalanceQueryKey,
    queryFn: () => getBalance(BALANCE_LEDGER_LIMIT),
    staleTime: 30_000,
  });

  const isRefreshing = overviewQuery.isFetching || sessionsQuery.isFetching || balanceQuery.isFetching;

  const handleRefresh = () => {
    void overviewQuery.refetch();
    void sessionsQuery.refetch();
    void balanceQuery.refetch();
  };

  const revokeMutation = useMutation({
    mutationFn: (sessionID: string) => revokeSession(sessionID),
    onSuccess: () => {
      showToast("success", t("会话已撤销"));
      void queryClient.invalidateQueries({ queryKey: ["me-sessions"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  const autoRenewMutation = useMutation({
    mutationFn: (input: { subscriptionID: string; autoRenew: boolean }) =>
      setAutoRenew(input.subscriptionID, input.autoRenew),
    onSuccess: (_result, input) => {
      showToast("success", input.autoRenew ? t("已开启自动续费") : t("已关闭自动续费"));
      void queryClient.invalidateQueries({ queryKey: ["me-overview"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  const overview = overviewQuery.data;
  const subscription = overview?.subscription ?? null;
  const subscriptionTone = subscription ? subscriptionState(subscription.status) : null;
  const sessions = sessionsQuery.data?.items ?? [];
  const dailyItems = overview?.usage_daily ?? [];
  const sparklineValues = dailyItems.map((d) => d.total_bytes);

  return (
    <div className="tenant-workbench">
      <PageHeader
        title={t("概览")}
        description={t("先确认套餐余量，再管理接入密钥、平台范围和当前会话。")}
        meta={<PageMeta label={t("数据状态")} value={<DataFreshness tone={overviewQuery.isFetching ? "warn" : overviewQuery.isSuccess ? "live" : "neutral"} label={overviewQuery.isFetching ? t("更新中") : overviewQuery.isSuccess ? t("已同步") : t("未加载")} />} />}
        actions={
          <Button variant="secondary" onClick={handleRefresh} disabled={isRefreshing} title={t("刷新")}>
            <RefreshCw size={14} className={isRefreshing ? "animate-spin" : undefined} aria-hidden />
            <span>{t("刷新")}</span>
          </Button>
        }
      />

      {overviewQuery.isLoading ? (
        <div className="page-content">
          <LoadingState label={t("正在加载概览数据...")} className="py-16" />
        </div>
      ) : overviewQuery.isError ? (
        <div className="page-content">
          <ErrorState message={formatApiErrorMessage(overviewQuery.error, t)} onRetry={() => void overviewQuery.refetch()} className="py-12" />
        </div>
      ) : overview ? (
        <div className="page-content">
          <div className="tenant-statusline" aria-label={t("套餐和配额状态")}>
            <div className="tenant-statusline__lead">
              <div className="flex items-start justify-between gap-4">
                <div className="min-w-0">
                  <span className="micro">{t("当前套餐")}</span>
                  <strong className="mt-2 block truncate text-lg font-semibold text-ink">
                    {subscription?.plan_name || t("暂无有效订阅")}
                  </strong>
                </div>
                <span className="shrink-0">
                  <StateLine
                    tone={subscriptionTone?.tone ?? "warn"}
                    label={subscriptionTone ? t(subscriptionTone.label) : t("需要购买")}
                  />
                </span>
              </div>
              <div className="mt-4 flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-ink-soft">
                <span>
                  <span className="micro mr-2">{t("到期")}</span>
                  <span className="readout text-ink">
                    {subscription && subscription.expires_at_ns > 0 ? formatNsDate(subscription.expires_at_ns) : subscription ? t("永不过期") : "—"}
                  </span>
                </span>
                {subscription?.price_micro ? <span>{formatMicro(subscription.price_micro)} USDT / {t("周期")}</span> : null}
              </div>
            </div>
            <div className="tenant-statusline__cell">
              <span className="tenant-statusline__label">{t("周期流量")}</span>
              <strong className="tenant-statusline__value">{formatBytes(overview.period_used_bytes)}</strong>
              <span className="tenant-statusline__hint">
                {overview.remaining_bytes === null ? t("无限制") : t("剩余 {{value}}", { value: formatBytes(Math.max(0, overview.remaining_bytes)) })}
              </span>
            </div>
            <div className="tenant-statusline__cell">
              <span className="tenant-statusline__label">{t("当前余额")}</span>
              <strong className={cn("tenant-statusline__value", balanceQuery.data && balanceQuery.data.balance_micro > 0 ? "text-signal" : "text-ink")}>{balanceQuery.data ? formatMicro(balanceQuery.data.balance_micro) : "—"}</strong>
              <span className="tenant-statusline__hint">{balanceQuery.isError ? t("读取失败") : t("USDT，可用于购买和续费")}</span>
            </div>
            <div className="tenant-statusline__cell">
              <span className="tenant-statusline__label">{t("访问额度")}</span>
              <strong className="tenant-statusline__value">{overview.max_keys > 0 ? `${overview.active_keys} / ${overview.max_keys}` : `${overview.active_keys} / ${t("无限制")}`}</strong>
              <span className="tenant-statusline__hint">{t("实时连接 {{count}} 路", { count: overview.live_concurrent })}</span>
            </div>
          </div>

          <div className="tenant-access-band mt-7">
            <WorkSection
              className="tenant-access-panel"
              title={t("接入工作台")}
              description={t("密钥是访问平台代理的主入口，平台授权和连接状态在这里快速确认。")}
              meta={t("{{count}} 个平台", { count: overview.platforms?.length ?? 0 })}
              actions={<Link className="link-quiet text-xs text-accent" to={mePaths.keys}>{t("管理密钥")} <ArrowUpRight size={13} className="inline" /></Link>}
            >
              <div className="tenant-access-panel__body">
                <div className="tenant-access-line">
                  <span>{t("访问密钥")}</span>
                  <code>{overview.max_keys > 0 ? t("已使用 {{active}} / {{max}}", { active: overview.active_keys, max: overview.max_keys }) : t("已创建 {{active}} 个", { active: overview.active_keys })}</code>
                  <Button variant="ghost" size="sm" asChild><Link to={mePaths.keys}><KeyRound size={14} aria-hidden /><span>{t("打开")}</span></Link></Button>
                </div>
                <div className="tenant-access-line">
                  <span>{t("授权平台")}</span>
                  <code>{overview.platforms?.length ? overview.platforms.map((platform) => platform.platform_name || platform.platform_id).join(", ") : t("暂无限制或未授权")}</code>
                  <Button variant="ghost" size="sm" asChild><Link to={mePaths.guide}><ArrowUpRight size={14} aria-hidden /><span>{t("接入指南")}</span></Link></Button>
                </div>
                <div className="tenant-access-line">
                  <span>{t("当前连接")}</span>
                  <code>{overview.live_concurrent > 0 ? t("正在使用 {{count}} 路", { count: overview.live_concurrent }) : t("当前没有活跃连接")}</code>
                  <span className="text-xs text-ink-faint"><Activity size={14} aria-hidden /></span>
                </div>
              </div>
            </WorkSection>

            <div className="tenant-action-stack" aria-label={t("下一步操作")}>
              <div className="tenant-action-stack__row">
                <strong>{t("补充余额")}</strong>
                <span>{t("兑换码或链上转账都可以立即更新余额。")}</span>
                <Link className="action link-quiet text-xs text-accent" to={mePaths.deposit}><WalletCards size={14} aria-hidden /><span>{t("去充值")}</span> <ArrowUpRight size={13} aria-hidden /></Link>
              </div>
              <div className="tenant-action-stack__row">
                <strong>{subscription ? t("续费或更换套餐") : t("购买第一份套餐")}</strong>
                <span>{subscription ? t("保留当前接入配置，按套餐周期继续使用。") : t("先购买套餐，再创建访问密钥。")}</span>
                {subscription && subscription.price_micro > 0 ? <Button className="action" size="sm" variant="primary" onClick={() => setRenewing(true)}><RefreshCw size={14} aria-hidden /><span>{t("续费当前套餐")}</span></Button> : <Button className="action" size="sm" variant="primary" asChild><Link to={mePaths.plans}><ShoppingCart size={14} aria-hidden /><span>{t("查看套餐")}</span></Link></Button>}
              </div>
              <div className="tenant-action-stack__row">
                <strong>{t("安全设置")}</strong>
                <span>{t("管理会话、语言和账户安全选项。")}</span>
                <Link className="action link-quiet text-xs text-accent" to="/account"><span>{t("打开账户安全")}</span> <ArrowUpRight size={13} aria-hidden /></Link>
              </div>
            </div>
          </div>

          <div className="grid min-w-0 grid-cols-1 gap-7 mt-8 lg:grid-cols-[minmax(0,1.15fr)_minmax(300px,.85fr)]">
            <WorkSection title={t("配额与实时使用")} description={t("剩余量、周期窗口和实时连接分别反映可用空间与当前负载。")}>
              <div className="space-y-5">
                {subscription?.expires_at_ns ? <SubscriptionTimeRow subscription={subscription} /> : null}
                {overview.traffic_bytes > 0 ? <QuotaRow overview={overview} /> : <div className="text-xs text-ink-soft">{t("当前套餐没有周期流量限制。")}</div>}
                <ReadoutStrip>
                  <ReadoutCell><Readout label={t("周期已用")} value={formatBytes(overview.period_used_bytes)} size="sm" /></ReadoutCell>
                  <ReadoutCell><Readout label={t("周期剩余")} value={overview.remaining_bytes === null ? t("无限制") : formatBytes(Math.max(0, overview.remaining_bytes))} tone={overview.remaining_bytes !== null && overview.remaining_bytes <= 0 ? "alert" : "signal"} size="sm" /></ReadoutCell>
                  <ReadoutCell><Readout label={t("最大并发")} value={overview.max_concurrent > 0 ? String(overview.max_concurrent) : t("无限制")} unit={overview.max_concurrent > 0 ? t("路") : undefined} size="sm" /></ReadoutCell>
                  <ReadoutCell><Readout label={t("实时连接")} value={String(overview.live_concurrent)} unit={t("路")} tone={overview.live_concurrent > 0 ? "live" : "muted"} size="sm" /></ReadoutCell>
                </ReadoutStrip>
                {subscription ? (
                  <div className="flex flex-wrap items-center justify-between gap-3 border-t border-rule-faint pt-4">
                    <div className="min-w-0">
                      <strong className="block text-xs font-medium text-ink">{t("自动续费")}</strong>
                      <span className="mt-1 block text-xs text-ink-soft">{subscription.price_micro > 0 ? t("到期前 7 天尝试从余额扣 {{price}} USDT。", { price: formatMicro(subscription.price_micro) }) : t("该套餐未定价，无法自动续费。")}</span>
                    </div>
                    <Switch checked={subscription.auto_renew} disabled={autoRenewMutation.isPending || subscription.price_micro <= 0} onCheckedChange={(checked) => autoRenewMutation.mutate({ subscriptionID: subscription.id, autoRenew: checked })} />
                  </div>
                ) : null}
              </div>
            </WorkSection>

            <WorkSection title={t("可访问平台")} description={t("当前套餐和密钥允许访问的平台代理范围。")} meta={t("{{count}} 个", { count: overview.platforms?.length ?? 0 })}>
              {overview.platforms?.length ? (
                <div className="tenant-platform-list">
                  {overview.platforms.map((platform) => (
                    <div className="tenant-platform-list__row" key={platform.platform_id}>
                      <span className="tenant-platform-list__mark" aria-hidden><Activity size={14} /></span>
                      <span className="min-w-0"><strong className="block truncate text-xs text-ink">{platform.platform_name || platform.platform_id}</strong><small className="mt-0.5 block truncate font-mono text-2xs text-ink-faint">{platform.platform_id}</small></span>
                      <span className="text-2xs text-signal">{t("可用")}</span>
                    </div>
                  ))}
                </div>
              ) : <EmptyState title={t("暂无可路由平台")} hint={t("当前套餐没有配置平台限制，或运营侧尚未配置可用平台。")} className="py-8" />}
            </WorkSection>
          </div>

          <div className="mt-8">
            <WorkSection title={t("近 30 天用量") } description={t("按天统计的历史入站与出站流量，只展示接口返回的记录。")} meta={dailyItems.length ? t("{{count}} 天", { count: dailyItems.length }) : t("暂无记录")} actions={sparklineValues.length >= 2 ? <div className="w-40"><Sparkline values={sparklineValues} tone="accent" height={30} /></div> : null}>
              {dailyItems.length === 0 ? <EmptyState title={t("暂无每日流量数据")} hint={t("最近 30 天内未记录到流量消耗。")} className="py-10" /> : <TableWrap><Table><THead><TR><TH>{t("日期")}</TH><TH className="text-right">{t("入站流量")}</TH><TH className="text-right">{t("出站流量")}</TH><TH className="text-right">{t("合计流量")}</TH></TR></THead><TBody>{dailyItems.map((day) => <TR key={day.day_ns}><TD className="readout text-xs">{formatNsDate(day.day_ns)}</TD><TDNum><span className="text-xs text-ink-soft">{formatBytes(day.ingress_bytes)}</span></TDNum><TDNum><span className="text-xs text-ink-soft">{formatBytes(day.egress_bytes)}</span></TDNum><TDNum><span className="text-xs font-semibold">{formatBytes(day.total_bytes)}</span></TDNum></TR>)}</TBody></Table></TableWrap>}
            </WorkSection>
          </div>

          <div className="mt-8">
            <WorkSection title={t("登录会话")} description={t("撤销不再使用的设备，当前会话不能从这里撤销。")} meta={sessionsQuery.data ? t("{{count}} 个会话", { count: sessions.length }) : t("未加载")} actions={<DataFreshness tone={sessionsQuery.isFetching ? "warn" : sessionsQuery.isSuccess ? "live" : "neutral"} label={sessionsQuery.isFetching ? t("更新中") : sessionsQuery.isSuccess ? t("已同步") : t("未加载")} />}>
              {sessionsQuery.isLoading ? <LoadingState label={t("正在加载会话列表...")} className="py-8" /> : sessionsQuery.isError ? <ErrorState message={formatApiErrorMessage(sessionsQuery.error, t)} onRetry={() => void sessionsQuery.refetch()} className="my-2" /> : sessions.length === 0 ? <EmptyState title={t("暂无活跃会话")} hint={t("未查询到任何活动的登录会话。") } className="py-8" /> : <TableWrap><Table><THead><TR><TH>{t("登录 IP")}</TH><TH>{t("客户端环境")}</TH><TH>{t("登录时间")}</TH><TH>{t("会话到期")}</TH><TH className="text-right">{t("操作")}</TH></TR></THead><TBody>{sessions.map((sess) => <TR key={sess.id}><TD className="font-mono text-xs text-ink"><div className="flex items-center gap-2"><span>{sess.ip || "-"}</span>{sess.current ? <span className="text-2xs text-signal">{t("当前设备")}</span> : null}</div></TD><TD className="max-w-xs truncate text-xs text-ink-soft" title={sess.user_agent}>{sess.user_agent || "-"}</TD><TD className="readout font-mono text-xs text-ink-soft">{formatNsDateTime(sess.created_at_ns)}</TD><TD className="readout font-mono text-xs text-ink-soft">{formatNsDateTime(sess.expires_at_ns)}</TD><TD className="text-right">{sess.current ? <span className="text-xs text-ink-faint">{t("当前会话不可撤销")}</span> : <Button variant="secondary" size="sm" onClick={() => revokeMutation.mutate(sess.id)} disabled={revokeMutation.isPending}>{t("撤销")}</Button>}</TD></TR>)}</TBody></Table></TableWrap>}
            </WorkSection>
          </div>

          <div className="mt-8">
            <BalancePanel />
          </div>
        </div>
      ) : (
        <div className="page-content">
          <EmptyState title={t("概览暂无数据")} hint={t("服务没有返回可用的概览记录，请稍后重试。")} className="py-12" />
        </div>
      )}

      {renewing && subscription && (
        <RenewDialog
          subscription={subscription}
          onClose={() => setRenewing(false)}
          onCommitted={(result) => {
            showToast("success", result.replayed ? t("重复请求：已存在的订阅生效，本次未扣费。") : t("已扣费 {{amount}} USDT，已续费一期。", { amount: formatMicro(result.charged_micro) }));
            void queryClient.invalidateQueries({ queryKey: ["me-overview"] });
            void queryClient.invalidateQueries({ queryKey: ["me-balance"] });
          }}
        />
      )}

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
  );
}

/**
 * The confirmation step for a manual renewal, which is also where the
 * idempotency key is minted.
 *
 * The key is minted once, when the dialog mounts, and kept for its lifetime: a
 * retry after a network error answers with the first call's result instead of
 * extending a second period. Closing and reopening really is another decision,
 * so it mints a new one.
 */
function RenewDialog({
  subscription,
  onClose,
  onCommitted,
}: {
  subscription: SubscriptionView;
  onClose: () => void;
  onCommitted: (result: PurchaseResult) => void;
}) {
  const { t } = useI18n();
  const [requestID] = useState(newRequestID);
  const [result, setResult] = useState<PurchaseResult | null>(null);

  const renewMutation = useMutation({
    mutationFn: (input: { subscriptionID: string; requestID: string }) =>
      renew(input.subscriptionID, input.requestID),
    onSuccess: (res) => {
      setResult(res);
      onCommitted(res);
    },
  });

  return (
    <DialogRoot
      open
      onOpenChange={(open) => {
        if (!open) {
          renewMutation.reset();
          onClose();
        }
      }}
    >
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="w-full max-w-md p-6">
          <DialogTitle className="text-base font-semibold text-ink">
            {result ? t("续费完成") : t("确认续费")}
          </DialogTitle>
          {result ? (
            <>
              <div className="mt-4 flex items-center gap-2">
                <span
                  aria-hidden
                  className={cn("state-dot", !result.replayed && "state-dot--signal")}
                />
                <span className="state-text">
                  {result.replayed ? t("重复请求，已存在的订阅生效") : t("续费成功")}
                </span>
              </div>

              <div className="mt-3 space-y-2 rounded-control border border-rule bg-paper-inset p-3 text-xs">
                <div className="flex items-center justify-between">
                  <span className="text-ink-soft">{t("本次扣款")}</span>
                  <span className="readout text-ink">
                    {result.replayed
                      ? t("未扣费")
                      : `${formatMicro(result.charged_micro)} USDT`}
                  </span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-ink-soft">{t("当前余额")}</span>
                  <span className="readout text-ink">
                    {formatMicro(result.balance_micro)} USDT
                  </span>
                </div>
                <div className="flex items-center justify-between border-t border-rule pt-2">
                  <span className="text-ink-soft">{t("当前到期")}</span>
                  <span className="readout text-ink">
                    {result.subscription.expires_at_ns > 0
                      ? formatNsDate(result.subscription.expires_at_ns)
                      : t("永不过期")}
                  </span>
                </div>
              </div>

              <div className="mt-6 flex items-center justify-end">
                <Button variant="primary" onClick={onClose}>
                  {t("完成")}
                </Button>
              </div>
            </>
          ) : (
            <>
              <p className="mt-1 text-xs text-ink-soft">
                {t("扣款与延长同时完成，余额不足时不会扣款。")}
              </p>

              <div className="mt-4 space-y-2 rounded-control border border-rule bg-paper-inset p-3 text-xs">
                <div className="flex items-center justify-between">
                  <span className="text-ink-soft">{t("套餐")}</span>
                  <span className="font-medium text-ink">{subscription.plan_name}</span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-ink-soft">{t("当前到期")}</span>
                  <span className="readout text-ink">
                    {subscription.expires_at_ns > 0
                      ? formatNsDate(subscription.expires_at_ns)
                      : t("永不过期")}
                  </span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-ink-soft">{t("扣款金额")}</span>
                  <span className="readout font-mono text-ink">
                    {formatMicro(subscription.price_micro)} USDT
                  </span>
                </div>
              </div>

              {renewMutation.isError && (
                <div className="mt-3 rounded-control border border-alert/30 bg-alert-wash p-3 text-xs text-alert">
                  {formatApiErrorMessage(renewMutation.error, t)}
                </div>
              )}

              <div className="mt-6 flex items-center justify-end gap-3">
                <Button
                  variant="secondary"
                  onClick={onClose}
                  disabled={renewMutation.isPending}
                >
                  {t("取消")}
                </Button>
                <Button
                  variant="primary"
                  onClick={() =>
                    renewMutation.mutate({
                      subscriptionID: subscription.id,
                      requestID,
                    })
                  }
                  loading={renewMutation.isPending}
                >
                  {t("确认续费")}
                </Button>
              </div>
            </>
          )}
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}
