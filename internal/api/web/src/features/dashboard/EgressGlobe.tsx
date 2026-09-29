import { useEffect, useMemo, useRef, useState } from "react";
import { ErrorState, LoadingState } from "../../components/ui/QueryState";
import { useI18n } from "../../i18n";
import {
  CHART_FONT_MONO,
  CHART_FONT_SANS,
  globeMarkerColor,
  globePaletteFor,
  readGlobeTheme,
  type GlobePalette,
  type GlobeTheme,
} from "./chartPalette";
import { echarts, type EChartsInstance } from "./echartsCore";
import { formatCount } from "./format";
import { globeTextureDataUrl } from "./globeTexture";
import type { RegionExitCount } from "./types";
import { useReducedMotion } from "./useReducedMotion";
import {
  buildRegionCentroidIndex,
  buildRegionNameIndex,
  loadWorldGeoJson,
  type WorldGeoJson,
} from "./worldMap";
import EgressMap from "./EgressMap";
// The extension pack is the import that registers it: `echarts-gl` adds the
// `globe` component and the `scatter3D` series to the ECharts instance
// echartsCore hands out, and shaves nothing off the other charts because only
// this lazily loaded component pulls it in. echartsCore stays the single module
// that imports ECharts itself — this one only extends it.
import "echarts-gl";

/** echarts-gl measures its world in these units: the sphere is this wide. */
const GLOBE_RADIUS = 100;

/**
 * How far the camera stands off the sphere.
 *
 * sqrt(d² − R²))`, and the constants are pinned by measurement rather than by
 * assuming the camera's field of view: the scale is 1/d, and the panel reported
 * its sphere at 246px across in an 857x404 box at 260 units and 303px at 200.
 * 175 therefore lands it at roughly 345px — 85% of the height, which fills a
 * panel whose whole job is the sphere while leaving the atmosphere ring inside
 * the frame. It is height-driven because this panel is always wider than it is
 * tall: it takes 8 of the board's 12 columns.
 */
const GLOBE_DISTANCE = 175;

/** Degrees per second. Fast enough to notice, slow enough to read a country. */
const GLOBE_AUTO_ROTATE_SPEED = 8;

/** Marker diameter in pixels: a 1-node region against a 500-node one. */
const MARKER_MIN_SIZE = 7;
const MARKER_MAX_SIZE = 26;

/**
 * The egress globe: the same question the flat map answers, asked in three
 * dimensions.
 *
 * One marker per region, at the centre of the region's largest ring, sized by
 * the node count and coloured by the band the count falls in — the legend under
 * the panel is that ramp, in the same order. No arcs: the console knows how many
 * nodes leave through a country, not what any two of them say to each other, and
 * a line drawn between them would be an invention that reads as a measurement.
 *
 * The sphere itself is painted from the committed `world-110m.geo.json` (see
 * globeTexture.ts), so the coastline here and the coastline on the paper map
 * come from one file.
 */
