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
 * it that has to be squinted at.
 */
export const GLOBE_DARK: GlobePalette = {
  base: "#0b1220",
  land: "#1b2a44",
  coast: "#2f4a72",
  graticule: "rgba(47, 74, 114, 0.42)",
  atmosphere: "#3f6ea8",
  markerStroke: "rgba(5, 10, 18, 0.75)",
  ramp: EXIT_COUNT_BANDS.map((band) => band.color).reverse(),
  tooltipPaper: "#111a2b",
  tooltipRule: "#2f4a72",
  tooltipInk: "#e9eff9",
  tooltipInkSoft: "#9db0ca",
  tooltipSignal: "#3ecfa0",
};

/**
 * The paper theme, for the day the board is switched back. Same structure, the
 * luminance axis inverted: the land is the pale step and the busiest region is
 * the deepest blue, which is the order `EXIT_COUNT_BANDS` already runs in.
 */
export const GLOBE_LIGHT: GlobePalette = {
  base: "#e2e8f2",
  land: "#c3d2e8",
  coast: "#8299bd",
  graticule: "rgba(130, 153, 189, 0.35)",
  atmosphere: "#0b5f8a",
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
