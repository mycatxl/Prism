import type { SelectHTMLAttributes } from "react";
import { forwardRef } from "react";
import { cn } from "../../lib/cn";

type SelectProps = SelectHTMLAttributes<HTMLSelectElement> & {
  invalid?: boolean;
};

/**
 * Native select.
 *
 * Deliberately native rather than a custom listbox: the browser's own dropdown
 * already handles keyboard, type-ahead, mobile pickers and screen readers, and a
 * rebuilt one would only be worse at all four. Only the closed control is styled,
 * to match `Input`.
 */
export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { className, invalid, children, ...props },
  ref,
) {
  return (
    <select
      ref={ref}
      aria-invalid={invalid || undefined}
      className={cn(
        "h-[var(--control-h)] w-full rounded-control border bg-paper-raised px-2 text-sm text-ink",
        "disabled:cursor-not-allowed disabled:opacity-50",
        // Same hover/active treatment as `Input`; an invalid select keeps the
        // alert edge.
        invalid
          ? "border-alert"
          : "border-rule hover:border-rule-strong active:border-ink-faint",
        className,
      )}
      {...props}
    >
      {children}
    </select>
  );
});