import { useEffect, useRef } from "react";
import { useI18n } from "../../i18n";
import {
  CHART_FONT_MONO,
  CHART_FONT_SANS,
  CHART_INK,
  CHART_INK_FAINT,
  CHART_INK_SOFT,
  CHART_LIVE,
  CHART_PAPER_RAISED,
  CHART_RULE,
  CHART_RULE_STRONG,
  CHART_SIGNAL,
} from "./chartPalette";
import { echarts, type ChartOption, type EChartsInstance } from "./echartsCore";
import { formatBytes, formatCount, formatShortBytes, formatShortCount, formatTimestamp } from "./format";
import { useReducedMotion } from "./useReducedMotion";

type Point = [number, number];

type AxisTooltipParam = {
  seriesId?: string;
  value?: unknown;
};

const SERIES_EGRESS = "egress";
const SERIES_INGRESS = "ingress";
const SERIES_CONNECTIONS = "connections";

/**
 * Throughput and connections over the selected window, on one pair of axes.
 *
 * Two y-axes, because the two quantities are not comparable: bytes per bucket on
 * the left, concurrent connections on the right. The connections series is
 * dashed as well as differently coloured, so the right-axis reading is legible
 * without reading the legend first.
 */
export default function TrafficChart({
  egress,
  ingress,
  connections,
}: {
  egress: Point[];
  ingress: Point[];
  connections: Point[];
}) {
  const { t } = useI18n();
  const reducedMotion = useReducedMotion();
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<EChartsInstance | null>(null);
  const hasDrawnRef = useRef(false);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) {
      return;
    }
    const chart = echarts.init(container, undefined, { renderer: "canvas" });
    chartRef.current = chart;
    hasDrawnRef.current = false;
    const observer = new ResizeObserver(() => chart.resize());
    observer.observe(container);
    return () => {
      observer.disconnect();
      chart.dispose();
      chartRef.current = null;
    };
  }, []);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) {
      return;
    }
    const animate = !reducedMotion && !hasDrawnRef.current;
    hasDrawnRef.current = true;
    chart.setOption(
      buildOption({
        egress,
        ingress,
        connections,
        labels: {
          egress: t("上传流量"),
          ingress: t("下载流量"),
          connections: t("实时连接数"),
        },
        animate,
      }),
      { notMerge: true },
    );
  }, [egress, ingress, connections, t, reducedMotion]);

  return (
    <div className="flex h-full min-h-[220px] flex-col">
      <div
        ref={containerRef}
        className="min-h-0 flex-1"
        role="img"
        aria-label={`${t("流量")} ${t("实时连接数")}`}
      />
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 pt-2 text-xs text-ink-soft">
        <span className="inline-flex items-center gap-1.5">
          <span aria-hidden className="h-0.5 w-3.5 bg-signal" />
          {t("上传流量")}
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span aria-hidden className="h-0.5 w-3.5 bg-live" />
          {t("下载流量")}
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span aria-hidden className="w-3.5 border-t border-dashed border-ink-soft" />
          {t("实时连接数")}
        </span>
      </div>
    </div>
  );
}

