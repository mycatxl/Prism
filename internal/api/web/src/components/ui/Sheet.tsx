import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * A side sheet for detail views. Radix supplies focus trapping, escape handling and
 * the scroll lock; the appearance is a paper panel that slides in from the right.
 */
export function Sheet({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
  width = "md",
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  width?: "sm" | "md" | "lg";
}) {
  const widthClass = { sm: "sm:max-w-md", md: "sm:max-w-2xl", lg: "sm:max-w-4xl" }[width];

  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-ink/25" />
        <DialogPrimitive.Content
          className={cn(
            "fixed inset-y-0 right-0 z-50 flex w-full flex-col border-l border-rule bg-paper-raised",
            "focus:outline-none",
            widthClass,
          )}
        >
          <header className="flex items-start justify-between gap-4 border-b border-rule px-5 py-3">
            <div className="min-w-0">
              <DialogPrimitive.Title className="truncate text-base font-semibold">
                {title}
              </DialogPrimitive.Title>
              {description && (
                <DialogPrimitive.Description className="mt-0.5 text-xs text-ink-soft">
                  {description}
                </DialogPrimitive.Description>
              )}
            </div>
            <DialogPrimitive.Close
              aria-label="关闭"
              className="-mr-1 rounded-control p-1 text-ink-soft transition-colors hover:bg-paper-sunk hover:text-ink"
            >
              <X size={16} />
            </DialogPrimitive.Close>
          </header>

          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>

          {footer && (
            <footer className="border-t border-rule px-5 py-3">{footer}</footer>
          )}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
