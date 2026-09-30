import type { InputHTMLAttributes } from "react";
import { forwardRef } from "react";
import { cn } from "../../lib/cn";

/**
 * Checkbox.
 *
 * Native, like `Select`, and for the same reason: the browser's own control
 * already handles keyboard, indeterminate state, form reset and screen readers.
 *
 * The box is 14px rather than the 28px control step, and that is deliberate: a
 * checkbox is never the hit target on its own here. It is always inside a
 * `<label>` that carries the click area, so the box is the *visual* and the
 * label is the target. `accent-color` is the only way a native checkbox takes a
 * token, so the colour is bound here rather than left to each call site.
 */
export const Checkbox = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  function Checkbox({ className, ...rest }, ref) {
    return (
      <input
        ref={ref}
        type="checkbox"
        className={cn(
          "size-3.5 shrink-0 accent-[var(--color-signal)]",
          // The box is the visual; the label around it is the target, so the only
          // states that belong here are the ones a box can show without moving:
          // a hover step on the accent and the disabled pair every control shares.
          "hover:brightness-110 active:brightness-95 disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        {...rest}
      />
    );
  },
);
