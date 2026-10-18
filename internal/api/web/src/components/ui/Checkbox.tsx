import type { InputHTMLAttributes } from "react";
import { forwardRef } from "react";
import { cn } from "../../lib/cn";

/**
 * Checkbox: a native control struck square, in the accent token. The browser
 * keeps its own keyboard and touch behaviour; only the paint is ours.
 */
export const Checkbox = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  function Checkbox({ className, ...rest }, ref) {
    return (
      <input
        ref={ref}
        type="checkbox"
        className={cn(
          "size-4 shrink-0 cursor-pointer rounded-[2px] border border-rule-strong bg-paper-raised accent-[var(--color-accent)]",
          "hover:border-ink-faint active:brightness-95 disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        {...rest}
      />
    );
  },
);
