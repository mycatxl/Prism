import type { SelectHTMLAttributes } from "react";
import { forwardRef } from "react";
import { cn } from "../../lib/cn";

type SelectProps = SelectHTMLAttributes<HTMLSelectElement> & {
  invalid?: boolean;
};

/**
 * Native select inside a ruled box with token height (`--control-h`).
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
        "action h-[var(--control-h)] w-full rounded-control border bg-paper-raised px-2.5 text-sm text-ink",
        "focus:border-accent disabled:cursor-not-allowed disabled:opacity-50",
        invalid ? "border-alert" : "border-rule hover:border-rule-strong active:border-accent",
        className,
      )}
      {...props}
    >
      {children}
    </select>
  );
});
