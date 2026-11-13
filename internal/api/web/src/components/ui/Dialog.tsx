import * as RadixDialog from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

export function Dialog({ open, onOpenChange, title, description, children, footer, className }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  className?: string;
}) {
  return (
    <RadixDialog.Root open={open} onOpenChange={onOpenChange}>
      <RadixDialog.Portal>
        <RadixDialog.Overlay className="workspace-drawer-overlay" />
        <RadixDialog.Content className={cn("apple-dialog", className)}>
          <header className="apple-dialog__header">
            <div><RadixDialog.Title>{title}</RadixDialog.Title>{description ? <RadixDialog.Description>{description}</RadixDialog.Description> : null}</div>
            <RadixDialog.Close className="workspace-icon-button" aria-label="Close"><X size={16} aria-hidden /></RadixDialog.Close>
          </header>
          <div className="apple-dialog__body">{children}</div>
          {footer ? <footer className="apple-dialog__footer">{footer}</footer> : null}
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  );
}

export const Modal = Dialog;
