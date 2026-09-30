import { AlertTriangle } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "../../lib/cn";
import { Button } from "./Button";

/**
 * The three states every data region passes through, in one component so they look
 * the same everywhere: loading, failed, and empty.
 *
 * Loading uses a static, shimmer-free glass skeleton stack so it never loops a
 * decorative animation and naturally respects reduced motion.
 */
export function LoadingState({ label = "读取中", className }: { label?: string; className?: string }) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-3 py-10 text-sm text-ink-soft",
        className,
      )}
      role="status"
    >
      <div aria-hidden className="flex w-full max-w-xs flex-col gap-2 px-4">
        <div className="h-2.5 w-3/4 rounded-control border border-rule-faint bg-paper-inset" />
        <div className="h-2.5 w-1/2 rounded-control border border-rule-faint bg-paper-inset/75" />
      </div>
      <span>{label}</span>
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
        "flex flex-col items-start gap-2.5 rounded-card-sm border border-alert/35 bg-alert-wash px-4 py-3 text-sm text-alert",
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
      <p className="text-sm font-semibold text-ink">{title}</p>
      {hint && <p className="max-w-sm text-xs leading-relaxed text-ink-soft">{hint}</p>}
      {action && <div className="mt-1.5">{action}</div>}
    </div>
  );
}
