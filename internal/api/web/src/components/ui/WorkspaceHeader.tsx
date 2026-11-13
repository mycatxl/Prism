import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

export function WorkspaceHeader({ title, description, meta, actions, tabs, className }: {
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  tabs?: ReactNode;
  className?: string;
}) {
  return (
    <header className={cn("workspace-page-header", className)}>
      <div className="workspace-page-header__inner">
        <div className="workspace-page-header__title-row">
          <div className="workspace-page-header__identity"><h1 className="workspace-page-header__title">{title}</h1>{meta ? <div className="workspace-page-header__meta">{meta}</div> : null}</div>
          {actions ? <div className="workspace-page-header__actions">{actions}</div> : null}
        </div>
        {description ? <p className="workspace-page-header__description">{description}</p> : null}
        {tabs ? <div className="workspace-page-header__tabs">{tabs}</div> : null}
      </div>
    </header>
  );
}

export function WorkspaceMeta({ label, value }: { label: ReactNode; value: ReactNode }) {
  return <span className="workspace-page-meta"><span className="micro">{label}</span><span className="readout text-xs text-ink-soft">{value}</span></span>;
}

export function RouteView({ children, className, bleed = false }: { children: ReactNode; className?: string; bleed?: boolean }) {
  return <section className={cn("workspace-route flex min-h-full flex-col", bleed ? "px-0" : "px-[var(--page-gutter)] py-5 lg:py-7", className)}>{children}</section>;
}
