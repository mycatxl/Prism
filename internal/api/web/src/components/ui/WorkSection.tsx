import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

export function WorkSection({
  title,
  description,
  meta,
  actions,
  children,
  className,
}: {
  title: ReactNode;
  description?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("work-section", className)}>
      <header className="work-section__header">
        <div className="min-w-0">
          <div className="work-section__title-row">
            <h2 className="work-section__title">{title}</h2>
            {meta ? <span className="work-section__meta">{meta}</span> : null}
          </div>
          {description ? <p className="work-section__description">{description}</p> : null}
        </div>
        {actions ? <div className="work-section__actions">{actions}</div> : null}
      </header>
      <div className="work-section__body">{children}</div>
    </section>
  );
}

export function WorkSectionLabel({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("work-section__label", className)}>{children}</div>;
}
