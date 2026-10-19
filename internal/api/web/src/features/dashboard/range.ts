import type { TimeWindow } from "./types";

/**
 * The windows the overview screen reads over, in milliseconds.
 *
 * The range control moved to the overview screen itself, so this module keeps
 * only what a caller still asks for: the window a key names, and the two polling
 * intervals the page hands to react-query.
 */
type RangeKey = "1h" | "6h" | "24h";

const RANGE_MS: Record<RangeKey, number> = {
  "1h": 60 * 60 * 1000,
  "6h": 6 * 60 * 60 * 1000,
  "24h": 24 * 60 * 60 * 1000,
};

/** Snapshot polling stays at the interval this page already used. */
export const SNAPSHOT_REFRESH_MS = 15_000;
/** The exit map reads the whole inventory; regions change on import, not by the second. */
export const NODE_EXITS_REFRESH_MS = 60_000;

/**
 * Built at call time rather than memoised: every refetch has to advance the
 * trailing edge, or the live half of the screen would freeze at mount time.
 */
export function getTimeWindow(rangeKey: RangeKey): TimeWindow {
  const to = new Date();
  const from = new Date(to.getTime() - RANGE_MS[rangeKey]);
  return {
    from: from.toISOString(),
    to: to.toISOString(),
  };
}
