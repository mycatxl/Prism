/**
 * The design tokens as literals, for the places ECharts needs a value rather than
 * a class name: canvas fills, axis strokes and tooltip surfaces.
 *
 * These are copies of `src/styles/design.css` — the chart cannot read CSS
 * variables off its canvas, and a second palette would drift. When a colour here
 * changes, `scripts/check-contrast.mjs` is what proves the change is still legible;
 * it checks the CSS tokens, so keep the two in step. `tests/chart-palette.test.mjs`
 * is what proves they *are* in step: it reads both files and fails on any pair that
 * disagrees.
 *
 * Everything below mirrors the **light** theme, because the flat map is the day
 * board; the globe carries its own palette (see `GLOBE_DARK`), and its tooltip
 * mirrors the dark primitives.
 */
export const CHART_PAPER = "#f4f6fb";
export const CHART_PAPER_SUNK = "#eceef4";
export const CHART_PAPER_RAISED = "#fcfcfe";
export const CHART_PAPER_INSET = "#fafbfd";
export const CHART_RULE = "#d5dbe6";
export const CHART_RULE_STRONG = "#9fa9ba";
export const CHART_INK = "#0b111e";
export const CHART_INK_SOFT = "#364152";
export const CHART_INK_FAINT = "#526075";
export const CHART_SIGNAL = "#086a50";
export const CHART_SIGNAL_DEEP = "#054937";
export const CHART_LIVE = "#0a5d88";
export const CHART_GRID = "#e6eaf2";
export const CHART_AXIS = "#556274";

/**
 * The categorical sequence, in the order a chart should spend it.
 *
 * Spread across the luminance axis as well as the hue axis, so the lines stay
 * apart in greyscale — the fallback that a printed screenshot, a colour-blind
 * reader and a three-metre viewing distance all land on. Indigo is the primary
 * series, teal is healthy, amber is warned, red is failing.
 */
export const CHART_SERIES = [
  "#2b2470",
  "#04745c",
  "#b86c04",
  "#dd2408",
  "#6d1eb5",
  "#0393c9",
] as const;

export const CHART_FONT_SANS = '"Manrope", ui-sans-serif, system-ui, sans-serif';
export const CHART_FONT_MONO = '"IBM Plex Mono", ui-monospace, "SFMono-Regular", monospace';

/**
 * The choropleth ramp: one hue, five luminance steps, from pale paper to the
 * primary series colour.
 *
 * Deliberately not a continuous gradient and deliberately not ECharts' red / yellow
 * / green ramp — a country's colour has to be readable as a count band, and the
 * lightest step still sits clearly above the colour an empty country keeps, so the
 * coloured area *is* the footprint. The deepest step is `CHART_SERIES[0]`: the
 * legend's last swatch and the primary series line are the same colour on purpose.
 */
