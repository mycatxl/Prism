import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * A panel is the console's one framing primitive: a region with a 1px 12% edge and
 * the smallest step of elevation, so it reads as a region rather than as a wash of
 * text. Depth above this step belongs to overlays, never to peers.
 */
export function Panel({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("panel", className)} {...rest} />;
}

/**
 * The panel header: a fixed 44px band, 16px padding, a 14px/600 title and an
 * action slot on the right. The height is a token, so two panels side by side
 * always agree on where their bodies start.
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
        "flex min-h-[var(--panel-header-h)] items-center gap-3 border-b border-rule px-4 py-2",
        className,
      )}
    >
      <div className="flex min-w-0 flex-1 items-baseline gap-2">
        <As className="truncate text-sm font-semibold">{title}</As>
        {meta && <span className="label shrink-0 whitespace-nowrap">{meta}</span>}
      </div>
      {description && (
        <p className="hidden min-w-0 max-w-[46ch] truncate text-xs text-ink-faint xl:block">
          {description}
        </p>
      )}
      {actions && <div className="panel-actions flex shrink-0 items-center gap-1.5">{actions}</div>}
    </div>
  );
}

/**
 * The toolbar strip: filters and view controls for the region below it. 40px tall
 * with 28px controls, so it reads as furniture rather than as content, and it
 * sticks under the page header while the table scrolls.
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
        "flex min-h-[var(--toolbar-h)] flex-wrap items-center gap-2 border-b border-rule-faint bg-paper-inset px-4 py-1.5",
        className,
      )}
    >
      {children}
    </div>
  );
}

/** The body of a panel: the 16px gutter every panel shares. */
export function PanelBody({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("px-4 py-3", className)}>{children}</div>;
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
        "flex min-h-11 flex-wrap items-center gap-3 border-t border-rule px-4 py-2 text-xs text-ink-faint",
        className,
      )}
    >
      {children}
    </div>
  );
}

/**
 * A section heading for content that is not framed by a panel. The rule under it
 * is what makes the grouping visible without spending a box on it.
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
    <div className={cn("flex items-baseline justify-between gap-3 pb-1.5", className)}>
      <h2 className="text-sm font-semibold">{children}</h2>
      {trailing}
    </div>
  );
}
