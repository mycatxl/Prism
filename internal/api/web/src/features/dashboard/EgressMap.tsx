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

type MapSource = Parameters<typeof echarts.registerMap>[1];

/** One region with the position its hub resolved to. */
type PlacedRegion = RegionTraffic & { hub: [number, number] };

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
 * The egress map: where this network's traffic leaves the world.
 *
 * The plate answers the question the globe answered in three dimensions, but in
 * the form a wall display can be read at a glance: the continents are filled, one
 * hub sits over each region that carries traffic, and a flight line joins the
 * hubs to the busiest one. The lines are *not* traffic measurements — nothing in
 * the inventory says which node talks to which — so they are drawn as the
 * region-to-hub relationship they actually are, and the tooltip says so.
 *
 * A hub sits on the region's **busiest member country** (`hubFor`), so every
 * marker is a place the pool really exits from rather than a decorative point
 * near a number. Countries with no exits keep the sea colour: the filled area
 * *is* the footprint, and a faint coastline is the honest way to say "nothing
 * here".
 *
 * The canvas is transparent, so the panel's own ground is the sea and the world
 * has no frame drawn around it.
 */
export default function EgressMap({ regions }: { regions: RegionTraffic[] }) {
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
    chart.setOption(buildOption({ regions: placed, nameIndex, palette, isEnglish, t, animate }), {
      notMerge: true,
    });
  }, [placed, geo, nameIndex, centroidIndex, palette, isEnglish, reducedMotion, t]);

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

  return <div ref={containerRef} className="h-full w-full" role="img" aria-label={t("全球流量")} />;
}

type TooltipParam = { name?: string; seriesType?: string; data?: unknown };

function buildOption({
  regions,
  nameIndex,
  palette,
  isEnglish,
  t,
  animate,
}: {
  regions: PlacedRegion[];
  nameIndex: Map<string, { en: string; zh: string }>;
  palette: MapPalette;
  isEnglish: boolean;
  t: (text: string, options?: Record<string, unknown>) => string;
  animate: boolean;
}): ChartOption {
  const byRegion = new Map(regions.map((item) => [item.id, item]));
  /** Where each region's colour comes from: its position in the table. */
  const colorOf = new Map(
    regions.map((item, index) => [item.id, palette.series[index % palette.series.length]]),
  );
  const busiest = regions.reduce<PlacedRegion | null>(
    (best, item) => (best === null || item.exits > best.exits ? item : best),
    null,
  );

  /** Every hub with at least one node behind it. */
  const hubs = regions.filter((item) => item.exits > 0);

  const maxExits = busiest?.exits ?? 0;
  const hubSize = (exits: number) => {
    if (maxExits <= 0) {
      return palette.hubMin;
    }
    // Area, not radius: a hub twice the size reads as twice the pool.
    const ratio = Math.sqrt(exits / maxExits);
    return palette.hubMin + ratio * (palette.hubMax - palette.hubMin);
  };

  /*
   * The flight lines: every hub to the busiest one.
   *
   * A region pair is drawn only when both ends carry traffic, and the line is
   * styled as the relationship (a route the network operates), never as a
   * volume — `lineStyle.width` is constant, so two lines of different weight
   * never imply a measurement nobody took.
   */
  const lines = busiest
    ? hubs
        .filter((hub) => hub.id !== busiest.id)
        .map((hub) => ({ coords: [hub.hub, busiest.hub] as Array<[number, number]> }))
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
        // The hub series carries the region id in `name`, so the tooltip reads
        // the region's own row rather than re-deriving it from the geometry.
        const region = byRegion.get(name);
        if (region) {
          return [
            `<div style="display:flex;align-items:baseline;gap:6px">`,
            `<span style="font-weight:600">${escapeHtml(t(region.name))}</span>`,
            `<span style="font-family:${CHART_FONT_MONO};font-size:11px;color:${CHART_INK_FAINT}">${escapeHtml(region.hubIso)}</span>`,
            `</div>`,
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
          `<div style="display:flex;align-items:baseline;gap:6px">`,
          `<span style="font-weight:600">${escapeHtml(title)}</span>`,
          `<span style="font-family:${CHART_FONT_MONO};font-size:11px;color:${CHART_INK_FAINT}">${escapeHtml(name)}</span>`,
          `</div>`,
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
       * The insets are the reference plate's land box, measured off the art and
       * held as percentages so the plate keeps its proportions as the board
       * grows. With all four set, ECharts stretches the projection to fill the
       * box instead of fitting it — which is what makes the box *be* the land
       * bounding box rather than something the aspect ratio decides.
       */
      left: "0.8%",
      right: "3.2%",
      top: "0.4%",
      bottom: "13.1%",
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
          scale: 2.6,
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
          // region's row uses in the table beside the plate.
          itemStyle: {
            color: colorOf.get(hub.id) ?? palette.series[0],
            borderColor: palette.hubStroke,
            borderWidth: 1.5,
          },
        })),
      },
      {
        // The centre of the plate: the region every line meets at.
        type: "scatter",
        coordinateSystem: "geo",
        zlevel: 4,
        silent: true,
        symbolSize: (value: unknown) =>
          hubSize(Number((value as [number, number, number])[2] ?? 0)) * 1.6,
        itemStyle: {
          color: palette.hubCore,
          borderColor: busiest ? (colorOf.get(busiest.id) ?? palette.series[0]) : palette.hubCore,
          borderWidth: 2,
        },
        data: busiest
          ? [{ name: busiest.id, value: [...busiest.hub, busiest.exits] as [number, number, number] }]
          : [],
      },
    ],
  };
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
