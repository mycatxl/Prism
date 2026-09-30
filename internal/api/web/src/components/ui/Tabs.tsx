import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cn } from "../../lib/cn";

/**
 * Segmented control inside a glass trough; the active segment is a raised glass
 * pill with identical Radix ARIA/keyboard behaviour.
 */
export const Tabs = TabsPrimitive.Root;

export function TabsList({
  className,
  ...rest
}: React.ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn(
        "inline-flex min-h-[var(--control-h-lg)] flex-wrap items-center gap-1 rounded-control border border-glass-edge bg-glass p-1 shadow-[inset_0_1px_0_0_var(--color-glass-highlight)]",
        className,
      )}
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
        "action inline-flex h-[var(--control-h-sm)] items-center justify-center rounded-[calc(var(--radius-control)-3px)] px-3 text-xs font-medium text-ink-soft select-none",
        "hover:text-ink",
        "data-[state=active]:border data-[state=active]:border-glass-edge data-[state=active]:bg-glass-strong data-[state=active]:font-semibold data-[state=active]:text-ink data-[state=active]:shadow-xs",
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
