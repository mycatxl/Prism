import { Drawer as HeroDrawer, useOverlayState } from "@heroui/react";
import { X } from "lucide-react";
import type { ReactNode } from "react";
import { useCallback, useEffect, useRef } from "react";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";

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
  const { t } = useI18n();
  const state = useOverlayState({ isOpen: open, onOpenChange });
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const widthClass = {
    sm: "sm:max-w-md",
    md: "sm:max-w-2xl",
    lg: "sm:max-w-4xl",
  }[width];

  const handleOpenAutoFocus = useCallback(() => {
    const active = document.activeElement;
    if (active instanceof HTMLElement && active !== document.body) {
      restoreFocusRef.current = active;
    }
  }, []);

  const handleCloseAutoFocus = useCallback(() => {
    const target = restoreFocusRef.current;
    restoreFocusRef.current = null;
    if (!target?.isConnected || target.hasAttribute("disabled")) return;
    target.focus({ preventScroll: true });
  }, []);

  useEffect(() => {
    if (open) handleOpenAutoFocus();
    else handleCloseAutoFocus();
  }, [handleCloseAutoFocus, handleOpenAutoFocus, open]);

  return (
    <HeroDrawer state={state}>
      <HeroDrawer.Backdrop variant="blur">
        <HeroDrawer.Content placement="right" className={cn("workspace-sheet overlay-surface", widthClass)}>
          <HeroDrawer.Dialog>
            <HeroDrawer.Header className="workspace-sheet__header">
              <div className="min-w-0">
                <HeroDrawer.Heading className="break-words text-base font-semibold text-ink">
                  {title}
                </HeroDrawer.Heading>
                {description ? <p className="mt-1 text-xs text-ink-soft">{description}</p> : null}
              </div>
              <HeroDrawer.CloseTrigger className="workspace-icon-button" aria-label={t("关闭")}>
                <X size={16} aria-hidden />
              </HeroDrawer.CloseTrigger>
            </HeroDrawer.Header>
            <HeroDrawer.Body className="workspace-sheet__body">{children}</HeroDrawer.Body>
            {footer ? <HeroDrawer.Footer className="workspace-sheet__footer">{footer}</HeroDrawer.Footer> : null}
          </HeroDrawer.Dialog>
        </HeroDrawer.Content>
      </HeroDrawer.Backdrop>
    </HeroDrawer>
  );
}
