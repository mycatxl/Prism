import * as Dialog from "@radix-ui/react-dialog";
import { useRef, type ReactNode } from "react";

type DialogSurfaceProps = {
  children: ReactNode;
  title: string;
  onClose: () => void;
  variant?: "modal" | "drawer" | "command" | "navigation";
};

// Shared Radix focus boundary for inherited editors and the new workbench.
export function DialogSurface({
  children,
  title,
  onClose,
  variant = "modal",
}: DialogSurfaceProps) {
  const returnFocus = useRef(document.activeElement as HTMLElement | null);
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-backdrop" />
        <Dialog.Content
          className={`dialog-surface dialog-${variant}`}
          aria-describedby={undefined}
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            if (returnFocus.current?.isConnected) returnFocus.current.focus();
          }}
        >
          <Dialog.Title className="sr-only">{title}</Dialog.Title>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
