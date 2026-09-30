import { cva, type VariantProps } from "class-variance-authority";
import type { HTMLAttributes } from "react";
import { cn } from "../../lib/cn";

/**
 * Status is the only fully rounded shape in the system (`--radius-chip` / `rounded-full`
 * on the static dot), so the shape alone carries "this is a state".
 *
 * `dot` adds a static leading marker for live states (never animated).
 */
const badge = cva("pill border", {
  variants: {
    tone: {
      neutral: "border-glass-edge bg-glass text-ink-soft",
      signal: "border-signal/25 bg-signal-wash text-signal",
      live: "border-live/25 bg-live-wash text-live",
      warn: "border-warn/25 bg-warn-wash text-warn",
      alert: "border-alert/25 bg-alert-wash text-alert",
      accent: "border-accent/25 bg-accent-wash text-accent",
      outline: "border-glass-edge-strong bg-transparent text-ink-soft",
    },
  },
  defaultVariants: { tone: "neutral" },
});

export type BadgeProps = HTMLAttributes<HTMLSpanElement> &
  VariantProps<typeof badge> & { dot?: boolean };

export function Badge({ className, tone, dot, children, ...rest }: BadgeProps) {
  return (
    <span className={cn(badge({ tone }), className)} {...rest}>
      {dot && <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-current" />}
      {children}
    </span>
  );
}
