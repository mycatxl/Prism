import { cva, type VariantProps } from "class-variance-authority";
import type { HTMLAttributes } from "react";
import { cn } from "../../lib/cn";

/**
 * Status is the only fully rounded shape in the system, so the shape alone says
 * "this is a state" without needing a label to explain it.
 *
 * `dot` adds a small leading marker for states that are live rather than
 * categorical (running, in flight). It is deliberately static: a pulsing dot
 * carries no state a colour and a word do not already carry, and it makes a
 * dense board twitch.
 */
const badge = cva("pill", {
  variants: {
    tone: {
      neutral: "bg-paper-sunk text-ink-soft",
      signal: "bg-signal-wash text-signal-deep",
      live: "bg-live-wash text-live",
      warn: "bg-warn-wash text-warn",
      alert: "bg-alert-wash text-alert",
      outline: "border border-rule text-ink-soft",
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
