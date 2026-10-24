import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, RefreshCw } from "lucide-react";
import { useState, type ReactNode } from "react";
import {
  CommandSurface,
  ControlButton,
  ControlSelect,
  ControlSwitch,
  EmptyWorkState,
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
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime } from "../../lib/time";
import {
  acknowledgeAlert,
  getUsageAlerts,
  getUsageDaily,
  getUsagePlans,
  getUsagePlatforms,
  getUsageSummary,
  getUsageUsers,
} from "./api";

const NANOS_PER_MILLI = 1_000_000;
const ALERT_LIST_LIMIT = 50;

type Translator = (text: string, values?: Record<string, unknown>) => string;

type QueryLike = { isError: boolean; data?: unknown };

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
  return formatDateTime(date.toISOString());
}

function formatCount(value: number | null | undefined): string {
  return typeof value === "number" && Number.isFinite(value) ? String(value) : "-";
}

function formatTraffic(value: number | null | undefined): string {
  return typeof value === "number" && Number.isFinite(value) ? formatBytes(value) : "-";
}

function trafficReadout(value: number | null | undefined): ReactNode {
  const formatted = formatTraffic(value);
  if (formatted === "-") return formatted;
  const [figure, unit] = formatted.split(" ");
  return <><span>{figure}</span><small>{unit}</small></>;
}

function formatThresholdRatio(ratio: number): string {
  return Number.isFinite(ratio) ? `${(ratio * 100).toFixed(1)}%` : "-";
}

function queryTone(query: QueryLike): "live" | "warn" | "neutral" {
  if (query.isError && !query.data) return "warn";
  if (query.data) return query.isError ? "warn" : "live";
  return "neutral";
}

function queryLabel(query: QueryLike, t: Translator, ready: string): string {
  if (query.isError && !query.data) return t("读取失败");
  if (query.data) return query.isError ? t("数据可能已过期") : t(ready);
  return t("等待数据");
}

function QueryFailure({ query, t, retry }: { query: { error: unknown; refetch: () => unknown }; t: Translator; retry: () => void }) {
  return (
    <EmptyWorkState
      title={t("读取失败")}
      detail={formatApiErrorMessage(query.error, t)}
      action={<ControlButton variant="secondary" size="sm" onPress={retry}>{t("重试")}</ControlButton>}
    />
  );
}

