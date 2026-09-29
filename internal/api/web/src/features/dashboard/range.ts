import type { TimeWindow } from "./types";

/**
 * The window the overview screen is read over.
 *
 * Moved here unchanged from the dashboard's former trend view, so the control,
 * the ranges it offers and the polling cadence derived from the metric
 * endpoints' own bucket/step sizes all behave exactly as they did.
 */
export type RangeKey = "1h" | "6h" | "24h";

export type RangeOption = {
  key: RangeKey;
  label: string;
  ms: number;
};

export const RANGE_OPTIONS: RangeOption[] = [
  { key: "1h", label: "最近 1 小时", ms: 60 * 60 * 1000 },
  { key: "6h", label: "最近 6 小时", ms: 6 * 60 * 60 * 1000 },
  { key: "24h", label: "最近 24 小时", ms: 24 * 60 * 60 * 1000 },
];

export const DEFAULT_RANGE_KEY: RangeKey = "6h";

/** Snapshot polling stays at the interval this page already used. */
export const SNAPSHOT_REFRESH_MS = 15_000;
/** The exit map reads the whole inventory; regions change on import, not by the second. */
export const NODE_EXITS_REFRESH_MS = 60_000;

export function parseRangeKey(value: string | null): RangeKey {
  const match = RANGE_OPTIONS.find((item) => item.key === value);
  return match ? match.key : DEFAULT_RANGE_KEY;
}

export function rangeOption(key: RangeKey): RangeOption {
  return RANGE_OPTIONS.find((item) => item.key === key) ?? RANGE_OPTIONS[1];
}

/**
 * Built at call time rather than memoised: every refetch has to advance the
 * trailing edge, or the live half of the screen would freeze at mount time.
 */
export function getTimeWindow(rangeKey: RangeKey): TimeWindow {
  const option = rangeOption(rangeKey);
  const to = new Date();
  const from = new Date(to.getTime() - option.ms);
  return {
    from: from.toISOString(),
    to: to.toISOString(),
  };
}

const DEFAULT_REALTIME_REFRESH_SECONDS = 15;
const MIN_REALTIME_REFRESH_MS = 1_000;
const DEFAULT_HISTORY_REFRESH_MS = 60_000;
const MIN_HISTORY_REFRESH_MS = 15_000;
const MAX_HISTORY_REFRESH_MS = 300_000;

function normalizePositiveSeconds(seconds: number | undefined): number | null {
  if (typeof seconds !== "number" || !Number.isFinite(seconds) || seconds <= 0) {
    return null;
  }
  return seconds;
}

export function realtimeRefreshMsFromSteps(stepSeconds: Array<number | undefined>): number {
  const steps = stepSeconds.map(normalizePositiveSeconds).filter((value): value is number => value !== null);
  if (!steps.length) {
    return DEFAULT_REALTIME_REFRESH_SECONDS * 1000;
  }
  return Math.max(MIN_REALTIME_REFRESH_MS, Math.round(Math.min(...steps) * 1000));
}

export function historyRefreshMsFromBuckets(bucketSeconds: Array<number | undefined>): number {
  const buckets = bucketSeconds.map(normalizePositiveSeconds).filter((value): value is number => value !== null);
  if (!buckets.length) {
    return DEFAULT_HISTORY_REFRESH_MS;
  }
  const intervalMs = Math.round(Math.min(...buckets) * 1000);
  return Math.min(MAX_HISTORY_REFRESH_MS, Math.max(MIN_HISTORY_REFRESH_MS, intervalMs));
}