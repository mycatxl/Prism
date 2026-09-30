import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  ChevronRight,
  CircleAlert,
  Gauge,
  Globe2,
  Plus,
  RefreshCw,
  Rss,
  Share2,
  Waypoints,
  Zap,
} from "lucide-react";
import { lazy, Suspense, useMemo, useState, useSyncExternalStore } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Donut } from "../../components/ui/Donut";
import { Page } from "../../components/ui/PageHeader";
import { Panel, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout } from "../../components/ui/Readout";
import { Sparkline } from "../../components/ui/Sparkline";
import { Table, TableWrap, TBody, TD, TDClip, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { useI18n } from "../../i18n";
import { ApiError, apiRequest } from "../../lib/api-client";
import { formatRelativeTime } from "../../lib/time";
import { listAuditLogs } from "../audit/api";
import { listNodes } from "../nodes/api";
import { listPlatforms } from "../platforms/api";
import { listSubscriptions } from "../subscriptions/api";
import {
  type DashboardGlobalHistoryData,
  type DashboardGlobalRealtimeData,
  getDashboardGlobalHistoryData,
  getDashboardGlobalRealtimeData,
  getDashboardGlobalSnapshotData,
  listNodeExitFacts,
} from "./api";
import { EXIT_COUNT_BANDS, exitCountBandLabel } from "./chartPalette";
import {
  PLACEHOLDER,
  formatBytes,
  formatCount,
  formatLatency,
  formatPercent,
  formatTimestamp,
  toEpochMs,
} from "./format";
import {
  DEFAULT_RANGE_KEY,
  NODE_EXITS_REFRESH_MS,
  RANGE_OPTIONS,
  SNAPSHOT_REFRESH_MS,
  type RangeKey,
  getTimeWindow,
  historyRefreshMsFromBuckets,
  parseRangeKey,
  rangeOption,
  realtimeRefreshMsFromSteps,
} from "./range";
import { aggregateExitsByRegion } from "./worldMap";
import LatencyProfile from "./LatencyProfile";

/**
 * The charts are the only ECharts consumers in the console, so they load with the
 * board and the canvas, not with the route table.
 */
const EgressMap = lazy(() => import("./EgressMap"));
const EgressGlobe = lazy(() => import("./EgressGlobe"));
const TrafficChart = lazy(() => import("./TrafficChart"));

type Point = [number, number];

/** The two views of the egress panel: the sphere, and the plate it replaced. */
type MapView = "globe" | "flat";

function toPoints<T>(items: T[], valueOf: (item: T) => number, stampOf: (item: T) => string): Point[] {
  const points: Point[] = [];
  for (const item of items) {
    const stamp = toEpochMs(stampOf(item));
    if (stamp === null) {
      continue;
    }
    points.push([stamp, guardValue(valueOf(item))]);
  }
  return points.sort((left, right) => left[0] - right[0]);
}

function guardValue(value: number): number {
  return Number.isFinite(value) ? value : 0;
}

/**
 * The change between the two halves of the window, as a fraction.
 *
 * This is the only honest delta a single window can produce: the board fetches one
 * range, so a "vs yesterday" figure would need a second fetch that nobody asked for,
 * and a trend arrow with no basis is a decoration. `null` means "not a trend" — an
 * empty or all-zero first half, or fewer than two readings.
 */
function halfDelta(values: number[]): number | null {
  if (values.length < 4) {
    return null;
  }
  const middle = Math.floor(values.length / 2);
  const first = values.slice(0, middle).reduce((sum, value) => sum + value, 0);
  const second = values.slice(middle).reduce((sum, value) => sum + value, 0);
  if (first <= 0) {
    return null;
  }
  return (second - first) / first;
}

/** The change since the previous reading of a live series, as a fraction. */
function stepDelta(values: number[]): number | null {
  if (values.length < 2) {
    return null;
  }
  const previous = values[values.length - 2];
  const latest = values[values.length - 1];
  if (!Number.isFinite(previous) || previous <= 0) {
    return null;
  }
  return (latest - previous) / previous;
}

function subscribeOnline(callback: () => void) {
  window.addEventListener("online", callback);
  window.addEventListener("offline", callback);
  return () => {
    window.removeEventListener("online", callback);
    window.removeEventListener("offline", callback);
  };
}

/**
 * A trend chip: an arrow, a signed percentage, and the basis in its own `title` and
 * in the line under it.
 *
 * Deliberately not a `Badge`. The pill is reserved for *state*, and a trend is a
 * measurement — spending the pill here would flatten the one shape that says "this
 * is a status". The arrow and the sign carry the direction, so the colour is never
 * the only channel.
 */
function Delta({ value, basis }: { value: number | null; basis: string }) {
  if (value === null || !Number.isFinite(value)) {
    return null;
  }
  const up = value >= 0;
  return (
    <span
      className={up ? "readout text-2xs text-signal" : "readout text-2xs text-alert"}
      title={basis}
    >
      {up ? "▲" : "▼"} {formatPercent(Math.abs(value))}
    </span>
  );
}

/** One KPI: an icon, the reading, its trend with the basis, and the series behind it. */
function Kpi({
  icon: Icon,
  label,
  value,
  unit,
  trend,
  basis,
  series,
  tone,
}: {
  icon: typeof Activity;
  label: string;
  value: string;
  unit?: string;
  trend: number | null;
  basis: string;
  series: number[];
  tone: "accent" | "signal" | "live" | "alert";
}) {
  const iconTone = {
    accent: "bg-accent-wash text-accent",
    signal: "bg-signal-wash text-signal",
    live: "bg-live-wash text-live",
    alert: "bg-alert-wash text-alert",
  }[tone];
  return (
    <Panel className="min-w-0 p-4">
      <div className="flex items-start gap-2.5">
        <span aria-hidden className={`grid size-7 shrink-0 place-items-center rounded-control ${iconTone}`}>
          <Icon size={15} />
        </span>
        <Readout className="min-w-0 flex-1" label={label} value={value} unit={unit} size="md" />
      </div>
      <div className="mt-2 flex items-center gap-2 text-2xs text-ink-faint">
        <Delta value={trend} basis={basis} />
        <span className="truncate">{basis}</span>
      </div>
      <Sparkline values={series} tone={tone} className="mt-2" />
    </Panel>
  );
}

/** One row of the quick-action list: a real destination, never a fake button. */
function QuickAction({ to, icon: Icon, label }: { to: string; icon: typeof Activity; label: string }) {
  return (
    <Link
      to={to}
      className="action flex min-h-[var(--control-h-xl)] items-center gap-2.5 rounded-control px-2.5 text-sm text-ink-soft hover:bg-glass hover:text-ink"
    >
      <Icon size={15} aria-hidden className="shrink-0 text-ink-faint" />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      <ChevronRight size={14} aria-hidden className="shrink-0 text-ink-faint" />
    </Link>
  );
}

/**
 * The overview: a bento board for the whole inventory.
 *
 * Two columns of glass panes rather than one uniform grid. The left column is the
 * data story — where traffic leaves from, what the pool is doing, what arrived last
 * — and the right column holds the small, always-on panes: instance state, the four
 * destinations an operator reaches for mid-incident, the latency shape, the platform
 * split and the change log. The asymmetry is the point: a board of twelve equal
 * rectangles makes every fact look equally important, which is the same as saying
 * nothing.
 *
 * Every figure here is fetched. A panel with nothing behind it renders its empty
 * state instead of a placeholder number, and every trend states its basis, because
 * the two failure modes of a dashboard are a number nobody measured and a direction
 * nobody can check.
 */
export function WorkbenchPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const [mapView, setMapView] = useState<MapView>("globe");
  const rangeKey = parseRangeKey(params.get("range"));
  const queryClient = useQueryClient();
  const online = useSyncExternalStore(
    subscribeOnline,
    () => navigator.onLine,
    () => true,
  );

  const snapshot = useQuery({
    queryKey: ["dashboard-global-snapshot"],
    queryFn: getDashboardGlobalSnapshotData,
    refetchInterval: SNAPSHOT_REFRESH_MS,
    placeholderData: (previous) => previous,
  });

  const realtime = useQuery({
    queryKey: ["workbench", "realtime", rangeKey],
    queryFn: async () => {
      const previous = queryClient.getQueryData<DashboardGlobalRealtimeData>(["workbench", "realtime", rangeKey]);
      return getDashboardGlobalRealtimeData(getTimeWindow(rangeKey), previous);
    },
    refetchInterval: (query) => {
      const data = query.state.data as DashboardGlobalRealtimeData | undefined;
      return realtimeRefreshMsFromSteps([
        data?.realtime_throughput.step_seconds,
        data?.realtime_connections.step_seconds,
        data?.realtime_leases.step_seconds,
      ]);
    },
    placeholderData: (previous) => previous,
  });

  const history = useQuery({
    queryKey: ["workbench", "history", rangeKey],
    queryFn: async () => {
      const previous = queryClient.getQueryData<DashboardGlobalHistoryData>(["workbench", "history", rangeKey]);
      return getDashboardGlobalHistoryData(getTimeWindow(rangeKey), previous);
    },
    refetchInterval: (query) => {
      const data = query.state.data as DashboardGlobalHistoryData | undefined;
      return historyRefreshMsFromBuckets([
        data?.history_traffic.bucket_seconds,
        data?.history_requests.bucket_seconds,
        data?.history_node_pool.bucket_seconds,
      ]);
    },
    placeholderData: (previous) => previous,
  });

  const nodes = useQuery({
    queryKey: ["workbench", "node-exits"],
    queryFn: ({ signal }) => listNodeExitFacts(signal),
    refetchInterval: NODE_EXITS_REFRESH_MS,
    placeholderData: (previous) => previous,
  });

  /*
   * The shell already polls the instance info under this key, so the status pane
   * reads the same cache entry instead of opening a second request for a version
   * string nobody changes.
   */
  const info = useQuery({
    queryKey: ["system-info", "shell"],
    queryFn: () => apiRequest<{ version: string }>("/api/v1/system/info"),
    refetchInterval: 30_000,
    retry: false,
  });

  const recentNodes = useQuery({
    queryKey: ["workbench", "recent-nodes"],
    queryFn: ({ signal }) => listNodes({ limit: 6, sort_by: "created_at", sort_order: "desc" }, signal),
    refetchInterval: NODE_EXITS_REFRESH_MS,
    placeholderData: (previous) => previous,
  });

  const platforms = useQuery({
    queryKey: ["workbench", "platforms"],
    queryFn: () => listPlatforms({ limit: 100 }),
    staleTime: 60_000,
    refetchInterval: 120_000,
  });

  const subscriptions = useQuery({
    queryKey: ["workbench", "subscriptions"],
    queryFn: () => listSubscriptions({ limit: 200 }),
    staleTime: 60_000,
    refetchInterval: 120_000,
  });

  const events = useQuery({
    queryKey: ["workbench", "events"],
    queryFn: () => listAuditLogs({ limit: 5 }),
    refetchInterval: 60_000,
  });

  const pool = snapshot.data?.snapshot_node_pool;
  const latency = snapshot.data?.snapshot_latency_global;

  const nodeFacts = useMemo(() => nodes.data ?? [], [nodes.data]);
  const { regions, unknown } = useMemo(() => aggregateExitsByRegion(nodeFacts), [nodeFacts]);

  const leaseItems = useMemo(() => realtime.data?.realtime_leases.items ?? [], [realtime.data]);
  const latestLease = leaseItems.at(-1);

  const requestItems = useMemo(() => history.data?.history_requests.items ?? [], [history.data]);
  const windowRequests = useMemo(
    () =>
      requestItems.reduce(
        (totals, item) => ({
          total: totals.total + item.total_requests,
          success: totals.success + item.success_requests,
        }),
        { total: 0, success: 0 },
      ),
    [requestItems],
  );

  const trafficItems = useMemo(() => history.data?.history_traffic.items ?? [], [history.data]);
  const ingressPoints = useMemo(
    () => toPoints(trafficItems, (item) => item.ingress_bytes, (item) => item.bucket_start),
    [trafficItems],
  );
  const egressPoints = useMemo(
    () => toPoints(trafficItems, (item) => item.egress_bytes, (item) => item.bucket_start),
    [trafficItems],
  );
  const windowVolume = useMemo(
    () => trafficItems.reduce((total, item) => total + item.ingress_bytes + item.egress_bytes, 0),
    [trafficItems],
  );

  const connectionItems = useMemo(() => realtime.data?.realtime_connections.items ?? [], [realtime.data]);
  const connectionPoints = useMemo(
    () => toPoints(connectionItems, (item) => item.inbound_connections + item.outbound_connections, (item) => item.ts),
    [connectionItems],
  );

  /*
   * The trend series, one array per KPI, straight from the series the board already
   * fetched. Nothing here is synthesised: a bucket with no samples contributes 0 to
   * the requests line and is skipped for latency, so a gap in sampling reads as a gap
   * rather than as a zero.
   */
  const requestSeries = useMemo(() => requestItems.map((item) => guardValue(item.total_requests)), [requestItems]);
  const errorSeries = useMemo(
    () => requestItems.map((item) => guardValue(1 - item.success_rate)),
    [requestItems],
  );
  const leaseSeries = useMemo(() => leaseItems.map((item) => guardValue(item.active_leases)), [leaseItems]);

  const latencyItems = useMemo(
    () => history.data?.history_access_latency.items ?? [],
    [history.data],
  );
  /*
   * A latency histogram carries a count per upper bound, not the samples, so the mean
   * is the weighted mean of the bin upper bounds — an estimate, and labelled as one
   * wherever it is shown. It is still the board's only window-wide latency figure.
   */
  const latencySeries = useMemo(
    () =>
      latencyItems
        .map((item) => {
          const total = item.buckets.reduce((sum, bucket) => sum + Math.max(0, bucket.count), 0);
          if (total <= 0) {
            return 0;
          }
          return item.buckets.reduce((sum, bucket) => sum + bucket.le_ms * Math.max(0, bucket.count), 0) / total;
        })
        .filter((value) => value > 0),
    [latencyItems],
  );
  const averageLatency = useMemo(() => {
    if (latencySeries.length === 0) {
      return null;
    }
    return latencySeries.reduce((sum, value) => sum + value, 0) / latencySeries.length;
  }, [latencySeries]);

  const busy = snapshot.isFetching || realtime.isFetching || history.isFetching || nodes.isFetching;
  const poolHealthy = pool?.healthy_nodes ?? 0;
  const topRegions = useMemo(() => regions.slice(0, 8), [regions]);
  const busiestRegion = topRegions[0]?.exits ?? 0;

  const windowSuccessRate = windowRequests.total > 0 ? windowRequests.success / windowRequests.total : null;
  const errorRate = windowSuccessRate === null ? null : 1 - windowSuccessRate;

  const platformSlices = useMemo(() => {
    const items = platforms.data?.items ?? [];
    const ranked = [...items]
      .sort((left, right) => right.routable_node_count - left.routable_node_count)
      .map((item) => ({ label: item.name, value: item.routable_node_count }));
    if (ranked.length <= 5) {
      return ranked;
    }
    const head = ranked.slice(0, 5);
    const tail = ranked.slice(5).reduce((sum, item) => sum + item.value, 0);
    return [...head, { label: t("其他"), value: tail }];
  }, [platforms.data, t]);

  const subscriptionState = useMemo(() => {
    const items = subscriptions.data?.items ?? [];
    let enabled = 0;
    let disabled = 0;
    let failed = 0;
    let nodes = 0;
    let healthy = 0;
    for (const item of items) {
      if (!item.enabled) {
        disabled += 1;
      } else if (item.last_error) {
        failed += 1;
      } else {
        enabled += 1;
      }
      nodes += item.node_count;
      healthy += item.healthy_node_count;
    }
    return { total: items.length, enabled, disabled, failed, nodes, healthy };
  }, [subscriptions.data]);

  const selectRange = (next: RangeKey) => {
    const nextParams = new URLSearchParams(params);
    if (next === DEFAULT_RANGE_KEY) {
      nextParams.delete("range");
    } else {
      nextParams.set("range", next);
    }
    setParams(nextParams, { replace: true });
  };

  const chartEmpty = ingressPoints.length === 0 && egressPoints.length === 0 && connectionPoints.length === 0;
  const chartFallback = <LoadingState className="h-full" label={t("正在加载")} />;
  const offline = t("无法连接服务，请检查连接后重试。");
  const basisHalf = t("较前半段");
  const basisStep = t("较上一采样");
  const latencyBasis = t("按直方图分箱上界估算");

  const unauthorized = info.error instanceof ApiError && info.error.status === 401;
  const disconnected = !online || info.isError;
  const instanceState = unauthorized
    ? { tone: "alert" as const, label: t("令牌失效") }
    : disconnected
      ? { tone: "warn" as const, label: t("连接中断") }
      : info.data
        ? { tone: "signal" as const, label: t("实例在线") }
        : { tone: "neutral" as const, label: t("连接中") };

  return (
    <Page bleed>
      <div className="flex flex-col gap-3 px-[var(--page-gutter)] py-3.5 lg:gap-4 2xl:gap-5 2xl:py-5">
        {snapshot.isError && <ErrorState message={offline} onRetry={() => void snapshot.refetch()} />}

        <div className="grid min-w-0 gap-3 lg:gap-4 2xl:gap-5 xl:grid-cols-12">
          {/* The left column: the data story, from "where does traffic leave from" to
              "what changed in the last five minutes". */}
          <div className="flex min-w-0 flex-col gap-3 lg:gap-4 2xl:gap-5 xl:col-span-8">
            <Panel className="hero-gradient min-w-0 overflow-hidden">
              <div className="flex flex-wrap items-start justify-between gap-3 px-5 py-4">
                <div className="min-w-0">
                  <h1 className="truncate text-xl font-semibold text-ink">{t("总览看板")}</h1>
                  <p className="mt-1 max-w-[68ch] text-xs text-ink-soft">
                    {t("查看线路健康、出口质量和实时连接。")}
                  </p>
                </div>
                <div className="flex shrink-0 flex-wrap items-center gap-2">
                  <div
                    role="group"
                    aria-label={t("时间范围")}
                    className="inline-flex h-[var(--control-h)] divide-x divide-rule overflow-hidden rounded-control border border-glass-edge-strong bg-glass"
                  >
                    {RANGE_OPTIONS.map((option) => (
                      <Button
                        key={option.key}
                        variant="quiet"
                        size="sm"
                        className="h-full rounded-none border-0 px-2.5 aria-pressed:bg-glass-strong aria-pressed:font-semibold aria-pressed:text-ink"
                        aria-pressed={option.key === rangeKey}
                        onClick={() => selectRange(option.key)}
                      >
                        {t(option.label)}
                      </Button>
                    ))}
                  </div>
                  <Button
                    variant="secondary"
                    size="icon"
                    type="button"
                    title={t("刷新")}
                    aria-label={t("刷新")}
                    disabled={busy}
                    onClick={() => void queryClient.invalidateQueries()}
                  >
                    <RefreshCw size={15} className={busy ? "animate-spin" : ""} />
                  </Button>
                  <Button asChild variant="primary">
                    <Link to="/subscriptions?create=1">
                      <Plus size={15} />
                      {t("添加订阅")}
                    </Link>
                  </Button>
                </div>
              </div>

              <div className="grid grid-cols-2 gap-x-4 gap-y-3 border-t border-glass-edge-strong px-5 py-4 xl:grid-cols-4">
                <Readout
                  label={t("节点总数")}
                  value={pool ? formatCount(pool.total_nodes) : PLACEHOLDER}
                  hint={`${t("健康节点")} ${pool ? formatCount(poolHealthy) : PLACEHOLDER}`}
                />
                <Readout
                  tone="signal"
                  label={t("健康节点")}
                  value={pool ? formatCount(poolHealthy) : PLACEHOLDER}
                  hint={`${t("可路由节点")} ${pool ? formatCount(pool.total_nodes) : PLACEHOLDER}`}
                />
                <Readout
                  tone="live"
                  label={t("成功率")}
                  value={windowSuccessRate === null ? PLACEHOLDER : formatPercent(windowSuccessRate)}
                  hint={`${t("成功请求")} ${formatCount(windowRequests.success)} / ${t("总请求")} ${formatCount(windowRequests.total)}`}
                />
                <Readout
                  label={t("平均延迟")}
                  value={averageLatency === null ? PLACEHOLDER : formatLatency(averageLatency)}
                  hint={latencyBasis}
                />
              </div>
            </Panel>

            <Panel className="flex min-w-0 flex-col">
              <PanelHeader
                title={t("出口 / 区域")}
                meta={
                  <>
                    {formatCount(regions.length)} {t("地区")}
                  </>
                }
                actions={
                  <>
                    <div
                      role="group"
                      aria-label={t("视图")}
                      className="inline-flex items-center divide-x divide-rule overflow-hidden rounded-control border border-glass-edge"
                    >
                      <Button
                        variant="ghost"
                        size="sm"
                        className="rounded-none border-0 aria-pressed:bg-glass-strong aria-pressed:font-semibold aria-pressed:text-ink"
                        aria-pressed={mapView === "globe"}
                        onClick={() => setMapView("globe")}
                      >
                        {t("立体地球")}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="rounded-none border-0 aria-pressed:bg-glass-strong aria-pressed:font-semibold aria-pressed:text-ink"
                        aria-pressed={mapView === "flat"}
                        onClick={() => setMapView("flat")}
                      >
                        {t("平面地图")}
                      </Button>
                    </div>
                    <Button asChild variant="ghost" size="sm">
                      <Link to="/nodes">{t("查看节点池")}</Link>
                    </Button>
                  </>
                }
              />
              <div className="grid min-h-0 flex-1 gap-2 p-2 lg:grid-cols-5">
                <div className="min-h-[clamp(300px,38vh,520px)] lg:col-span-3">
                  {nodes.isError ? (
                    <ErrorState className="my-auto" message={offline} onRetry={() => void nodes.refetch()} />
                  ) : !nodes.data ? (
                    <LoadingState className="h-full" label={t("正在加载")} />
                  ) : nodeFacts.length === 0 ? (
                    <EmptyState
                      className="h-full justify-center"
                      title={t("建立你的第一个节点池")}
                      hint={t("添加订阅链接或导入本地节点，开始查看线路状态。")}
                      action={
                        <Button asChild variant="primary">
                          <Link to="/subscriptions?create=1">{t("开始导入")}</Link>
                        </Button>
                      }
                    />
                  ) : (
                    <Suspense fallback={chartFallback}>
                      {mapView === "globe" ? <EgressGlobe regions={regions} /> : <EgressMap regions={regions} />}
                    </Suspense>
                  )}
                </div>

                <div className="flex min-w-0 flex-col lg:col-span-2">
                  <h3 className="micro px-1 pb-1.5">{t("节点占比")}</h3>
                  {topRegions.length === 0 ? (
                    <EmptyState className="flex-1 justify-center" title={t("暂无出口数据")} />
                  ) : (
                    <TableWrap className="flex-1">
                      <Table className="min-w-0">
                        <THead>
                          <TR>
                            <TH>{t("地区")}</TH>
                            <TH className="text-right">{t("出口")}</TH>
                            <TH className="text-right">{t("健康")}</TH>
                          </TR>
                        </THead>
                        <TBody>
                          {topRegions.map((region) => {
                            const share = busiestRegion > 0 ? region.exits / busiestRegion : 0;
                            return (
                              <TR key={region.region}>
                                <TD className="font-medium">
                                  <span className="inline-flex min-w-0 items-center gap-2">
                                    <span
                                      aria-hidden
                                      className="block h-1.5 shrink-0 rounded-[2px] bg-series-1"
                                      style={{ width: `${Math.max(4, Math.round(share * 28))}px` }}
                                    />
                                    {region.region || t("未知")}
                                  </span>
                                </TD>
                                <TDNum>{formatCount(region.exits)}</TDNum>
                                <TDNum className="text-ink-faint">{formatCount(region.healthy)}</TDNum>
                              </TR>
                            );
                          })}
                        </TBody>
                      </Table>
                    </TableWrap>
                  )}
                  <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 px-1 pb-1 text-2xs text-ink-faint">
                    <span className="micro">{t("节点")}</span>
                    {EXIT_COUNT_BANDS.map((band) => (
                      <span key={band.min} className="inline-flex items-center gap-1.5">
                        <span
                          aria-hidden
                          className="size-2.5 rounded-[2px] border border-rule-faint"
                          style={{ backgroundColor: band.color }}
                        />
                        <span className="readout">{exitCountBandLabel(band)}</span>
                      </span>
                    ))}
                    <span className="ml-auto inline-flex items-center gap-1.5">
                      <Globe2 size={12} aria-hidden />
                      {t("地区")} {t("未知")}
                      <span className="readout text-ink-soft">{formatCount(unknown)}</span>
                    </span>
                  </div>
                </div>
              </div>
            </Panel>

            <div className="grid min-w-0 gap-3 sm:grid-cols-2 lg:gap-4 2xl:gap-5 xl:grid-cols-4">
              <Kpi
                icon={Activity}
                tone="accent"
                label={t("总请求数")}
                value={formatCount(windowRequests.total)}
                trend={halfDelta(requestSeries)}
                basis={basisHalf}
                series={requestSeries}
              />
              <Kpi
                icon={Gauge}
                tone="live"
                label={t("平均延迟")}
                value={averageLatency === null ? PLACEHOLDER : formatLatency(averageLatency)}
                trend={halfDelta(latencySeries)}
                basis={latencyBasis}
                series={latencySeries}
              />
              <Kpi
                icon={CircleAlert}
                tone="alert"
                label={t("错误率")}
                value={errorRate === null ? PLACEHOLDER : formatPercent(errorRate)}
                trend={halfDelta(errorSeries)}
                basis={basisHalf}
                series={errorSeries}
              />
              <Kpi
                icon={Zap}
                tone="signal"
                label={t("活跃租约")}
                value={latestLease ? formatCount(latestLease.active_leases) : PLACEHOLDER}
                trend={stepDelta(leaseSeries)}
                basis={basisStep}
                series={leaseSeries}
              />
            </div>

            <Panel className="flex min-w-0 flex-col">
              <PanelHeader
                title={t("流量概览")}
                meta={
                  <>
                    {t("窗口累计")} {formatBytes(windowVolume)}
                  </>
                }
                actions={
                  <Button asChild variant="ghost" size="sm">
                    <Link to="/request-logs">{t("请求日志")}</Link>
                  </Button>
                }
              />
              <div className="h-[220px] px-2 py-2 2xl:h-[280px]">
                {realtime.isError || history.isError ? (
                  <ErrorState
                    className="my-auto"
                    message={offline}
                    onRetry={() => {
                      void realtime.refetch();
                      void history.refetch();
                    }}
                  />
                ) : (realtime.isLoading && !realtime.data) || (history.isLoading && !history.data) ? (
                  <LoadingState className="h-full" label={t("正在加载")} />
                ) : chartEmpty ? (
                  <EmptyState className="h-full justify-center" title={t("暂无流量采样")} />
                ) : (
                  <Suspense fallback={chartFallback}>
                    <TrafficChart egress={egressPoints} ingress={ingressPoints} connections={connectionPoints} />
                  </Suspense>
                )}
              </div>
              <div className="grid grid-cols-2 gap-4 border-t border-rule-faint px-4 py-3">
                <Readout
                  size="sm"
                  label={t("入口流量")}
                  value={formatBytes(trafficItems.reduce((total, item) => total + item.ingress_bytes, 0))}
                  delta={
                    <Delta
                      value={halfDelta(trafficItems.map((item) => guardValue(item.ingress_bytes)))}
                      basis={basisHalf}
                    />
                  }
                />
                <Readout
                  size="sm"
                  label={t("出口流量")}
                  value={formatBytes(trafficItems.reduce((total, item) => total + item.egress_bytes, 0))}
                  delta={
                    <Delta
                      value={halfDelta(trafficItems.map((item) => guardValue(item.egress_bytes)))}
                      basis={basisHalf}
                    />
                  }
                />
              </div>
            </Panel>

            <Panel className="flex min-w-0 flex-col">
              <PanelHeader
                title={t("最近加入节点")}
                meta={
                  <>
                    {formatCount(recentNodes.data?.total ?? 0)} {t("节点")}
                  </>
                }
                actions={
                  <Button asChild variant="ghost" size="sm">
                    <Link to="/nodes">{t("查看全部")}</Link>
                  </Button>
                }
              />
              {recentNodes.isError ? (
                <ErrorState className="my-4 mx-4" message={offline} onRetry={() => void recentNodes.refetch()} />
              ) : !recentNodes.data ? (
                <LoadingState className="my-6" label={t("正在加载")} />
              ) : recentNodes.data.items.length === 0 ? (
                <EmptyState className="flex-1 justify-center" title={t("建立你的第一个节点池")} />
              ) : (
                <TableWrap>
                  <Table>
                    <THead>
                      <TR>
                        <TH>{t("节点")}</TH>
                        <TH>{t("地区")}</TH>
                        <TH>{t("出口 IP")}</TH>
                        <TH className="text-right">{t("延迟")}</TH>
                        <TH className="text-right">{t("失败次数")}</TH>
                        <TH>{t("状态")}</TH>
                      </TR>
                    </THead>
                    <TBody>
                      {recentNodes.data.items.map((node) => {
                        const state = node.circuit_open_since
                          ? { tone: "alert" as const, label: t("熔断") }
                          : !node.enabled
                            ? { tone: "neutral" as const, label: t("已停用") }
                            : !node.has_outbound
                              ? { tone: "warn" as const, label: t("无出口") }
                              : { tone: "signal" as const, label: t("正常") };
                        return (
                          <TR key={node.node_hash}>
                            <TDClip className="max-w-[16rem] font-medium" title={node.display_tag ?? node.node_hash}>
                              {node.display_tag || node.node_hash.slice(0, 12)}
                            </TDClip>
                            <TD className="text-ink-soft">{node.region || PLACEHOLDER}</TD>
                            <TD className="readout text-ink-soft">{node.egress_ip || PLACEHOLDER}</TD>
                            <TDNum>
                              {node.reference_latency_ms === undefined
                                ? PLACEHOLDER
                                : formatLatency(node.reference_latency_ms)}
                            </TDNum>
                            <TDNum className="text-ink-faint">{formatCount(node.failure_count)}</TDNum>
                            <TD>
                              <Badge tone={state.tone} dot>
                                {state.label}
                              </Badge>
                            </TD>
                          </TR>
                        );
                      })}
                    </TBody>
                  </Table>
                </TableWrap>
              )}
            </Panel>

            <Panel className="flex min-w-0 flex-col">
              <PanelHeader
                title={t("订阅状态")}
                meta={
                  <>
                    {formatCount(subscriptionState.total)} {t("订阅")}
                  </>
                }
                actions={
                  <Button asChild variant="ghost" size="sm">
                    <Link to="/subscriptions">{t("查看全部")}</Link>
                  </Button>
                }
              />
              {subscriptions.isError ? (
                <ErrorState className="my-4 mx-4" message={offline} onRetry={() => void subscriptions.refetch()} />
              ) : !subscriptions.data ? (
                <LoadingState className="my-6" label={t("正在加载")} />
              ) : subscriptionState.total === 0 ? (
                <EmptyState
                  className="flex-1 justify-center"
                  title={t("还没有订阅")}
                  hint={t("添加订阅链接或导入本地节点，开始查看线路状态。")}
                  action={
                    <Button asChild variant="primary">
                      <Link to="/subscriptions?create=1">{t("开始导入")}</Link>
                    </Button>
                  }
                />
              ) : (
                <div className="flex flex-col gap-3 px-4 py-3">
                  <div aria-hidden className="flex h-2 w-full overflow-hidden rounded-[2px] bg-paper-sunk">
                    {[
                      { key: "enabled", count: subscriptionState.enabled, className: "bg-signal" },
                      { key: "failed", count: subscriptionState.failed, className: "bg-alert" },
                      { key: "disabled", count: subscriptionState.disabled, className: "bg-rule-strong" },
                    ].map((segment) => (
                      <span
                        key={segment.key}
                        className={segment.className}
                        style={{ width: `${(segment.count / subscriptionState.total) * 100}%` }}
                      />
                    ))}
                  </div>
                  <div className="grid grid-cols-3 gap-4">
                    <Readout size="sm" label={t("启用")} value={formatCount(subscriptionState.enabled)} />
                    <Readout size="sm" label={t("异常")} value={formatCount(subscriptionState.failed)} />
                    <Readout size="sm" label={t("已停用")} value={formatCount(subscriptionState.disabled)} />
                  </div>
                  <p className="text-2xs text-ink-faint">
                    {t("健康")} {formatCount(subscriptionState.healthy)} / {t("节点")}{" "}
                    {formatCount(subscriptionState.nodes)}
                  </p>
                </div>
              )}
            </Panel>
          </div>

          {/* The right column: the always-on panes. */}
          <div className="flex min-w-0 flex-col gap-3 lg:gap-4 2xl:gap-5 xl:col-span-4">
            <Panel className="min-w-0 p-4">
              <div className="flex items-center gap-2.5">
                <Badge tone={instanceState.tone} dot>
                  {instanceState.label}
                </Badge>
                <span className="readout ml-auto shrink-0 text-2xs text-ink-faint">
                  {info.data?.version ?? PLACEHOLDER}
                </span>
              </div>
              <div className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2">
                <Readout
                  size="sm"
                  label={t("健康节点")}
                  value={pool ? formatCount(poolHealthy) : PLACEHOLDER}
                  hint={`/ ${pool ? formatCount(pool.total_nodes) : PLACEHOLDER}`}
                />
                <Readout
                  size="sm"
                  label={t("健康出口 IP")}
                  value={pool ? formatCount(pool.healthy_egress_ip_count) : PLACEHOLDER}
                  hint={`/ ${pool ? formatCount(pool.egress_ip_count) : PLACEHOLDER}`}
                />
              </div>
              <p className="mt-3 flex items-center gap-1.5 text-2xs text-ink-faint">
                <span className="readout">{t("同步")}</span>
                {pool?.generated_at ? formatRelativeTime(pool.generated_at) : PLACEHOLDER}
                {pool?.generated_at && <span className="readout">· {rangeOption(rangeKey).label}</span>}
              </p>
            </Panel>

            <Panel className="flex min-w-0 flex-col">
              <PanelHeader title={t("快捷操作")} />
              <div className="flex flex-col gap-0.5 p-2">
                <QuickAction to="/subscriptions?create=1" icon={Rss} label={t("添加订阅")} />
                <QuickAction to="/platforms" icon={Waypoints} label={t("新建平台")} />
                <QuickAction to="/endpoints" icon={Share2} label={t("接入点")} />
                <QuickAction to="/jobs" icon={Zap} label={t("检测任务")} />
              </div>
            </Panel>

            <Panel className="flex min-w-0 flex-col">
              <PanelHeader
                title={t("节点延迟分布")}
                meta={
                  <>
                    {t("节点数")} {formatCount(latency?.sample_count ?? 0)}
                  </>
                }
              />
              <div className="flex-1 py-1">
                {snapshot.isError ? (
                  <ErrorState className="mx-4 my-3" message={offline} onRetry={() => void snapshot.refetch()} />
                ) : !latency ? (
                  <LoadingState className="my-6" label={t("正在加载")} />
                ) : (
                  <LatencyProfile
                    buckets={latency.buckets}
                    overflowCount={latency.overflow_count}
                    overflowMs={latency.overflow_ms}
                  />
                )}
              </div>
            </Panel>

            <Panel className="flex min-w-0 flex-col">
              <PanelHeader
                title={t("平台分布")}
                meta={
                  <>
                    {formatCount(platforms.data?.total ?? 0)} {t("平台")}
                  </>
                }
                actions={
                  <Button asChild variant="ghost" size="sm">
                    <Link to="/platforms">{t("查看全部")}</Link>
                  </Button>
                }
              />
              {platforms.isError ? (
                <ErrorState className="mx-4 my-3" message={offline} onRetry={() => void platforms.refetch()} />
              ) : !platforms.data ? (
                <LoadingState className="my-6" label={t("正在加载")} />
              ) : platformSlices.length === 0 ? (
                <EmptyState
                  className="flex-1 justify-center"
                  title={t("无平台")}
                  hint={t("创建平台以聚合节点")}
                  action={
                    <Button asChild variant="primary">
                      <Link to="/platforms">{t("创建平台")}</Link>
                    </Button>
                  }
                />
              ) : (
                <div className="px-4 py-3">
                  <Donut slices={platformSlices} centerValue={formatCount(platforms.data.total)} centerLabel={t("平台")} />
                </div>
              )}
            </Panel>

            <Panel className="flex min-w-0 flex-col">
              <PanelHeader
                title={t("最近变更")}
                actions={
                  <Button asChild variant="ghost" size="sm">
                    <Link to="/audit">{t("查看全部")}</Link>
                  </Button>
                }
              />
              {events.isError ? (
                <ErrorState className="mx-4 my-3" message={offline} onRetry={() => void events.refetch()} />
              ) : !events.data ? (
                <LoadingState className="my-6" label={t("正在加载")} />
              ) : events.data.items.length === 0 ? (
                <EmptyState className="flex-1 justify-center" title={t("暂无变更记录")} />
              ) : (
                <ul className="flex flex-col px-2 py-1.5">
                  {events.data.items.map((entry) => {
                    const [method, route] = entry.action.split(" ");
                    const tone =
                      method === "DELETE"
                        ? ("alert" as const)
                        : method === "POST"
                          ? ("signal" as const)
                          : ("live" as const);
                    const toneClass = {
                      alert: "text-alert",
                      signal: "text-signal",
                      live: "text-live",
                    }[tone];
                    return (
                      <li key={entry.id} className="flex items-start gap-2.5 rounded-control px-2 py-1.5">
                        <span aria-hidden className={`readout shrink-0 pt-0.5 text-2xs ${toneClass}`}>
                          {method || PLACEHOLDER}
                        </span>
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-xs text-ink" title={route ?? ""}>
                            {route ? route.replace(/^\/api\/v1\//, "") : entry.action}
                          </span>
                          <span className="block truncate text-2xs text-ink-faint">
                            {entry.target || entry.remote_addr || PLACEHOLDER}
                          </span>
                        </span>
                        <span className="readout shrink-0 pt-0.5 text-2xs text-ink-faint">
                          {formatTimestamp(entry.at_ns / 1_000_000)}
                        </span>
                      </li>
                    );
                  })}
                </ul>
              )}
            </Panel>
          </div>
        </div>
      </div>
    </Page>
  );
}
