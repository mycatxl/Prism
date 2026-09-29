import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cn } from "../../lib/cn";

/**
 * Tabs are ruled, not boxed: the active tab is marked by a 2px underline sitting
 * on the shared hairline, so a tab set reads as part of the page rather than as
 * another container.
 */
export const Tabs = TabsPrimitive.Root;

export function TabsList({
  className,
  ...rest
}: React.ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn("flex items-center gap-4 border-b border-rule", className)}
      {...rest}
    />
  );
}

export function TabsTrigger({
  className,
  ...rest
}: React.ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      className={cn(
        "-mb-px border-b-2 border-transparent px-0.5 pb-2 text-sm font-medium text-ink-soft transition-colors",
        "hover:text-ink",
        "data-[state=active]:border-signal data-[state=active]:text-ink",
        className,
      )}
      {...rest}
    />
  );
}

export function TabsContent({
  className,
  ...rest
}: React.ComponentProps<typeof TabsPrimitive.Content>) {
  return <TabsPrimitive.Content className={cn("outline-none", className)} {...rest} />;
}
