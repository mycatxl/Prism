import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

/**
 * A side sheet for detail views. Radix supplies focus trapping, escape handling and
 * the scroll lock; the surface is an elevated glass sheet with generous panel radius.
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
        <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-ink/35 backdrop-blur-xs" />
        <DialogPrimitive.Content
          className={cn(
            "sheet-shell glass-elevated fixed right-0 z-50 flex w-full flex-col focus:outline-none",
            widthClass,
          )}
        >
          <header className="sheet-header flex items-start justify-between gap-4">
            <div className="min-w-0">
              <DialogPrimitive.Title className="truncate text-base font-semibold text-ink">
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
              className="action -mr-1 rounded-control p-1.5 text-ink-soft hover:bg-glass hover:text-ink"
            >
              <X size={16} />
            </DialogPrimitive.Close>
          </header>

          <div className="sheet-body min-h-0 flex-1 overflow-y-auto">{children}</div>

          {footer && (
            <footer className="sheet-footer">{footer}</footer>
          )}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
