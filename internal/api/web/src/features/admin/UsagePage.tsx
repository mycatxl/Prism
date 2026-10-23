import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, RefreshCw } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Button } from "../../components/ui/Button";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import {
  Panel,
  PanelBody,
  PanelFooter,
  PanelHeader,
  PanelToolbar,
} from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { Select } from "../../components/ui/Select";
import { Sparkline } from "../../components/ui/Sparkline";
import { Switch } from "../../components/ui/Switch";
import {
  Table,
  TableWrap,
  TBody,
  TD,
  TDClip,
  TDNum,
  TH,
  THead,
  TR,
} from "../../components/ui/Table";
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

/*
 * The operator's usage desk, on the Signal Console rhythm: the page header
 * states the page and carries the statistics window and the refresh, and each
 * region below is one panel - a header that says what it is, a toolbar only
 * where a control belongs, the data grid, and a footer that carries the count.
 * The figures a reader compares - traffic, windows, thresholds - are `cell-num`
 * cells whose units drop into a `<small>`; a status is the dot-and-word pair.
 * The queries, their keys and every interaction are unchanged; only the
 * arrangement moved.
 */

const NANOS_PER_MILLI = 1_000_000;

/** The backend caps this list; the number is stated in the panel description. */
const ALERT_LIST_LIMIT = 50;

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

/** Alerts are triaged by the clock, so the day is not enough of a timestamp. */
function formatNsDateTime(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  return formatDateTime(date.toISOString());
}

/** `threshold_ratio` arrives as a fraction (0-1) and reads as a percentage. */
function formatThresholdRatio(ratio: number): string {
  if (!Number.isFinite(ratio)) return "-";
  return `${(ratio * 100).toFixed(1)}%`;
}

/** `formatBytes` keeps figure and unit apart; here the unit drops into a `<small>`. */
function trafficReadout(bytes: number): ReactNode {
  const [figure, unit] = formatBytes(bytes).split(" ");
  return (
    <>
      {figure}
      <small>{unit}</small>
    </>
  );
}

