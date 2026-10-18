import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cn } from "../../lib/cn";

/**
 * Segmented control inside an inset trough; the active segment is the raised one,
 * with Radix's ARIA and keyboard behaviour untouched. Both shapes are rectangles —
 * the only pill in the system is `Badge`.
 */
export const Tabs = TabsPrimitive.Root;

export function TabsList({
  className,
  ...rest
}: React.ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn(
        "inline-flex min-h-[var(--control-h-lg)] flex-wrap items-center gap-1 rounded-control border border-rule bg-paper-inset p-1",
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
        "data-[state=active]:border data-[state=active]:border-rule data-[state=active]:bg-paper-raised data-[state=active]:font-semibold data-[state=active]:text-ink data-[state=active]:shadow-xs",
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
