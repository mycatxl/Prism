import * as RadixSwitch from "@radix-ui/react-switch";
import type { ComponentPropsWithoutRef } from "react";
import { cn } from "../../lib/cn";

export function Switch({ className, ...props }: ComponentPropsWithoutRef<typeof RadixSwitch.Root>) {
  return (
    <RadixSwitch.Root {...props} className={cn("workspace-switch__control", className)}>
      <RadixSwitch.Thumb className="workspace-switch__thumb" />
    </RadixSwitch.Root>
  );
}