function buildOption({
  egress,
  ingress,
  connections,
  labels,
  animate,
}: {
  egress: Point[];
  ingress: Point[];
  connections: Point[];
  labels: { egress: string; ingress: string; connections: string };
  animate: boolean;
}): ChartOption {
  const stamps = [...egress, ...ingress, ...connections].map((point) => point[0]);
  const spanMs = stamps.length ? Math.max(...stamps) - Math.min(...stamps) : 0;
  const showSeconds = spanMs <= 2 * 60 * 60 * 1000;
  const showDate = spanMs > 20 * 60 * 60 * 1000;
  const axisTime = (value: number) => formatTimestamp(value, { date: showDate, seconds: showSeconds });

  return {
    animation: animate,
    animationDuration: 420,
    animationEasing: "cubicOut",
    textStyle: { fontFamily: CHART_FONT_SANS, color: CHART_INK },
    grid: { left: 4, right: 4, top: 12, bottom: 2, containLabel: true },
    tooltip: {
      trigger: "axis",
      // A hairline crosshair instead of ECharts' shaded band: the cursor should
      // point at a timestamp, not paint over a region of the chart.
      axisPointer: { type: "line", lineStyle: { color: CHART_RULE_STRONG, width: 1 } },
      backgroundColor: CHART_PAPER_RAISED,
      borderColor: CHART_RULE,
      borderWidth: 1,
      padding: [8, 10],
      textStyle: { color: CHART_INK, fontFamily: CHART_FONT_SANS, fontSize: 12 },
      extraCssText: "box-shadow:none;border-radius:3px;",
      formatter: (params: unknown) => {
        const items = (Array.isArray(params) ? params : [params]) as AxisTooltipParam[];
        const first = items[0];
        if (!first || !Array.isArray(first.value)) {
          return "";
        }
        const stamp = Number((first.value as unknown[])[0]);
        const rows = items.map((item) => {
          const value = Number((item.value as unknown[])[1] ?? 0);
          const isConnections = item.seriesId === SERIES_CONNECTIONS;
          const label = labels[(item.seriesId ?? SERIES_EGRESS) as keyof typeof labels] ?? "";
          const colour = isConnections ? CHART_INK_SOFT : item.seriesId === SERIES_INGRESS ? CHART_LIVE : CHART_SIGNAL;
          return [
            `<div style="display:flex;gap:8px;align-items:baseline;justify-content:space-between">`,
            `<span style="color:${CHART_INK_SOFT};font-size:12px">${label}</span>`,
            `<span style="font-family:${CHART_FONT_MONO};font-size:13px;font-weight:600;color:${colour}">`,
            `${isConnections ? formatCount(value) : formatBytes(value)}`,
            `</span>`,
            `</div>`,
          ].join("");
        });
        return [
          `<div style="font-family:${CHART_FONT_MONO};font-size:11px;color:${CHART_INK_FAINT}">`,
          formatTimestamp(stamp, { date: showDate, seconds: true }),
          `</div>`,
          `<div style="margin-top:4px">`,
          rows.join(""),
          `</div>`,
        ].join("");
      },
    },
    xAxis: {
      type: "time",
      axisLine: { show: false },
      axisTick: { show: false },
      splitLine: { show: false },
      axisLabel: { color: CHART_INK_SOFT, fontSize: 11, hideOverlap: true, formatter: axisTime },
    },
    yAxis: [
      {
        type: "value",
        min: 0,
        splitLine: { lineStyle: { color: CHART_RULE, width: 1 } },
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: { color: CHART_INK_FAINT, fontSize: 11, formatter: (value: number) => formatShortBytes(value) },
      },
      {
        type: "value",
        min: 0,
        position: "right",
        splitLine: { show: false },
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: { color: CHART_INK_FAINT, fontSize: 11, formatter: (value: number) => formatShortCount(value) },
      },
    ],
    series: [
      {
        id: SERIES_EGRESS,
        name: labels.egress,
        type: "line",
        yAxisIndex: 0,
        showSymbol: false,
        symbol: "none",
        // Downsampling is the chart library's job: a 24-hour window holds far
        // more buckets than a wall display has pixels.
        sampling: "lttb",
        connectNulls: true,
        lineStyle: { width: 1.5, color: CHART_SIGNAL },
        itemStyle: { color: CHART_SIGNAL },
        emphasis: { focus: "series" },
        data: egress,
      },
      {
        id: SERIES_INGRESS,
        name: labels.ingress,
        type: "line",
        yAxisIndex: 0,
        showSymbol: false,
        symbol: "none",
        sampling: "lttb",
        connectNulls: true,
        lineStyle: { width: 1.5, color: CHART_LIVE },
        itemStyle: { color: CHART_LIVE },
        emphasis: { focus: "series" },
        data: ingress,
      },
      {
        id: SERIES_CONNECTIONS,
        name: labels.connections,
        type: "line",
        yAxisIndex: 1,
        showSymbol: false,
        symbol: "none",
        sampling: "lttb",
        connectNulls: true,
        lineStyle: { width: 1, color: CHART_INK_SOFT, type: [4, 3] },
        itemStyle: { color: CHART_INK_SOFT },
        emphasis: { focus: "series" },
        data: connections,
      },
    ],
  };
}