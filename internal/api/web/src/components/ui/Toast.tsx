import { CheckCircle2, CircleAlert, Info, TriangleAlert, X } from "lucide-react";
import { useLayoutEffect, useRef, useSyncExternalStore } from "react";
import { getToasts, removeToast, subscribeToasts, type ToastVariant } from "../../lib/toast-queue";

const iconFor: Record<ToastVariant, typeof CheckCircle2> = {
  success: CheckCircle2,
  danger: CircleAlert,
  warning: TriangleAlert,
  accent: Info,
};

export function ToastViewport() {
  const toasts = useSyncExternalStore(subscribeToasts, getToasts, getToasts);
  const viewportRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    const measure = () => document.documentElement.style.setProperty("--toast-stack-height", `${viewport.offsetHeight}px`);
    const observer = new ResizeObserver(measure);
    observer.observe(viewport);
    measure();
    return () => {
      observer.disconnect();
      document.documentElement.style.removeProperty("--toast-stack-height");
    };
  }, [toasts]);
  return (
    <div ref={viewportRef} className="toast-viewport" aria-live="polite" aria-relevant="additions removals">
      {toasts.map((toast) => {
        const Icon = iconFor[toast.variant];
        return (
          <div key={toast.id} className="toast-item" data-variant={toast.variant} role="status">
            <Icon size={17} aria-hidden />
            <span>{toast.title}</span>
            <button type="button" onClick={() => removeToast(toast.id)} aria-label="Close notification">
              <X size={14} aria-hidden />
            </button>
          </div>
        );
      })}
    </div>
  );
}
