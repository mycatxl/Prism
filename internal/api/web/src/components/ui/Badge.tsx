import type { HTMLAttributes } from "react";
import { cn } from "../../lib/cn";

export type BadgeTone = "neutral" | "signal" | "live" | "warn" | "alert" | "accent" | "outline";

export type BadgeProps = Omit<HTMLAttributes<HTMLSpanElement>, "color"> & {
  tone?: BadgeTone;
  dot?: boolean;
};

export function Badge({ className, tone = "neutral", dot, children, ...rest }: BadgeProps) {
  return (
    <span {...rest} data-tone={tone} className={cn("workspace-badge", className)}>
      {dot ? <span aria-hidden className="workspace-badge__dot" /> : null}
      {children}
    </span>
  );
}
