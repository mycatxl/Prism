import { useId, useMemo } from "react";
import { Area, Bar, CartesianGrid, ComposedChart, Line, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { useReducedMotion } from "../../hooks/useReducedMotion";
import { useI18n } from "../../i18n";
import { getCurrentLocale } from "../../i18n/locale";
import { formatBytes } from "../../lib/bytes";
import { ChartGradient, ChartTooltip, ChartTooltipRow } from "./ChartPrimitives";

type UsagePoint = {
  day_ns: number;
  ingress_bytes: number;
  egress_bytes: number;
  total_bytes: number;
};

const totalColor = "var(--color-chart-teal)";
const ingressColor = "var(--color-chart-cyan)";
const egressColor = "var(--color-chart-violet)";

function UsageTooltip({ active, payload }: { active?: boolean; payload?: Array<{ payload?: UsagePoint }> }) {
  const { t } = useI18n();
  const point = payload?.[0]?.payload;
  if (!active || !point) return null;

  const date = new Intl.DateTimeFormat(getCurrentLocale(), { year: "numeric", month: "short", day: "numeric", timeZone: "UTC" });
  return (
    <ChartTooltip label={date.format(new Date(point.day_ns / 1_000_000))}>
      <ChartTooltipRow color={ingressColor} label={t("入站流量")} value={formatBytes(point.ingress_bytes)} />
      <ChartTooltipRow color={egressColor} label={t("出站流量")} value={formatBytes(point.egress_bytes)} />
      <ChartTooltipRow color={totalColor} label={t("合计流量")} value={formatBytes(point.total_bytes)} />
    </ChartTooltip>
  );
}

/** Exact daily columns with a continuous trend; the adjacent table remains the data alternative. */
export function DailyUsageChart({ items }: { items: UsagePoint[] }) {
  const { t } = useI18n();
  const id = useId().replace(/:/g, "");
  const reducedMotion = useReducedMotion();
  const data = useMemo(() => [...items].sort((a, b) => a.day_ns - b.day_ns), [items]);
  const date = new Intl.DateTimeFormat(getCurrentLocale(), { month: "short", day: "numeric", timeZone: "UTC" });

  return (
    <div className="daily-usage-chart" role="group" aria-label={t("每日流量趋势")}>
      <div className="chart-legend"><span><i className="chart-dot" style={{ background: totalColor }} aria-hidden />{t("合计流量")}</span></div>
      <div className="daily-usage-chart__plot">
        <ResponsiveContainer width="100%" height="100%" minWidth={0}>
          <ComposedChart data={data} margin={{ top: 12, right: 12, bottom: 0, left: 0 }} accessibilityLayer>
            <defs>
              <ChartGradient id={`${id}-area`} color={totalColor} />
              <ChartGradient id={`${id}-bars`} color={totalColor} opacity={0.5} />
            </defs>
            <CartesianGrid stroke="var(--color-chart-grid)" strokeDasharray="3 3" vertical={false} />
            <XAxis dataKey="day_ns" tickFormatter={(value: number) => date.format(new Date(value / 1_000_000))} interval="preserveStartEnd" minTickGap={32} tickMargin={10} height={36} axisLine={false} tickLine={false} tick={{ fill: "var(--color-chart-axis)", fontSize: 12 }} />
            <YAxis width={64} tickCount={4} domain={[0, (max: number) => Math.max(1, max)]} tickFormatter={formatBytes} axisLine={false} tickLine={false} tickMargin={8} tick={{ fill: "var(--color-chart-axis)", fontSize: 12 }} />
            <Tooltip cursor={{ stroke: "var(--color-chart-teal)", strokeOpacity: 0.3, strokeDasharray: "3 3" }} content={<UsageTooltip />} wrapperStyle={{ outline: "none", zIndex: 10 }} isAnimationActive={!reducedMotion} />
            <Area type="monotone" dataKey="total_bytes" fill={`url(#${id}-area)`} stroke="none" tooltipType="none" activeDot={false} isAnimationActive={false} />
            <Bar dataKey="total_bytes" name={t("合计流量")} fill={`url(#${id}-bars)`} radius={[4, 4, 0, 0]} maxBarSize={20} activeBar={{ fill: totalColor, fillOpacity: 0.65 }} isAnimationActive={!reducedMotion} animationDuration={360} animationEasing="ease-out" />
            <Line type="monotone" dataKey="total_bytes" stroke={totalColor} strokeWidth={2.2} dot={data.length === 1 ? { r: 3, fill: totalColor } : false} activeDot={{ r: 4, fill: totalColor, stroke: "var(--color-paper-raised)", strokeWidth: 2 }} tooltipType="none" isAnimationActive={false} />
          </ComposedChart>
        </ResponsiveContainer>
      </div>
    </div>
  );
}
