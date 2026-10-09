import { useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useMemo, useState } from "react";
import { Cable, ChevronDown, ChevronRight, Globe2, HeartPulse, ListChecks, Maximize2, Pencil, Plus, Rss, Server, Trash2, Waypoints, X, type LucideIcon } from "lucide-react";
import { Link, useSearchParams } from "react-router-dom";
import { DataFreshnessBar } from "../../components/ui/DataFreshnessBar";
import { Button } from "../../components/ui/Button";
import {
  DialogClose,
  DialogContent,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "../../components/ui/Dialog";
import { Donut } from "../../components/ui/Donut";
import { Page } from "../../components/ui/PageHeader";
import { Panel, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout } from "../../components/ui/Readout";
import { Select } from "../../components/ui/Select";
import { useI18n } from "../../i18n";
import { listAuditLogs } from "../audit/api";
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
  realtimeRefreshMsFromSteps,
} from "./range";
import { SYSTEM_INFO_QUERY_KEY, getSystemInfo } from "../systemInfo/api";
import LatencyProfile from "./LatencyProfile";
import { aggregateRegionTraffic } from "./worldMap";
/**
 * The charts are the only ECharts consumers in the console, so they load with the
 * board and the canvas, not with the route table.
 */
const EgressMap = lazy(() => import("./EgressMap"));
const TrafficChart = lazy(() => import("./TrafficChart"));

type Point = [number, number];

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

/** The board's shortcuts: each one opens a real page, the first opens its create form. */
const QUICK_ACTIONS: { to: string; label: string; icon: LucideIcon }[] = [
  { to: "/subscriptions?create=1", label: "导入节点", icon: Rss },
  { to: "/platforms", label: "创建平台", icon: Waypoints },
  { to: "/endpoints", label: "新建接入点", icon: Cable },
  { to: "/jobs", label: "新建任务", icon: ListChecks },
];

type HeroTone = "accent" | "signal" | "live" | "warn";

/** One pool fact in the board's welcome band: an icon tile, the value and its label. */
function HeroStat({ icon: Icon, tone, label, value, hint }: { icon: LucideIcon; tone: HeroTone; label: string; value: string; hint?: string }) {
  return (
    <div className="hero-stat">
      <span className={`hero-stat__icon hero-stat__icon--${tone}`} aria-hidden>
        <Icon size={18} />
      </span>
      <span className="min-w-0">
        <span className="hero-stat__value readout">{value}</span>
        <span className="hero-stat__label">{label}{hint && <span className="hero-stat__hint"> · {hint}</span>}</span>
      </span>
    </div>
  );
}

/** The write methods the audit trail can record. */
type AlertMethod = "POST" | "PATCH" | "PUT" | "DELETE";

/**
 * The 告警 feed reads the audit log, so its sentences come from the write routes the
 * API actually records — this is the audit trail, not a synthetic alert stream.
 *
 * The middleware stores `METHOD <route pattern>` verbatim (`auditAction` in
 * `internal/api/audit.go`, e.g. `PATCH /api/v1/platforms/{id}`), so every key below is
 * that pattern's own text, braces and all: the table matches the router literally and
 * cannot drift from it. Each pattern maps the methods it accepts to one whole phrase
 * key, because a composed "verb + noun" would have to borrow the noun's plural, unit
 * and article from the dictionary, and those keys already carry the meaning the page
 * that owns them needs.
 */
