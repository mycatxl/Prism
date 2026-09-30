import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * The page header: one ruled band that answers where am I, what is this, and what
 * can I do here.
 *
 * 52px is the band's floor, not its height, so this is `min-h` and not `h`: the
 * description line and the tab strip both belong in this band, and both push it
 * past 52. Replacing the minimum with `h-[var(--page-header-h)]` clips exactly
 * those two things.
 *
 * The earlier panel had none of this: an `<h1>` with no band stretched across
 * 2288px of a 2560px screen, a description under it, and the actions floating to
 * the right, so the top of every page was a different shape. One band with a rule
 * under it is what puts every page on the same grid.
 *
 * `meta` is a row of facts that belong to the whole page — a count, a sync time, a
 * scope chip. It sits on the baseline of the title rather than in a card of its
 * own, because a page header made of tiles is a dashboard nobody asked for.
 */
export function PageHeader({
  title,
  description,
  meta,
  actions,
  tabs,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  tabs?: ReactNode;
  className?: string;
}) {
  return (
    <header
      className={cn(
        "sticky top-0 z-20 flex min-h-[var(--page-header-h)] flex-col justify-center gap-0.5 border-b border-rule bg-paper px-[var(--page-gutter)] py-1.5",
        className,
      )}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
        <h1 className="shrink-0">{title}</h1>
        {meta && (
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">{meta}</div>
        )}
        {actions && <div className="ml-auto flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
      {description && (
        <p className="mt-0.5 line-clamp-1 max-w-[68ch] text-xs text-ink-faint">{description}</p>
      )}
      {tabs && <div className="mt-1">{tabs}</div>}
    </header>
  );
}

/**
 * One fact in the page header: a micro-caps label and its value. Deliberately not
 * a tile — no box, no big numeral, no accent stripe.
 */
export function PageMeta({ label, value }: { label: ReactNode; value: ReactNode }) {
  return (
    <span className="inline-flex items-baseline gap-1.5 whitespace-nowrap">
      <span className="micro">{label}</span>
      <span className="readout text-xs text-ink-soft">{value}</span>
    </span>
  );
}

/**
 * The page shell. Every page uses it, so padding and the header band are one
 * decision instead of twelve: 16px on a laptop, 24px on a wall display.
 *
 * There is deliberately no maximum width. A 2560px operator screen showing a
 * centred 1280px column wastes half the hardware.
 */
export function Page({
  children,
  className,
  bleed = false,
}: {
  children: ReactNode;
  className?: string;
  bleed?: boolean;
}) {
  return (
    <section
      className={cn(
        "flex min-h-full flex-col bg-paper",
        bleed ? "px-0" : "px-[var(--page-gutter)] py-3 2xl:py-4",
        className,
      )}
    >
      {children}
    </section>
  );
}
