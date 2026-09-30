import { useMemo } from "react";
import { cn } from "../../lib/cn";

/**
 * A series as a hairline, for the one place a figure needs its own history beside
 * it: a KPI card.
 *
 * It is deliberately **not** a chart. A chart has axes, a scale and a tooltip; this
 * has none of them, so it may not be the only place a value is stated — the rule is
 * that a sparkline accompanies a number, never replaces it. It is `aria-hidden` for
 * the same reason: a screen reader gets the value, the delta and its basis from the
 * text beside it.
 *
 * Rendered as SVG in the `currentColor` of the caller's tone, so the shape follows
 * the same state colours as everything else, and `preserveAspectRatio="none"` lets
 * it fill whatever width the card gives it without a layout pass.
 */
export function Sparkline({
  values,
  tone = "accent",
  height = 34,
  className,
}: {
  values: number[];
  tone?: "accent" | "signal" | "live" | "warn" | "alert" | "muted";
  height?: number;
  className?: string;
}) {
  const toneClass = {
    accent: "text-accent",
    signal: "text-signal",
    live: "text-live",
    warn: "text-warn",
    alert: "text-alert",
    muted: "text-ink-faint",
  }[tone];

  const shape = useMemo(() => {
    const points = values.filter((value) => Number.isFinite(value));
    if (points.length < 2) {
      return null;
    }
    const highest = Math.max(...points);
    const lowest = Math.min(...points);
    // A flat series is drawn down the middle: pinning it to the floor would read as
    // "nothing happened" rather than as "nothing changed".
    const span = highest - lowest;
    const inked = (value: number) =>
      span === 0 ? height / 2 : height - 2 - ((value - lowest) / span) * (height - 4);
    const step = 100 / (points.length - 1);
    const line = points.map((value, index) => `${(index * step).toFixed(2)},${inked(value).toFixed(2)}`);
    return {
      line: line.join(" "),
      area: `M0,${height} L${line.join(" L")} L100,${height} Z`,
    };
  }, [values, height]);

  // Fewer than two readings is not a trend, and a drawn line would be a claim about
  // one. The block keeps the card's height so the grid does not jump when data lands.
  if (!shape) {
    return <span aria-hidden className={cn("block", toneClass, className)} style={{ height }} />;
  }

  return (
    <svg
      aria-hidden
      focusable="false"
      viewBox={`0 0 100 ${height}`}
      preserveAspectRatio="none"
      className={cn("block w-full", toneClass, className)}
      style={{ height }}
    >
      <path d={shape.area} fill="currentColor" opacity={0.12} />
      <polyline
        points={shape.line}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}