const ALERT_ACTIONS: Record<string, Partial<Record<AlertMethod, string>>> = {
  // Subscriptions: the refresh and the circuit-open cleanup, then the record itself.
  "/subscriptions/{id}/actions/refresh": { POST: "刷新订阅" },
  "/subscriptions/{id}/actions/cleanup-circuit-open-nodes": { POST: "清理订阅熔断节点" },
  "/subscriptions/{id}": { PATCH: "更新订阅", DELETE: "删除订阅" },
  "/subscriptions": { POST: "新建订阅" },

  // Nodes: the four single-node probes, and the probe that belongs to a shared IP.
  "/nodes/{hash}/actions/probe-egress": { POST: "探测节点出口" },
  "/nodes/{hash}/actions/probe-latency": { POST: "探测节点延迟" },
  "/nodes/{hash}/actions/probe-quality": { POST: "探测节点质量" },
  "/nodes/{hash}/actions/review-ippure": { POST: "复核节点纯净度" },
  "/quality/ip/{ip}/actions/probe": { POST: "探测 IP 质量" },

  // Platforms: the previews, the derived view, the reset and the leases.
  "/platforms/preview-filter": { POST: "预览平台筛选" },
  "/platforms/preview-scope": { POST: "预览平台范围" },
  "/platforms/{id}/actions/reset-to-default": { POST: "重置平台" },
  "/platforms/{id}/actions/rebuild-routable-view": { POST: "重建平台路由" },
  "/platforms/{id}/leases/{account}/actions/rotate": { POST: "轮换平台租约" },
  "/platforms/{id}/leases/{account}": { DELETE: "删除平台租约" },
  "/platforms/{id}/leases": { DELETE: "清空平台租约" },
  "/platforms/{id}": { PATCH: "更新平台", DELETE: "删除平台" },
  "/platforms": { POST: "新建平台" },

  // Intel: the jobs, the providers behind them and the checks they run.
  "/intel/jobs/{id}/actions/cancel": { POST: "取消情报任务" },
  "/intel/jobs/{id}/actions/retry-failed": { POST: "重试情报任务" },
  "/intel/jobs": { POST: "新建情报任务" },
  "/intel/providers/{id}/actions/resume": { POST: "恢复情报来源" },
  "/intel/providers/{id}/actions/refresh": { POST: "刷新情报来源" },
  "/intel/providers/{id}": { PATCH: "更新情报来源" },
  "/intel/checks/{id}": { PATCH: "更新检测项" },

  // The export profiles and the GeoIP dataset.
  "/export-profiles/{id}/actions/rotate-token": { POST: "轮换导出令牌" },
  "/export-profiles/{id}": { PATCH: "更新导出配置", DELETE: "删除导出配置" },
  "/export-profiles": { POST: "新建导出配置" },
  "/geoip/actions/update-now": { POST: "更新 GeoIP 数据" },
  "/geoip/lookup": { POST: "查询 GeoIP" },

  // The console's own settings and the account-header rules.
  "/system/config": { PATCH: "更新系统配置" },
  "/account-header-rules:resolve": { POST: "解析规则" },
  "/account-header-rules/{prefix...}": { PUT: "更新规则", DELETE: "删除规则" },

  // Endpoints last: their patterns share no prefix with anything above.
  "/endpoints/{id}": { PATCH: "更新接入点", DELETE: "删除接入点" },
  "/endpoints": { POST: "新建接入点" },
};

/**
 * Splits an audit entry's `METHOD /api/v1/path` into the method chip and the phrase an
 * operator reads. `phrase` is itself a translation key. A route the table does not know
 * falls back to the route text with the API prefix stripped, so an unlisted endpoint
 * still reads as itself rather than as a guessed verb.
 */
