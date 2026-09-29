import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/cn";

/** A region that floats slightly above the sheet: a 1px edge, never a shadow. */
export function Panel({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("panel", className)} {...rest} />;
}

/**
 * A titled region. The header is a hairline-ruled strip rather than a padded box,
 * so a page made of several of these reads as one continuous instrument.
 */
export function PanelHeader({
  title,
  description,
  actions,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex items-start justify-between gap-4 border-b border-rule px-4 py-2.5",
        className,
      )}
    >
      <div className="min-w-0">
        <h2 className="truncate text-sm font-semibold">{title}</h2>
        {description && (
          <p className="mt-0.5 text-xs leading-relaxed text-ink-soft">{description}</p>
        )}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-1.5">{actions}</div>}
    </div>
  );
}

/** Section heading for content that does not need a full panel. */
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
