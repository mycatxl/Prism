import type { SelectHTMLAttributes } from "react";
import { forwardRef } from "react";
import { cn } from "../../lib/cn";

type SelectProps = SelectHTMLAttributes<HTMLSelectElement> & {
  invalid?: boolean;
};

/**
 * Native select inside a glass well with token height (`--control-h`).
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
        "action h-[var(--control-h)] w-full rounded-control border bg-glass px-2.5 text-sm text-ink shadow-[inset_0_1px_0_0_var(--color-glass-highlight)]",
        "focus:border-accent focus:bg-glass-strong disabled:cursor-not-allowed disabled:opacity-50",
        invalid
          ? "border-alert"
          : "border-glass-edge hover:border-glass-edge-strong active:border-accent",
        className,
      )}
      {...props}
    >
      {children}
    </select>
  );
});
