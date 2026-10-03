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
 * Everything above `MAP_DARK` mirrors the **light** theme, because the traffic
 * timeline is the day chart; the egress map carries its own palette, because it is
 * the one surface that paints a night sky rather than paper.
 */
export const CHART_PAPER = "#f5f7fb";
export const CHART_PAPER_SUNK = "#edf0f5";
/*
 * The pane surfaces are the paper-first day theme: a cool canvas, a quiet inset
 * well and an elevated white surface. `tests/chart-palette.test.mjs` keeps these
 * literals synchronized with the CSS primitives.
 */
export const CHART_PAPER_RAISED = "#fcfdff";
export const CHART_PAPER_INSET = "#f1f4f8";
export const CHART_RULE = "#d7dee9";
export const CHART_RULE_STRONG = "#a6b2c2";
export const CHART_INK = "#0f1728";
export const CHART_INK_SOFT = "#425168";
export const CHART_INK_FAINT = "#5b6a80";
export const CHART_SIGNAL = "#086a50";
export const CHART_SIGNAL_DEEP = "#054937";
export const CHART_LIVE = "#0a5d88";
export const CHART_GRID = "#e7ecf3";
export const CHART_AXIS = "#5a687b";

/*
 * The accent, which the map spends on exactly one thing: the panel's own egress
 * marker. It is a state colour rather than a series colour, so it is the one mark
 * on the plate that never means "a region". Both values are `--p-accent` from
 * their own theme.
 */
export const CHART_ACCENT = "#3157c7";
export const CHART_ACCENT_DARK = "#818cf8";

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

/**
 * The same sequence, solved for a dark ground.
 *
 * A series colour is not a token that can be inverted: `series-1` at #2b2470 is
 * legible on paper and invisible on a night panel, so the dark theme states its
 * own six. The egress map spends them per region, which is what lets the map and
 * the table beside it agree about what each colour means — the same argument
 * `Donut.tsx` makes for its ring and its legend.
 */
export const CHART_SERIES_DARK = [
  "#4f5de2",
  "#248f75",
  "#bf841f",
  "#e88272",
  "#c6a0eb",
  "#70d4ec",
] as const;

export const CHART_FONT_SANS = '"Manrope", ui-sans-serif, system-ui, sans-serif';
export const CHART_FONT_MONO = '"IBM Plex Mono", ui-monospace, "SFMono-Regular", monospace';

/**
 * The egress map's own colours.
 *
 * The map is the one chart in the console that is painted rather than plotted: a
 * filled world seen at night, with the hubs this network actually exits from
 * burning on top of it. It cannot borrow the paper tokens — its ground is a night
 * sky, and a chart that read `--p-canvas` there would draw a pale rectangle inside
 * a dark panel.
 *
 * There is deliberately no sea colour here. The map's canvas is transparent, and
 * the sea is painted *under* it by the map wrapper's paper inset surface. The
 * wrapper owns the ground because a panel surface owns its material and follows
 * the active theme; a sea colour declared here would be a literal that could not.
 * `land` and `coast` are the two values that are art rather than token — the plate
 * is read at three metres, and the gap between the filled country and the sea is
 * what makes the footprint legible at that distance.
 * Everything a reader has to *interpret* — the tooltip's ink, the healthy signal —
 * is a dark-theme token, so the two halves stay one edit apart.
 */
export type MapPalette = {
  /** Country fill: the footprint, so a country with no exits keeps the sea. */
  land: string;
  /** The hairline that separates a filled country from the sea. */
  coast: string;
  /** Text drawn on the map itself. */
  ink: string;
  /**
   * The six region colours, spent in region order.
   *
   * A hub and its table row are the same region, so they are the same colour: the
   * plate is the summary and the table is the data, exactly as the ring and its
   * legend relate in `Donut.tsx`.
   */
  series: readonly string[];
  /** The dark ring that keeps a hub readable where it lands on land. */
  hubStroke: string;
  /**
   * The panel's own egress marker: the one point the network's traffic leaves
   * from, and the origin every flight line starts at.
   *
   * It comes from the panel's own egress region (`panel_egress_region` on
   * `/system/info`) rather than from the pool, so it is not a hub: it is not
   * sized by node count and it is not one of the six region colours.
   */
  origin: string;
  /** The ring that keeps the origin marker readable wherever it lands. */
  originStroke: string;
  /** The origin marker's diameter, in px: one step above the largest hub. */
  originSize: number;
  /** Hub diameter, in px, at the smallest and largest region. */
  hubMin: number;
  hubMax: number;
  /** The flight line's resting stroke. */
  line: string;
  /** The head of the travelling pulse. */
  lineTrail: string;
  /** Seconds for one pulse to cross a line. */
  linePeriod: number;
  /** The healthy figure in the tooltip. */
  tooltipSignal: string;
};

/**
 * The night plate, which is the map's home: the board is read in a dark room, on
 * glass, and a bright world would be the only thing on it that has to be squinted
 * at.
 */
export const MAP_DARK: MapPalette = {
  land: "#1a3458",
  coast: "#2b4a72",
  ink: "#f1f3f7",
  series: CHART_SERIES_DARK,
  hubStroke: "rgba(11, 22, 47, 0.85)",
  origin: CHART_ACCENT_DARK,
  originStroke: "rgba(11, 22, 47, 0.85)",
  originSize: 12,
  hubMin: 4.5,
  hubMax: 10,
  line: "#7fd6f0",
  lineTrail: "#d8f4ff",
  linePeriod: 5,
  tooltipSignal: "#34b888",
};

/**
 * The day plate, for the light theme.
 *
 * The map follows the theme because its ground *is* the panel's own surface: on
 * the dark board that surface is a night sky, and on paper it is paper. A plate
 * that stayed dark in the light theme would be a night sky inside a pale panel —
 * the one thing the board's own rule ("a chart's ground is the pane's inset")
 * exists to prevent.
 *
 * `land` is therefore a filled step below the wrapper's paper inset surface rather
 * than a lit one above it: on the dark plate the countries glow and the sea is the
 * absence; on paper the countries are the ink and the sea is the sheet. `coast` is
 * the crisper hairline that keeps two neighbouring countries — or a country and the
 * sea — from fusing at a glance. A lit country re-fills its own footprint in the
 * region's series colour at 0.22 (see `EgressMap.tsx`), which lands at roughly
 * `#aeb4d1` over this `land` for `series-1`: a tint of the ground, not a solid,
 * which is the point of that opacity.
 */
export const MAP_LIGHT: MapPalette = {
  land: "#d3ddec",
  coast: "#a9b8d0",
  ink: CHART_INK,
  series: CHART_SERIES,
  hubStroke: "rgba(255, 255, 255, 0.9)",
  origin: CHART_ACCENT,
  originStroke: "rgba(255, 255, 255, 0.9)",
  originSize: 12,
  hubMin: 4.5,
  hubMax: 10,
  line: "#2f6fa8",
  lineTrail: "#0b111e",
  linePeriod: 5,
  tooltipSignal: CHART_SIGNAL_DEEP,
};