function alertPhrase(action: string): { method: AlertMethod | ""; phrase: string } {
  const raw = action.trim();
  if (!raw) {
    return { method: "", phrase: "" };
  }
  const separator = raw.indexOf(" ");
  const method = (separator < 0 ? raw : raw.slice(0, separator)) as AlertMethod | "";
  const route = (separator < 0 ? "" : raw.slice(separator + 1).trim()).replace(/^\/api\/v1/, "");
  const known = method === "" ? undefined : ALERT_ACTIONS[route]?.[method];
  return { method, phrase: known ?? (route || raw) };
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


/**
 * The overview board keeps the primary traffic evidence in a golden-ratio reading lane
 * and the supporting evidence in a narrower side lane. Every figure is fetched; empty
 * panels render shared empty states rather than invented KPI values.
 */
export function WorkbenchPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  /*
   * The expanded plate is a state of the board, not a route: the operator opens it
   * from the plate's own header and closes it with Escape, the scrim or the X.
   */
  const [mapExpanded, setMapExpanded] = useState(false);
  const rangeKey = parseRangeKey(params.get("range"));
  const queryClient = useQueryClient();

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
    queryKey: SYSTEM_INFO_QUERY_KEY,
    queryFn: getSystemInfo,
    refetchInterval: 30_000,
    retry: false,
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
  const { regions, unknown } = useMemo(() => aggregateRegionTraffic(nodeFacts), [nodeFacts]);

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

  const poolHealthy = pool?.healthy_nodes ?? 0;
  const mapActivity = Boolean(
    info.data?.panel_egress_region &&
      ((realtime.data?.realtime_throughput.items.at(-1)?.ingress_bps ?? 0) > 0 ||
        (realtime.data?.realtime_throughput.items.at(-1)?.egress_bps ?? 0) > 0 ||
        (realtime.data?.realtime_connections.items.at(-1)?.inbound_connections ?? 0) > 0 ||
        (realtime.data?.realtime_connections.items.at(-1)?.outbound_connections ?? 0) > 0),
  );
  /*
   * The plate and the table read the same six rows: the map is the summary and
   * the table is the data, so they cannot disagree about a region. `regions` is
   * already ranked by node count, which is the order the table reads in.
   */

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


  const latestUpdatedAt = Math.max(
    snapshot.dataUpdatedAt,
    realtime.dataUpdatedAt,
    history.dataUpdatedAt,
    nodes.dataUpdatedAt,
    info.dataUpdatedAt,
    platforms.dataUpdatedAt,
    subscriptions.dataUpdatedAt,
    events.dataUpdatedAt,
  );
  const dashboardFetching =
    snapshot.isFetching ||
    realtime.isFetching ||
    history.isFetching ||
    nodes.isFetching ||
    info.isFetching ||
    platforms.isFetching ||
    subscriptions.isFetching ||
    events.isFetching;
  const dashboardError =
    snapshot.isError ||
    realtime.isError ||
    history.isError ||
    nodes.isError ||
    info.isError ||
    platforms.isError ||
    subscriptions.isError ||
    events.isError;
  const retryDashboard = () => {
    void Promise.all([
      snapshot.refetch(),
      realtime.refetch(),
      history.refetch(),
      nodes.refetch(),
      info.refetch(),
      platforms.refetch(),
      subscriptions.refetch(),
      events.refetch(),
    ]);
  };

  return (
    <Page bleed>
      <h1 className="sr-only">{t("总览看板")}</h1>
      <div className="page-content page-content--dashboard wb-board precision-dashboard">
        <section className="dashboard-hero" aria-labelledby="dashboard-hero-title">
          <div className="dashboard-hero__top">
            <div className="dashboard-hero__heading">
              <h2 id="dashboard-hero-title" className="dashboard-hero__title">{t("欢迎回来")}</h2>
              <p className="dashboard-hero__description">{t("实时查看流量、连接和出口资源。")}</p>
            </div>
              <DataFreshnessBar
                range={
                  <div className="dashboard-range-control">
                    <span className="micro">{t("时间范围")}</span>
                    <div className="relative inline-flex items-center">
                      <Select
                        aria-label={t("时间范围")}
                        value={rangeKey}
                        onChange={(event) => selectRange(event.target.value as RangeKey)}
                        className="wb-timerange-select"
                      >
                        {RANGE_OPTIONS.map((option) => (
                          <option key={option.key} value={option.key}>
                            {t(option.label)}
                          </option>
                        ))}
                      </Select>
                      <ChevronDown size={13} className="wb-timerange-icon" aria-hidden />
                    </div>
                  </div>
                }
                updatedAt={Number.isFinite(latestUpdatedAt) ? latestUpdatedAt : null}
                isFetching={dashboardFetching}
                error={dashboardError ? offline : undefined}
                onRetry={dashboardError ? retryDashboard : undefined}
              />
          </div>
          <div className="dashboard-hero__stats">
            <HeroStat
              icon={Server}
              tone="accent"
              label={t("健康节点")}
              value={pool ? `${formatCount(poolHealthy)} / ${formatCount(pool.total_nodes)}` : PLACEHOLDER}
            />
            <HeroStat
              icon={HeartPulse}
              tone="signal"
              label={t("健康出口 IP")}
              value={pool ? `${formatCount(pool.healthy_egress_ip_count)} / ${formatCount(pool.egress_ip_count)}` : PLACEHOLDER}
            />
            <HeroStat
              icon={Globe2}
              tone="live"
              label={t("地区覆盖")}
              value={formatCount(regions.length)}
              hint={unknown > 0 ? `${t("未定位")} ${formatCount(unknown)}` : undefined}
            />
            <HeroStat
              icon={Rss}
              tone={subscriptionState.failed > 0 ? "warn" : "signal"}
              label={t("订阅健康")}
              value={subscriptionState.total ? `${formatCount(subscriptionState.enabled)} / ${formatCount(subscriptionState.total)}` : PLACEHOLDER}
              hint={subscriptionState.failed > 0 ? `${formatCount(subscriptionState.failed)} ${t("异常")}` : undefined}
            />
          </div>
          <p className="dashboard-hero__meta">
            {t("资源快照")} · {pool?.generated_at ? formatTimestamp(Date.parse(pool.generated_at), { seconds: true }) : PLACEHOLDER}
            <Link to="/nodes" className="dashboard-hero__link">{t("查看节点池")}</Link>
          </p>
          <nav className="dashboard-quick" aria-label={t("快捷操作")}>
            {QUICK_ACTIONS.map((action) => (
              <Link key={action.to} to={action.to} className="dashboard-quick__item action">
                <action.icon size={15} aria-hidden />
                <span className="truncate">{t(action.label)}</span>
                <ChevronRight size={14} aria-hidden className="dashboard-quick__chevron" />
              </Link>
            ))}
          </nav>
        </section>

        <div className="dashboard-evidence-grid">
          <Panel className="dashboard-traffic-pane">
            <PanelHeader
              title={t("流量趋势")}
              meta={`${t("窗口累计")} ${formatBytes(windowVolume)}`}
              actions={
                <Button asChild variant="ghost" size="sm">
                  <Link to="/request-logs">{t("请求日志")}</Link>
                </Button>
              }
            />
            <div className="dashboard-traffic-readouts">
              <Readout label={t("总请求数")} value={formatCount(windowRequests.total)} delta={<Delta value={halfDelta(requestSeries)} basis={basisHalf} />} />
              <Readout label={t("平均延迟")} value={averageLatency === null ? PLACEHOLDER : formatLatency(averageLatency)} hint={latencyBasis} tone="live" />
              <Readout label={t("错误率")} value={errorRate === null ? PLACEHOLDER : formatPercent(errorRate)} delta={<Delta value={halfDelta(errorSeries)} basis={basisHalf} />} tone="alert" />
              <Readout label={t("活跃租约")} value={latestLease ? formatCount(latestLease.active_leases) : PLACEHOLDER} delta={<Delta value={stepDelta(leaseSeries)} basis={basisStep} />} tone="signal" />
            </div>
            <div className="dashboard-chart-well">
              {realtime.isError || history.isError ? (
                <ErrorState message={offline} onRetry={() => { void realtime.refetch(); void history.refetch(); }} />
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
            <div className="dashboard-traffic-footer">
              <Readout size="sm" label={t("入口流量")} value={formatBytes(trafficItems.reduce((total, item) => total + item.ingress_bytes, 0))} />
              <Readout size="sm" label={t("出口流量")} value={formatBytes(trafficItems.reduce((total, item) => total + item.egress_bytes, 0))} />
            </div>
          </Panel>

          <Panel className="dashboard-map-pane">
            <PanelHeader
              title={t("出口地图")}
              meta={`${formatCount(regions.length)} ${t("地区")}`}
              actions={
                <Button variant="ghost" size="icon" aria-label={t("展开大屏")} onClick={() => setMapExpanded(true)}>
                  <Maximize2 size={15} aria-hidden />
                </Button>
              }
            />
            <div className="dashboard-map-well">
              {nodes.isError ? (
                <ErrorState message={offline} onRetry={() => void nodes.refetch()} />
              ) : !nodes.data ? (
                <LoadingState className="h-full" label={t("正在加载")} />
              ) : nodeFacts.length === 0 ? (
                <EmptyState
                  className="h-full justify-center"
                  title={t("建立你的第一个节点池")}
                  hint={t("添加订阅链接或导入本地节点，开始查看线路状态。")}
                  action={<Button asChild variant="primary"><Link to="/subscriptions?create=1">{t("开始导入")}</Link></Button>}
                />
              ) : (
                <Suspense fallback={chartFallback}>
                  <EgressMap activity={mapActivity} regions={regions} origin={{ region: info.data?.panel_egress_region, ip: info.data?.panel_egress_ip }} />
                </Suspense>
              )}
            </div>
            <div className="dashboard-region-index">
              {regions.length === 0 ? (
                <span className="text-xs text-ink-faint">{t("暂无出口数据")}</span>
              ) : regions.map((region) => (
                <div key={region.id} className="dashboard-region-row">
                  <span className="truncate text-xs text-ink">{region.name}</span>
                  <span className="dashboard-region-row__bar" aria-hidden><span style={{ width: `${Math.max(2, region.share * 100)}%` }} /></span>
                  <span className="readout text-2xs text-ink-soft">{formatCount(region.exits)} · {formatPercent(region.share)}</span>
                </div>
              ))}
            </div>
          </Panel>
        </div>

        <div className="dashboard-support-grid">
          <Panel className="dashboard-support-pane">
            <PanelHeader title={t("延迟分布")} meta={`${t("样本")} ${formatCount(latency?.sample_count ?? 0)}`} />
            {snapshot.isError ? <ErrorState className="m-4" message={offline} onRetry={() => void snapshot.refetch()} /> : !latency ? <LoadingState label={t("正在加载")} /> : <div className="p-4"><LatencyProfile buckets={latency.buckets} overflowCount={latency.overflow_count} overflowMs={latency.overflow_ms} /></div>}
          </Panel>

          <Panel className="dashboard-support-pane">
            <PanelHeader title={t("平台分布")} meta={`${formatCount(platforms.data?.total ?? 0)} ${t("平台")}`} actions={<Button asChild variant="ghost" size="sm"><Link to="/platforms">{t("查看全部")}</Link></Button>} />
            {platforms.isError ? <ErrorState className="m-4" message={offline} onRetry={() => void platforms.refetch()} /> : !platforms.data ? <LoadingState label={t("正在加载")} /> : platformSlices.length === 0 ? <EmptyState title={t("无平台")} hint={t("创建平台以聚合节点")} action={<Button asChild variant="primary"><Link to="/platforms">{t("创建平台")}</Link></Button>} /> : <div className="dashboard-platform-content"><Donut slices={platformSlices} centerValue={formatCount(platforms.data.total)} centerLabel={t("平台")} /></div>}
          </Panel>

          <Panel className="dashboard-support-pane">
            <PanelHeader title={t("订阅状态")} meta={`${formatCount(subscriptionState.total)} ${t("订阅")}`} actions={<Button asChild variant="ghost" size="sm"><Link to="/subscriptions">{t("查看全部")}</Link></Button>} />
            {subscriptions.isError ? <ErrorState className="m-4" message={offline} onRetry={() => void subscriptions.refetch()} /> : !subscriptions.data ? <LoadingState label={t("正在加载")} /> : subscriptionState.total === 0 ? <EmptyState title={t("还没有订阅")} action={<Button asChild variant="primary"><Link to="/subscriptions?create=1">{t("开始导入")}</Link></Button>} /> : <div className="dashboard-subscription-content"><div className="dashboard-subscription-bar" aria-hidden>{[{ key: "enabled", count: subscriptionState.enabled, className: "bg-signal" }, { key: "failed", count: subscriptionState.failed, className: "bg-alert" }, { key: "disabled", count: subscriptionState.disabled, className: "bg-rule-strong" }].map((segment) => <span key={segment.key} className={segment.className} style={{ width: `${(segment.count / subscriptionState.total) * 100}%` }} />)}</div><div className="grid grid-cols-3 gap-3"><Readout size="sm" label={t("启用")} value={formatCount(subscriptionState.enabled)} /><Readout size="sm" label={t("异常")} value={formatCount(subscriptionState.failed)} tone="alert" /><Readout size="sm" label={t("已停用")} value={formatCount(subscriptionState.disabled)} tone="muted" /></div><p className="text-2xs text-ink-faint">{t("健康")} {formatCount(subscriptionState.healthy)} / {t("节点")} {formatCount(subscriptionState.nodes)}</p></div>}
          </Panel>
        </div>

        <Panel className="dashboard-changes-pane">
          <PanelHeader title={t("最近变更")} meta={t("真实审计写操作")} actions={<Button asChild variant="ghost" size="sm"><Link to="/audit">{t("查看全部")}</Link></Button>} />
          {events.isError ? <ErrorState className="m-4" message={offline} onRetry={() => void events.refetch()} /> : !events.data ? <LoadingState label={t("正在加载")} /> : events.data.items.length === 0 ? <EmptyState title={t("暂无变更记录")} /> : <ul className="dashboard-changes-list">{events.data.items.map((entry) => { const { method, phrase } = alertPhrase(entry.action); const toneClass = method === "DELETE" ? "text-alert" : method === "POST" || method === "PUT" ? "text-signal" : "text-live"; return <li key={entry.id} title={entry.action} className="dashboard-change-row"><span className={`dashboard-change-row__icon dashboard-change-row__icon--${method === "DELETE" ? "alert" : method === "POST" || method === "PUT" ? "signal" : "live"}`} aria-hidden>{method === "DELETE" ? <Trash2 size={14} /> : method === "POST" || method === "PUT" ? <Plus size={14} /> : <Pencil size={14} />}</span><span className={`readout sr-only ${toneClass}`}>{method || PLACEHOLDER}</span><span className="min-w-0 flex-1"><span className="block truncate text-xs text-ink">{t(phrase)}</span><span className="block truncate text-2xs text-ink-faint">{entry.target || entry.remote_addr || PLACEHOLDER}</span></span><span className="readout text-2xs text-ink-faint">{formatTimestamp(entry.at_ns / 1_000_000)}</span></li>; })}</ul>}
        </Panel>
      </div>

      <DialogRoot open={mapExpanded} onOpenChange={setMapExpanded}>
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="wb-map-dialog flex h-[calc(100vh-1rem)] w-[calc(100vw-1rem)] max-h-[calc(100vh-1rem)] max-w-[calc(100vw-1rem)] flex-col">
            <div className="flex min-h-[var(--panel-header-h)] shrink-0 items-center justify-between gap-3 border-b border-rule-faint px-4 py-2.5 sm:px-5">
              <DialogTitle>{t("出口地图")}</DialogTitle>
              <DialogClose asChild><Button variant="ghost" size="icon" aria-label={t("关闭")}><X size={16} aria-hidden /></Button></DialogClose>
            </div>
            <div className="wb-map-dialog__body min-h-0 flex-1 bg-paper-inset"><Suspense fallback={chartFallback}><EgressMap activity={mapActivity} regions={regions} origin={{ region: info.data?.panel_egress_region, ip: info.data?.panel_egress_ip }} /></Suspense></div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>
    </Page>
  );
}
