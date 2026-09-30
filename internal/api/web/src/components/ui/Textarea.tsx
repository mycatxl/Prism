import type { TextareaHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement> & {
  invalid?: boolean;
};

/**
 * Multi-line text field inside a glass well. Matches `Input` so a form reads as one
 * control set.
 */
export function Textarea({ className, invalid, ...props }: TextareaProps) {
  return (
    <textarea
      aria-invalid={invalid || undefined}
      className={cn(
        "action min-h-20 w-full resize-y rounded-control border bg-glass px-2.5 py-2 text-sm text-ink shadow-[inset_0_1px_0_0_var(--color-glass-highlight)]",
        "placeholder:text-ink-faint focus:border-accent focus:bg-glass-strong disabled:cursor-not-allowed disabled:opacity-50",
        invalid
          ? "border-alert"
          : "border-glass-edge hover:border-glass-edge-strong active:border-accent",
        className,
      )}
      {...props}
    />
  );
}
