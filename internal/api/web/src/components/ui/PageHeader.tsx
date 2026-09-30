import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * The page header: a 52px minimum glass band with title, metadata, actions,
 * optional description, and optional tab strip.
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
        "sticky top-0 z-20 flex min-h-[var(--page-header-h)] flex-col justify-center gap-1 border-b border-glass-edge bg-glass px-[var(--page-gutter)] py-2 backdrop-blur-md",
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
      {tabs && <div className="mt-1.5">{tabs}</div>}
    </header>
  );
}

/**
 * One fact in the page header: a micro-caps label and its value.
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
 * The page shell: transparent over the glowing ground so Bento glass panels float
 * cleanly on the canvas.
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
        "flex min-h-full flex-col",
        bleed ? "px-0" : "px-[var(--page-gutter)] py-3.5 2xl:py-5",
        className,
      )}
    >
      {children}
    </section>
  );
}
