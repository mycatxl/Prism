import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

export type DonutSlice = {
  label: string;
  value: number;
};

/**
 * A share-of-total ring with its legend beside it.
 *
 * The ring is the *summary* and the legend is the data: every slice prints its own
 * label, count and percentage as text, which is why the SVG underneath is
 * `aria-hidden` — a screen reader reads the list, not a circle. On its own a ring
 * states a proportion nobody can measure, which is the failure the design system
 * prohibits; with the legend it is the fastest way to see which of a handful of
 * platforms actually carries the pool.
 *
 * Slice colours come from the six `series-*` tokens by position, so the ring, a
 * table in the neighbouring pane and a chart line can never disagree about which
 * colour means what. More than six slices means the caller should have grouped the
 * tail — the ring keeps cycling rather than inventing a seventh hue.
 */
export function Donut({
  slices,
  centerValue,
  centerLabel,
  className,
}: {
  slices: DonutSlice[];
  centerValue?: ReactNode;
  centerLabel?: ReactNode;
  className?: string;
}) {
  const total = slices.reduce((sum, slice) => sum + Math.max(0, slice.value), 0);
  const radius = 42;
  const circumference = 2 * Math.PI * radius;
  const stroke = 12;

  let drawn = 0;

  return (
    <div className={cn("flex min-w-0 flex-col items-center gap-4 sm:flex-row sm:items-center", className)}>
      <div className="relative shrink-0" style={{ width: 132, height: 132 }}>
        <svg
          aria-hidden
          focusable="false"
          viewBox="0 0 100 100"
          width={132}
          height={132}
          className="block -rotate-90"
        >
          <circle
            cx={50}
            cy={50}
            r={radius}
            fill="none"
            stroke="var(--color-paper-inset)"
            strokeWidth={stroke}
          />
          {total > 0 &&
            slices.map((slice, index) => {
              const share = Math.max(0, slice.value) / total;
              const length = share * circumference;
              // A 2px gap between segments: two adjacent tokens can be close enough in
              // weight that the seam is what tells them apart.
              const gap = slices.length > 1 ? Math.min(2, length / 3) : 0;
              const dash = Math.max(0, length - gap);
              const offset = -drawn;
              drawn += length;
              if (dash <= 0) {
                return null;
              }
              return (
                <circle
                  key={slice.label}
                  cx={50}
                  cy={50}
                  r={radius}
                  fill="none"
                  stroke={`var(--color-series-${(index % 6) + 1})`}
                  strokeWidth={stroke}
                  strokeDasharray={`${dash} ${circumference - dash}`}
                  strokeDashoffset={offset}
                />
              );
            })}
        </svg>
        <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
          <span className="numeral text-2xl">{centerValue ?? total}</span>
          {centerLabel && <span className="micro mt-0.5">{centerLabel}</span>}
        </div>
      </div>

      <ul className="min-w-0 flex-1 space-y-1.5">
        {slices.map((slice, index) => {
          const share = total > 0 ? Math.max(0, slice.value) / total : 0;
          return (
            <li key={slice.label} className="flex min-w-0 items-center gap-2 text-xs">
              <span
                aria-hidden
                className="size-2.5 shrink-0 rounded-[2px]"
                style={{ backgroundColor: `var(--color-series-${(index % 6) + 1})` }}
              />
              <span className="min-w-0 flex-1 truncate text-ink-soft">{slice.label}</span>
              <span className="readout shrink-0 text-ink">{slice.value}</span>
              <span className="readout w-12 shrink-0 text-right text-ink-faint">
                {(share * 100).toFixed(1)}%
              </span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
