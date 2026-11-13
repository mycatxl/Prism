import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import type { ReactElement, ReactNode } from "react";

export function Dropdown({ trigger, label, children, align = "end" }: {
  trigger: ReactElement;
  label: string;
  children: ReactNode;
  align?: "start" | "center" | "end";
}) {
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>{trigger}</DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content sideOffset={8} align={align} className="workspace-account-menu" aria-label={label}>{children}</DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

export function DropdownItem({ children, onSelect, destructive = false }: { children: ReactNode; onSelect?: () => void; destructive?: boolean }) {
  return <DropdownMenu.Item onSelect={onSelect} className={destructive ? "is-danger" : undefined}>{children}</DropdownMenu.Item>;
}

export function DropdownSeparator() {
  return <DropdownMenu.Separator className="workspace-account-menu__separator" />;
}
