import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * A row of facts about the region below it: label, figure, one line of context.
 *
 * This is not the hero-metric template. The figures sit at the same weight as
 * the rest of the console's data, in a single band with hairline separators, so a
 * page states its condition without turning four numbers into a poster.
 */
export function StatStrip({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <dl
      className={cn(
        "grid grid-cols-2 gap-px border-y border-rule bg-rule sm:grid-cols-3 lg:grid-cols-4",
        className,
      )}
    >
      {children}
    </dl>
  );
}

export function Stat({
  label,
  value,
  hint,
  tone = "default",
}: {
  label: ReactNode;
  value: ReactNode;
  hint?: ReactNode;
  tone?: "default" | "signal" | "warn" | "alert" | "live";
}) {
  const toneClass =
    tone === "signal"
      ? "text-signal"
      : tone === "warn"
        ? "text-warn"
        : tone === "alert"
          ? "text-alert"
          : tone === "live"
            ? "text-live"
            : "text-ink";

  return (
    <div className="flex min-w-0 flex-col gap-1 bg-paper px-3 py-3">
      <dt className="truncate text-2xs font-medium tracking-[0.04em] text-ink-faint uppercase">{label}</dt>
      <dd className={cn("readout text-base leading-tight font-semibold tabular-nums", toneClass)}>{value}</dd>
      {hint && <p className="truncate text-2xs text-ink-faint">{hint}</p>}
    </div>
  );
}
