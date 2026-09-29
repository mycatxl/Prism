import * as SwitchPrimitive from "@radix-ui/react-switch";
import { cn } from "../../lib/cn";

/**
 * Radix carries the keyboard and ARIA behaviour; this file only supplies the
 * appearance. The track is deliberately rectangular so it does not compete with
 * the pill shape reserved for status.
 */
export function Switch({
  className,
  ...rest
}: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      className={cn(
        "peer inline-flex h-4.5 w-8 shrink-0 cursor-pointer items-center rounded-full border border-rule transition-colors",
        "data-[state=checked]:border-signal data-[state=checked]:bg-signal",
        "data-[state=unchecked]:bg-paper-sunk",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...rest}
    >
      <SwitchPrimitive.Thumb
        className={cn(
          "pointer-events-none block size-3.5 rounded-full bg-paper-raised shadow-sm ring-0 transition-transform",
          "data-[state=checked]:translate-x-[15px] data-[state=unchecked]:translate-x-px",
        )}
      />
    </SwitchPrimitive.Root>
  );
}
