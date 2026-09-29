/**
 * The design tokens as literals, for the places ECharts needs a value rather
 * than a class name: canvas fills, axis strokes and tooltip surfaces.
 *
 * These are copies of `src/styles/design.css` — the chart cannot read CSS
 * variables off its canvas, and a second palette would drift.
 */
export const CHART_PAPER = "#f2f4f7";
export const CHART_PAPER_SUNK = "#e6eaf0";
export const CHART_PAPER_RAISED = "#ffffff";
export const CHART_RULE = "#d2d8e1";
export const CHART_RULE_STRONG = "#a6afbd";
export const CHART_INK = "#0a0f16";
export const CHART_INK_SOFT = "#39434f";
export const CHART_INK_FAINT = "#566170";
export const CHART_SIGNAL = "#0a6b52";
export const CHART_SIGNAL_DEEP = "#06483a";
export const CHART_LIVE = "#0b5f8a";

export const CHART_FONT_SANS = '"IBM Plex Sans", ui-sans-serif, system-ui, sans-serif';
export const CHART_FONT_MONO = '"IBM Plex Mono", ui-monospace, "SFMono-Regular", monospace';

/**
 * The choropleth ramp: one hue, five steps, paper to signal.
 *
 * Deliberately not a continuous gradient and deliberately not ECharts' red /
 * yellow / green ramp — a country's colour should be readable as a count band,
 * and the paper step at the bottom lets a single exit read as "almost nothing".
 */
export const EXIT_COUNT_BANDS: Array<{ min: number; max: number; color: string }> = [
  { min: 1, max: 2, color: "#dfe6ef" },
  { min: 3, max: 5, color: "#b2ccc4" },
  { min: 6, max: 11, color: "#74ac9e" },
  { min: 12, max: 24, color: "#2f8570" },
  { min: 25, max: Number.POSITIVE_INFINITY, color: CHART_SIGNAL },
];

export function exitCountBandLabel(band: { min: number; max: number }): string {
  if (!Number.isFinite(band.max)) {
    return `${band.min}+`;
  }
  if (band.min === band.max) {
    return `${band.min}`;
  }
  return `${band.min}–${band.max}`;
}