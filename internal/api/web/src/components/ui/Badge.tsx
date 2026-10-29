import { Chip as HeroChip } from "@heroui/react";
import type { HTMLAttributes } from "react";
import { cn } from "../../lib/cn";

export type BadgeTone = "neutral" | "signal" | "live" | "warn" | "alert" | "accent" | "outline";

export type BadgeProps = Omit<HTMLAttributes<HTMLSpanElement>, "color"> & {
  tone?: BadgeTone;
  dot?: boolean;
};

const colorMap: Record<BadgeTone, "default" | "success" | "warning" | "danger" | "accent"> = {
  neutral: "default",
  signal: "success",
  live: "success",
  warn: "warning",
  alert: "danger",
  accent: "accent",
  outline: "default",
};

export function Badge({ className, tone = "neutral", dot, children, ...rest }: BadgeProps) {
  return (
    <HeroChip
      {...rest}
      size="sm"
      color={colorMap[tone]}
      variant={tone === "neutral" || tone === "outline" ? "secondary" : "soft"}
      className={cn("workspace-badge", className)}
    >
      {dot ? <span aria-hidden className="workspace-badge__dot" /> : null}
      {children}
    </HeroChip>
  );
}
