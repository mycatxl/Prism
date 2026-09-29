/**
 * The design tokens as literals, for the places ECharts needs a value rather
 * than a class name: canvas fills, axis strokes and tooltip surfaces.
 *
 * These are copies of `src/styles/design.css` — the chart cannot read CSS
 * variables off its canvas, and a second palette would drift.
 */
export const CHART_PAPER = "#f3f5f2";
export const CHART_PAPER_SUNK = "#e8ece7";
export const CHART_PAPER_RAISED = "#fbfcfa";
export const CHART_RULE = "#d3dbd4";
export const CHART_RULE_STRONG = "#b6c1b9";
export const CHART_INK = "#101713";
export const CHART_INK_SOFT = "#57635c";
export const CHART_INK_FAINT = "#8b968f";
export const CHART_SIGNAL = "#0b7a6e";
export const CHART_SIGNAL_DEEP = "#064e47";
export const CHART_LIVE = "#0e7490";

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
  { min: 1, max: 2, color: CHART_PAPER_SUNK },
  { min: 3, max: 5, color: "#bcd9d2" },
  { min: 6, max: 11, color: "#83bfb4" },
  { min: 12, max: 24, color: "#3f9d8f" },
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