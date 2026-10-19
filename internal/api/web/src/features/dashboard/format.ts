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