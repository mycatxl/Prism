import { useEffect, useMemo, useRef, useState } from "react";
import { ErrorState, LoadingState } from "../../components/ui/QueryState";
import { useI18n } from "../../i18n";
import {
  CHART_FONT_MONO,
  CHART_FONT_SANS,
  CHART_INK,
  CHART_INK_FAINT,
  CHART_INK_SOFT,
  CHART_PAPER_RAISED,
  CHART_RULE,
  MAP_DARK,
  MAP_LIGHT,
  type MapPalette,
} from "./chartPalette";
import { echarts, type ChartOption, type EChartsInstance } from "./echartsCore";
import { formatCount, formatLatency } from "./format";
import type { RegionTraffic } from "./types";
import { useReducedMotion } from "./useReducedMotion";
import {
  buildRegionCentroidIndex,
  buildRegionNameIndex,
  hubFor,
  loadWorldGeoJson,
  withoutAntarctica,
  type WorldGeoJson,
} from "./worldMap";

const MAP_NAME = "prism-world";

/** The tooltip key of the origin marker: not a region id, so it cannot collide. */
const ORIGIN_KEY = "panel-egress";

type MapSource = Parameters<typeof echarts.registerMap>[1];

/** One region with the position its hub resolved to. */
type PlacedRegion = RegionTraffic & { hub: [number, number] };

/**
 * The panel's own egress, as `/system/info` reports it.
 *
 * Both fields are optional and either may arrive empty: the backend answers this
 * pair from whatever it could resolve, and a panel with no egress region is a
 * normal state rather than an error.
 */
export type PanelEgress = { region?: string; ip?: string };

/** The origin, once the outline has confirmed where it is. */
type PlacedOrigin = { coords: [number, number]; region: string; ip: string };

/**
 * The map follows the theme, because its ground *is* the panel's own surface: on
 * the dark board that surface is a night sky and on paper it is paper. A plate
 * that stayed dark in the light theme would be a night sky inside a pale panel.
 * The attribute is read directly rather than through `useTheme()`, so the chart
 * does not pull the theme store into its lazy chunk; the observer is what makes
 * a toggle repaint it.
 */
function readMapTheme(): "dark" | "light" {
  if (typeof document === "undefined") {
    return "dark";
  }
  return document.documentElement.dataset.theme === "light" ? "light" : "dark";
}

function paletteFor(theme: "dark" | "light"): MapPalette {
  return theme === "light" ? MAP_LIGHT : MAP_DARK;
}

/**
 * The egress map: where this network's traffic leaves from, and where it goes.
 *
 * The plate answers the question the globe answered in three dimensions, but in
 * the form a wall display can be read at a glance: the continents are filled, one
 * hub sits over each region that carries traffic, and one flight line runs from
 * the panel's own egress **outward** to each of those hubs.
 *
 * The origin is the panel's own egress — `panel_egress_region` on
 * `/system/info`, resolved through the same centroid index the hubs use — and it
 * is deliberately not sized by node count, because it is not part of the pool.
 * The lines are *not* traffic measurements: nothing in the inventory says which
 * node talks to which, so they are drawn as the dispatch relationship they
 * actually are — traffic leaves the panel and arrives at a region — and the
 * tooltip says so.
 *
 * When the panel reports no egress region, or the outline does not carry the one
 * it reports, the plate draws **no origin and no lines**. Hubs on their own are
 * the honest picture; lines converging on whichever hub happened to be busiest
 * would be a fact nobody measured.
 *
 * A hub sits on the region's **busiest member country** (`hubFor`), so every
 * marker is a place the pool really exits from rather than a decorative point
 * near a number. That country is *filled* in its region's colour as well, at a low
 * opacity, so the plate draws the footprint and not only the marker: the lit area
 * is where the pool leaves from, and every country without exits keeps `land`.
 * The fill restates the hub's own colour — the same colour the table row beside
 * the plate carries — so it is a second reading of one fact, never a second fact.
 *
 * The canvas is transparent, so the sea is the ground painted under it: the
 * wrapper's two-token gradient in `WorkbenchPage.tsx`. The world has no frame
 * drawn around it.
 */
