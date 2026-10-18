import * as SwitchPrimitive from "@radix-ui/react-switch";
import { cn } from "../../lib/cn";

/**
 * Radix carries the keyboard and ARIA behaviour; this file supplies the ruled
 * track and the plate that slides in it.
 *
 * The track is an 18px visual with a 28px (`--control-h`) hit area drawn by a
 * pseudo-element, and uses `rounded-control` so the pill shape remains reserved
 * for `Badge`.
 */
export function Switch({
  className,
  ...rest
}: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      className={cn(
        "peer action relative inline-flex h-4.5 w-8 shrink-0 cursor-pointer items-center rounded-control border border-rule-strong",
        "before:absolute before:inset-x-0 before:top-1/2 before:h-[var(--control-h)] before:-translate-y-1/2 before:content-['']",
        "hover:border-ink-faint active:border-accent",
        "data-[state=checked]:border-accent data-[state=checked]:bg-accent",
        "data-[state=checked]:hover:border-accent-deep data-[state=checked]:active:border-accent-deep",
        "data-[state=unchecked]:bg-paper-inset data-[state=unchecked]:hover:bg-paper-elevated",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...rest}
    >
      <SwitchPrimitive.Thumb
        className={cn(
          "pointer-events-none block size-3.5 rounded-[1px] bg-paper-elevated shadow-xs ring-0 transition-transform",
          "data-[state=checked]:translate-x-[15px] data-[state=checked]:bg-on-accent data-[state=unchecked]:translate-x-px",
        )}
      />
    </SwitchPrimitive.Root>
  );
}
