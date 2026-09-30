import type { TextareaHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement> & {
  invalid?: boolean;
};

/**
 * Multi-line text field. Matches `Input` so a form reads as one control set.
 *
 * `invalid` drives both the border and `aria-invalid`, because a validation error
 * that is only visible is not reported to a screen reader.
 */
export function Textarea({ className, invalid, ...props }: TextareaProps) {
  return (
    <textarea
      aria-invalid={invalid || undefined}
      className={cn(
        "min-h-20 w-full resize-y rounded-control border bg-paper-raised px-2.5 py-1.5 text-sm text-ink",
        "placeholder:text-ink-faint disabled:cursor-not-allowed disabled:opacity-50",
        // A field's hover is a border step; active is a step further. Invalid keeps
        // the alert edge, which matters more than either.
        invalid
          ? "border-alert"
          : "border-rule hover:border-rule-strong active:border-ink-faint",
        className,
      )}
      {...props}
    />
  );
}