export default function EgressGlobe({ regions }: { regions: RegionExitCount[] }) {
  const { t, isEnglish } = useI18n();
  const reducedMotion = useReducedMotion();
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<EChartsInstance | null>(null);
  const [theme, setTheme] = useState<GlobeTheme>(readGlobeTheme);
  const [geo, setGeo] = useState<WorldGeoJson | null>(null);
  const [failed, setFailed] = useState<unknown>(null);
  const [attempt, setAttempt] = useState(0);
  // Whether this browser can draw a sphere is a property of the browser, so it
  // is asked for once, before the first paint, rather than discovered after one:
  // the panel never shows a chart it is about to swap for the flat map.
  const [unsupported, setUnsupported] = useState(() => !hasWebgl());

  // The theme lives on `<html data-theme>`, which is the panel's decision and
  // not this component's: follow it, so a board switched to paper gets a globe
  // painted for paper instead of a dark sphere on a light panel.
  useEffect(() => {
    const observer = new MutationObserver(() => setTheme(readGlobeTheme()));
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    let active = true;
    loadWorldGeoJson().then(
      (data) => {
        if (active) {
          setGeo(data);
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
  const palette = useMemo(() => globePaletteFor(theme), [theme]);

  // Painting the earth is the expensive half of mounting, so it happens once per
  // geometry and theme rather than once per render — and a browser that cannot
  // paint it is a browser that gets the flat map. It comes out as a data URL
  // rather than a canvas: globeTexture.ts records what echarts-gl does to a
  // canvas texture on the second render.
  const texture = useMemo(() => {
    if (!geo) {
      return null;
    }
    try {
      return globeTextureDataUrl(geo, palette);
    } catch (cause) {
      console.warn("egress globe: could not paint the earth texture", cause);
      return null;
    }
  }, [geo, palette]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container || !geo || !texture || unsupported) {
      return;
    }

    // The chart object and the verdict on its WebGL layer are decided together on
    // the next frame: echarts-gl builds the layer during the first update and,
    // when the browser refuses a context, says so on the panel itself as a div
    // marked `ecgl-nowebgl`. Deciding there rather than mid-effect is also what
    // keeps a failed panel from cascading a second render out of this one.
    let chart: EChartsInstance | null = null;
    let observer: ResizeObserver | null = null;
    try {
      chart = echarts.init(container, undefined, { renderer: "canvas" });
      chartRef.current = chart;
      // Same contract as the flat map: the board is read at several widths, so
      // the sphere follows its container rather than a fixed size.
      observer = new ResizeObserver(() => chart?.resize());
      observer.observe(container);
    } catch (cause) {
      console.warn("egress globe: could not create the chart", cause);
    }

    const frame = requestAnimationFrame(() => {
      const drawn = container.querySelector("canvas") !== null && container.querySelector(".ecgl-nowebgl") === null;
      if (!chart || !drawn) {
        setUnsupported(true);
      }
    });

    return () => {
      cancelAnimationFrame(frame);
      observer?.disconnect();
      chart?.dispose();
      chartRef.current = null;
    };
  }, [geo, texture, unsupported]);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart || !texture || !nameIndex || !centroidIndex) {
      return;
    }
    chart.setOption(
      buildOption({
        regions,
        centroidIndex,
        nameIndex,
        texture,
        palette,
        isEnglish,
        autoRotate: !reducedMotion,
        t,
      }),
    );
  }, [regions, texture, nameIndex, centroidIndex, palette, isEnglish, reducedMotion, t]);

  // The view that cannot be drawn is not a broken panel: the flat map still
  // answers the question, so a browser without WebGL (or a locked-down kiosk
  // without it) gets the map it had before and no error to clear.
  if (unsupported) {
    return <EgressMap regions={regions} />;
  }

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

  // Geometry in hand but nothing painted from it: the same fallback as no WebGL,
  // because a sphere without an earth texture is not a view of anything.
  if (!texture) {
    return <EgressMap regions={regions} />;
  }

  return <div ref={containerRef} className="h-full w-full" role="img" aria-label={t("地球视图")} />;
}

/**
 * Whether this browser can give us a WebGL context at all.
 *
 * The probe canvas is released again through `WEBGL_lose_context` rather than
 * left to the garbage collector: a browser only hands out a handful of contexts,
 * and one spent here would be one the sphere cannot have. The layers that matter
 * are checked twice — here before the chart exists, and on the panel afterwards
 * (see the `ecgl-nowebgl` check), because a context can also be refused later.
 */