export function UsagePage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [days, setDays] = useState<number>(30);
  /* The console's job is the acknowledgement queue, so the open alerts are the default view. */
  const [unacknowledgedOnly, setUnacknowledgedOnly] = useState(true);

  const summaryQuery = useQuery({
    queryKey: ["admin-usage-summary"],
    queryFn: getUsageSummary,
  });

  const dailyQuery = useQuery({
    queryKey: ["admin-usage-daily", days],
    queryFn: () => getUsageDaily({ days }),
  });

  const usersQuery = useQuery({
    queryKey: ["admin-usage-users", days],
    queryFn: () => getUsageUsers({ days, limit: 50 }),
  });

  const platformsQuery = useQuery({
    queryKey: ["admin-usage-platforms", days],
    queryFn: () => getUsagePlatforms({ days }),
  });

  const plansQuery = useQuery({
    queryKey: ["admin-usage-plans", days],
    queryFn: () => getUsagePlans({ days }),
  });

  const alertsQuery = useQuery({
    queryKey: ["admin-usage-alerts", unacknowledgedOnly],
    queryFn: () =>
      getUsageAlerts({
        limit: ALERT_LIST_LIMIT,
        unacknowledged: unacknowledgedOnly,
      }),
  });

  const isRefreshing =
    summaryQuery.isFetching ||
    dailyQuery.isFetching ||
    usersQuery.isFetching ||
    plansQuery.isFetching ||
    platformsQuery.isFetching ||
    alertsQuery.isFetching;

  const handleRefresh = () => {
    void summaryQuery.refetch();
    void dailyQuery.refetch();
    void usersQuery.refetch();
    void plansQuery.refetch();
    void platformsQuery.refetch();
    void alertsQuery.refetch();
  };

  /*
   * Acknowledging writes through to the same meters the page reads, so the usage
   * queries are re-read with the list. A repeat acknowledge answers 404 because
   * the alert is already closed, and the failure path re-reads the list too, so
   * the stale row leaves the screen instead of waiting for a manual refresh.
   */
  const acknowledgeMutation = useMutation({
    mutationFn: (id: string) => acknowledgeAlert(id),
    onSuccess: () => {
      showToast("success", t("告警已确认"));
      void queryClient.invalidateQueries({ queryKey: ["admin-usage-alerts"] });
      handleRefresh();
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
      void queryClient.invalidateQueries({ queryKey: ["admin-usage-alerts"] });
    },
  });

  const summary = summaryQuery.data;
  const dailyItems = dailyQuery.data?.items ?? [];
  const sparklineValues = dailyItems.map((d) => d.total_bytes);
  const userItems = usersQuery.data?.items ?? [];
  const platformItems = platformsQuery.data?.items ?? [];
  const planItems = plansQuery.data?.items ?? [];
  const alertItems = alertsQuery.data?.items ?? [];
  const unacknowledgedCount = alertItems.filter(
    (alert) => alert.acknowledged_at_ns <= 0,
  ).length;

  const showDaily = !dailyQuery.isLoading && !dailyQuery.isError;
  const showUsers = !usersQuery.isLoading && !usersQuery.isError;
  const showPlatforms = !platformsQuery.isLoading && !platformsQuery.isError;
  const showPlans = !plansQuery.isLoading && !plansQuery.isError;
  const showAlerts = !alertsQuery.isLoading && !alertsQuery.isError;

  return (
    <Page bleed>
      <PageHeader
        title={t("用量")}
        description={t("全站资源用量、用户流量排名与每日用量趋势统计。")}
        meta={<PageMeta label={t("统计周期")} value={t("最近 {{days}} 天", { days })} />}
        actions={
          <>
            <Select
              aria-label={t("统计周期")}
              className="w-28"
              value={String(days)}
              onChange={(e) => setDays(Number(e.target.value))}
            >
              <option value="7">{t("最近 7 天")}</option>
              <option value="30">{t("最近 30 天")}</option>
              <option value="90">{t("最近 90 天")}</option>
            </Select>
            <Button
              variant="secondary"
              size="sm"
              onClick={handleRefresh}
              disabled={isRefreshing}
              title={t("刷新")}
            >
              <RefreshCw
                size={14}
                className={isRefreshing ? "animate-spin" : undefined}
              />
              <span>{t("刷新")}</span>
            </Button>
          </>
        }
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="page-content page-content--fill">
        {/* Site meters: one strip of readouts, not five framed cards. */}
        {summaryQuery.isError ? (
          <Panel>
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(summaryQuery.error, t)}
                onRetry={() => void summaryQuery.refetch()}
              />
            </PanelBody>
          </Panel>
        ) : summaryQuery.isLoading ? (
          <Panel>
            <PanelBody>
              <LoadingState label={t("正在加载用量概览...")} />
            </PanelBody>
          </Panel>
        ) : (
          <ReadoutStrip
            aria-busy={summaryQuery.isFetching}
            className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5"
          >
            <ReadoutCell>
              <Readout
                label={t("活跃用户")}
                value={String(summary?.active_users ?? 0)}
                unit={t("人")}
                tone="signal"
              />
            </ReadoutCell>
            <ReadoutCell>
              <Readout
                label={t("总用户数")}
                value={String(summary?.total_users ?? 0)}
                unit={t("人")}
                tone="ink"
              />
            </ReadoutCell>
            <ReadoutCell>
              <Readout
                label={t("今日全站流量")}
                value={formatBytes(summary?.today_bytes ?? 0)}
                tone="ink"
              />
            </ReadoutCell>
            <ReadoutCell>
              <Readout
                label={t("本周期总流量")}
                value={formatBytes(summary?.period_bytes ?? 0)}
                tone="ink"
              />
            </ReadoutCell>
            <ReadoutCell>
              <Readout
                label={t("即将到期用户")}
                value={String(summary?.expiring_soon_users ?? 0)}
                unit={t("人")}
                hint={
                  summary?.expiring_within_days
                    ? t("未来 {{days}} 天内", { days: summary.expiring_within_days })
                    : undefined
                }
                tone={
                  (summary?.expiring_soon_users ?? 0) > 0 ? "warn" : "muted"
                }
              />
            </ReadoutCell>
          </ReadoutStrip>
        )}

        {/* Daily traffic trend */}
        <Panel className="flex min-w-0 flex-col">
          <PanelHeader
            title={t("每日流量趋势")}
            description={t("所选时间范围内的全站每日流量消耗趋势。")}
            actions={
              sparklineValues.length >= 2 ? (
                <div className="w-36">
                  <Sparkline values={sparklineValues} tone="accent" height={28} />
                </div>
              ) : undefined
            }
          />
          {dailyQuery.isLoading ? (
            <PanelBody>
              <LoadingState label={t("正在加载每日流量...")} />
            </PanelBody>
          ) : null}
          {dailyQuery.isError ? (
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(dailyQuery.error, t)}
                onRetry={() => void dailyQuery.refetch()}
              />
            </PanelBody>
          ) : null}
          {showDaily && dailyItems.length === 0 ? (
            <PanelBody>
              <EmptyState
                title={t("暂无每日流量数据")}
                hint={t("所选时间窗口内未记录到流量数据。")}
              />
            </PanelBody>
          ) : null}
          {showDaily && dailyItems.length > 0 ? (
            <TableWrap aria-busy={dailyQuery.isFetching}>
              <Table className="min-w-[480px]">
                <THead>
                  <TR>
                    <TH>{t("日期")}</TH>
                    <TH className="text-right">{t("入站流量")}</TH>
                    <TH className="text-right">{t("出站流量")}</TH>
                    <TH className="text-right">{t("合计流量")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {dailyItems.map((day) => (
                    <TR key={day.day_ns}>
                      <TDClip className="readout whitespace-nowrap text-xs">
                        {formatNsDate(day.day_ns)}
                      </TDClip>
                      <TDNum className="whitespace-nowrap text-xs text-ink-soft">
                        <span className="readout">{trafficReadout(day.ingress_bytes)}</span>
                      </TDNum>
                      <TDNum className="whitespace-nowrap text-xs text-ink-soft">
                        <span className="readout">{trafficReadout(day.egress_bytes)}</span>
                      </TDNum>
                      <TDNum className="whitespace-nowrap text-xs">
                        <span className="readout">{trafficReadout(day.total_bytes)}</span>
                      </TDNum>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          ) : null}
          {showDaily && dailyItems.length > 0 ? (
            <PanelFooter>
              <p className="text-xs text-ink-soft">
                {t("共 {{count}} 天", { count: dailyItems.length })}
              </p>
            </PanelFooter>
          ) : null}
        </Panel>

        {/* User usage ranking: the closest thing to a tenant detail ledger the API carries. */}
        <Panel className="flex min-w-0 flex-col">
          <PanelHeader
            title={t("用户用量排行")}
            description={t("统计所选时间窗口内流量消耗最高的用户（前 50 名）。")}
          />
          {usersQuery.isLoading ? (
            <PanelBody>
              <LoadingState label={t("正在加载用户用量排行...")} />
            </PanelBody>
          ) : null}
          {usersQuery.isError ? (
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(usersQuery.error, t)}
                onRetry={() => void usersQuery.refetch()}
              />
            </PanelBody>
          ) : null}
          {showUsers && userItems.length === 0 ? (
            <PanelBody>
              <EmptyState
                title={t("暂无用户用量数据")}
                hint={t("所选时间窗口内未记录到用户流量。")}
              />
            </PanelBody>
          ) : null}
          {showUsers && userItems.length > 0 ? (
            <TableWrap aria-busy={usersQuery.isFetching}>
              <Table className="min-w-[720px]">
                <THead>
                  <TR>
                    <TH>{t("用户名")}</TH>
                    <TH>{t("套餐")}</TH>
                    <TH className="text-right">{t("入站流量")}</TH>
                    <TH className="text-right">{t("出站流量")}</TH>
                    <TH className="text-right">{t("合计流量")}</TH>
                    <TH className="text-right">{t("周期用量")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {userItems.map((u) => (
                    <TR key={u.user_id}>
                      <TDClip className="font-medium text-ink">{u.username}</TDClip>
                      <TDClip className="text-xs text-ink-soft">
                        {u.plan_name || "-"}
                      </TDClip>
                      <TDNum className="whitespace-nowrap text-xs text-ink-soft">
                        <span className="readout">{trafficReadout(u.ingress_bytes)}</span>
                      </TDNum>
                      <TDNum className="whitespace-nowrap text-xs text-ink-soft">
                        <span className="readout">{trafficReadout(u.egress_bytes)}</span>
                      </TDNum>
                      <TDNum className="whitespace-nowrap text-xs">
                        <span className="readout">{trafficReadout(u.total_bytes)}</span>
                      </TDNum>
                      <TDNum className="whitespace-nowrap text-xs">
                        <span className="readout">
                          {formatBytes(u.period_used_bytes)} /{" "}
                          {u.traffic_bytes > 0
                            ? formatBytes(u.traffic_bytes)
                            : t("不限")}
                        </span>
                      </TDNum>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          ) : null}
          {showUsers && userItems.length > 0 ? (
            <PanelFooter>
              <p className="text-xs text-ink-soft">
                {t("共 {{count}} 名用户", { count: userItems.length })}
              </p>
            </PanelFooter>
          ) : null}
        </Panel>

        {/* Two smaller distributions side by side */}
        <div className="grid min-w-0 grid-cols-1 gap-4 lg:grid-cols-2">
          {/* Platform usage distribution */}
          <Panel className="flex min-w-0 flex-col">
            <PanelHeader
              title={t("平台用量分布")}
              description={t("各平台在所选时间窗口内的流量分发情况。")}
            />
            {platformsQuery.isLoading ? (
              <PanelBody>
                <LoadingState label={t("正在加载平台用量...")} />
              </PanelBody>
            ) : null}
            {platformsQuery.isError ? (
              <PanelBody>
                <ErrorState
                  message={formatApiErrorMessage(platformsQuery.error, t)}
                  onRetry={() => void platformsQuery.refetch()}
                />
              </PanelBody>
            ) : null}
            {showPlatforms && platformItems.length === 0 ? (
              <PanelBody>
                <EmptyState
                  title={t("暂无平台用量数据")}
                  hint={t("所选时间窗口内未记录到平台流量。")}
                />
              </PanelBody>
            ) : null}
            {showPlatforms && platformItems.length > 0 ? (
              <TableWrap aria-busy={platformsQuery.isFetching}>
                <Table>
                  <THead>
                    <TR>
                      <TH>{t("平台标识")}</TH>
                      <TH>{t("平台名称")}</TH>
                      <TH className="text-right">{t("合计流量")}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {platformItems.map((p) => (
                      <TR key={p.platform_id}>
                        <TDClip className="readout text-xs" title={p.platform_id}>
                          {p.platform_id}
                        </TDClip>
                        <TDClip className="font-medium text-ink">
                          {p.platform_name || p.platform_id}
                        </TDClip>
                        <TDNum className="whitespace-nowrap text-xs">
                          <span className="readout">{trafficReadout(p.total_bytes)}</span>
                        </TDNum>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              </TableWrap>
            ) : null}
            {showPlatforms && platformItems.length > 0 ? (
              <PanelFooter>
                <p className="text-xs text-ink-soft">
                  {t("共 {{count}} 个平台", { count: platformItems.length })}
                </p>
              </PanelFooter>
            ) : null}
          </Panel>

          {/* Plan usage distribution: what each plan carries, not what one account did. */}
          <Panel className="flex min-w-0 flex-col">
            <PanelHeader
              title={t("套餐用量分布")}
              description={t("每个套餐当前承载的账号数与流量占用。")}
            />
            {plansQuery.isLoading ? (
              <PanelBody>
                <LoadingState label={t("正在加载套餐用量...")} />
              </PanelBody>
            ) : null}
            {plansQuery.isError ? (
              <PanelBody>
                <ErrorState
                  message={formatApiErrorMessage(plansQuery.error, t)}
                  onRetry={() => void plansQuery.refetch()}
                />
              </PanelBody>
            ) : null}
            {showPlans && planItems.length === 0 ? (
              <PanelBody>
                <EmptyState
                  title={t("暂无套餐用量数据")}
                  hint={t("所选时间窗口内没有套餐承载流量。")}
                />
              </PanelBody>
            ) : null}
            {showPlans && planItems.length > 0 ? (
              <TableWrap aria-busy={plansQuery.isFetching}>
                <Table>
                  <THead>
                    <TR>
                      <TH>{t("套餐")}</TH>
                      <TH className="text-right">{t("订阅账号")}</TH>
                      <TH className="text-right">{t("窗口流量")}</TH>
                      <TH className="text-right">{t("本周期已用")}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {planItems.map((p) => (
                      <TR key={p.plan_id || "unsubscribed"}>
                        <TDClip className="font-medium text-ink">
                          {p.plan_name || t("无活跃订阅")}
                        </TDClip>
                        <TDNum className="whitespace-nowrap text-xs">
                          <span className="readout">{p.subscribers}</span>
                        </TDNum>
                        <TDNum className="whitespace-nowrap text-xs">
                          <span className="readout">{trafficReadout(p.total_bytes)}</span>
                        </TDNum>
                        <TDNum className="whitespace-nowrap text-xs">
                          <span className="readout">{trafficReadout(p.period_bytes)}</span>
                        </TDNum>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              </TableWrap>
            ) : null}
            {showPlans && planItems.length > 0 ? (
              <PanelFooter>
                <p className="text-xs text-ink-soft">
                  {t("共 {{count}} 个套餐", { count: planItems.length })}
                </p>
              </PanelFooter>
            ) : null}
          </Panel>
        </div>

        {/* Usage anomaly alerts: what the detector flagged, and the queue to clear. */}
        <Panel className="flex min-w-0 flex-col">
          <PanelHeader
            title={t("异常告警")}
            description={t(
              "用量异常检测结果，按检测时间倒序排列，最多显示最近 {{limit}} 条。",
              { limit: ALERT_LIST_LIMIT },
            )}
            meta={
              <span className="inline-flex shrink-0 items-center gap-2">
                <span
                  aria-hidden
                  className={
                    unacknowledgedCount > 0 ? "state-dot state-dot--warn" : "state-dot"
                  }
                />
                <span className="state-text">
                  {t("未确认 {{count}} 条", { count: unacknowledgedCount })}
                </span>
              </span>
            }
          />
          <PanelToolbar>
            <span className="text-xs text-ink-soft">
              {unacknowledgedOnly ? t("仅显示未确认") : t("全部告警")}
            </span>
            <Switch
              aria-label={unacknowledgedOnly ? t("仅显示未确认") : t("全部告警")}
              checked={unacknowledgedOnly}
              onCheckedChange={setUnacknowledgedOnly}
            />
            <Button
              variant="secondary"
              size="sm"
              className="ml-auto"
              onClick={() => void alertsQuery.refetch()}
              disabled={alertsQuery.isFetching}
              title={t("刷新")}
            >
              <RefreshCw
                size={14}
                className={alertsQuery.isFetching ? "animate-spin" : undefined}
              />
              <span>{t("刷新")}</span>
            </Button>
          </PanelToolbar>
          {alertsQuery.isLoading ? (
            <PanelBody>
              <LoadingState label={t("正在加载异常告警...")} />
            </PanelBody>
          ) : null}
          {alertsQuery.isError ? (
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(alertsQuery.error, t)}
                onRetry={() => void alertsQuery.refetch()}
              />
            </PanelBody>
          ) : null}
          {showAlerts && alertItems.length === 0 ? (
            <PanelBody>
              <EmptyState
                title={unacknowledgedOnly ? t("暂无未确认告警") : t("暂无异常告警")}
                hint={
                  unacknowledgedOnly
                    ? t("所有已检测到的用量异常均已确认。")
                    : t("用量检测尚未发现异常。")
                }
              />
            </PanelBody>
          ) : null}
          {showAlerts && alertItems.length > 0 ? (
            <TableWrap aria-busy={alertsQuery.isFetching}>
              <Table className="min-w-[860px]">
                <THead>
                  <TR>
                    <TH>{t("检测时间")}</TH>
                    <TH>{t("账号")}</TH>
                    <TH>{t("套餐")}</TH>
                    <TH className="text-right">{t("检测窗口")}</TH>
                    <TH className="text-right">{t("阈值比例")}</TH>
                    <TH className="text-right">{t("已用 / 额度")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {alertItems.map((alert) => {
                    const acknowledged = alert.acknowledged_at_ns > 0;
                    const acknowledging =
                      acknowledgeMutation.isPending && acknowledgeMutation.variables === alert.id;
                    return (
                      <TR key={alert.id}>
                        <TDClip className="readout whitespace-nowrap text-xs">
                          {formatNsDateTime(alert.detected_at_ns)}
                        </TDClip>
                        <TDClip className="text-ink">
                          <span className="font-semibold">
                            {alert.username || alert.user_id}
                          </span>
                          {alert.username ? (
                            <span className="ml-1.5 readout text-xs text-ink-faint">
                              {alert.user_id}
                            </span>
                          ) : null}
                        </TDClip>
                        <TDClip className="text-xs text-ink-soft">
                          {alert.plan_name || t("无活跃订阅")}
                        </TDClip>
                        <TDNum className="whitespace-nowrap text-xs text-ink-soft">
                          {t("{{minutes}} 分钟", { minutes: alert.window_minutes })}
                        </TDNum>
                        <TDNum className="whitespace-nowrap text-xs text-ink-soft">
                          {formatThresholdRatio(alert.threshold_ratio)}
                        </TDNum>
                        <TDNum className="whitespace-nowrap text-xs">
                          <span className="readout">
                            {formatBytes(alert.used_bytes)} /{" "}
                            {alert.quota_bytes > 0
                              ? formatBytes(alert.quota_bytes)
                              : t("不限")}
                          </span>
                        </TDNum>
                        <TD className="whitespace-nowrap">
                          <span className="inline-flex items-center gap-2">
                            <span
                              aria-hidden
                              className={
                                acknowledged ? "state-dot" : "state-dot state-dot--warn"
                              }
                            />
                            <span className="state-text">
                              {acknowledged ? t("已确认") : t("未确认")}
                            </span>
                          </span>
                        </TD>
                        <TD className="text-right">
                          {acknowledged ? (
                            <span className="text-xs text-ink-faint">-</span>
                          ) : (
                            <Button
                              variant="secondary"
                              size="sm"
                              loading={acknowledging}
                              onClick={() => acknowledgeMutation.mutate(alert.id)}
                              title={t("确认告警")}
                            >
                              <Check size={14} />
                              <span>{t("确认")}</span>
                            </Button>
                          )}
                        </TD>
                      </TR>
                    );
                  })}
                </TBody>
              </Table>
            </TableWrap>
          ) : null}
          {showAlerts && alertItems.length > 0 ? (
            <PanelFooter>
              <p className="text-xs text-ink-soft">
                {t("共 {{count}} 条", { count: alertItems.length })}
              </p>
            </PanelFooter>
          ) : null}
        </Panel>
      </div>
    </Page>
  );
}
