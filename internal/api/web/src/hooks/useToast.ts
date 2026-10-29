import { useCallback } from "react";
import { appToastQueue } from "../lib/toast-queue";

type ToastTone = "success" | "error" | "warning" | "info";
type ToastVariant = "success" | "danger" | "warning" | "accent";

const TOAST_VARIANT: Record<ToastTone, ToastVariant> = {
  success: "success",
  error: "danger",
  warning: "warning",
  info: "accent",
};

const TOAST_TIMEOUT: Record<ToastTone, number> = {
  success: 3_000,
  error: 5_000,
  warning: 4_000,
  info: 4_000,
};

export function useToast() {
  const showToast = useCallback((tone: ToastTone, text: string) => {
    appToastQueue.add(
      {
        title: text,
        variant: TOAST_VARIANT[tone],
      },
      { timeout: TOAST_TIMEOUT[tone] },
    );
  }, []);

  const dismissToast = useCallback((id: number) => {
    void id;
  }, []);

  const toasts: readonly unknown[] = [];

  return { toasts, showToast, dismissToast };
}