function hasWebgl(): boolean {
  try {
    const probe = document.createElement("canvas");
    const gl: { getExtension(name: string): unknown } | null =
      probe.getContext("webgl2") ?? probe.getContext("webgl");
    if (!gl) {
      return false;
    }
    const release = gl.getExtension("WEBGL_lose_context") as { loseContext?: () => void } | undefined;
    release?.loseContext?.();
    return true;
  } catch {
    return false;
  }
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

/** A marker's diameter: area rises with the count, so 400 nodes is not 4x a 100. */
function markerSize(exits: number, maxExits: number): number {
  const share = maxExits > 0 ? Math.min(1, Math.max(0, exits / maxExits)) : 0;
  return Math.round(MARKER_MIN_SIZE + (MARKER_MAX_SIZE - MARKER_MIN_SIZE) * Math.sqrt(share));
}

type GlobeMarker = {
  value: [number, number, number, number, string];
  itemStyle: { color: string };
};

type TooltipParams = { value?: unknown };

function buildOption({
  regions,
  centroidIndex,
  nameIndex,
  texture,
  palette,
  isEnglish,
  autoRotate,
  t,
}: {
  regions: RegionExitCount[];
  centroidIndex: Map<string, [number, number]>;
  nameIndex: Map<string, { en: string; zh: string }>;
  texture: string;
  palette: GlobePalette;
  isEnglish: boolean;
  autoRotate: boolean;
  t: (text: string, options?: Record<string, unknown>) => string;
}) {
  const maxExits = regions.reduce((largest, region) => Math.max(largest, region.exits), 0);
  const data: GlobeMarker[] = [];

  for (const region of regions) {
    const centroid = centroidIndex.get(region.region);
    if (!centroid) {
      // A region with no shape in the committed outline is not plotted here for
      // the same reason the flat map cannot colour it: there is nowhere honest
      // to put it.
      continue;
    }
    data.push({
      value: [centroid[0], centroid[1], region.exits, region.healthy, region.region],
      itemStyle: { color: globeMarkerColor(palette, region.exits) },
    });
  }

  return {
    // The rotation is the motion here; an entrance animation on top of it would
    // only re-run every refresh.
    animation: false,
    textStyle: { fontFamily: CHART_FONT_SANS, color: palette.tooltipInk },
    tooltip: {
      trigger: "item",
      backgroundColor: palette.tooltipPaper,
      borderColor: palette.tooltipRule,
      borderWidth: 1,
      padding: [8, 10],
      textStyle: { color: palette.tooltipInk, fontFamily: CHART_FONT_SANS, fontSize: 12 },
      extraCssText: "box-shadow:none;border-radius:3px;",
      formatter: (params: unknown) => {
        const value = (params as TooltipParams).value;
        if (!Array.isArray(value)) {
          return "";
        }
        const key = String(value[4] ?? "");
        const exits = Number(value[2] ?? 0);
        const healthy = Number(value[3] ?? 0);
        const names = nameIndex.get(key);
        const title = names ? (isEnglish ? names.en : names.zh) : key;
        return [
          `<div style="display:flex;align-items:baseline;gap:6px">`,
          `<span style="font-weight:600">${escapeHtml(title)}</span>`,
          `<span style="font-family:${CHART_FONT_MONO};font-size:11px;color:${palette.tooltipInkSoft}">${escapeHtml(key)}</span>`,
          `</div>`,
          `<div style="margin-top:4px;display:flex;gap:8px;align-items:baseline;justify-content:space-between">`,
          `<span style="color:${palette.tooltipInkSoft};font-size:12px">${escapeHtml(t("节点"))}</span>`,
          `<span style="font-family:${CHART_FONT_MONO};font-size:13px;font-weight:600">${formatCount(exits)}</span>`,
          `</div>`,
          `<div style="display:flex;gap:8px;align-items:baseline;justify-content:space-between">`,
          `<span style="color:${palette.tooltipInkSoft};font-size:12px">${escapeHtml(t("健康"))}</span>`,
          `<span style="font-family:${CHART_FONT_MONO};font-size:13px;color:${palette.tooltipSignal}">${formatCount(healthy)}</span>`,
          `</div>`,
        ].join("");
      },
    },
    globe: {
      baseTexture: texture,
      // `color` keeps the texture exactly as painted and refuses to be lit: the
      // lambert light follows the system clock, which would leave the board dark
      // at night and bright at noon. The sphere's depth cue is its silhouette,
      // its coastlines and the atmosphere ring, and that is enough.
      shading: "color",
      // No skybox at all. The default `auto` would drape the earth texture around
      // the whole panel, and a colour environment is drawn as an opaque box, so
      // the sphere would sit in a rectangle of its own that the panel cannot see
      // through. With `none` the layer keeps its transparent clear, and the panel
      // background shows around the globe the way it shows around any other chart.
      environment: "none",
      globeRadius: GLOBE_RADIUS,
      // The altitude axis is what lifts a marker off the surface, and it is
      // scaled to `globeOuterRadius - globeRadius`. Collapsing the two keeps the
      // count in the third data slot from floating the busiest region half a
      // sphere into space: the count is carried by the marker's size instead.
      globeOuterRadius: GLOBE_RADIUS,
      atmosphere: {
        show: true,
        color: palette.atmosphere,
        glowPower: 5,
        innerGlowPower: 3,
      },
      viewControl: {
        // No entry in the console ever explains itself by moving; this one does,
        // and a preference for less motion is respected here like everywhere else.
        autoRotate,
        autoRotateSpeed: GLOBE_AUTO_ROTATE_SPEED,
        autoRotateAfterStill: 2,
        distance: GLOBE_DISTANCE,
        minDistance: 150,
        maxDistance: 420,
        // A slight tilt is what makes the sphere read as a sphere rather than as
        // a disc; exact tilt varies by product taste, this one keeps the equator
        // in the middle third of the panel.
        alpha: 14,
        // Roam is bound by zrender to the canvas inside this panel, so a wheel or
        // a drag only ever reaches the globe while the pointer is over it — the
        // page keeps scrolling everywhere else. Panning is off beyond that: the
        // control is for looking at the globe, not for moving it off centre.
        panSensitivity: 0,
      },
    },
    series: [
      {
        type: "scatter3D",
        coordinateSystem: "globe",
        symbol: "circle",
        symbolSize: (value: unknown) => {
          const exits = Array.isArray(value) ? Number(value[2] ?? 0) : 0;
          return markerSize(exits, maxExits);
        },
        label: { show: false },
        emphasis: {
          // The hover label is the region key, which is what the operator is
          // looking for when the tooltip has already answered "how many".
          label: {
            show: true,
            color: palette.tooltipInk,
            backgroundColor: palette.tooltipPaper,
            borderColor: palette.tooltipRule,
            borderWidth: 1,
            fontFamily: CHART_FONT_MONO,
            fontSize: 11,
          },
        },
        data,
      },
    ],
  };
}
