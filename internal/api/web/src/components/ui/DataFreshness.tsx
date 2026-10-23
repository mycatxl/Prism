import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

export function DataFreshness({
  label,
  tone = "neutral",
  className,
}: {
  label: ReactNode;
  tone?: "neutral" | "live" | "warn" | "alert";
  className?: string;
}) {
  return (
    <span className={cn("freshness", `freshness--${tone}`, className)} role="status">
      <span aria-hidden className="freshness__dot" />
      <span>{label}</span>
    </span>
  );
}
