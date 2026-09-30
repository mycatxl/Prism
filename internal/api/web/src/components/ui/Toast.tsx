import { X } from "lucide-react";
import { createPortal } from "react-dom";
import type { ToastItem } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";

interface ToastContainerProps {
  toasts: ToastItem[];
  onDismiss: (id: number) => void;
}

const TONE: Record<string, string> = {
  success: "border-signal/40 bg-signal-wash text-signal-deep",
  error: "border-alert/40 bg-alert-wash text-alert",
  warning: "border-warn/40 bg-warn-wash text-warn",
  info: "border-live/40 bg-live-wash text-live",
};

/**
 * Transient confirmations and failures rendered as floating glass slips.
 */
export function ToastContainer({ toasts, onDismiss }: ToastContainerProps) {
  const { t } = useI18n();

  if (toasts.length === 0) return null;

  return createPortal(
    <div
      className="pointer-events-none fixed bottom-4 left-1/2 z-[60] flex w-[min(24rem,calc(100vw-2rem))] -translate-x-1/2 flex-col gap-2"
      aria-live="polite"
    >
      {toasts.map((toast) => (
        <div
          key={toast.id}
          className={cn(
            "pointer-events-auto flex items-start gap-2.5 rounded-panel border px-3.5 py-2.5 text-sm shadow-lg backdrop-blur-md transition-opacity",
            TONE[toast.tone] ?? "border-glass-edge-strong bg-paper-elevated text-ink",
            toast.exiting && "opacity-0",
          )}
        >
          <span className="min-w-0 flex-1 leading-snug">{t(toast.text)}</span>
          <button
            type="button"
            className="action -mr-1 shrink-0 rounded-control p-0.5 opacity-70 hover:opacity-100"
            aria-label={t("关闭")}
            onClick={() => onDismiss(toast.id)}
          >
            <X size={13} />
          </button>
        </div>
      ))}
    </div>,
    document.body,
  );
}
