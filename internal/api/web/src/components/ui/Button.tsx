import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { LoaderCircle } from "lucide-react";
import type { ButtonHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

/*
 * Control states:
 *   default   the variant's own classes
 *   hover     per variant, below
 *   focus     the shared `:focus-visible` outline in `design.css`
 *   active    `active:` per variant, a step past hover
 *   disabled  `disabled:opacity-45` + no pointer events
 *   loading   the `loading` prop: a spinner *and* `disabled` *and* `aria-busy`
 */
const button = cva(
  "action inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-control border border-transparent font-medium select-none disabled:pointer-events-none disabled:opacity-45",
  {
    variants: {
      variant: {
        primary:
          "bg-accent text-on-accent shadow-xs hover:bg-accent-deep active:bg-accent-deep",
        secondary:
          "border-glass-edge bg-glass text-ink shadow-[inset_0_1px_0_0_var(--color-glass-highlight)] hover:border-glass-edge-strong hover:bg-glass-strong active:border-glass-edge-strong active:bg-glass-strong",
        ghost:
          "bg-transparent text-ink-soft hover:border-glass-edge hover:bg-glass hover:text-ink active:bg-glass-strong active:text-ink",
        danger:
          "border-alert/40 bg-alert text-on-alert shadow-xs hover:brightness-95 active:brightness-90",
        quiet:
          "border-transparent text-ink-soft hover:border-glass-edge hover:text-ink active:text-ink",
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
