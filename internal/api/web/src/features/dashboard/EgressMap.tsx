import { useEffect, useMemo, useRef, useState } from "react";
import { ErrorState, LoadingState } from "../../components/ui/QueryState";
import { useI18n } from "../../i18n";
import {
  CHART_FONT_MONO,
  CHART_FONT_SANS,
  CHART_INK,
  CHART_INK_FAINT,
  CHART_INK_SOFT,
  CHART_PAPER,
  CHART_PAPER_RAISED,
  CHART_RULE,
  CHART_SIGNAL_DEEP,
  EXIT_COUNT_BANDS,
} from "./chartPalette";
import { echarts, type ChartOption, type EChartsInstance } from "./echartsCore";
import { formatCount } from "./format";
import type { RegionExitCount } from "./types";
import { useReducedMotion } from "./useReducedMotion";
import { buildRegionNameIndex, loadWorldGeoJson, type WorldGeoJson } from "./worldMap";

const MAP_NAME = "prism-world";

type MapSource = Parameters<typeof echarts.registerMap>[1];

type MapTooltipParam = { name?: string };
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

/**
 * The exit map: how many nodes leave through each country.
 *
 * A choropleth rather than a scatter or a bubble map, because the question the
 * screen answers is "which countries, and how many" — and a filled country is
 * the only encoding that stays legible at wall-display distance. Countries with
 * no exits keep the paper colour, so the coloured area *is* the footprint: a
 * faint outline is the honest way to say "nothing here".
 */
export default function EgressMap({ regions }: { regions: RegionExitCount[] }) {
  const { t, isEnglish } = useI18n();
  const reducedMotion = useReducedMotion();
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<EChartsInstance | null>(null);
  const hasDrawnRef = useRef(false);
  const [geo, setGeo] = useState<WorldGeoJson | null>(null);
  const [failed, setFailed] = useState<unknown>(null);
  const [attempt, setAttempt] = useState(0);

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

  useEffect(() => {
    const container = containerRef.current;
    if (!container || !geo) {
      return;
    }
    echarts.registerMap(MAP_NAME, geo as unknown as MapSource);
    const chart = echarts.init(container, undefined, { renderer: "canvas" });
    chartRef.current = chart;
    hasDrawnRef.current = false;
    // The screen is read at several widths (1280 and 1920 are the two it is
    // built for), so the map follows its container instead of a fixed size.
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
    if (!chart || !geo || !nameIndex) {
      return;
    }
    // One entrance, on the first paint only: a refresh that re-ran the whole
    // animation every minute would be decoration, not state.
    const animate = !reducedMotion && !hasDrawnRef.current;
    hasDrawnRef.current = true;
    chart.setOption(buildOption({ regions, nameIndex, isEnglish, t, animate }), { notMerge: true });
  }, [regions, geo, nameIndex, isEnglish, reducedMotion, t]);

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

  return (
    <div ref={containerRef} className="h-full w-full" role="img" aria-label={t("出口 / 区域")} />
  );
}

function buildOption({
  regions,
  nameIndex,
  isEnglish,
  t,
  animate,
}: {
  regions: RegionExitCount[];
  nameIndex: Map<string, { en: string; zh: string }>;
  isEnglish: boolean;
  t: (text: string, options?: Record<string, unknown>) => string;
  animate: boolean;
}): ChartOption {
  const byRegion = new Map(regions.map((item) => [item.region, item]));

  return {
    animation: animate,
    animationDuration: 420,
    animationEasing: "cubicOut",
    textStyle: { fontFamily: CHART_FONT_SANS, color: CHART_INK },
    tooltip: {
      trigger: "item",
      backgroundColor: CHART_PAPER_RAISED,
      borderColor: CHART_RULE,
      borderWidth: 1,
      padding: [8, 10],
      textStyle: { color: CHART_INK, fontFamily: CHART_FONT_SANS, fontSize: 12 },
      extraCssText: "box-shadow:none;border-radius:3px;",
      formatter: (params: unknown) => {
        const name = (params as MapTooltipParam).name;
        if (!name) {
          return "";
        }
        const names = nameIndex.get(name);
        const title = names ? (isEnglish ? names.en : names.zh) : name;
        // The series carries the whole node pool, so a country with no entry has
        // no exits — that is a measured zero, not unknown data.
        const exits = byRegion.get(name)?.exits ?? 0;
        const healthy = byRegion.get(name)?.healthy ?? 0;
        return [
          `<div style="display:flex;align-items:baseline;gap:6px">`,
          `<span style="font-weight:600">${escapeHtml(title)}</span>`,
          `<span style="font-family:${CHART_FONT_MONO};font-size:11px;color:${CHART_INK_FAINT}">${escapeHtml(name)}</span>`,
          `</div>`,
          `<div style="margin-top:4px;display:flex;gap:8px;align-items:baseline;justify-content:space-between">`,
          `<span style="color:${CHART_INK_SOFT};font-size:12px">${escapeHtml(t("节点"))}</span>`,
          `<span style="font-family:${CHART_FONT_MONO};font-size:13px;font-weight:600">${formatCount(exits)}</span>`,
          `</div>`,
          `<div style="display:flex;gap:8px;align-items:baseline;justify-content:space-between">`,
          `<span style="color:${CHART_INK_SOFT};font-size:12px">${escapeHtml(t("健康"))}</span>`,
          `<span style="font-family:${CHART_FONT_MONO};font-size:13px;color:${CHART_SIGNAL_DEEP}">${formatCount(healthy)}</span>`,
          `</div>`,
        ].join("");
      },
    },
    visualMap: {
      type: "piecewise",
      show: false,
      pieces: EXIT_COUNT_BANDS.map((band) => ({
        min: band.min,
        ...(Number.isFinite(band.max) ? { max: band.max } : {}),
        color: band.color,
      })),
      outOfRange: { color: CHART_PAPER },
    },
    series: [
      {
        type: "map",
        map: MAP_NAME,
        roam: false,
        selectedMode: false,
        top: 4,
        bottom: 4,
        left: 4,
        right: 4,
        label: { show: false },
        itemStyle: { areaColor: CHART_PAPER, borderColor: CHART_RULE, borderWidth: 0.6 },
        emphasis: {
          label: {
            show: true,
            color: CHART_INK,
            fontFamily: CHART_FONT_MONO,
            fontSize: 11,
            fontWeight: "bold",
          },
          itemStyle: { areaColor: CHART_SIGNAL_DEEP, borderColor: CHART_INK, borderWidth: 0.8 },
        },
        data: regions.map((item) => ({ name: item.region, value: item.exits })),
      },
    ],
  };
}