import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { LoaderCircle } from "lucide-react";
import type { ButtonHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

/*
 * A button is a stamped plate on the sheet: square, hairline, no lift.
 *
 * Control states:
 *   default   the variant's own classes
 *   hover     a step in fill, never a shadow
 *   focus     the shared `:focus-visible` outline in `design.css`
 *   active    one step past hover
 *   disabled  `disabled:opacity-45` + no pointer events
 *   loading   the `loading` prop: a spinner *and* `disabled` *and* `aria-busy`
 */
const button = cva(
  "action inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-control border border-transparent font-medium select-none disabled:pointer-events-none disabled:opacity-45",
  {
    variants: {
      variant: {
        primary: "border-accent-deep bg-accent text-on-accent hover:bg-accent-deep active:bg-accent-deep",
        secondary:
          "border-rule bg-paper-raised text-ink hover:border-rule-strong hover:bg-paper-elevated active:bg-paper-inset",
        ghost:
          "bg-transparent text-ink-soft hover:bg-hover hover:text-ink active:bg-paper-inset active:text-ink",
        danger: "border-alert bg-alert text-on-alert hover:brightness-95 active:brightness-90",
        quiet: "border-transparent text-ink-soft hover:text-ink active:text-ink",
      },
      size: {
        sm: "h-[var(--control-h-sm)] px-2.5 text-xs",
        md: "h-[var(--control-h)] px-3 text-sm",
        lg: "h-[var(--control-h-lg)] px-4 text-base",
        xl: "h-[var(--control-h-xl)] px-4 text-sm",
        icon: "size-[var(--control-h)] p-0",
      },
    },
    defaultVariants: { variant: "secondary", size: "md" },
  },
);

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof button> & {
    /**
     * Work is in flight. The button is disabled and announced as busy, and the
     * label is kept. Not available with `asChild`: Radix's `Slot` merges its props
     * onto exactly one child element.
     */
    loading?: boolean;
  } & ({ asChild: true; loading?: never } | { asChild?: false; loading?: boolean });

export function Button({
  className,
  variant,
  size,
  asChild,
  loading,
  disabled,
  children,
  ...rest
}: ButtonProps) {
  const Comp = asChild ? Slot : "button";
  // `Slot` counts `null` as a child, so a two-child shape cannot be built and then
  // discarded: the content has to be a single value on the `asChild` path.
  const content = loading ? (
    <>
      <LoaderCircle size={14} aria-hidden className="shrink-0 animate-spin" />
      {children}
    </>
  ) : (
    children
  );
  return (
    <Comp
      className={cn(button({ variant, size }), className)}
      disabled={disabled || loading || undefined}
      aria-busy={loading || undefined}
      {...rest}
    >
      {content}
    </Comp>
  );
}
