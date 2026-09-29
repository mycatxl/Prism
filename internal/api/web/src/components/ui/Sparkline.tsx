import { cn } from "../../lib/cn";

/**
 * An inline trend for a single series.
 *
 * Hand-drawn as an SVG path rather than pulled from a chart library: it renders on
 * the same baseline as the number it belongs to and adds no runtime weight. A
 * library is used for the screens that need axes, legends and interaction.
 */
export function Sparkline({
  values,
  width = 72,
  height = 20,
  tone = "signal",
  className,
  ariaLabel,
}: {
  values: number[];
  width?: number;
  height?: number;
  tone?: "signal" | "live" | "warn" | "alert" | "muted";
  className?: string;
  ariaLabel?: string;
}) {
  const cleaned = values.filter((v) => Number.isFinite(v));
  if (cleaned.length < 2) {
    return <span className={cn("block", className)} style={{ width, height }} aria-hidden />;
  }

  const min = Math.min(...cleaned);
  const max = Math.max(...cleaned);
  const span = max - min || 1;
  const stepX = width / (cleaned.length - 1);

  const points = cleaned.map((value, index) => {
    const x = index * stepX;
    const y = height - ((value - min) / span) * (height - 2) - 1;
    return [x, y] as const;
  });

  const line = points.map(([x, y], i) => `${i === 0 ? "M" : "L"}${x.toFixed(2)},${y.toFixed(2)}`).join(" ");
  const area = `${line} L${width},${height} L0,${height} Z`;

  const stroke = {
    signal: "var(--color-signal)",
    live: "var(--color-live)",
    warn: "var(--color-warn)",
    alert: "var(--color-alert)",
    muted: "var(--color-ink-faint)",
  }[tone];

  return (
    <svg
      className={cn("block overflow-visible", className)}
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      role={ariaLabel ? "img" : undefined}
      aria-label={ariaLabel}
      aria-hidden={ariaLabel ? undefined : true}
    >
      <path d={area} fill={stroke} opacity={0.08} />
      <path d={line} fill="none" stroke={stroke} strokeWidth={1.25} strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}
