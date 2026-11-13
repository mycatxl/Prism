import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/cn";

export function SurfaceCard({ className, children, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <section className={cn("workspace-surface-card isolate overflow-hidden", className)} {...rest}>{children}</section>;
}

export function SurfaceHeader({ title, description, meta, actions, className, as: As = "h2" }: {
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  className?: string;
  as?: "h2" | "h3";
}) {
  return (
    <header className={cn("workspace-surface-card__header", className)}>
      <div className="workspace-surface-card__header-main">
        <div className="workspace-surface-card__header-title-row"><As className="workspace-surface-card__header-title">{title}</As>{meta ? <span className="workspace-surface-card__header-meta">{meta}</span> : null}</div>
        {description ? <p className="workspace-surface-card__header-description">{description}</p> : null}
      </div>
      {actions ? <div className="workspace-surface-card__header-actions">{actions}</div> : null}
    </header>
  );
}

export function SurfaceToolbar({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("workspace-surface-card__toolbar", className)}>{children}</div>;
}

export function SurfaceBody({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("workspace-surface-card__body", className)}>{children}</div>;
}

export function SurfaceFooter({ children, className }: { children: ReactNode; className?: string }) {
  return <footer className={cn("workspace-surface-card__footer", className)}>{children}</footer>;
}

export function SectionHeading({ children, className, trailing }: { children: ReactNode; className?: string; trailing?: ReactNode }) {
  return <div className={cn("workspace-section-title", className)}><h2>{children}</h2>{trailing}</div>;
}