export default function EgressMap({
  regions,
  origin,
}: {
  regions: RegionTraffic[];
  origin?: PanelEgress;
}) {
  const { t, isEnglish } = useI18n();
  const reducedMotion = useReducedMotion();
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<EChartsInstance | null>(null);
  const hasDrawnRef = useRef(false);
  const [theme, setTheme] = useState<"dark" | "light">(readMapTheme);
  const [geo, setGeo] = useState<WorldGeoJson | null>(null);
  const [failed, setFailed] = useState<unknown>(null);
  const [attempt, setAttempt] = useState(0);

  const palette = useMemo(() => paletteFor(theme), [theme]);

  /* The report is normalised to two primitives, so a re-render of the board that
     re-sends the same query data does not rebuild the option. */
  const originRegion = origin?.region?.trim().toUpperCase() ?? "";
  const originIp = origin?.ip?.trim() ?? "";

  useEffect(() => {
    const observer = new MutationObserver(() => setTheme(readMapTheme()));
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    let active = true;
    loadWorldGeoJson().then(
      (data) => {
        if (active) {
          setGeo(withoutAntarctica(data));
        }
      },
      (cause: unknown) => {
        if (active) {
          setFailed(cause);
        }
      },
    );
    return () => {
      active = false;
    };
  }, [attempt]);

  const nameIndex = useMemo(() => (geo ? buildRegionNameIndex(geo) : null), [geo]);
  const centroidIndex = useMemo(() => (geo ? buildRegionCentroidIndex(geo) : null), [geo]);

  /*
   * Geography is the second input: a region's hub is its busiest country's box
   * centre, so the marker can only be placed once the outline has arrived. A
   * region whose busiest code the outline does not carry falls back to the
   * region's own label position rather than disappearing.
   */
  const placed = useMemo(() => {
    if (!centroidIndex) {
      return [];
    }
    return regions.flatMap((region) => {
      const hub = hubFor(region.hubIso, centroidIndex);
      return hub ? [{ ...region, hub }] : [];
    });
  }, [regions, centroidIndex]);

  /*
   * The origin, resolved through the same index and with no fallback: the index
   * is keyed by the outline's ISO-2 codes, and a code the outline does not carry
   * leaves the plate without an origin rather than with a guessed one. There is
   * deliberately no continent fallback here — the marker claims "traffic leaves
   * from *this place*", which a regional approximation would not support.
   */
  const placedOrigin = useMemo(() => {
    if (!centroidIndex || !originRegion) {
      return null;
    }
    const coords = centroidIndex.get(originRegion);
    return coords ? { coords, region: originRegion, ip: originIp } : null;
  }, [centroidIndex, originRegion, originIp]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container || !geo) {
      return;
    }
    echarts.registerMap(MAP_NAME, geo as unknown as MapSource);
    const chart = echarts.init(container, undefined, { renderer: "canvas" });
    chartRef.current = chart;
    hasDrawnRef.current = false;
    // The board is read at several widths, so the map follows its container
    // instead of a fixed size.
    const observer = new ResizeObserver(() => chart.resize());
    observer.observe(container);
    return () => {
      observer.disconnect();
      chart.dispose();
      chartRef.current = null;
    };
  }, [geo]);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart || !geo || !nameIndex || !centroidIndex) {
      return;
    }
    // One entrance, on the first paint only: a refresh that re-ran the whole
    // animation every minute would be decoration, not state.
    const animate = !reducedMotion && !hasDrawnRef.current;
    hasDrawnRef.current = true;
    chart.setOption(
      buildOption({ regions: placed, origin: placedOrigin, nameIndex, palette, theme, isEnglish, t, animate }),
      { notMerge: true },
    );
  }, [placed, placedOrigin, geo, nameIndex, centroidIndex, palette, theme, isEnglish, reducedMotion, t]);

  if (failed) {
    return (
      <ErrorState
        className="my-auto"
        message={t("地图数据加载失败")}
        onRetry={() => {
          setFailed(null);
          setAttempt((current) => current + 1);
        }}
      />
    );
  }

  if (!geo) {
    return <LoadingState className="h-full" label={t("正在加载")} />;
  }

  return <div ref={containerRef} className="h-full w-full" role="img" aria-label={t("出口分布")} />;
}

type TooltipParam = { name?: string; seriesType?: string; data?: unknown };

