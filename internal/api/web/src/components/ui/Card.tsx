import type { HTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/cn";

export function Card({ className, ...props }: HTMLAttributes<HTMLElement>) {
  return <section {...props} className={cn("workspace-card", className)} />;
}

export function CardHeader({ className, ...props }: HTMLAttributes<HTMLElement>) {
  return <header {...props} className={cn("workspace-card__head", className)} />;
}

export function CardTitle({ className, ...props }: HTMLAttributes<HTMLHeadingElement>) {
  return <h2 {...props} className={cn("workspace-card__title", className)} />;
}

export function CardDescription({ className, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return <p {...props} className={cn("workspace-card__description", className)} />;
}

export function CardBody({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div {...props} className={cn("workspace-card__body", className)} />;
}

export function CardFooter({ className, children, ...props }: HTMLAttributes<HTMLElement> & { children?: ReactNode }) {
  return <footer {...props} className={cn("workspace-card__footer", className)}>{children}</footer>;
}
