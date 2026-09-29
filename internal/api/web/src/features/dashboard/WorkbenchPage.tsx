import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Globe2, Plus, RefreshCw } from "lucide-react";
import { lazy, Suspense, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Button } from "../../components/ui/Button";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import {
  Table,
  TableWrap,
  TBody,
  TD,
  TDNum,
  TH,
  THead,
  TR,
} from "../../components/ui/Table";
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
 * The overview: a command board for the whole inventory.
 *
 * Four panels on the 8px grid — where traffic leaves, how fast the nodes answer,
 * what is moving right now, and which countries the pool actually lives in. The
 * earlier version of this screen had no panels at all, only hairline bands, and a
 * band is not structure: with nothing framed, an operator cannot tell where one
 * region of the board ends and the next begins, which is precisely what "flat"
 * means when it is looked at rather than described.
 *
 * The one thing this screen deliberately does not do is tile the top with KPI
 * cards. There is a single strip of four values — they are read once and then
 * watched — and the rest of the board belongs to the data.
 */
export function WorkbenchPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const [mapView, setMapView] = useState<MapView>("globe");
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

  // The strip: what the inventory holds right now, plus the request record over
  // the selected window.
  const leaseItems = useMemo(() => realtime.data?.realtime_leases.items ?? [], [realtime.data]);
  const latestLease = leaseItems.at(-1);

  const requestItems = useMemo(() => history.data?.history_requests.items ?? [], [history.data]);
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

  const busy = snapshot.isFetching || realtime.isFetching || history.isFetching || nodes.isFetching;
  const poolHealthy = pool?.healthy_nodes ?? 0;
  // The ranking board reads the top of the same aggregation the map draws, so the
  // two can never disagree about which countries the pool lives in.
  const topRegions = useMemo(() => regions.slice(0, 8), [regions]);
  const busiestRegion = topRegions[0]?.exits ?? 0;

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

  return (
    <Page bleed>
      <PageHeader
        title={t("总览看板")}
        description={t("查看线路健康、出口质量和实时连接。")}
        meta={
          <>
            <PageMeta label={t("节点")} value={pool ? formatCount(pool.total_nodes) : PLACEHOLDER} />
            <PageMeta label={t("健康")} value={pool ? formatCount(poolHealthy) : PLACEHOLDER} />
            <PageMeta
              label={t("出口 IP")}
              value={pool ? formatCount(pool.egress_ip_count) : PLACEHOLDER}
            />
            <PageMeta
              label={t("同步")}
              value={pool?.generated_at ? formatRelativeTime(pool.generated_at) : PLACEHOLDER}
            />
          </>
        }
        actions={
          <>
            <div
              role="group"
              aria-label={t("时间范围")}
              className="inline-flex h-[var(--control-h)] divide-x divide-rule overflow-hidden rounded-control border border-rule bg-paper-raised"
            >
              {RANGE_OPTIONS.map((option) => (
                <Button
                  key={option.key}
                  variant="quiet"
                  size="sm"
                  className="h-full rounded-none border-0 px-2.5 aria-pressed:bg-paper-sunk aria-pressed:font-semibold aria-pressed:text-ink"
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
          </>
        }
      />

      {snapshot.isError && (
        <div className="px-4 pt-3 lg:px-5 2xl:px-6">
          <ErrorState message={offline} onRetry={() => void snapshot.refetch()} />
        </div>
      )}

      <div className="flex flex-col gap-3 px-4 py-3 lg:px-5 lg:py-4 2xl:gap-4 2xl:px-6 2xl:py-5">
        {/* One strip of values, not four cards: a band that is nothing but KPI
            tiles spends a whole screen on numbers nobody reads twice. */}
        <ReadoutStrip>
          <ReadoutCell>
            <Readout
              label={t("可路由节点")}
              value={pool ? formatCount(pool.total_nodes) : PLACEHOLDER}
              hint={`${t("健康")} ${pool ? formatCount(poolHealthy) : PLACEHOLDER}`}
            />
          </ReadoutCell>
          <ReadoutCell>
            <Readout
              label={t("出口 IP 数")}
              value={pool ? formatCount(pool.egress_ip_count) : PLACEHOLDER}
              hint={`${t("健康出口 IP")} ${pool ? formatCount(pool.healthy_egress_ip_count) : PLACEHOLDER}`}
            />
          </ReadoutCell>
          <ReadoutCell>
            <Readout
              tone="live"
              label={t("活跃租约数")}
              value={latestLease ? formatCount(latestLease.active_leases) : PLACEHOLDER}
              hint={t(rangeOption(rangeKey).label)}
            />
          </ReadoutCell>
          <ReadoutCell>
            <Readout
              tone="signal"
              label={t("请求成功率")}
              value={latestMeasuredRequest ? formatPercent(latestMeasuredRequest.success_rate) : PLACEHOLDER}
              hint={`${t("成功请求")} ${formatCount(windowRequests.success)} / ${t("总请求")} ${formatCount(windowRequests.total)}`}
            />
          </ReadoutCell>
        </ReadoutStrip>

        {/* The board itself: the map leads, because "where does traffic leave from"
            is the question this console exists to answer. */}
        <div className="grid gap-3 2xl:gap-4 xl:grid-cols-12">
          <Panel className="flex min-w-0 flex-col xl:col-span-8">
            <PanelHeader
              title={t("出口 / 区域")}
              meta={
                <>
                  {formatCount(regions.length)} {t("地区")}
                </>
              }
              actions={
                <>
                  {/* One question, two views. The sphere is what the board is
                      read from, and the flat map stays one click away for the
                      reader who wants a border rather than a marker. */}
                  <div
                    role="group"
                    aria-label={t("视图")}
                    className="inline-flex items-center divide-x divide-rule overflow-hidden rounded-control border border-rule"
                  >
                    <Button
                      variant="ghost"
                      size="sm"
                      className="rounded-none border-0 aria-pressed:bg-paper-sunk aria-pressed:font-semibold aria-pressed:text-ink"
                      aria-pressed={mapView === "globe"}
                      onClick={() => setMapView("globe")}
                    >
                      {t("立体地球")}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="rounded-none border-0 aria-pressed:bg-paper-sunk aria-pressed:font-semibold aria-pressed:text-ink"
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
            <div className="min-h-[clamp(320px,42vh,560px)] flex-1 px-2 py-2">
              {nodes.isError ? (
                <ErrorState
                  className="my-auto"
                  message={offline}
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
                <Suspense fallback={chartFallback}>
                  {mapView === "globe" ? (
                    <EgressGlobe regions={regions} />
                  ) : (
                    <EgressMap regions={regions} />
                  )}
                </Suspense>
              )}
            </div>
            {/* The legend is a row of swatches with their band, on the panel's own
                gutter rather than in a caption box. */}
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 border-t border-rule-faint px-4 py-2 text-2xs text-ink-faint">
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
          </Panel>

          <Panel className="flex min-w-0 flex-col xl:col-span-4">
            <PanelHeader
              title={t("节点延迟分布")}
              meta={
                <>
                  {t("节点数")} {formatCount(latency?.sample_count ?? 0)}
                </>
              }
            />
            <div className="flex-1 px-0 py-1">
              {snapshot.isError ? (
                <ErrorState message={offline} onRetry={() => void snapshot.refetch()} />
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
          </Panel>
        </div>

        <div className="grid gap-3 2xl:gap-4 xl:grid-cols-12">
          <Panel className="flex min-w-0 flex-col xl:col-span-7">
            <PanelHeader
              title={t("流量")}
              meta={
                <>
                  {t("流量累计")} {formatBytes(windowVolume)}
                </>
              }
            />
            <div className="h-[240px] px-2 py-2 2xl:h-[300px]">
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
          </Panel>

          {/* The ranking board: a progress bar per row instead of a chart. Reading
              "which countries carry the pool" off a bar list takes one glance; off a
              pie chart it takes a legend and a guess. */}
          <Panel className="flex min-w-0 flex-col xl:col-span-5">
            <PanelHeader
              title={t("出口地区排行")}
              meta={
                <>
                  {formatCount(topRegions.length)} / {formatCount(regions.length)}
                </>
              }
            />
            {topRegions.length === 0 ? (
              <EmptyState className="flex-1 justify-center" title={t("暂无出口数据")} />
            ) : (
              <TableWrap className="flex-1">
                <Table>
                  <THead>
                    <TR>
                      <TH>{t("地区")}</TH>
                      <TH className="w-1/2">{t("节点占比")}</TH>
                      <TH className="text-right">{t("健康")}</TH>
                      <TH className="text-right">{t("节点数")}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {topRegions.map((region) => {
                      const share = busiestRegion > 0 ? region.exits / busiestRegion : 0;
                      return (
                        <TR key={region.region}>
                          <TD className="font-medium">{region.region}</TD>
                          <TD>
                            <span
                              aria-hidden
                              className="block h-1.5 rounded-[2px] bg-series-1"
                              style={{ width: `${Math.max(2, Math.round(share * 100))}%` }}
                            />
                          </TD>
                          <TDNum className="text-ink-faint">
                            {formatCount(region.healthy)}
                          </TDNum>
                          <TDNum className="text-ink">{formatCount(region.exits)}</TDNum>
                        </TR>
                      );
                    })}
                  </TBody>
                </Table>
              </TableWrap>
            )}
          </Panel>
        </div>
      </div>
    </Page>
  );
}
