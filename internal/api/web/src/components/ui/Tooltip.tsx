import { Tooltip as HeroTooltip } from "@heroui/react";
import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

/*
 * One tooltip for the panel, on HeroUI's overlay.
 *
 * HeroUI's trigger renders its own focusable element around the child. Everything
 * this wraps is already a button with an accessible name of its own, so the
 * wrapper is taken out of the tab order and out of the accessibility tree —
 * otherwise every icon button would answer to Tab twice and announce itself as a
 * generic "button" holding another button. Pointer and keyboard focus on the
 * child still bubble up to the wrapper, which is what opens the tip.
 */
export function Tooltip({
  content,
  children,
  side = "top",
  className,
  triggerClassName,
}: {
  content: ReactNode;
  children: ReactNode;
  side?: "top" | "right" | "bottom" | "left";
  className?: string;
  triggerClassName?: string;
}) {
  if (!content) return <>{children}</>;
  return (
    <HeroTooltip delay={350} closeDelay={0}>
      <HeroTooltip.Trigger role="none" tabIndex={-1} className={cn("inline-flex", triggerClassName)}>
        {children}
      </HeroTooltip.Trigger>
      <HeroTooltip.Content
        placement={side}
        offset={6}
        showArrow
        className={cn("max-w-xs text-xs leading-relaxed", className)}
      >
        {content}
      </HeroTooltip.Content>
    </HeroTooltip>
  );
}
