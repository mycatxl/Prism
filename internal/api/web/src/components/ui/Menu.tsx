import * as MenuPrimitive from "@radix-ui/react-dropdown-menu";
import type { ComponentProps } from "react";
import { cn } from "../../lib/cn";

/**
 * Dropdown action menu (Radix): a trigger, then a glass-elevated list of items.
 * Use it for a small set of related one-shot actions, e.g. export formats.
 */
export const Menu = MenuPrimitive.Root;
export const MenuTrigger = MenuPrimitive.Trigger;

export function MenuContent({ className, sideOffset = 6, align = "end", ...rest }: ComponentProps<typeof MenuPrimitive.Content>) {
  return (
    <MenuPrimitive.Portal>
      <MenuPrimitive.Content
        sideOffset={sideOffset}
        align={align}
        className={cn(
          "glass-elevated z-50 min-w-[12rem] rounded-panel border border-glass-edge-strong bg-paper-elevated p-1 text-sm text-ink shadow-lg",
          className,
        )}
        {...rest}
      />
    </MenuPrimitive.Portal>
  );
}

export function MenuLabel({ className, ...rest }: ComponentProps<typeof MenuPrimitive.Label>) {
  return <MenuPrimitive.Label className={cn("px-2.5 pb-1 pt-1.5 text-xs text-ink-soft", className)} {...rest} />;
}

export function MenuItem({ className, ...rest }: ComponentProps<typeof MenuPrimitive.Item>) {
  return (
    <MenuPrimitive.Item
      className={cn(
        "flex cursor-default select-none items-center justify-between gap-3 rounded-control px-2.5 py-1.5 outline-none",
        "data-[highlighted]:bg-glass-strong data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
        className,
      )}
      {...rest}
    />
  );
}

export function MenuSeparator({ className, ...rest }: ComponentProps<typeof MenuPrimitive.Separator>) {
  return <MenuPrimitive.Separator className={cn("my-1 h-px bg-glass-edge", className)} {...rest} />;
}
