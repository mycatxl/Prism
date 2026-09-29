import { AlertTriangle, Loader2 } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "../../lib/cn";
import { Button } from "./Button";

/**
 * The three states every data region passes through, in one component so they look
 * the same everywhere: loading, failed, and empty.
 *
 * An empty screen is an invitation to act, so `empty` takes an optional action
 * rather than only a message.
 */
export function LoadingState({ label = "读取中", className }: { label?: string; className?: string }) {
  return (
    <div
      className={cn("flex items-center justify-center gap-2 py-10 text-sm text-ink-soft", className)}
      role="status"
    >
      <Loader2 size={15} className="animate-spin" />
      {label}
    </div>
  );
}

export function ErrorState({
  message,
  onRetry,
  className,
}: {
  message: ReactNode;
  onRetry?: () => void;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-start gap-2 border border-alert/30 bg-alert-wash px-4 py-3 text-sm text-alert",
        className,
      )}
      role="alert"
    >
      <div className="flex items-start gap-2">
        <AlertTriangle size={15} className="mt-0.5 shrink-0" />
        <div className="min-w-0">{message}</div>
      </div>
      {onRetry && (
        <Button size="sm" variant="secondary" onClick={onRetry} className="border-alert/40">
          重试
        </Button>
      )}
    </div>
  );
}

export function EmptyState({
  title,
  hint,
  action,
  className,
}: {
  title: ReactNode;
  hint?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col items-center gap-2 py-12 text-center", className)}>
      <p className="text-sm font-medium text-ink">{title}</p>
      {hint && <p className="max-w-sm text-xs leading-relaxed text-ink-soft">{hint}</p>}
      {action && <div className="mt-1">{action}</div>}
    </div>
  );
}
