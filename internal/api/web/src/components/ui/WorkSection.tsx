import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

export function WorkSection({ title, description, meta, actions, children, className }: {
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("workspace-section", className)}>
      <header className="workspace-section__header">
        <div className="min-w-0">
          <div className="workspace-section__title-row"><h2 className="workspace-section__title">{title}</h2>{meta ? <span className="workspace-section__meta">{meta}</span> : null}</div>
          {description ? <p className="workspace-section__description">{description}</p> : null}
        </div>
        {actions ? <div className="workspace-section__actions">{actions}</div> : null}
      </header>
      <div className="workspace-section__body">{children}</div>
    </section>
  );
}

export function WorkSectionLabel({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("workspace-section__label", className)}>{children}</div>;
}
