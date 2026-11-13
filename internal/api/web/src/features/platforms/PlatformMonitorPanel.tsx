import { useQuery } from "@tanstack/react-query";
import { Link2 } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Area, Bar, BarChart, CartesianGrid, Cell, ComposedChart, Line, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { ControlButton, ControlSelect, DetailFact, EmptyWorkState, StatusMark, WorkspaceCard } from "../../components/control-plane";
import { ChartGradient, ChartTooltip, ChartTooltipRow } from "../../components/ui/ChartPrimitives";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { apiRequest } from "../../lib/api-client";
import { formatApiErrorMessage } from "../../lib/error-message";
import { opsPaths } from "../../lib/paths";
import type {
  HistoryAccessLatencyResponse,
  HistoryLeaseLifetimeResponse,
  HistoryRequestsItem,
  HistoryResponse,
  LatencyBucket,
  RealtimeLeasesResponse,
  SnapshotNodeLatencyDistribution,
  SnapshotPlatformNodePool,
  TimeWindow,
} from "../dashboard/types";
import type { Platform } from "./types";

type RangeKey = "1h" | "6h" | "24h";

type RangeOption = {
  key: RangeKey;
  label: string;
  ms: number;
};

type PlatformHistoryBundle = {
  requests: HistoryResponse<HistoryRequestsItem>;
  accessLatency: HistoryAccessLatencyResponse;
  leaseLifetime: HistoryLeaseLifetimeResponse;
};

type PlatformSnapshotBundle = {
  nodePool: SnapshotPlatformNodePool;
  nodeLatency: SnapshotNodeLatencyDistribution;
};

type TrendLineDefinition = {
  dataKey: string;
  name: string;
  color: string;
};

type TrendLineChartProps = {
  data: Array<Record<string, number | string>>;
  lines: TrendLineDefinition[];
  yTickFormatter?: (value: number) => string;
  tooltipValueFormatter?: (value: number) => string;
  emptyText: string;
};

type HistogramBarPoint = {
  lower_ms: number;
  upper_ms: number;
  label: string;
  count: number;
};

type LatencyHistogramProps = {
  buckets: LatencyBucket[];
  emptyText: string;
};

const RANGE_OPTIONS: RangeOption[] = [
  { key: "1h", label: "最近 1 小时", ms: 60 * 60 * 1000 },
  { key: "6h", label: "最近 6 小时", ms: 6 * 60 * 60 * 1000 },
  { key: "24h", label: "最近 24 小时", ms: 24 * 60 * 60 * 1000 },
];

const DEFAULT_REALTIME_REFRESH_MS = 15_000;
const MIN_REALTIME_REFRESH_MS = 1_000;
const DEFAULT_HISTORY_REFRESH_MS = 60_000;
const MIN_HISTORY_REFRESH_MS = 15_000;
const MAX_HISTORY_REFRESH_MS = 300_000;
const SNAPSHOT_REFRESH_MS = 5_000;
const MAX_TREND_POINTS = 360;
const MAX_HISTOGRAM_BUCKETS = 120;
const METRICS_BASE = "/api/v1/metrics";
const EMPTY_REALTIME_ITEMS: RealtimeLeasesResponse["items"] = [];
const EMPTY_HISTORY_REQUEST_ITEMS: HistoryResponse<HistoryRequestsItem>["items"] = [];
const EMPTY_ACCESS_LATENCY_ITEMS: HistoryAccessLatencyResponse["items"] = [];
const EMPTY_LEASE_LIFETIME_ITEMS: HistoryLeaseLifetimeResponse["items"] = [];

function toNumber(raw: unknown): number {
  const value = Number(raw);
  if (!Number.isFinite(value)) {
    return 0;
  }
  return value;
}

function toString(raw: unknown): string {
  return typeof raw === "string" ? raw : "";
}

function numberLocale(): string {
  return isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
}

function formatCount(value: number): string {
  return new Intl.NumberFormat(numberLocale()).format(Math.round(value));
}

function formatKnownMetric(value: number | undefined, available: boolean, formatter: (value: number) => string = formatCount): string {
  if (!available || typeof value !== "number" || !Number.isFinite(value)) {
    return "—";
  }
  return formatter(value);
}

function formatPercent(value: number): string {
  return `${(value * 100).toFixed(1)}%`;
}

function formatShortNumber(value: number): string {
  const abs = Math.abs(value);
  if (abs >= 1_000_000_000) {
    return `${(value / 1_000_000_000).toFixed(1)}G`;
  }
  if (abs >= 1_000_000) {
    return `${(value / 1_000_000).toFixed(1)}M`;
  }
  if (abs >= 1_000) {
    return `${(value / 1_000).toFixed(1)}K`;
  }
  return `${Math.round(value)}`;
}

function formatLatency(value: number): string {
  if (!Number.isFinite(value) || value < 0) {
    return "0ms";
  }
  if (value >= 1000) {
    const seconds = value / 1000;
    return `${seconds >= 10 ? seconds.toFixed(0) : seconds.toFixed(1)}s`;
  }
  return `${Math.round(value)}ms`;
}