export function UsagePage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [days, setDays] = useState(30);
  const [unacknowledgedOnly, setUnacknowledgedOnly] = useState(true);

  const summaryQuery = useQuery({ queryKey: ["admin-usage-summary"], queryFn: getUsageSummary });
  const dailyQuery = useQuery({ queryKey: ["admin-usage-daily", days], queryFn: () => getUsageDaily({ days }) });
  const usersQuery = useQuery({ queryKey: ["admin-usage-users", days], queryFn: () => getUsageUsers({ days, limit: 50 }) });
  const platformsQuery = useQuery({ queryKey: ["admin-usage-platforms", days], queryFn: () => getUsagePlatforms({ days }) });
  const plansQuery = useQuery({ queryKey: ["admin-usage-plans", days], queryFn: () => getUsagePlans({ days }) });
  const alertsQuery = useQuery({
    queryKey: ["admin-usage-alerts", unacknowledgedOnly],
    queryFn: () => getUsageAlerts({ limit: ALERT_LIST_LIMIT, unacknowledged: unacknowledgedOnly }),
  });

  const isRefreshing = [summaryQuery, dailyQuery, usersQuery, platformsQuery, plansQuery, alertsQuery].some((query) => query.isFetching);
  const refresh = () => {
    void Promise.all([
      summaryQuery.refetch(),
      dailyQuery.refetch(),
      usersQuery.refetch(),
      platformsQuery.refetch(),
      plansQuery.refetch(),
      alertsQuery.refetch(),
    ]);
  };

  const acknowledgeMutation = useMutation({
    mutationFn: (id: string) => acknowledgeAlert(id),
    onSuccess: async () => {
      showToast("success", t("告警已确认"));
      await queryClient.invalidateQueries({ queryKey: ["admin-usage-alerts"] });
      refresh();
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
      void queryClient.invalidateQueries({ queryKey: ["admin-usage-alerts"] });
    },
  });

  const summary = summaryQuery.data;
  const dailyItems = dailyQuery.data?.items ?? [];
  const userItems = usersQuery.data?.items ?? [];
  const platformItems = platformsQuery.data?.items ?? [];
  const planItems = plansQuery.data?.items ?? [];
  const alertItems = alertsQuery.data?.items ?? [];
  const unacknowledgedCount = alertItems.filter((alert) => alert.acknowledged_at_ns <= 0).length;

  return (
    <PageFrame
      eyebrow="Customers / usage"
      title={t("用量")}
      description={t("全站资源用量、用户流量排名与每日用量趋势统计。")}
      meta={<StatusMark tone={queryTone(summaryQuery)} label={queryLabel(summaryQuery, t, "数据已同步")} />}
      actions={
        <>
          <ControlSelect aria-label={t("统计周期")} value={String(days)} onChange={(event) => setDays(Number(event.target.value))}>
            <option value="7">{t("最近 7 天")}</option>
            <option value="30">{t("最近 30 天")}</option>
            <option value="90">{t("最近 90 天")}</option>
          </ControlSelect>
          <ControlButton variant="secondary" size="sm" onPress={refresh} loading={isRefreshing}>
            <RefreshCw size={14} aria-hidden />
            {t("刷新")}
          </ControlButton>
        </>
      }
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <WorkSurface
        eyebrow="Customers / service meters"
        title={t("用量摘要")}
        meta={t("最近 {{days}} 天", { days })}
        description={t("只显示用量接口返回的真实汇总；缺少字段时保持未知。")}
      >
        {summaryQuery.isError && !summaryQuery.data ? <QueryFailure query={summaryQuery} t={t} retry={() => void summaryQuery.refetch()} /> : null}
        {summaryQuery.isLoading && !summaryQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载用量概览...")}</div> : null}
        {summary ? (
          <div className="cp-data-grid" aria-busy={summaryQuery.isFetching}>
            <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("活跃用户")}</span><strong className="cp-data-grid__value">{formatCount(summary.active_users)}</strong><span className="cp-data-grid__hint">{t("当前活跃账号")}</span></div>
            <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("总用户数")}</span><strong className="cp-data-grid__value">{formatCount(summary.total_users)}</strong><span className="cp-data-grid__hint">{t("服务端汇总")}</span></div>
            <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("今日全站流量")}</span><strong className="cp-data-grid__value">{formatTraffic(summary.today_bytes)}</strong><span className="cp-data-grid__hint">{t("今日记录")}</span></div>
            <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("本周期总流量")}</span><strong className="cp-data-grid__value">{formatTraffic(summary.period_bytes)}</strong><span className="cp-data-grid__hint">{t("当前统计周期")}</span></div>
            <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("即将到期用户")}</span><strong className="cp-data-grid__value">{formatCount(summary.expiring_soon_users)}</strong><span className="cp-data-grid__hint">{summary.expiring_within_days > 0 ? t("未来 {{days}} 天内", { days: summary.expiring_within_days }) : t("窗口未返回")}</span></div>
          </div>
        ) : null}
      </WorkSurface>

      <WorkSurface
        eyebrow="Customers / daily evidence"
        title={t("每日流量趋势")}
        meta={dailyQuery.data ? t("共 {{count}} 天", { count: dailyItems.length }) : t("未加载")}
        description={t("按日期展开接口返回的入站、出站和合计流量；不在缺少数据时绘制趋势。")}
      >
        {dailyQuery.isError && !dailyQuery.data ? <QueryFailure query={dailyQuery} t={t} retry={() => void dailyQuery.refetch()} /> : null}
        {dailyQuery.isLoading && !dailyQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载每日流量...")}</div> : null}
        {!dailyQuery.isLoading && !dailyQuery.isError && dailyItems.length === 0 ? <EmptyWorkState title={t("暂无每日流量数据")} detail={t("所选时间窗口内未记录到流量数据。" )} /> : null}
        {dailyItems.length ? <ObjectList>{dailyItems.map((day) => <ObjectRow key={day.day_ns} leading={<span className="cp-platform-row__mark" aria-hidden>DAY</span>} title={formatNsDate(day.day_ns)} subtitle={t("每日流量证据")} facts={<><span>{t("入站")} <span className="readout">{trafficReadout(day.ingress_bytes)}</span></span><span>{t("出站")} <span className="readout">{trafficReadout(day.egress_bytes)}</span></span><span>{t("合计")} <span className="readout">{trafficReadout(day.total_bytes)}</span></span></>} status={<QueryBadge label={t("已记录")} tone="signal" />} />)}</ObjectList> : null}
      </WorkSurface>

      <WorkSurface
        eyebrow="Customers / account ranking"
        title={t("用户用量排行")}
        meta={usersQuery.data ? t("共 {{count}} 名用户", { count: userItems.length }) : t("未加载")}
        description={t("统计所选时间窗口内流量消耗最高的用户，最多显示前 50 名。")}
      >
        {usersQuery.isError && !usersQuery.data ? <QueryFailure query={usersQuery} t={t} retry={() => void usersQuery.refetch()} /> : null}
        {usersQuery.isLoading && !usersQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载用户用量排行...")}</div> : null}
        {!usersQuery.isLoading && !usersQuery.isError && userItems.length === 0 ? <EmptyWorkState title={t("暂无用户用量数据")} detail={t("所选时间窗口内未记录到用户流量。" )} /> : null}
        {userItems.length ? <ObjectList>{userItems.map((user) => <ObjectRow key={user.user_id} leading={<span className="cp-platform-row__mark" aria-hidden>USR</span>} title={user.username || user.user_id} subtitle={user.plan_name || t("无活跃订阅")} facts={<><span>{t("入站")} <span className="readout">{trafficReadout(user.ingress_bytes)}</span></span><span>{t("出站")} <span className="readout">{trafficReadout(user.egress_bytes)}</span></span><span>{t("合计")} <span className="readout">{trafficReadout(user.total_bytes)}</span></span><span>{t("周期用量")} <span className="readout">{formatTraffic(user.period_used_bytes)} / {user.traffic_bytes > 0 ? formatTraffic(user.traffic_bytes) : t("不限")}</span></span></>} status={<QueryBadge label={user.expires_at_ns > 0 ? formatNsDate(user.expires_at_ns) : t("未提供")} />} />)}</ObjectList> : null}
      </WorkSurface>

      <div className="cp-split">
        <WorkSurface eyebrow="Customers / platform distribution" title={t("平台用量分布")} meta={platformsQuery.data ? t("共 {{count}} 个平台", { count: platformItems.length }) : t("未加载")} description={t("各平台在所选时间窗口内的真实流量分发。")}>
          {platformsQuery.isError && !platformsQuery.data ? <QueryFailure query={platformsQuery} t={t} retry={() => void platformsQuery.refetch()} /> : null}
          {platformsQuery.isLoading && !platformsQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载平台用量...")}</div> : null}
          {!platformsQuery.isLoading && !platformsQuery.isError && platformItems.length === 0 ? <EmptyWorkState title={t("暂无平台用量数据")} detail={t("所选时间窗口内未记录到平台流量。" )} /> : null}
          {platformItems.length ? <ObjectList>{platformItems.map((platform) => <ObjectRow key={platform.platform_id} leading={<span className="cp-platform-row__mark" aria-hidden>PLT</span>} title={platform.platform_name || platform.platform_id} subtitle={platform.platform_id} facts={<span>{t("合计流量")} <span className="readout">{trafficReadout(platform.total_bytes)}</span></span>} status={<QueryBadge label={t("已记录")} tone="signal" />} />)}</ObjectList> : null}
        </WorkSurface>
        <WorkSurface eyebrow="Customers / plan distribution" title={t("套餐用量分布")} meta={plansQuery.data ? t("共 {{count}} 个套餐", { count: planItems.length }) : t("未加载")} description={t("每个套餐当前承载的账号数与流量占用。") }>
          {plansQuery.isError && !plansQuery.data ? <QueryFailure query={plansQuery} t={t} retry={() => void plansQuery.refetch()} /> : null}
          {plansQuery.isLoading && !plansQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载套餐用量...")}</div> : null}
          {!plansQuery.isLoading && !plansQuery.isError && planItems.length === 0 ? <EmptyWorkState title={t("暂无套餐用量数据")} detail={t("所选时间窗口内没有套餐承载流量。" )} /> : null}
          {planItems.length ? <ObjectList>{planItems.map((plan) => <ObjectRow key={plan.plan_id || "unsubscribed"} leading={<span className="cp-platform-row__mark" aria-hidden>PLN</span>} title={plan.plan_name || t("无活跃订阅")} subtitle={t("{{count}} 个订阅账号", { count: plan.subscribers })} facts={<><span>{t("窗口流量")} <span className="readout">{trafficReadout(plan.total_bytes)}</span></span><span>{t("本周期已用")} <span className="readout">{trafficReadout(plan.period_bytes)}</span></span></>} status={<QueryBadge label={t("已记录")} tone="signal" />} />)}</ObjectList> : null}
        </WorkSurface>
      </div>

      <WorkSurface
        eyebrow="Customers / anomaly queue"
        title={t("异常告警")}
        meta={alertsQuery.data ? t("未确认 {{count}} 条", { count: unacknowledgedCount }) : t("未加载")}
        description={t("用量异常检测结果按检测时间倒序排列，最多显示最近 {{limit}} 条。", { limit: ALERT_LIST_LIMIT })}
      >
        <CommandSurface actions={<ControlButton variant="secondary" size="sm" onPress={() => void alertsQuery.refetch()} loading={alertsQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>}>
          <ControlSwitch label={t("仅显示未确认")} hint={unacknowledgedOnly ? t("当前只读取开放告警。") : t("当前同时读取已确认告警。")} isSelected={unacknowledgedOnly} onChange={setUnacknowledgedOnly} />
        </CommandSurface>
        {alertsQuery.isError && !alertsQuery.data ? <QueryFailure query={alertsQuery} t={t} retry={() => void alertsQuery.refetch()} /> : null}
        {alertsQuery.isLoading && !alertsQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载异常告警...")}</div> : null}
        {!alertsQuery.isLoading && !alertsQuery.isError && alertItems.length === 0 ? <EmptyWorkState title={unacknowledgedOnly ? t("暂无未确认告警") : t("暂无异常告警")} detail={unacknowledgedOnly ? t("所有已检测到的用量异常均已确认。") : t("用量检测尚未发现异常。")} /> : null}
        {alertItems.length ? <ObjectList>{alertItems.map((alert) => {
          const acknowledged = alert.acknowledged_at_ns > 0;
          const acknowledging = acknowledgeMutation.isPending && acknowledgeMutation.variables === alert.id;
          return <ObjectRow key={alert.id} leading={<span className="cp-platform-row__mark" aria-hidden>ALT</span>} title={alert.username || alert.user_id} subtitle={`${formatNsDateTime(alert.detected_at_ns)} · ${alert.plan_name || t("无活跃订阅")}`} facts={<><span>{t("检测窗口")} {alert.window_minutes} {t("分钟")}</span><span>{t("阈值比例")} {formatThresholdRatio(alert.threshold_ratio)}</span><span>{t("已用 / 额度")} {formatTraffic(alert.used_bytes)} / {alert.quota_bytes > 0 ? formatTraffic(alert.quota_bytes) : t("不限")}</span></>} status={<StatusMark tone={acknowledged ? "signal" : "warn"} label={acknowledged ? t("已确认") : t("未确认")} />} action={acknowledged ? <QueryBadge label={t("已确认")} /> : <ControlButton variant="secondary" size="sm" onPress={() => acknowledgeMutation.mutate(alert.id)} loading={acknowledging}><Check size={14} aria-hidden />{t("确认")}</ControlButton>} />;
        })}</ObjectList> : null}
      </WorkSurface>
    </PageFrame>
  );
}

export default UsagePage;
