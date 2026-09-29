import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";

/**
 * Formatters for the instrument readouts.
 *
 * Every value on this screen is mono and tabular, so the formatters keep digits
 * and unit separate where possible: the unit is quieter than the number it
 * belongs to.
 */
export const PLACEHOLDER = "--";

function numberLocale(): string {
  return isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
}

export function formatCount(value: number): string {
  return new Intl.NumberFormat(numberLocale()).format(Math.round(value));
}

/** `value` is a fraction (0–1), which is what the metrics API reports. */
export function formatPercent(value: number): string {
  if (!Number.isFinite(value)) {
    return PLACEHOLDER;
  }
  return `${(value * 100).toFixed(1)}%`;
}

export function formatBytes(value: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let next = value;
  let unitIndex = 0;
  while (next >= 1024 && unitIndex < units.length - 1) {
    next /= 1024;
    unitIndex += 1;
  }
  return `${next.toFixed(next >= 100 ? 0 : 1)} ${units[unitIndex]}`;
}

export function formatShortBytes(value: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let next = value;
  let unitIndex = 0;
  while (next >= 1024 && unitIndex < units.length - 1) {
    next /= 1024;
    unitIndex += 1;
  }
  return `${next.toFixed(next >= 100 ? 0 : 1)}${units[unitIndex]}`;
}

export function formatShortCount(value: number): string {
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

/** Latency reads in ms while it is small and in s once it stops being small. */
export function formatLatency(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) {
    return PLACEHOLDER;
  }
  if (ms >= 1000) {
    const seconds = ms / 1000;
    return `${seconds >= 10 ? seconds.toFixed(0) : seconds.toFixed(1)}s`;
  }
  return `${Math.round(ms)}ms`;
}

/**
 * The clock, at the precision the window needs: seconds while the window is
 * short enough that they matter, the date once it is not.
 */
export function formatTimestamp(ms: number, options: { date?: boolean; seconds?: boolean } = {}): string {
  return new Intl.DateTimeFormat(numberLocale(), {
    ...(options.date ? { month: "2-digit" as const, day: "2-digit" as const } : {}),
    hour: "2-digit",
    minute: "2-digit",
    ...(options.seconds ? { second: "2-digit" as const } : {}),
    hour12: false,
  }).format(new Date(ms));
}

/** Timestamps arrive as RFC3339; ECharts and the readouts want epoch ms. */
export function toEpochMs(input: string): number | null {
  const value = Date.parse(input);
  return Number.isNaN(value) ? null : value;
}