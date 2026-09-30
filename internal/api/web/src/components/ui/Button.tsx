import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { LoaderCircle } from "lucide-react";
import type { ButtonHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

/*
 * The seven states `DESIGN.md:130-131` asks of every control, and where each one
 * lives:
 *
 *   default   the variant's own classes
 *   hover     per variant, below
 *   focus     the shared `:focus-visible` outline in `design.css` — one rule for
 *             the whole console, so a component that drew its own would be the
 *             second decision about the same thing
 *   active    `active:` per variant, a step past hover
 *   disabled  `disabled:opacity-45` + no pointer events
 *   loading   the `loading` prop: a spinner *and* `disabled` *and* `aria-busy`,
 *             because a spinner that leaves the button clickable invites a
 *             double submit, and one a screen reader cannot announce is not a
 *             state at all
 *   error     not a Button state. A button does not fail; the form field does
 *             (`Input`/`Select`/`Textarea` carry `invalid` + `aria-invalid`).
 */
const button = cva(
  "action inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-control font-medium select-none disabled:pointer-events-none disabled:opacity-45",
  {
    variants: {
      variant: {
        // The accent owns interaction in every screen, so "clickable" and
        // "healthy" never look the same in a dense table.
        primary: "bg-accent text-on-accent hover:bg-accent-deep active:bg-accent-deep",
        secondary:
          "border border-rule-strong bg-paper-raised text-ink hover:border-ink-faint hover:bg-paper-sunk active:bg-paper-sunk active:border-ink-faint",
        ghost: "text-ink-soft hover:bg-paper-sunk hover:text-ink active:bg-paper-sunk active:text-ink",
        danger: "bg-alert text-on-alert hover:bg-ink active:bg-ink",
        quiet: "border border-transparent text-ink-soft hover:text-ink active:text-ink",
      },
      size: {
        sm: "h-[var(--control-h-sm)] px-2 text-xs",
        md: "h-[var(--control-h)] px-3 text-sm",
        lg: "h-[var(--control-h-lg)] px-4 text-base",
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
     * label is kept: the words are the part a reader is holding on to, and the
     * spinner is the part that says it has not finished. Pages used to swap the
     * label for "保存中..." instead, which moved the button's own text — and its
     * width — out from under the pointer that just clicked it.
     *
     * Not available with `asChild`: Radix's `Slot` merges its props onto exactly
     * one child element, so a spinner rendered alongside that child would be a
     * second one and `Slot` throws. A link that navigates has no in-flight state
     * to announce anyway.
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