export const EXIT_COUNT_BANDS: Array<{ min: number; max: number; color: string }> = [
  { min: 1, max: 2, color: "#dfe0f8" },
  { min: 3, max: 5, color: "#b9bcef" },
  { min: 6, max: 11, color: "#8f93e4" },
  { min: 12, max: 24, color: "#5257c9" },
  { min: 25, max: Number.POSITIVE_INFINITY, color: "#2b2470" },
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

/**
 * The globe's own colours.
 *
 * The 3D egress view is the one chart in the console that cannot borrow the CSS
 * tokens: its ground is a sphere seen against the panel, painted from a canvas
 * the globe builds itself, and its tooltip floats over a WebGL layer that the
 * theme's cascade never reaches. The literals live here so both themes stay one
 * edit away from the design tokens, and no component hardcodes a colour of its
 * own.
 *
 * The ramp is `EXIT_COUNT_BANDS` read from the other end. On paper the *fewest*
 * exits take the palest step; on a near-black sphere legibility runs the other
 * way, so the brightest step is the busiest region. The band boundaries are not
 * restated — `exitCountBandIndex` walks the same table the legend prints, so a
 * marker and its swatch can never disagree.
 */
export type GlobePalette = {
  /** The sphere where no country is: the sea, and the void behind the globe. */
  base: string;
  /** Country fill. */
  land: string;
  /** Coastline, one hairline brighter than the land it outlines. */
  coast: string;
  /** The graticule, drawn under the land so it only crosses open water. */
  graticule: string;
  /** The atmosphere ring, and the rim glow the globe shader adds to the earth. */
  atmosphere: string;
  /** The hairline that keeps a marker readable on top of a country. */
  markerStroke: string;
  /** Marker colours, dimmest first — index with `exitCountBandIndex`. */
  ramp: readonly string[];
  tooltipPaper: string;
  tooltipRule: string;
  tooltipInk: string;
  tooltipInkSoft: string;
  tooltipSignal: string;
};

/**
 * The dark panel, which is the globe's home: the board is read at night, on
 * glass, from three metres away, and a bright sphere would be the only thing on
 * it that has to be squinted at. The ground is the dark canvas; the land and the
 * coast are the sphere's own indigo, which is the one thing here that is art
 * rather than a token.
 */
export const GLOBE_DARK: GlobePalette = {
  base: "#070d1a",
  land: "#1b2440",
  coast: "#33456b",
  graticule: "rgba(51, 69, 107, 0.45)",
  atmosphere: "#4a5fc4",
  markerStroke: "rgba(5, 10, 18, 0.75)",
  ramp: EXIT_COUNT_BANDS.map((band) => band.color).reverse(),
  tooltipPaper: "#1a1f2b",
  tooltipRule: "#2a3447",
  tooltipInk: "#f1f3f7",
  tooltipInkSoft: "#aeb9cb",
  tooltipSignal: "#34b888",
};

/**
 * The day board, for the theme the console can be switched to. Same structure, the
 * luminance axis inverted: the land is the pale step and the busiest region is
 * the deepest indigo, which is the order `EXIT_COUNT_BANDS` already runs in.
 */
export const GLOBE_LIGHT: GlobePalette = {
  base: "#e6e9f5",
  land: "#cbd1ea",
  coast: "#8a93c4",
  graticule: "rgba(138, 147, 196, 0.35)",
  atmosphere: "#4338ca",
  markerStroke: "rgba(255, 255, 255, 0.85)",
  ramp: EXIT_COUNT_BANDS.map((band) => band.color),
  tooltipPaper: CHART_PAPER_RAISED,
  tooltipRule: CHART_RULE,
  tooltipInk: CHART_INK,
  tooltipInkSoft: CHART_INK_SOFT,
  tooltipSignal: CHART_SIGNAL_DEEP,
};

/** The two boards the console can be read on. */
export type GlobeTheme = "dark" | "light";

/**
 * The theme the document is currently wearing.
 *
 * The panel's theme is a document-level attribute, not a prop: a chart reading
 * it is the only way the sphere can follow a board that is switched at runtime.
 * Nothing but an explicit `light` leaves the dark globe, so a document that
 * never sets the attribute gets the board this console is built for.
 */
export function readGlobeTheme(): GlobeTheme {
  if (typeof document === "undefined") {
    return "dark";
  }
  return document.documentElement.dataset.theme === "light" ? "light" : "dark";
}

/** The globe palette for a theme, as a pure lookup the caller can memoise. */
export function globePaletteFor(theme: GlobeTheme): GlobePalette {
  return theme === "light" ? GLOBE_LIGHT : GLOBE_DARK;
}

/**
 * Which band a count falls in, as an index into `GlobePalette.ramp`.
 *
 * The boundaries come from `EXIT_COUNT_BANDS`, so the globe, the flat map's
 * visual map and the legend row are the same five steps in the same order.
 */
export function exitCountBandIndex(exits: number): number {
  const index = EXIT_COUNT_BANDS.findIndex((band) => exits >= band.min && exits <= band.max);
  return index === -1 ? 0 : index;
}

/** The colour of one marker: the count's band, on the globe's own ramp. */
export function globeMarkerColor(palette: GlobePalette, exits: number): string {
  return palette.ramp[exitCountBandIndex(exits)];
}
