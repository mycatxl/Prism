import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe2, Plus, RefreshCw } from "lucide-react";
import { lazy, Suspense, useMemo } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Button } from "../../components/ui/Button";
import { SectionTitle } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { Sparkline } from "../../components/ui/Sparkline";
import { useI18n } from "../../i18n";
import { formatRelativeTime } from "../../lib/time";
import {
  type DashboardGlobalHistoryData,
  type DashboardGlobalRealtimeData,
  getDashboardGlobalHistoryData,
  getDashboardGlobalRealtimeData,
  getDashboardGlobalSnapshotData,
  listNodeExitFacts,
} from "./api";
import { EXIT_COUNT_BANDS, exitCountBandLabel } from "./chartPalette";
import { PLACEHOLDER, formatBytes, formatCount, formatPercent, toEpochMs } from "./format";
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
 * The two charts are the only ECharts consumers in the panel, so they load with
 * the map and the canvas, not with the route table.
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

function toSeries<T>(items: T[], valueOf: (item: T) => number): number[] {
  return items.map((item) => guardValue(valueOf(item)));
}

/**
 * The overview screen: where traffic leaves, and how it is doing.
 *
 * Three bands on one sheet of paper, separated by hairlines — an instrument
 * strip, the exit map, then the live half. Nothing is boxed into a card: the
 * structure is the rules and the alignment, which is what makes a wall display
 * readable from a few metres away.
 */
