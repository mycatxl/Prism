import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * The page header: a minimum-height band on the canvas with the page's name, the
 * facts that qualify it, the actions that belong to it, and its tab strip.
 *
 * Its stickiness and its fill come from `.page-header` in `design.css`, not from a
 * utility here: the shell has to be able to move the header below the instance bar
 * on a phone, and a `top-0` utility in this file would win that argument by layer
 * order no matter what the stylesheet said.
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
        "page-header",
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
 * The page shell: a plain column on the canvas. It adds no ground, glow or frame of
 * its own — the shell owns the measure, the page's panels own their rules.
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
        bleed ? "px-0" : "px-[var(--page-gutter)] py-4 lg:py-5",
        className,
      )}
    >
      {children}
    </section>
  );
}
