import * as SwitchPrimitive from "@radix-ui/react-switch";
import { cn } from "../../lib/cn";

/**
 * Radix carries the keyboard and ARIA behaviour; this file only supplies the
 * appearance.
 *
 * Two sizes, deliberately different: the track is an 18px visual (the proportion
 * a 14px thumb needs), and the *hit area* is the 28px control step the rest of
 * the console uses, drawn by a pseudo-element so a pointer does not have to find
 * an 18px-tall target. The track is rectangular so it does not compete with the
 * pill shape reserved for status (`DESIGN.md:126-127`).
 */
export function Switch({
  className,
  ...rest
}: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      className={cn(
        "peer relative inline-flex h-4.5 w-8 shrink-0 cursor-pointer items-center rounded-control border border-rule transition-colors",
        "before:absolute before:inset-x-0 before:top-1/2 before:h-[var(--control-h)] before:-translate-y-1/2 before:content-['']",
        // Hover and active step the track's own colours; the checked state keeps
        // the signal fill, so a hover on a checked switch is a deeper signal
        // rather than a different colour.
        "hover:border-rule-strong active:border-ink-faint",
        "data-[state=checked]:border-signal data-[state=checked]:bg-signal",
        "data-[state=checked]:hover:border-signal-deep data-[state=checked]:active:border-signal-deep",
        "data-[state=unchecked]:hover:bg-paper-sunk data-[state=unchecked]:active:bg-paper-sunk",
        "data-[state=unchecked]:bg-paper-sunk",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...rest}
    >
      <SwitchPrimitive.Thumb
        className={cn(
          "pointer-events-none block size-3.5 rounded-control bg-paper-raised shadow-sm ring-0 transition-transform",
          "data-[state=checked]:translate-x-[15px] data-[state=unchecked]:translate-x-px",
        )}
      />
    </SwitchPrimitive.Root>
  );
}
