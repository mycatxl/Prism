import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * A single measured value.
 *
 * The value is mono and tabular because these numbers get compared down a column;
 * the unit sits on the same baseline, quieter, so the digits line up.
 */
export function Readout({
  value,
  unit,
  label,
  hint,
  tone = "ink",
  size = "md",
  className,
}: {
  value: ReactNode;
  unit?: ReactNode;
  label?: ReactNode;
  hint?: ReactNode;
  tone?: "ink" | "signal" | "live" | "warn" | "alert" | "muted";
  size?: "sm" | "md" | "lg" | "xl";
  className?: string;
}) {
  const toneClass = {
    ink: "text-ink",
    signal: "text-signal-deep",
    live: "text-live",
    warn: "text-warn",
    alert: "text-alert",
    muted: "text-ink-faint",
  }[tone];

  const sizeClass = {
    sm: "text-base",
    md: "text-xl",
    lg: "text-3xl",
    xl: "text-4xl",
  }[size];

  return (
    <div className={cn("min-w-0", className)}>
      {label && <div className="label truncate">{label}</div>}
      <div className="flex items-baseline gap-1">
        <span className={cn("readout font-semibold leading-none", sizeClass, toneClass)}>
          {value}
        </span>
        {unit && <span className="readout text-xs text-ink-faint">{unit}</span>}
      </div>
      {hint && <div className="mt-0.5 truncate text-xs text-ink-faint">{hint}</div>}
    </div>
  );
}

/**
 * The instrument strip: several readouts on one baseline, separated by hairlines
 * rather than boxed into cards. Used as the header of the overview screen.
 */
export function ReadoutStrip({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={cn(
        "grid divide-x divide-rule border-y border-rule bg-paper-raised",
        className,
      )}
    >
      {children}
    </div>
  );
}

export function ReadoutCell({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return <div className={cn("px-4 py-3", className)}>{children}</div>;
}