export function WorkbenchPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
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

  const pool = snapshot.data?.snapshot_node_pool;
  const latency = snapshot.data?.snapshot_latency_global;

  const nodeFacts = useMemo(() => nodes.data ?? [], [nodes.data]);
  const { regions, unknown } = useMemo(() => aggregateExitsByRegion(nodeFacts), [nodeFacts]);

  // Band 1 — the four values that describe the pool, each with the trend the
  // panel already holds for it.
  const leaseItems = useMemo(() => realtime.data?.realtime_leases.items ?? [], [realtime.data]);
  const leaseValues = useMemo(() => toSeries(leaseItems, (item) => item.active_leases), [leaseItems]);
  const latestLease = leaseItems.at(-1);

  const nodePoolItems = useMemo(() => history.data?.history_node_pool.items ?? [], [history.data]);
  const nodeHealthyValues = useMemo(() => toSeries(nodePoolItems, (item) => item.healthy_nodes), [nodePoolItems]);
  const egressIpValues = useMemo(() => toSeries(nodePoolItems, (item) => item.egress_ip_count), [nodePoolItems]);

  const requestItems = useMemo(() => history.data?.history_requests.items ?? [], [history.data]);
  const successRateValues = useMemo(() => toSeries(requestItems, (item) => item.success_rate), [requestItems]);
  const latestMeasuredRequest = useMemo(
    () => [...requestItems].reverse().find((item) => item.total_requests > 0),
    [requestItems],
  );
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

  // Band 3 — throughput per bucket over the window, and the connections that
  // were in flight while those buckets were written.
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

  const busy =
    snapshot.isFetching || realtime.isFetching || history.isFetching || nodes.isFetching;
  const poolHealthy = pool?.healthy_nodes ?? 0;

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

  return (
    <section className="min-h-full bg-paper px-4 py-4 lg:px-6 lg:py-5">
      <header className="flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
        <div className="min-w-0">
          <h1 className="text-2xl">{t("总览看板")}</h1>
          <p className="mt-1 max-w-[60ch] text-sm text-ink-soft">
            {t("查看线路健康、出口质量和实时连接。")}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div
            role="group"
            aria-label={t("时间范围")}
            className="inline-flex divide-x divide-rule overflow-hidden rounded-control border border-rule bg-paper-raised"
          >
            {RANGE_OPTIONS.map((option) => (
              <Button
                key={option.key}
                variant="quiet"
                size="sm"
                className="rounded-none border-0 px-3 aria-pressed:bg-paper-sunk aria-pressed:font-semibold aria-pressed:text-ink"
                aria-pressed={option.key === rangeKey}
                onClick={() => selectRange(option.key)}
              >
                {t(option.label)}
              </Button>
            ))}
          </div>
          <Button
            variant="ghost"
            size="icon"
            type="button"
            title={t("刷新")}
            aria-label={t("刷新")}
            disabled={busy}
            onClick={() => void queryClient.invalidateQueries()}
          >
            <RefreshCw size={16} className={busy ? "animate-spin" : ""} />
          </Button>
          <Button asChild variant="primary">
            <Link to="/subscriptions?create=1">
              <Plus size={15} />
              {t("添加订阅")}
            </Link>
          </Button>
        </div>
      </header>

      {snapshot.isError && (
        <ErrorState
          className="mt-4"
          message={t("无法连接服务，请检查连接后重试。")}
          onRetry={() => void snapshot.refetch()}
        />
      )}

      {/* Band 1 — the instrument strip: one baseline, hairline separations. */}
      <div className="mt-4">
        <ReadoutStrip className="grid-cols-2 lg:grid-cols-4">
          <ReadoutCell className="px-4 py-4 xl:px-6">
            <div className="flex flex-wrap items-end justify-between gap-x-3 gap-y-1">
              <Readout
                size="lg"
                label={t("可路由节点")}
                value={pool ? formatCount(pool.total_nodes) : PLACEHOLDER}
                hint={`${t("健康")} ${pool ? formatCount(poolHealthy) : PLACEHOLDER}`}
              />
              <Sparkline className="shrink-0" values={nodeHealthyValues} width={48} height={20} tone="muted" />
            </div>
          </ReadoutCell>
          <ReadoutCell className="px-4 py-4 xl:px-6">
            <div className="flex flex-wrap items-end justify-between gap-x-3 gap-y-1">
              <Readout
                size="lg"
                label={t("出口 IP 数")}
                value={pool ? formatCount(pool.egress_ip_count) : PLACEHOLDER}
                hint={`${t("健康出口 IP")} ${pool ? formatCount(pool.healthy_egress_ip_count) : PLACEHOLDER}`}
              />
              <Sparkline className="shrink-0" values={egressIpValues} width={48} height={20} tone="muted" />
            </div>
          </ReadoutCell>
          <ReadoutCell className="px-4 py-4 xl:px-6">
            <div className="flex flex-wrap items-end justify-between gap-x-3 gap-y-1">
              <Readout
                size="lg"
                label={t("活跃租约数")}
                value={latestLease ? formatCount(latestLease.active_leases) : PLACEHOLDER}
                hint={t(rangeOption(rangeKey).label)}
              />
              <Sparkline className="shrink-0" values={leaseValues} width={48} height={20} tone="live" />
            </div>
          </ReadoutCell>
          <ReadoutCell className="px-4 py-4 xl:px-6">
            <div className="flex flex-wrap items-end justify-between gap-x-3 gap-y-1">
              <Readout
                size="lg"
                label={t("请求成功率")}
                value={latestMeasuredRequest ? formatPercent(latestMeasuredRequest.success_rate) : PLACEHOLDER}
                hint={`${t("成功请求")} ${formatCount(windowRequests.success)} / ${t("总请求")} ${formatCount(windowRequests.total)}`}
              />
              <Sparkline className="shrink-0" values={successRateValues} width={48} height={20} tone="signal" />
            </div>
          </ReadoutCell>
        </ReadoutStrip>
      </div>

      {/* Band 2 — the map: the hero, and the only place the layout is allowed
          to be tall. */}
      <section className="mt-6">
        <SectionTitle
          trailing={
            <span className="text-xs text-ink-faint">
              {formatCount(regions.length)} {t("地区")}
            </span>
          }
        >
          {t("出口 / 区域")}
        </SectionTitle>
        <div className="mt-2 h-[clamp(320px,44vh,620px)]">
          {nodes.isError ? (
            <ErrorState
              className="my-auto"
              message={t("无法连接服务，请检查连接后重试。")}
              onRetry={() => void nodes.refetch()}
            />
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
            <Suspense fallback={<LoadingState className="h-full" label={t("正在加载")} />}>
              <EgressMap regions={regions} />
            </Suspense>
          )}
        </div>
        <div className="mt-2 flex flex-wrap items-center justify-between gap-x-6 gap-y-1 text-2xs text-ink-faint">
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
            <span>{t("节点")}</span>
            {EXIT_COUNT_BANDS.map((band) => (
              <span key={band.min} className="inline-flex items-center gap-1.5">
                <span
                  aria-hidden
                  className="size-2.5 border border-rule"
                  style={{ backgroundColor: band.color }}
                />
                <span className="readout">{exitCountBandLabel(band)}</span>
              </span>
            ))}
          </div>
          <span className="inline-flex items-center gap-1.5">
            <Globe2 size={12} aria-hidden />
            {t("地区")} {t("未知")}
            <span className="readout text-ink-soft">{formatCount(unknown)}</span>
          </span>
        </div>
      </section>

      {/* Band 3 — the live half: a vertical rule, not two cards. */}
      <section className="mt-6 border-t border-rule pt-4 lg:grid lg:grid-cols-[minmax(0,1.7fr)_minmax(0,1fr)]">
        <div className="min-w-0 lg:pr-6">
          <SectionTitle
            trailing={
              <span className="text-xs text-ink-faint">
                {t("流量累计")}{" "}
                <span className="readout text-ink-soft">{formatBytes(windowVolume)}</span>
              </span>
            }
          >
            {t("流量")}
          </SectionTitle>
          <div className="mt-2 h-[240px] xl:h-[280px]">
            {realtime.isError || history.isError ? (
              <ErrorState
                className="my-auto"
                message={t("无法连接服务，请检查连接后重试。")}
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
              <Suspense fallback={<LoadingState className="h-full" label={t("正在加载")} />}>
                <TrafficChart egress={egressPoints} ingress={ingressPoints} connections={connectionPoints} />
              </Suspense>
            )}
          </div>
        </div>

        <div className="mt-5 min-w-0 border-t border-rule pt-4 lg:mt-0 lg:border-t-0 lg:border-l lg:pl-6 lg:pt-0">
          <SectionTitle
            trailing={
              <span className="text-xs text-ink-faint">
                {t("节点数")}{" "}
                <span className="readout text-ink-soft">{formatCount(latency?.sample_count ?? 0)}</span>
              </span>
            }
          >
            {t("节点延迟分布")}
          </SectionTitle>
          <div className="mt-2">
            {snapshot.isError ? (
              <ErrorState
                message={t("无法连接服务，请检查连接后重试。")}
                onRetry={() => void snapshot.refetch()}
              />
            ) : !latency ? (
              <LoadingState label={t("正在加载")} />
            ) : (
              <LatencyProfile
                buckets={latency.buckets}
                overflowCount={latency.overflow_count}
                overflowMs={latency.overflow_ms}
              />
            )}
          </div>
        </div>
      </section>

      <footer className="mt-4 flex flex-wrap items-center justify-between gap-x-6 gap-y-1 border-t border-rule pt-2 text-xs text-ink-faint">
        <span>
          {pool?.generated_at
            ? `${t("快照更新")} ${formatRelativeTime(pool.generated_at)}`
            : t("等待服务数据")}
        </span>
        <span className="inline-flex items-center gap-3">
          <Link to="/nodes">{t("查看节点池")}</Link>
        </span>
      </footer>
    </section>
  );
}