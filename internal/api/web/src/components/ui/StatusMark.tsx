import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/cn";

export type StatusMarkTone = "neutral" | "signal" | "live" | "warn" | "alert" | "accent";

const toneClass: Record<StatusMarkTone, string> = {
  neutral: "text-ink-faint",
  signal: "text-signal",
  live: "text-live",
  warn: "text-warn",
  alert: "text-alert",
  accent: "text-accent",
};

export type StatusMarkProps = HTMLAttributes<HTMLDivElement> & {
  tone?: StatusMarkTone;
  label: ReactNode;
  age?: ReactNode;
  value?: ReactNode;
};

/** A compact runtime state marker without the pill semantics of Badge. */
export function StatusMark({
  className,
  tone = "neutral",
  label,
  age,
  value,
  ...rest
}: StatusMarkProps) {
  return (
    <div className={cn("flex min-w-0 items-center gap-2", className)} {...rest}>
      <span aria-hidden className={cn("size-2 shrink-0 bg-current", toneClass[tone])} />
      <span className="min-w-0 truncate text-sm text-ink">{label}</span>
      {age !== undefined && <span className="shrink-0 text-xs text-ink-faint">{age}</span>}
      {value !== undefined && <span className="readout ml-auto shrink-0">{value}</span>}
    </div>
  );
}
