import { Card as HeroCard, Surface as HeroSurface } from "@heroui/react";
import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/cn";

export function SurfaceCard({ className, children, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return (
    <HeroCard className={cn("workspace-surface-card isolate overflow-hidden", className)} variant="default" {...rest}>
      {children}
    </HeroCard>
  );
}

export function SurfaceHeader({
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
    <HeroCard.Header className={cn("workspace-surface-card__header", className)}>
      <div className="workspace-surface-card__header-main">
        <div className="workspace-surface-card__header-title-row">
          <As className="workspace-surface-card__header-title">{title}</As>
          {meta ? <span className="workspace-surface-card__header-meta">{meta}</span> : null}
        </div>
        {description ? <p className="workspace-surface-card__header-description">{description}</p> : null}
      </div>
      {actions ? <div className="workspace-surface-card__header-actions">{actions}</div> : null}
    </HeroCard.Header>
  );
}

export function SurfaceToolbar({ children, className }: { children: ReactNode; className?: string }) {
  return <HeroSurface className={cn("workspace-surface-card__toolbar", className)} variant="secondary">{children}</HeroSurface>;
}

export function SurfaceBody({ children, className }: { children: ReactNode; className?: string }) {
  return <HeroCard.Content className={cn("workspace-surface-card__body", className)}>{children}</HeroCard.Content>;
}

export function SurfaceFooter({ children, className }: { children: ReactNode; className?: string }) {
  return <HeroCard.Footer className={cn("workspace-surface-card__footer", className)}>{children}</HeroCard.Footer>;
}

export function SectionHeading({ children, className, trailing }: { children: ReactNode; className?: string; trailing?: ReactNode }) {
  return (
    <div className={cn("workspace-section-title", className)}>
      <h2>{children}</h2>
      {trailing}
    </div>
  );
}
