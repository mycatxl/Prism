import { cva, type VariantProps } from "class-variance-authority";
import type { HTMLAttributes } from "react";
import { cn } from "../../lib/cn";

/**
 * A badge is a stamp: square, bordered, set in the mono face. It is not a pill —
 * a seal is struck, and the corner radius is reserved for no other shape.
 *
 * The stamp never carries state on colour alone: the word inside is the state,
 * and the tone only agrees with it.
 */
const badge = cva("pill border", {
  variants: {
    tone: {
      neutral: "border-rule-strong bg-transparent text-ink-soft",
      signal: "border-signal/45 bg-signal-wash text-signal",
      live: "border-live/45 bg-live-wash text-live",
      warn: "border-warn/45 bg-warn-wash text-warn",
      alert: "border-alert/45 bg-alert-wash text-alert",
      accent: "border-accent/45 bg-accent-wash text-accent",
      outline: "border-rule-strong bg-transparent text-ink-soft",
    },
  },
  defaultVariants: { tone: "neutral" },
});

export type BadgeProps = HTMLAttributes<HTMLSpanElement> &
  VariantProps<typeof badge> & { dot?: boolean };

export function Badge({ className, tone, dot, children, ...rest }: BadgeProps) {
  return (
    <span className={cn(badge({ tone }), className)} {...rest}>
      {dot && <span aria-hidden className="size-1.5 shrink-0 bg-current" />}
      {children}
    </span>
  );
}
