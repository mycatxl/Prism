import type { InputHTMLAttributes } from "react";
import { forwardRef } from "react";
import { cn } from "../../lib/cn";

/**
 * Text field: a ruled box on the sheet, `--radius-control` corners and the token
 * height. `invalid` drives both the border and `aria-invalid`, because a
 * validation error that is only visible is not reported to a screen reader.
 */
export const Input = forwardRef<
  HTMLInputElement,
  InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }
>(function Input({ className, invalid, ...rest }, ref) {
  return (
    <input
      ref={ref}
      aria-invalid={invalid || undefined}
      className={cn(
        "action h-[var(--control-h)] w-full rounded-control border bg-paper-raised px-2.5 text-sm text-ink",
        "placeholder:text-ink-faint focus:border-accent disabled:cursor-not-allowed disabled:opacity-50",
        invalid ? "border-alert" : "border-rule hover:border-rule-strong active:border-accent",
        className,
      )}
      {...rest}
    />
  );
});

// One implementation, two import paths.
export { Textarea } from "./Textarea";

export function Fieldset({
  label,
  hint,
  htmlFor,
  children,
  className,
}: {
  label: string;
  hint?: string;
  htmlFor?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("space-y-1.5", className)}>
      <label htmlFor={htmlFor} className="block text-xs font-medium text-ink-soft">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs text-ink-faint">{hint}</p>}
    </div>
  );
}
