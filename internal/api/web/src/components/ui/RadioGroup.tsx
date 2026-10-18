import type { ReactNode } from "react";
import { useId } from "react";
import { cn } from "../../lib/cn";

export type RadioOption = {
  value: string;
  label: string;
  /** One line on what choosing this value does, in the operator's words. */
  description?: ReactNode;
  /** Extra content shown under the description while the option is selected. */
  detail?: ReactNode;
  disabled?: boolean;
};

export type RadioGroupProps = {
  /** Shared `name` for the native inputs; the legend is its visible label. */
  legend: string;
  options: RadioOption[];
  value: string;
  onValueChange: (value: string) => void;
  /** Rendered between the legend and the options: the question's context. */
  hint?: ReactNode;
  /** Error text; also marks every card as invalid for assistive tech. */
  error?: string;
  disabled?: boolean;
  className?: string;
};

/*
 * A radio group drawn as selectable cards.
 *
 * Why a native input rather than a Radix primitive: one value out of three is
 * exactly what a radio group is, so the browser's own group keyboard model
 * (arrows move and select, Tab leaves the group) is already correct and the
 * library would only re-implement it. The input keeps that behaviour and keeps
 * the form's semantics; `sr-only` hides it visually while `has-[:…]` on the
 * label carries all of its states, so nothing is drawn from a `<div>` that
 * cannot be focused or read out.
 *
 * The indicator is a square, not a dot inside a circle: the design system
 * reserves the round shape for `Badge`, so selection is shown by a filled
 * inset square plus the accent border and wash. Selection is therefore never
 * carried by colour alone - the mark itself changes, and each option states in
 * words what it selects.
 */
export function RadioGroup({
  legend,
  options,
  value,
  onValueChange,
  hint,
  error,
  disabled = false,
  className,
}: RadioGroupProps) {
  const groupId = useId();

  return (
    <fieldset className={cn("min-w-0", className)} aria-invalid={error ? true : undefined}>
      <legend className="block text-xs font-medium text-ink-soft">{legend}</legend>
      {hint && <p className="mt-1 max-w-[68ch] text-xs leading-relaxed text-ink-faint">{hint}</p>}
      <div className="mt-2.5 grid gap-2">
        {options.map((option) => {
          const optionId = `${groupId}-${option.value}`;
          const selected = option.value === value;
          const optionDisabled = disabled || option.disabled;
          return (
            <label
              key={option.value}
              htmlFor={optionId}
              data-selected={selected ? "true" : undefined}
              className={cn(
                "group relative flex cursor-pointer items-start gap-3 rounded-control border bg-paper-inset px-3 py-2.5",
                "border-rule hover:border-rule-strong hover:bg-hover",
                "has-[:checked]:border-accent has-[:checked]:bg-accent-wash",
                "active:border-accent",
                // The input is visually hidden, so the focus ring has to be drawn
                // by the card the operator can actually see.
                "has-[:focus-visible]:outline has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-accent",
                error && "border-alert",
                optionDisabled && "cursor-not-allowed opacity-50 hover:border-rule hover:bg-hover",
              )}
            >
              <input
                id={optionId}
                type="radio"
                name={groupId}
                value={option.value}
                checked={selected}
                disabled={optionDisabled}
                onChange={() => onValueChange(option.value)}
                className="sr-only"
              />
              <span
                aria-hidden
                className={cn(
                  "mt-0.5 grid size-4 shrink-0 place-items-center rounded-[3px] border border-rule-strong bg-paper-raised",
                  "group-has-[:checked]:border-accent group-has-[:checked]:bg-accent",
                )}
              >
                <span className="size-2 rounded-[1px] bg-on-accent opacity-0 group-has-[:checked]:opacity-100" />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block text-sm font-medium text-ink">{option.label}</span>
                {option.description && (
                  <span className="mt-0.5 block text-xs leading-relaxed text-ink-faint">
                    {option.description}
                  </span>
                )}
                {selected && option.detail && <span className="mt-2 block">{option.detail}</span>}
              </span>
            </label>
          );
        })}
      </div>
      {error && <p className="mt-1.5 text-xs text-alert">{error}</p>}
    </fieldset>
  );
}
