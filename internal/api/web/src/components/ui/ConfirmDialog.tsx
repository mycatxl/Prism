import type { ReactNode } from "react";
import { useId } from "react";
import { useI18n } from "../../i18n";
import { Button } from "./Button";
import { DialogContent, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from "./Dialog";

/**
 * One confirmation surface for destructive work.
 *
 * Nine pages used to call `window.confirm`, which cannot be translated, cannot
 * show what exactly is about to be deleted, and blocks the whole tab. This one
 * names the object, states the consequence, and keeps focus inside the dialog.
 *
 * The caller owns `open` because the pending row usually lives in page state:
 * closing the dialog must not clear it, or the confirm callback has nothing to
 * act on.
 */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  cancelLabel,
  tone = "danger",
  loading = false,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: "danger" | "primary";
  loading?: boolean;
  onConfirm: () => void;
}) {
  const { t } = useI18n();
  const descriptionId = useId();

  return (
    <DialogRoot open={open} onOpenChange={onOpenChange}>
      <DialogPortal>
        <DialogOverlay />
        <DialogContent
          aria-describedby={description ? descriptionId : undefined}
          className="w-[min(30rem,calc(100vw-2rem))] p-4"
        >
          <DialogTitle>{title}</DialogTitle>
          {description && (
            <div id={descriptionId} className="mt-2 text-sm leading-relaxed text-ink-soft">
              {description}
            </div>
          )}
          <div className="mt-4 flex justify-end gap-2">
            <Button variant="secondary" onClick={() => onOpenChange(false)} disabled={loading}>
              {cancelLabel ?? t("取消")}
            </Button>
            <Button variant={tone} onClick={onConfirm} loading={loading}>
              {confirmLabel ?? t("确认")}
            </Button>
          </div>
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}
