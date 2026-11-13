import * as RadixSelect from "@radix-ui/react-select";
import { Check, ChevronDown } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

export type SelectItem = { value: string; label: ReactNode; disabled?: boolean };

export function Select({ value, items, onValueChange, label, disabled, className }: {
  value: string;
  items: readonly SelectItem[];
  onValueChange: (value: string) => void;
  label: string;
  disabled?: boolean;
  className?: string;
}) {
  return (
    <RadixSelect.Root value={value} onValueChange={onValueChange} disabled={disabled}>
      <RadixSelect.Trigger className={cn("workspace-select__trigger", className)} aria-label={label}>
        <RadixSelect.Value />
        <RadixSelect.Icon><ChevronDown size={15} aria-hidden /></RadixSelect.Icon>
      </RadixSelect.Trigger>
      <RadixSelect.Portal>
        <RadixSelect.Content className="workspace-select__popover" position="popper" sideOffset={6}>
          <RadixSelect.Viewport>
            {items.map((item) => (
              <RadixSelect.Item key={item.value} value={item.value} disabled={item.disabled} className="workspace-select__option">
                <RadixSelect.ItemText>{item.label}</RadixSelect.ItemText>
                <RadixSelect.ItemIndicator className="workspace-select__check"><Check size={14} /></RadixSelect.ItemIndicator>
              </RadixSelect.Item>
            ))}
          </RadixSelect.Viewport>
        </RadixSelect.Content>
      </RadixSelect.Portal>
    </RadixSelect.Root>
  );
}
