import type { ReactNode } from "react";

export function ChartGradient({ id, color, opacity = 0.25 }: { id: string; color: string; opacity?: number }) {
  return (
    <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
      <stop offset="0%" stopColor={color} stopOpacity={opacity} />
      <stop offset="100%" stopColor={color} stopOpacity={0.02} />
    </linearGradient>
  );
}

export function ChartTooltip({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="chart-tooltip">
      <p className="chart-tooltip__label">{label}</p>
      <div className="chart-tooltip__rows">{children}</div>
    </div>
  );
}

export function ChartTooltipRow({ color, label, value }: { color: string; label: ReactNode; value: ReactNode }) {
  return (
    <div className="chart-tooltip__row">
      <span className="chart-tooltip__name"><i className="chart-dot" style={{ background: color }} aria-hidden />{label}</span>
      <strong className="chart-tooltip__value">{value}</strong>
    </div>
  );
}
