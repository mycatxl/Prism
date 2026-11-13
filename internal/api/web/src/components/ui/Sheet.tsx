import * as Dialog from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import type { ReactNode } from "react";
import { useRef } from "react";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";

export function Sheet({ open, onOpenChange, title, description, children, footer, width = "md" }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  width?: "sm" | "md" | "lg";
}) {
  const { t } = useI18n();
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="workspace-drawer-overlay" />
        <Dialog.Content
          className={cn("workspace-sheet overlay-surface", `workspace-sheet--${width}`)}
          data-placement="center"
          {...(description ? {} : { "aria-describedby": undefined })}
          onOpenAutoFocus={() => {
            const active = document.activeElement;
            restoreFocusRef.current = active instanceof HTMLElement && active !== document.body ? active : null;
          }}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            const target = restoreFocusRef.current;
            restoreFocusRef.current = null;
            if (!target?.isConnected || target.hasAttribute("disabled")) return;
            requestAnimationFrame(() => target.focus({ preventScroll: true }));
          }}
        >
          <header className="workspace-sheet__header">
            <div className="min-w-0">
              <Dialog.Title className="break-words text-base font-semibold text-ink">{title}</Dialog.Title>
              {description ? <Dialog.Description className="mt-1 text-xs text-ink-soft">{description}</Dialog.Description> : null}
            </div>
            <Dialog.Close className="workspace-icon-button" aria-label={t("关闭")}><X size={16} aria-hidden /></Dialog.Close>
          </header>
          <div className="workspace-sheet__body">{children}</div>
          {footer ? <footer className="workspace-sheet__footer">{footer}</footer> : null}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
