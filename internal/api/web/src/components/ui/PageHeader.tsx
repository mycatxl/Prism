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
        "page-header glass-bar sticky top-0 z-20",
        "supports-[backdrop-filter]:bg-glass-strong/88",
        className,
      )}
    >
      <div className="page-header__inner">
        <div className="page-header__title-row">
          <div className="page-header__identity">
            <h1 className="page-header__title shrink-0">{title}</h1>
            {meta && <div className="page-header__meta">{meta}</div>}
          </div>
          {actions && <div className="page-header__actions">{actions}</div>}
        </div>
        {description && <p className="page-header__description">{description}</p>}
        {tabs && <div className="page-header__tabs">{tabs}</div>}
      </div>
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
        "page-shell flex min-h-full flex-col",
        bleed ? "px-0" : "px-[var(--page-gutter)] py-4 lg:py-5",
        className,
      )}
    >
      {children}
    </section>
  );
}
