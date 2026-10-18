import type { TextareaHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement> & {
  invalid?: boolean;
};

/**
 * Multi-line text field on an inset well. Matches `Input` so a form reads as one
 * control set.
 */
export function Textarea({ className, invalid, ...props }: TextareaProps) {
  return (
    <textarea
      aria-invalid={invalid || undefined}
      className={cn(
        "action min-h-20 w-full resize-y rounded-control border bg-paper-inset px-2.5 py-2 text-sm text-ink",
        "placeholder:text-ink-faint focus:border-accent focus:bg-paper-raised disabled:cursor-not-allowed disabled:opacity-50",
        invalid
          ? "border-alert"
          : "border-rule hover:border-rule-strong active:border-accent",
        className,
      )}
      {...props}
    />
  );
}
