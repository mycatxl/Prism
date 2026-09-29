import type { SelectHTMLAttributes } from "react";
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
export function Select({ className, invalid, children, ...props }: SelectProps) {
  return (
    <select
      aria-invalid={invalid || undefined}
      className={cn(
        "h-8 w-full rounded-control border bg-paper-raised px-2 text-sm text-ink",
        "disabled:cursor-not-allowed disabled:opacity-50",
        invalid ? "border-alert" : "border-rule",
        className,
      )}
      {...props}
    >
      {children}
    </select>
  );
}