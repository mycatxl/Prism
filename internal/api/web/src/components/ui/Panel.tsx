import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * A panel is the console's glass framing primitive: a region with radius
 * `--radius-panel` (20px), a 1px glass edge, the inner top rim-light, backdrop blur,
 * and soft shadow elevation.
 */
export function Panel({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("panel isolate overflow-hidden", className)} {...rest} />;
}

/**
 * The panel header: a quiet 44px token band, 16px padding, a 14px/600 title and an
 * action slot on the right. Soft divider underneath rather than a heavy rule.
 *
 * `meta` is for the one or two facts that belong to the whole region (a count, a
 * timestamp) and sits beside the title rather than under it.
 */
export function PanelHeader({
  title,
  description,
  meta,
  actions,
  className,
  as: As = "h2",
}: {
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  className?: string;
  as?: "h2" | "h3";
}) {
  return (
    <div
      className={cn(
        "panel-header flex min-h-[var(--panel-header-h)] items-start gap-4",
        className,
      )}
    >
      <div className="panel-header__main min-w-0 flex-1">
        <div className="panel-header__title-row flex min-w-0 items-baseline gap-2">
          <As className="panel-header__title min-w-0 truncate text-sm font-semibold tracking-tight text-ink">{title}</As>
          {meta && <span className="panel-header__meta label shrink-0 whitespace-nowrap">{meta}</span>}
        </div>
        {description && <p className="panel-header__description mt-1 max-w-[58ch] text-xs leading-relaxed text-ink-faint">{description}</p>}
      </div>
      {actions && <div className="panel-actions flex shrink-0 items-center gap-1.5">{actions}</div>}
    </div>
  );
}

/**
 * The toolbar strip: filters and view controls for the region below it. 40px tall
 * with 28px controls, framed by soft hairline dividers.
 */
export function PanelToolbar({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "panel-toolbar flex min-h-[var(--toolbar-h)] flex-wrap items-center gap-2",
        className,
      )}
    >
      {children}
    </div>
  );
}

/** The body of a panel: the 16px gutter every panel shares. */
export function PanelBody({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("panel-body", className)}>{children}</div>;
}

/** A panel footer for totals, pagination and bulk actions. */
export function PanelFooter({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "panel-footer flex min-h-11 flex-wrap items-center gap-3 text-xs",
        className,
      )}
    >
      {children}
    </div>
  );
}

/**
 * A section heading for content that is not framed by a panel.
 */
export function SectionTitle({
  children,
  className,
  trailing,
}: {
  children: ReactNode;
  className?: string;
  trailing?: ReactNode;
}) {
  return (
    <div className={cn("flex items-baseline justify-between gap-3 pb-2", className)}>
      <h2 className="text-sm font-semibold tracking-tight text-ink">{children}</h2>
      {trailing}
    </div>
  );
}
