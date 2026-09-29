import type { InputHTMLAttributes } from "react";
import { forwardRef } from "react";
import { cn } from "../../lib/cn";

/**
 * Text field.
 *
 * `invalid` drives both the border and `aria-invalid`, because a validation error
 * that is only visible is not reported to a screen reader.
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
        "h-8 w-full rounded-control border bg-paper-raised px-2.5 text-sm text-ink",
        "placeholder:text-ink-faint disabled:cursor-not-allowed disabled:opacity-50",
        invalid ? "border-alert" : "border-rule",
        className,
      )}
      {...rest}
    />
  );
});

// One implementation, two import paths: this was duplicated during the migration
// and the two copies had already drifted apart.
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
    <div className={cn("space-y-1", className)}>
      <label htmlFor={htmlFor} className="block text-xs font-medium text-ink-soft">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs text-ink-faint">{hint}</p>}
    </div>
  );
}