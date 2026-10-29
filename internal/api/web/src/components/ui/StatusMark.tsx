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

export function StatusMark({ className, tone = "neutral", label, age, value, ...rest }: StatusMarkProps) {
  return (
    <div className={cn("workspace-status-line", className)} {...rest}>
      <span aria-hidden className={cn("workspace-status-line__mark", toneClass[tone])} />
      <span className="workspace-status-line__label">{label}</span>
      {age !== undefined ? <span className="workspace-status-line__age">{age}</span> : null}
      {value !== undefined ? <span className="readout workspace-status-line__value">{value}</span> : null}
    </div>
  );
}