function buildOption({
  regions,
  origin,
  nameIndex,
  palette,
  isEnglish,
  t,
  theme,
  animate,
}: {
  regions: PlacedRegion[];
  origin: PlacedOrigin | null;
  nameIndex: Map<string, { en: string; zh: string }>;
  palette: MapPalette;
  isEnglish: boolean;
  /**
   * Which plate was chosen, explicitly rather than inferred from the palette's
   * identity: the lit footprint's opacity is a per-theme decision, and reading it
   * off the palette object would make an unrelated refactor of `paletteFor` change
   * the map's art.
   */
  theme: "dark" | "light";
  t: (text: string, options?: Record<string, unknown>) => string;
  animate: boolean;
}): ChartOption {
  const byRegion = new Map(regions.map((item) => [item.id, item]));
  /** Where each region's colour comes from: its position in the table. */
  const colorOf = new Map(
    regions.map((item, index) => [item.id, palette.series[index % palette.series.length]]),
  );

  /** Every hub with at least one node behind it: the destinations of the lines. */
  const hubs = regions.filter((item) => item.exits > 0);

  const maxExits = hubs.reduce((max, item) => Math.max(max, item.exits), 0);
  const hubSize = (exits: number) => {
    if (maxExits <= 0) {
      return palette.hubMin;
    }
    // Area, not radius: a hub twice the size reads as twice the pool.
    const ratio = Math.sqrt(exits / maxExits);
    return palette.hubMin + ratio * (palette.hubMax - palette.hubMin);
  };

  /*
   * The flight lines: the panel's own egress to every hub, one line each.
   *
   * The direction is the point. The line is a dispatch route the network
   * operates — traffic leaves the panel and arrives in a region — never a volume,
   * and `lineStyle.width` is constant so two lines of different weight never
   * imply a measurement nobody took.
   *
   * With no origin there are no lines at all: without a starting point there is
   * nothing honest to draw between two hubs.
   */
  const lines = origin
    ? hubs.map((hub) => ({ coords: [origin.coords, hub.hub] as Array<[number, number]> }))
    : [];

  return {
    animation: animate,
    animationDuration: 520,
    animationEasing: "cubicOut",
    textStyle: { fontFamily: CHART_FONT_SANS, color: palette.ink },
    tooltip: {
      trigger: "item",
      backgroundColor: CHART_PAPER_RAISED,
      borderColor: CHART_RULE,
      borderWidth: 1,
      padding: [8, 10],
      textStyle: { color: CHART_INK, fontFamily: CHART_FONT_SANS, fontSize: 12 },
      extraCssText: "box-shadow:none;border-radius:3px;",
      formatter: (params: unknown) => {
        const raw = params as TooltipParam;
        const name = raw.name;
        if (!name) {
          return "";
        }
        // The panel's own egress: the one mark that is not a region, and the only
        // one that explains what the lines mean.
        if (name === ORIGIN_KEY) {
          if (!origin) {
            return "";
          }
          const names = nameIndex.get(origin.region);
          const label = names ? (isEnglish ? names.en : names.zh) : origin.region;
          return [
            head(t("面板出口"), origin.region),
            row(t("地区"), label, CHART_INK),
            ...(origin.ip ? [row(t("出口 IP"), origin.ip, CHART_INK)] : []),
            `<div style="margin-top:6px;max-width:210px;color:${CHART_INK_SOFT};font-size:11px;line-height:1.35">${escapeHtml(t("流量从这里分发到各节点区域。"))}</div>`,
          ].join("");
        }
        // The hub series carries the region id in `name`, so the tooltip reads
        // the region's own row rather than re-deriving it from the geometry.
        const region = byRegion.get(name);
        if (region) {
          return [
            head(t(region.name), region.hubIso),
            row(t("节点"), formatCount(region.exits), CHART_INK),
            row(t("健康"), formatCount(region.healthy), palette.tooltipSignal),
            row(t("延迟"), region.latency === null ? t("未知") : formatLatency(region.latency), CHART_INK),
          ].join("");
        }
        const names = nameIndex.get(name);
        const title = names ? (isEnglish ? names.en : names.zh) : name;
        // The map series carries the whole outline, so a country with no entry
        // has no exits — that is a measured zero, not unknown data.
        return [
          head(title, name),
          row(t("节点"), formatCount(0), CHART_INK),
          row(t("健康"), formatCount(0), palette.tooltipSignal),
        ].join("");
      },
    },
    geo: {
      map: MAP_NAME,
      roam: false,
      silent: false,
      /*
       * The four sides are pinned to zero on purpose. With all four set, ECharts
       * stretches the projection to fill the box instead of fitting it into it — which
       * is only safe because the box already carries the map's own ratio
       * (`aspect-[259/100]` on the wrapper in `WorkbenchPage.tsx`). At that ratio
       * "stretch to the box" and "fit the projection" are the same operation, so the
       * world fills the card edge to edge with no dead sea at the flanks, no distortion,
       * and no auto-fit padding ECharts would otherwise leave around the outline.
       *
       * What this replaced: four left-unset sides, which made ECharts fit the projection
       * preserving its aspect ratio and centre it — the right answer while the box was a
       * fixed height that could not track the width. Before that, four measured percentage
       * insets made the panel's box *be* the land bounding box, and at the column's full
       * width the same numbers widened the world and cut off its southern edge.
       */
      left: 0,
      top: 0,
      right: 0,
      bottom: 0,
      itemStyle: {
        areaColor: palette.land,
        borderColor: palette.coast,
        borderWidth: 0.5,
      },
      emphasis: {
        disabled: true,
        itemStyle: { areaColor: palette.land },
        label: { show: false },
      },
      select: { disabled: true, itemStyle: { areaColor: palette.land } },
      /*
       * The lit footprint: one entry per placed region, filled in that region's own
       * series colour at a low opacity — the same colour that region's hub dot and
       * its row in the region table carry, so the map and the table beside it agree
       * about what a colour means. A country the pool exits from is therefore drawn
       * as well as marked, and every country with no exits keeps `areaColor` above.
       * The fill restates the marker; it is not a second figure.
       */
      regions: regions.map((region) => ({
        name: region.hubIso,
        itemStyle: {
          areaColor: colorOf.get(region.id) ?? palette.series[0],
          opacity: theme === "dark" ? 0.32 : 0.22,
          borderColor: palette.coast,
          borderWidth: 0.5,
        },
      })),
    },
    series: [
      {
        type: "lines",
        coordinateSystem: "geo",
        zlevel: 2,
        silent: true,
        effect: {
          show: animate,
          period: palette.linePeriod,
          trailLength: 0.16,
          symbol: "circle",
          symbolSize: 2.4,
          color: palette.lineTrail,
        },
        lineStyle: {
          color: palette.line,
          width: 0.7,
          opacity: 0.55,
          curveness: 0.24,
        },
        data: lines,
      },
      {
        type: "effectScatter",
        coordinateSystem: "geo",
        zlevel: 3,
        rippleEffect: {
          // A pulse is motion, so a reader who asked for stillness gets a solid
          // hub instead of a still frame of an animation: the ripple count is the
          // knob ECharts exposes, and zero draws none.
          number: animate ? 3 : 0,
          period: 3.4,
          scale: 2.2,
          brushType: "stroke",
        },
        symbolSize: (value: unknown) => hubSize(Number((value as [number, number, number])[2] ?? 0)),
        showEffectOn: animate ? "render" : "emphasis",
        emphasis: { scale: 1.15 },
        label: { show: false },
        data: hubs.map((hub) => ({
          name: hub.id,
          value: [...hub.hub, hub.exits] as [number, number, number],
          // The hub carries its region's series colour — the same colour that
          // region's row uses in the table.
          itemStyle: {
            color: colorOf.get(hub.id) ?? palette.series[0],
            borderColor: palette.hubStroke,
            borderWidth: 1.5,
          },
        })),
      },
      {
        // The panel's own egress: the origin the lines leave from, drawn one step
        // larger and in the accent rather than in a region colour, so it never
        // reads as a hub.
        type: "effectScatter",
        coordinateSystem: "geo",
        zlevel: 4,
        rippleEffect: {
          number: animate ? 3 : 0,
          period: 3,
          scale: 2.6,
          brushType: "stroke",
        },
        symbolSize: palette.originSize,
        showEffectOn: animate ? "render" : "emphasis",
        emphasis: { scale: 1.1 },
        label: { show: false },
        itemStyle: {
          color: palette.origin,
          borderColor: palette.originStroke,
          borderWidth: 2,
        },
        data: origin ? [{ name: ORIGIN_KEY, value: [...origin.coords, 0] as [number, number, number] }] : [],
      },
    ],
  };
}

/** The two-line head of a tooltip: the name and the code it belongs to. */
function head(title: string, code: string): string {
  return [
    `<div style="display:flex;align-items:baseline;gap:6px">`,
    `<span style="font-weight:600">${escapeHtml(title)}</span>`,
    `<span style="font-family:${CHART_FONT_MONO};font-size:11px;color:${CHART_INK_FAINT}">${escapeHtml(code)}</span>`,
    `</div>`,
  ].join("");
}

/** One `label / value` row of the tooltip, right-aligned like the table. */
function row(label: string, value: string, color: string): string {
  return [
    `<div style="margin-top:4px;display:flex;gap:8px;align-items:baseline;justify-content:space-between">`,
    `<span style="color:${CHART_INK_SOFT};font-size:12px">${escapeHtml(label)}</span>`,
    `<span style="font-family:${CHART_FONT_MONO};font-size:13px;font-weight:600;color:${color}">${escapeHtml(value)}</span>`,
    `</div>`,
  ].join("");
}

function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, (character) => {
    switch (character) {
      case "&":
        return "&amp;";
      case "<":
        return "&lt;";
      case ">":
        return "&gt;";
      case '"':
        return "&quot;";
      default:
        return "&#39;";
    }
  });
}
