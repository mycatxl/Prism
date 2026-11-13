export type ToastVariant = "success" | "danger" | "warning" | "accent";

export type AppToast = {
  id: number;
  title: string;
  variant: ToastVariant;
};

let nextId = 1;
let toasts: AppToast[] = [];
const listeners = new Set<() => void>();

function emit() {
  for (const listener of listeners) listener();
}

export function subscribeToasts(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function getToasts() {
  return toasts;
}

export function removeToast(id: number) {
  const next = toasts.filter((toast) => toast.id !== id);
  if (next.length === toasts.length) return;
  toasts = next;
  emit();
}

export const appToastQueue = {
  add(content: { title: string; variant: ToastVariant }, options?: { timeout?: number }) {
    const id = nextId++;
    toasts = [...toasts.slice(-3), { id, ...content }];
    emit();
    window.setTimeout(() => removeToast(id), options?.timeout ?? 4_000);
    return id;
  },
};
