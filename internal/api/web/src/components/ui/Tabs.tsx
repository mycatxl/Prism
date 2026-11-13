import * as RadixTabs from "@radix-ui/react-tabs";
import type { ComponentProps } from "react";
import { cn } from "../../lib/cn";

export function Tabs({ className, ...props }: ComponentProps<typeof RadixTabs.Root>) {
  return <RadixTabs.Root {...props} className={cn("workspace-tabs", className)} />;
}

export function TabsList({ className, ...props }: ComponentProps<typeof RadixTabs.List>) {
  return <RadixTabs.List {...props} className={cn("workspace-tabs__list", className)} />;
}

export function TabsTrigger({ value, className, ...props }: ComponentProps<typeof RadixTabs.Trigger>) {
  return <RadixTabs.Trigger {...props} value={value} className={cn("workspace-tabs__tab", className)} />;
}

export function TabsContent({ value, className, ...props }: ComponentProps<typeof RadixTabs.Content>) {
  return <RadixTabs.Content {...props} value={value} className={cn("outline-none", className)} />;
}