function formatLeaseDuration(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return "0ms";
  }

  if (value < 1000) {
    return `${Math.round(value)}ms`;
  }

  const english = isEnglishLocale(getCurrentLocale());
  const wholeSeconds = Math.floor(value / 1000);
  const days = Math.floor(wholeSeconds / 86_400);
  const hours = Math.floor((wholeSeconds % 86_400) / 3_600);
  const minutes = Math.floor((wholeSeconds % 3_600) / 60);
  const seconds = wholeSeconds % 60;

  if (english) {
    if (days > 0) {
      return `${days}d ${hours}h`;
    }
    if (hours > 0) {
      return `${hours}h ${minutes}m`;
    }
    if (minutes > 0) {
      return `${minutes}m ${seconds}s`;
    }
    return `${seconds}s`;
  }

  if (days > 0) {
    return `${days} 天 ${hours} 小时`;
  }
  if (hours > 0) {
    return `${hours} 小时 ${minutes} 分钟`;
  }
  if (minutes > 0) {
    return `${minutes} 分钟 ${seconds} 秒`;
  }
  return `${seconds} 秒`;
}

function formatClock(iso: string): string {
  if (!iso) {
    return "--";
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "--";
  }
  return new Intl.DateTimeFormat(numberLocale(), {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

function parseTimestamp(input: string, fallback: number): number {
  if (!input) {
    return fallback;
  }
  const value = Date.parse(input);
  if (Number.isNaN(value)) {
    return fallback;
  }
  return value;
}

function sortTimeSeriesByTimestamp<T>(items: T[], getTimestamp: (item: T) => string): T[] {
  return items
    .map((item, index) => ({
      item,
      index,
      sortKey: parseTimestamp(getTimestamp(item), index),
    }))
    .sort((left, right) => {
      if (left.sortKey === right.sortKey) {
        return left.index - right.index;
      }
      return left.sortKey - right.sortKey;
    })
    .map((entry) => entry.item);
}

function buildUniformSampleIndices(pointCount: number, maxPoints: number): number[] {
  if (pointCount <= 0 || maxPoints <= 0) {
    return [];
  }

  const capped = Math.min(pointCount, maxPoints);
  if (capped === pointCount) {
    return Array.from({ length: pointCount }, (_, index) => index);
  }
  if (capped === 1) {
    return [0];
  }
  if (capped === 2) {
    return [0, pointCount - 1];
  }

  const middleCount = capped - 2;
  const span = pointCount - 2;
  const selected = new Set<number>([0, pointCount - 1]);

  for (let i = 0; i < middleCount; i += 1) {
    const index = 1 + Math.floor((i * span) / middleCount);
    selected.add(Math.min(pointCount - 2, index));
  }

  return Array.from(selected).sort((a, b) => a - b);
}

function downsampleArray<T>(items: T[], maxPoints: number): T[] {
  if (items.length <= maxPoints) {
    return items;
  }
  const indices = buildUniformSampleIndices(items.length, maxPoints);
  return indices.map((index) => items[index] ?? items[items.length - 1]);
}

function sum(values: number[]): number {
  return values.reduce((acc, value) => acc + value, 0);
}

function successRate(total: number, success: number): number {
  if (total <= 0) {
    return 0;
  }
  return success / total;
}

function latestValue(values: number[]): number {
  if (!values.length) {
    return 0;
  }
  return values[values.length - 1] ?? 0;
}

function normalizePositiveSeconds(seconds: number | undefined): number | null {
  if (typeof seconds !== "number" || !Number.isFinite(seconds) || seconds <= 0) {
    return null;
  }
  return seconds;
}

function realtimeRefreshMs(stepSeconds: number | undefined): number {
  const step = normalizePositiveSeconds(stepSeconds);
  if (step === null) {
    return DEFAULT_REALTIME_REFRESH_MS;
  }
  return Math.max(MIN_REALTIME_REFRESH_MS, Math.round(step * 1000));
}

function historyRefreshMs(bucketSeconds: Array<number | undefined>): number {
  const buckets = bucketSeconds.map(normalizePositiveSeconds).filter((value): value is number => value !== null);
  if (!buckets.length) {
    return DEFAULT_HISTORY_REFRESH_MS;
  }
  const intervalMs = Math.round(Math.min(...buckets) * 1000);
  return Math.min(MAX_HISTORY_REFRESH_MS, Math.max(MIN_HISTORY_REFRESH_MS, intervalMs));
}

function getTimeWindow(rangeKey: RangeKey): TimeWindow {
  const option = RANGE_OPTIONS.find((item) => item.key === rangeKey) ?? RANGE_OPTIONS[1];
  const to = new Date();
  const from = new Date(to.getTime() - option.ms);
  return {
    from: from.toISOString(),
    to: to.toISOString(),
  };
}

function withWindow(path: string, window: TimeWindow, params?: Record<string, string>): string {
  const query = new URLSearchParams({
    from: window.from,
    to: window.to,
    ...(params ?? {}),
  });
  return `${path}?${query.toString()}`;
}

function normalizeRealtimeLeasesResponse(raw: RealtimeLeasesResponse): RealtimeLeasesResponse {
  return {
    platform_id: toString(raw.platform_id),
    step_seconds: toNumber(raw.step_seconds),
    items: Array.isArray(raw.items)
      ? raw.items.map((item) => ({
        ts: toString(item.ts),
        active_leases: toNumber(item.active_leases),
      }))
      : [],
  };
}

function normalizeHistoryRequestsResponse(raw: HistoryResponse<HistoryRequestsItem>): HistoryResponse<HistoryRequestsItem> {
  return {
    bucket_seconds: toNumber(raw.bucket_seconds),
    items: Array.isArray(raw.items)
      ? raw.items.map((item) => ({
        bucket_start: toString(item.bucket_start),
        bucket_end: toString(item.bucket_end),
        total_requests: toNumber(item.total_requests),
        success_requests: toNumber(item.success_requests),
        success_rate: toNumber(item.success_rate),
      }))
      : [],
  };
}

function normalizeHistoryAccessLatencyResponse(raw: HistoryAccessLatencyResponse): HistoryAccessLatencyResponse {
  return {
    bucket_seconds: toNumber(raw.bucket_seconds),
    bin_width_ms: toNumber(raw.bin_width_ms),
    overflow_ms: toNumber(raw.overflow_ms),
    items: Array.isArray(raw.items)
      ? raw.items.map((item) => ({
        bucket_start: toString(item.bucket_start),
        bucket_end: toString(item.bucket_end),
        sample_count: toNumber(item.sample_count),
        buckets: Array.isArray(item.buckets)
          ? item.buckets.map((bucket) => ({
            le_ms: toNumber(bucket.le_ms),
            count: toNumber(bucket.count),
          }))
          : [],
        overflow_count: toNumber(item.overflow_count),
      }))
      : [],
  };
}

function normalizeHistoryLeaseLifetimeResponse(raw: HistoryLeaseLifetimeResponse): HistoryLeaseLifetimeResponse {
  return {
    platform_id: toString(raw.platform_id),
    bucket_seconds: toNumber(raw.bucket_seconds),
    items: Array.isArray(raw.items)
      ? raw.items.map((item) => ({
        bucket_start: toString(item.bucket_start),
        bucket_end: toString(item.bucket_end),
        sample_count: toNumber(item.sample_count),
        p1_ms: toNumber(item.p1_ms),
        p5_ms: toNumber(item.p5_ms),
        p50_ms: toNumber(item.p50_ms),
      }))
      : [],
  };
}

function normalizePlatformNodePoolSnapshot(raw: SnapshotPlatformNodePool): SnapshotPlatformNodePool {
  return {
    generated_at: toString(raw.generated_at),
    platform_id: toString(raw.platform_id),
    routable_node_count: toNumber(raw.routable_node_count),
    egress_ip_count: toNumber(raw.egress_ip_count),
  };
}

function normalizeNodeLatencySnapshot(raw: SnapshotNodeLatencyDistribution): SnapshotNodeLatencyDistribution {
  return {
    generated_at: toString(raw.generated_at),
    scope: raw.scope === "platform" ? "platform" : "global",
    platform_id: raw.platform_id ? toString(raw.platform_id) : undefined,
    bin_width_ms: toNumber(raw.bin_width_ms),
    overflow_ms: toNumber(raw.overflow_ms),
    sample_count: toNumber(raw.sample_count),
    buckets: Array.isArray(raw.buckets)
      ? raw.buckets.map((bucket) => ({
        le_ms: toNumber(bucket.le_ms),
        count: toNumber(bucket.count),
      }))
      : [],
    overflow_count: toNumber(raw.overflow_count),
  };
}

async function fetchPlatformRealtimeLeases(platformId: string, window: TimeWindow): Promise<RealtimeLeasesResponse> {
  const data = await apiRequest<RealtimeLeasesResponse>(
    withWindow(`${METRICS_BASE}/realtime/leases`, window, { platform_id: platformId }),
  );
  return normalizeRealtimeLeasesResponse(data);
}

async function fetchPlatformHistoryRequests(
  platformId: string,
  window: TimeWindow,
): Promise<HistoryResponse<HistoryRequestsItem>> {
  const data = await apiRequest<HistoryResponse<HistoryRequestsItem>>(
    withWindow(`${METRICS_BASE}/history/requests`, window, { platform_id: platformId }),
  );
  return normalizeHistoryRequestsResponse(data);
}

async function fetchPlatformHistoryAccessLatency(
  platformId: string,
  window: TimeWindow,
): Promise<HistoryAccessLatencyResponse> {
  const data = await apiRequest<HistoryAccessLatencyResponse>(
    withWindow(`${METRICS_BASE}/history/access-latency`, window, { platform_id: platformId }),
  );
  return normalizeHistoryAccessLatencyResponse(data);
}

async function fetchPlatformHistoryLeaseLifetime(
  platformId: string,
  window: TimeWindow,
): Promise<HistoryLeaseLifetimeResponse> {
  const data = await apiRequest<HistoryLeaseLifetimeResponse>(
    withWindow(`${METRICS_BASE}/history/lease-lifetime`, window, { platform_id: platformId }),
  );
  return normalizeHistoryLeaseLifetimeResponse(data);
}

async function fetchPlatformSnapshotNodePool(platformId: string): Promise<SnapshotPlatformNodePool> {
  const query = new URLSearchParams({ platform_id: platformId });
  const data = await apiRequest<SnapshotPlatformNodePool>(`${METRICS_BASE}/snapshots/platform-node-pool?${query.toString()}`);
  return normalizePlatformNodePoolSnapshot(data);
}

async function fetchPlatformSnapshotNodeLatency(platformId: string): Promise<SnapshotNodeLatencyDistribution> {
  const query = new URLSearchParams({ platform_id: platformId });
  const data = await apiRequest<SnapshotNodeLatencyDistribution>(
    `${METRICS_BASE}/snapshots/node-latency-distribution?${query.toString()}`,
  );
  return normalizeNodeLatencySnapshot(data);
}

type TrendTooltipContentProps = {
  active?: boolean;
  payload?: Array<{ dataKey?: string | number; value?: number | string }>;
  label?: string;
  lines: TrendLineDefinition[];
  valueFormatter: (value: number) => string;
};

function TrendTooltipContent({ active, payload, label, lines, valueFormatter }: TrendTooltipContentProps) {
  if (!active || !payload?.length) {
    return null;
  }

  return (
    <ChartTooltip label={label ?? "—"}>
      {lines.map((line) => {
        const entry = payload.find((item) => item.dataKey === line.dataKey);
        const value = entry?.value == null ? NaN : Number(entry.value);

        return (
          <ChartTooltipRow key={line.dataKey} color={line.color} label={line.name} value={Number.isFinite(value) ? valueFormatter(value) : "—"} />
        );
      })}
    </ChartTooltip>
  );
}

function HistogramTooltipContent({ active, payload }: { active?: boolean; payload?: Array<{ payload?: HistogramBarPoint; value?: number | string }> }) {
  const { t } = useI18n();

  if (!active || !payload?.length) {
    return null;
  }

  const point = payload[0]?.payload as HistogramBarPoint | undefined;
  const count = Number(payload[0]?.value ?? point?.count ?? 0);
  const safeCount = Number.isFinite(count) ? count : 0;
  const lowerBound = typeof point?.lower_ms === "number" && Number.isFinite(point.lower_ms) ? point.lower_ms : 0;
  const upperBound = typeof point?.upper_ms === "number" && Number.isFinite(point.upper_ms) ? point.upper_ms : 0;

  return (
    <ChartTooltip label={`${formatCount(lowerBound)}–${formatCount(upperBound)} ms`}>
      <ChartTooltipRow color={latencyColor(upperBound)} label={t("样本")} value={formatCount(safeCount)} />
    </ChartTooltip>
  );
}

function EmptyChart({ text }: { text: string }) {
  return <EmptyWorkState embedded title={text} />;
}

function TrendLineChart({ data, lines, yTickFormatter, tooltipValueFormatter, emptyText }: TrendLineChartProps) {
  const id = useId().replace(/:/g, "");
  if (!data.length || !lines.length) {
    return <EmptyChart text={emptyText} />;
  }

  const formatYAxis = yTickFormatter ?? formatShortNumber;
  const formatTooltip = tooltipValueFormatter ?? formatYAxis;

  return (
    <div className="telemetry-chart">
      <ResponsiveContainer width="100%" height="100%" minWidth={0}>
        <ComposedChart data={data} margin={{ top: 12, right: 12, bottom: 4, left: 4 }} accessibilityLayer>
          <defs>{lines.map((line, index) => <ChartGradient key={line.dataKey} id={`${id}-${index}`} color={line.color} />)}</defs>
          <CartesianGrid stroke="var(--color-chart-grid)" strokeDasharray="3 3" vertical={false} />
          <XAxis
            dataKey="label"
            interval="preserveStartEnd"
            minTickGap={18}
            tickMargin={8}
            axisLine={false}
            tickLine={false}
            tick={{ fill: "var(--color-chart-axis)", fontSize: 12 }}
          />
          <YAxis
            width="auto"
            tickMargin={4}
            axisLine={false}
            tickLine={false}
            tick={{ fill: "var(--color-chart-axis)", fontSize: 12 }}
            tickFormatter={(value) => formatYAxis(toNumber(value))}
            domain={[0, "auto"]}
          />
          <Tooltip
            cursor={{ stroke: "var(--color-rule-strong)", strokeWidth: 1, strokeDasharray: "3 3" }}
            wrapperStyle={{ outline: "none", zIndex: 10 }}
            isAnimationActive={false}
            content={<TrendTooltipContent lines={lines} valueFormatter={formatTooltip} />}
          />
          {lines.map((line, index) => (
            <Area key={line.dataKey} type="monotone" dataKey={line.dataKey} fill={`url(#${id}-${index})`} stroke="none" tooltipType="none" activeDot={false} isAnimationActive={false} />
          ))}
          {lines.map((line) => (
            <Line
              key={line.dataKey}
              type="monotone"
              dataKey={line.dataKey}
              name={line.name}
              stroke={line.color}
              strokeWidth={2.2}
              dot={data.length === 1 ? { r: 3, fill: line.color } : false}
              activeDot={{ r: 4, stroke: "var(--color-paper-raised)", strokeWidth: 2, fill: line.color }}
              isAnimationActive={false}
            />
          ))}
        </ComposedChart>
      </ResponsiveContainer>
    </div>
  );
}

function compressHistogram(buckets: LatencyBucket[], limit = MAX_HISTOGRAM_BUCKETS): LatencyBucket[] {
  if (buckets.length <= limit) {
    return buckets;
  }

  const chunkSize = Math.ceil(buckets.length / limit);
  const grouped: LatencyBucket[] = [];

  for (let index = 0; index < buckets.length; index += chunkSize) {
    const chunk = buckets.slice(index, index + chunkSize);
    const total = chunk.reduce((acc, item) => acc + item.count, 0);
    grouped.push({
      le_ms: chunk[chunk.length - 1]?.le_ms ?? 0,
      count: total,
    });
  }

  return grouped;
}

function toHistogramBarPoints(buckets: LatencyBucket[]): HistogramBarPoint[] {
  const compact = compressHistogram(buckets);
  const inferredStep = compact.length >= 2 ? Math.max(1, compact[1].le_ms - compact[0].le_ms) : 0;
  const isLegacyUpperInclusive = compact.length > 0 && inferredStep > 0 && compact[0].le_ms === inferredStep;

  return compact.reduce<{ points: HistogramBarPoint[]; previousUpper: number }>(
    (acc, bucket) => {
      const lower = Math.max(0, acc.previousUpper + 1);
      const rawUpper = isLegacyUpperInclusive ? bucket.le_ms - 1 : bucket.le_ms;
      const upperInclusive = Math.max(lower, rawUpper);

      return {
        previousUpper: upperInclusive,
        points: [
          ...acc.points,
          {
            lower_ms: lower,
            upper_ms: upperInclusive,
            label: `${lower}`,
            count: Math.max(0, bucket.count),
          },
        ],
      };
    },
    { points: [], previousUpper: -1 },
  ).points;
}

// Classify by the inclusive upper bound: a mixed bucket never implies its
// slowest samples were fast. The tooltip retains the exact measured interval.
function latencyColor(upperMs: number): string {
  if (upperMs < 150) return "var(--color-latency-fast)";
  if (upperMs <= 500) return "var(--color-latency-medium)";
  return "var(--color-latency-slow)";
}

function LatencyHistogram({ buckets, emptyText }: LatencyHistogramProps) {
  const { t } = useI18n();
  if (!buckets.length) {
    return <EmptyChart text={emptyText} />;
  }

  const data = toHistogramBarPoints(buckets);
  if (!data.length) {
    return <EmptyChart text={emptyText} />;
  }

  return (
    <div className="latency-chart">
      <div className="chart-legend" aria-label={t("延迟分布")}>
        <span><i className="chart-dot" style={{ background: "var(--color-latency-fast)" }} aria-hidden />&lt; 150 ms</span>
        <span><i className="chart-dot" style={{ background: "var(--color-latency-medium)" }} aria-hidden />150–500 ms</span>
        <span><i className="chart-dot" style={{ background: "var(--color-latency-slow)" }} aria-hidden />&gt; 500 ms</span>
      </div>
      <div className="telemetry-chart">
        <ResponsiveContainer width="100%" height="100%" minWidth={0}>
          <BarChart data={data} margin={{ top: 12, right: 12, bottom: 4, left: 4 }} accessibilityLayer>
            <CartesianGrid stroke="var(--color-chart-grid)" strokeDasharray="3 3" vertical={false} />
            <XAxis
              dataKey="label"
              interval="preserveStartEnd"
              minTickGap={14}
              tickMargin={4}
              axisLine={false}
              tickLine={false}
              tick={{ fill: "var(--color-chart-axis)", fontSize: 12 }}
              tickFormatter={(value) => formatLatency(toNumber(value))}
            />
            <YAxis
              width="auto"
              allowDecimals={false}
              tickMargin={4}
              axisLine={false}
              tickLine={false}
              tick={{ fill: "var(--color-chart-axis)", fontSize: 12 }}
              tickFormatter={(value) => formatShortNumber(toNumber(value))}
            />
            <Tooltip
              cursor={{ fill: "var(--color-chart-grid)", radius: 4 }}
              wrapperStyle={{ outline: "none", zIndex: 10 }}
              isAnimationActive={false}
              content={<HistogramTooltipContent />}
            />
            <Bar
              dataKey="count"
              radius={[4, 4, 0, 0]}
              maxBarSize={28}
              fillOpacity={0.86}
              activeBar={{ fillOpacity: 1, stroke: "var(--color-paper-raised)", strokeWidth: 1 }}
              isAnimationActive={false}
            >
              {data.map((point) => <Cell key={point.lower_ms} fill={latencyColor(point.upper_ms)} />)}
            </Bar>
          </BarChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}

export function PlatformMonitorPanel({ platform }: { platform: Platform }) {
  const { t } = useI18n();
  const [rangeKey, setRangeKey] = useState<RangeKey>("6h");

  const realtimeQuery = useQuery({
    queryKey: ["platform-monitor", "realtime-leases", platform.id, rangeKey],
    queryFn: async () => {
      const window = getTimeWindow(rangeKey);
      return fetchPlatformRealtimeLeases(platform.id, window);
    },
    refetchInterval: (query) => {
      const data = query.state.data as RealtimeLeasesResponse | undefined;
      return realtimeRefreshMs(data?.step_seconds);
    },
    placeholderData: (previous) => previous,
  });

  const historyQuery = useQuery({
    queryKey: ["platform-monitor", "history", platform.id, rangeKey],
    queryFn: async () => {
      const window = getTimeWindow(rangeKey);
      const [requests, accessLatency, leaseLifetime] = await Promise.all([
        fetchPlatformHistoryRequests(platform.id, window),
        fetchPlatformHistoryAccessLatency(platform.id, window),
        fetchPlatformHistoryLeaseLifetime(platform.id, window),
      ]);
      return {
        requests,
        accessLatency,
        leaseLifetime,
      } satisfies PlatformHistoryBundle;
    },
    refetchInterval: (query) => {
      const data = query.state.data as PlatformHistoryBundle | undefined;
      return historyRefreshMs([
        data?.requests.bucket_seconds,
        data?.accessLatency.bucket_seconds,
        data?.leaseLifetime.bucket_seconds,
      ]);
    },
    placeholderData: (previous) => previous,
  });

  const snapshotQuery = useQuery({
    queryKey: ["platform-monitor", "snapshots", platform.id],
    queryFn: async () => {
      const [nodePool, nodeLatency] = await Promise.all([
        fetchPlatformSnapshotNodePool(platform.id),
        fetchPlatformSnapshotNodeLatency(platform.id),
      ]);
      return {
        nodePool,
        nodeLatency,
      } satisfies PlatformSnapshotBundle;
    },
    refetchInterval: SNAPSHOT_REFRESH_MS,
    placeholderData: (previous) => previous,
  });

  const monitorError = realtimeQuery.error ?? historyQuery.error ?? snapshotQuery.error;

  const isInitialLoading =
    !realtimeQuery.data &&
    !historyQuery.data &&
    !snapshotQuery.data &&
    (realtimeQuery.isLoading || historyQuery.isLoading || snapshotQuery.isLoading);
  const isRefreshing =
    (realtimeQuery.isFetching || historyQuery.isFetching || snapshotQuery.isFetching) &&
    !isInitialLoading;

  const realtimeItems = realtimeQuery.data?.items ?? EMPTY_REALTIME_ITEMS;
  const sortedRealtimeItems = useMemo(
    () => sortTimeSeriesByTimestamp(realtimeItems, (item) => item.ts),
    [realtimeItems],
  );

  const requestsItems = historyQuery.data?.requests.items ?? EMPTY_HISTORY_REQUEST_ITEMS;
  const sortedRequestsItems = useMemo(
    () => sortTimeSeriesByTimestamp(requestsItems, (item) => item.bucket_start),
    [requestsItems],
  );

  const accessLatencyItems = historyQuery.data?.accessLatency.items ?? EMPTY_ACCESS_LATENCY_ITEMS;
  const sortedAccessLatencyItems = useMemo(
    () => sortTimeSeriesByTimestamp(accessLatencyItems, (item) => item.bucket_start),
    [accessLatencyItems],
  );

  const leaseLifetimeItems = historyQuery.data?.leaseLifetime.items ?? EMPTY_LEASE_LIFETIME_ITEMS;
  const sortedLeaseLifetimeItems = useMemo(
    () => sortTimeSeriesByTimestamp(leaseLifetimeItems, (item) => item.bucket_start),
    [leaseLifetimeItems],
  );

  const latestActiveLeases = latestValue(sortedRealtimeItems.map((item) => item.active_leases));
  const totalRequests = sum(sortedRequestsItems.map((item) => item.total_requests));
  const successRequests = sum(sortedRequestsItems.map((item) => item.success_requests));
  const requestSuccessRatio = successRate(totalRequests, successRequests);

  const latestLeaseLifetime = sortedLeaseLifetimeItems[sortedLeaseLifetimeItems.length - 1];
  const latestP50LeaseMs = latestLeaseLifetime?.p50_ms ?? 0;

  const latestAccessLatency = sortedAccessLatencyItems[sortedAccessLatencyItems.length - 1];

  const snapshotNodePool = snapshotQuery.data?.nodePool;
  const snapshotLatency = snapshotQuery.data?.nodeLatency;

  const leaseTrendData = useMemo(() => {
    return downsampleArray(sortedRealtimeItems, MAX_TREND_POINTS).map((item) => ({
      label: formatClock(item.ts),
      active_leases: item.active_leases,
    }));
  }, [sortedRealtimeItems]);

  const requestTrendData = useMemo(() => {
    return downsampleArray(sortedRequestsItems, MAX_TREND_POINTS).map((item) => ({
      label: formatClock(item.bucket_start),
      total_requests: item.total_requests,
      success_requests: item.success_requests,
    }));
  }, [sortedRequestsItems]);

  const leaseLifetimeTrendData = useMemo(() => {
    return downsampleArray(sortedLeaseLifetimeItems, MAX_TREND_POINTS).map((item) => ({
      label: formatClock(item.bucket_start),
      p1_ms: item.p1_ms,
      p5_ms: item.p5_ms,
      p50_ms: item.p50_ms,
    }));
  }, [sortedLeaseLifetimeItems]);

  const hasAnyData = Boolean(realtimeQuery.data || historyQuery.data || snapshotQuery.data);
  const realtimeAvailable = Boolean(realtimeQuery.data && sortedRealtimeItems.length > 0);
  const requestsAvailable = Boolean(historyQuery.data);
  const snapshotPoolAvailable = Boolean(snapshotNodePool);
  const snapshotLatencyAvailable = Boolean(snapshotLatency);
  const leaseLifetimeAvailable = Boolean(latestLeaseLifetime);
  const accessLatencyAvailable = Boolean(latestAccessLatency);
  const monitorTone = monitorError && !hasAnyData ? "warn" : hasAnyData ? (monitorError ? "warn" : "live") : "neutral";
  const monitorLabel = monitorError && !hasAnyData
    ? t("读取失败")
    : monitorError
      ? t("数据可能已过期")
      : hasAnyData
        ? t("监控已同步")
        : t("等待监控数据");

  return (
    <div className="workspace-stack">
      <WorkspaceCard
        eyebrow="Delivery / telemetry"
        title={t("平台监控")}
        meta={<StatusMark tone={monitorTone} label={monitorLabel} />}
        description={t("查看当前平台的租约、请求成功率、延迟和节点情况。")}
        actions={
          <ControlSelect
            label={t("时间范围")}
            className="workspace-monitor-range"
            aria-label={t("时间范围")}
            selectedKey={rangeKey}
            items={RANGE_OPTIONS.map((option) => ({ id: option.key, label: t(option.label) }))}
            onSelectionChange={(key) => {
              const next = RANGE_OPTIONS.find((option) => option.key === key);
              if (next) setRangeKey(next.key);
            }}
          />
        }
      >
        {monitorError && !hasAnyData && !isInitialLoading ? (
          <EmptyWorkState embedded
            title={t("平台监控数据读取失败")}
            detail={formatApiErrorMessage(monitorError, t)}
            action={<ControlButton variant="secondary" size="sm" onPress={() => void Promise.all([realtimeQuery.refetch(), historyQuery.refetch(), snapshotQuery.refetch()])}>{t("重试")}</ControlButton>}
          />
        ) : null}
        {monitorError && hasAnyData ? <p className="workspace-notice workspace-notice--warn">{formatApiErrorMessage(monitorError, t)}</p> : null}
        {isInitialLoading ? <div className="workspace-register-state"><span className="workspace-spinner" aria-hidden />{t("平台监控数据加载中...")}</div> : null}
        {!hasAnyData && !monitorError && !isInitialLoading ? <EmptyWorkState embedded title={t("暂无平台监控数据")} detail={t("当前时间范围内没有服务端监控样本。")} /> : null}
        {hasAnyData ? (
          <div className="workspace-data-grid workspace-monitor-summary" aria-busy={isRefreshing}>
            <div className="workspace-data-grid__cell">
              <span className="workspace-data-grid__label">{t("活跃租约")}</span>
              <strong className="workspace-data-grid__value">{formatKnownMetric(latestActiveLeases, realtimeAvailable)}</strong>
              <span className="workspace-data-grid__hint">{realtimeAvailable ? t("当前实时值") : t("未加载")}</span>
              <Link to={`${opsPaths.platforms}/${encodeURIComponent(platform.id)}?tab=ops#platform-lease-management`} className="workspace-metric-link"><Link2 size={14} aria-hidden />{t("租约管理")}</Link>
            </div>
            <div className="workspace-data-grid__cell">
              <span className="workspace-data-grid__label">{t("请求成功率")}</span>
              <strong className="workspace-data-grid__value">{formatKnownMetric(requestSuccessRatio, requestsAvailable, formatPercent)}</strong>
              <span className="workspace-data-grid__hint">{requestsAvailable ? `${t("成功")} ${formatCount(successRequests)} / ${t("总计")} ${formatCount(totalRequests)}` : t("未加载")}</span>
            </div>
            <div className="workspace-data-grid__cell">
              <span className="workspace-data-grid__label">{t("可路由节点")}</span>
              <strong className="workspace-data-grid__value">{formatKnownMetric(snapshotNodePool?.routable_node_count, snapshotPoolAvailable)}</strong>
              <span className="workspace-data-grid__hint">{snapshotPoolAvailable ? `${t("出口 IP")} ${formatCount(snapshotNodePool?.egress_ip_count ?? 0)}` : t("未加载")}</span>
              <Link to={`${opsPaths.nodes}?platform_id=${encodeURIComponent(platform.id)}`} className="workspace-metric-link"><Link2 size={14} aria-hidden />{t("节点视图")}</Link>
            </div>
            <div className="workspace-data-grid__cell">
              <span className="workspace-data-grid__label">{t("租约 P50 存活时长")}</span>
              <strong className="workspace-data-grid__value">{formatKnownMetric(latestP50LeaseMs, leaseLifetimeAvailable, formatLeaseDuration)}</strong>
              <span className="workspace-data-grid__hint">{leaseLifetimeAvailable ? t("历史租约时长统计") : t("未加载")}</span>
            </div>
          </div>
        ) : null}
      </WorkspaceCard>

      {hasAnyData ? (
        <>
          <div className="workspace-monitor-grid">
            <WorkspaceCard eyebrow="Telemetry / leases" title={t("活跃租约趋势")} meta={isRefreshing ? t("监控数据刷新中") : undefined} description={t("平台实时租约数量")} className="workspace-monitor-surface">
              <div className="workspace-monitor-chart"><TrendLineChart data={leaseTrendData} emptyText={t("暂无租约实时数据")} yTickFormatter={formatShortNumber} lines={[{ dataKey: "active_leases", name: t("活跃租约"), color: "var(--color-chart-cyan)" }]} /></div>
            </WorkspaceCard>

            <WorkspaceCard eyebrow="Telemetry / requests" title={t("请求统计")} description={t("总请求数 / 成功请求数")} className="workspace-monitor-surface">
              <div className="workspace-monitor-chart"><TrendLineChart data={requestTrendData} emptyText={t("暂无请求统计数据")} yTickFormatter={formatShortNumber} lines={[{ dataKey: "total_requests", name: t("总请求数"), color: "var(--color-chart-cyan)" }, { dataKey: "success_requests", name: t("成功请求数"), color: "var(--color-chart-teal)" }]} /></div>
              <dl className="workspace-detail-facts workspace-monitor-facts">
                <DetailFact label={t("总请求")} value={formatKnownMetric(totalRequests, requestsAvailable)} mono />
                <DetailFact label={t("成功请求")} value={formatKnownMetric(successRequests, requestsAvailable)} mono />
              </dl>
            </WorkspaceCard>

            <WorkspaceCard eyebrow="Telemetry / lease lifetime" title={t("租约存活分位趋势")} description="P1 / P5 / P50" className="workspace-monitor-surface">
              <div className="workspace-monitor-chart"><TrendLineChart data={leaseLifetimeTrendData} emptyText={t("暂无租约生命周期数据")} yTickFormatter={formatLatency} tooltipValueFormatter={formatLeaseDuration} lines={[{ dataKey: "p1_ms", name: "P1", color: "var(--color-chart-violet)" }, { dataKey: "p5_ms", name: "P5", color: "var(--color-chart-cyan)" }, { dataKey: "p50_ms", name: "P50", color: "var(--color-chart-teal)" }]} /></div>
              <dl className="workspace-detail-facts workspace-monitor-facts workspace-monitor-facts--three">
                <DetailFact label="P1" value={formatKnownMetric(latestLeaseLifetime?.p1_ms, leaseLifetimeAvailable, formatLeaseDuration)} mono />
                <DetailFact label="P5" value={formatKnownMetric(latestLeaseLifetime?.p5_ms, leaseLifetimeAvailable, formatLeaseDuration)} mono />
                <DetailFact label="P50" value={formatKnownMetric(latestLeaseLifetime?.p50_ms, leaseLifetimeAvailable, formatLeaseDuration)} mono />
              </dl>
            </WorkspaceCard>

            <WorkspaceCard eyebrow="Telemetry / node pool" title={t("平台节点快照")} description={t("当前平台节点池与延迟样本")} className="workspace-monitor-surface">
              <dl className="workspace-detail-facts workspace-monitor-facts workspace-monitor-facts--four">
                <DetailFact label={t("可路由节点数")} value={formatKnownMetric(snapshotNodePool?.routable_node_count, snapshotPoolAvailable)} mono />
                <DetailFact label={t("出口 IP 数")} value={formatKnownMetric(snapshotNodePool?.egress_ip_count, snapshotPoolAvailable)} mono />
                <DetailFact label={t("延迟样本数")} value={formatKnownMetric(snapshotLatency?.sample_count, snapshotLatencyAvailable)} mono />
                <DetailFact label={t("快照更新时间")} value={snapshotLatencyAvailable && snapshotLatency?.generated_at ? formatClock(snapshotLatency.generated_at) : "—"} mono />
              </dl>
            </WorkspaceCard>

            <WorkspaceCard eyebrow="Telemetry / access latency" title={t("访问延迟分布（历史最新桶）")} description={t("历史访问延迟分布")} className="workspace-monitor-surface workspace-monitor-surface--wide">
              <div className="workspace-monitor-chart"><LatencyHistogram buckets={latestAccessLatency?.buckets ?? []} emptyText={t("暂无访问延迟分布数据")} /></div>
              <dl className="workspace-detail-facts workspace-monitor-facts workspace-monitor-facts--three">
                <DetailFact label={t("时间")} value={accessLatencyAvailable ? formatClock(latestAccessLatency?.bucket_end ?? "") : "—"} mono />
                <DetailFact label={t("样本")} value={formatKnownMetric(latestAccessLatency?.sample_count, accessLatencyAvailable)} mono />
                <DetailFact label={t("溢出")} value={formatKnownMetric(latestAccessLatency?.overflow_count, accessLatencyAvailable)} mono />
              </dl>
            </WorkspaceCard>

            <WorkspaceCard eyebrow="Telemetry / node latency" title={t("节点延迟分布（实时快照）")} description={t("实时节点延迟分布快照")} className="workspace-monitor-surface workspace-monitor-surface--wide">
              <div className="workspace-monitor-chart"><LatencyHistogram buckets={snapshotLatency?.buckets ?? []} emptyText={t("暂无节点延迟快照数据")} /></div>
              <dl className="workspace-detail-facts workspace-monitor-facts workspace-monitor-facts--three">
                <DetailFact label={t("样本")} value={formatKnownMetric(snapshotLatency?.sample_count, snapshotLatencyAvailable)} mono />
                <DetailFact label={t("溢出")} value={formatKnownMetric(snapshotLatency?.overflow_count, snapshotLatencyAvailable)} mono />
                <DetailFact label={t("分桶")} value={snapshotLatencyAvailable ? `${formatCount(snapshotLatency?.bin_width_ms ?? 0)}ms` : "—"} mono />
              </dl>
            </WorkspaceCard>
          </div>
        </>
      ) : null}
    </div>
  );
}
