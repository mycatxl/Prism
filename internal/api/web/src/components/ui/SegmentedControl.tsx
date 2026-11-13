import * as Tabs from "@radix-ui/react-tabs";
import type { ReactNode } from "react";

export function SegmentedControl({ label, value, items, onValueChange, className }: {
  label: string;
  value: string;
  items: ReadonlyArray<{ value: string; label: ReactNode; disabled?: boolean }>;
  onValueChange: (value: string) => void;
  className?: string;
}) {
  return (
    <Tabs.Root value={value} onValueChange={onValueChange} className={className}>
      <Tabs.List className="workspace-tabs__list" aria-label={label}>
        {items.map((item) => <Tabs.Trigger key={item.value} value={item.value} disabled={item.disabled} className="workspace-tabs__tab">{item.label}</Tabs.Trigger>)}
      </Tabs.List>
    </Tabs.Root>
  );
}
