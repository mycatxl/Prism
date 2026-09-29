/**
 * The design tokens as literals, for the places ECharts needs a value rather than
 * a class name: canvas fills, axis strokes and tooltip surfaces.
 *
 * These are copies of `src/styles/design.css` — the chart cannot read CSS
 * variables off its canvas, and a second palette would drift. When a colour here
 * changes, `scripts/check-contrast.mjs` is what proves the change is still legible;
 * it checks the CSS tokens, so keep the two in step.
 */
export const CHART_PAPER = "#f1f3f7";
export const CHART_PAPER_SUNK = "#e5e9f0";
export const CHART_PAPER_RAISED = "#ffffff";
export const CHART_PAPER_INSET = "#f8fafc";
export const CHART_RULE = "#d1d7e0";
export const CHART_RULE_STRONG = "#a4adbb";
export const CHART_INK = "#0a0f16";
export const CHART_INK_SOFT = "#39434f";
export const CHART_INK_FAINT = "#566170";
export const CHART_SIGNAL = "#0a6b52";
export const CHART_SIGNAL_DEEP = "#06483a";
export const CHART_LIVE = "#0b5f8a";
export const CHART_GRID = "#e8ecf2";
export const CHART_AXIS = "#5d6876";

/**
 * The categorical sequence, in the order a chart should spend it.
 *
 * Spread across the luminance axis as well as the hue axis, so the lines stay
 * apart in greyscale — the fallback that a printed screenshot, a colour-blind
 * reader and a three-metre viewing distance all land on. Blue is the primary
 * series, teal is healthy, amber is warned, red is failing.
 */
export const CHART_SERIES = [
  "#0554bb",
  "#03725c",
  "#ba6e05",
  "#dd2206",
  "#6505d1",
  "#059bd1",
] as const;

export const CHART_FONT_SANS = '"IBM Plex Sans", ui-sans-serif, system-ui, sans-serif';
export const CHART_FONT_MONO = '"IBM Plex Mono", ui-monospace, "SFMono-Regular", monospace';

/**
 * The choropleth ramp: one hue, five luminance steps, from near the panel ground to
 * the primary series colour.
 *
 * Deliberately not a continuous gradient and deliberately not ECharts' red / yellow
 * / green ramp — a country's colour has to be readable as a count band, and the
 * lightest step still sits clearly above the colour an empty country keeps, so the
 * coloured area *is* the footprint.
 */
export const EXIT_COUNT_BANDS: Array<{ min: number; max: number; color: string }> = [
  { min: 1, max: 2, color: "#cedef4" },
  { min: 3, max: 5, color: "#9abeef" },
  { min: 6, max: 11, color: "#5f9eef" },
  { min: 12, max: 24, color: "#1073f5" },
  { min: 25, max: Number.POSITIVE_INFINITY, color: "#0554bb" },
];

export function exitCountBandLabel(band: { min: number; max: number }): string {
  if (!Number.isFinite(band.max)) {
    return `${band.min}+`;
  }
  if (band.min === band.max) {
    return `${band.min}`;
  }
  return `${band.min}\u2013${band.max}`;
}
