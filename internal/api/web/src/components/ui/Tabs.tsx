import { Tabs as HeroTabs } from "@heroui/react";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "../../lib/cn";

type TabsRootProps = Omit<
  ComponentProps<typeof HeroTabs>,
  "children" | "selectedKey" | "defaultSelectedKey" | "onSelectionChange"
> & {
  children: ReactNode;
  value?: string;
  defaultValue?: string;
  onValueChange?: (value: string) => void;
};

export function Tabs({
  value,
  defaultValue,
  onValueChange,
  className,
  children,
  ...rest
}: TabsRootProps) {
  return (
    <HeroTabs
      {...rest}
      selectedKey={value}
      defaultSelectedKey={defaultValue}
      onSelectionChange={(key) => onValueChange?.(String(key))}
      variant="primary"
      className={cn("workspace-tabs", className)}
    >
      {children}
    </HeroTabs>
  );
}

export function TabsList({ className, ...rest }: ComponentProps<typeof HeroTabs.List>) {
  return <HeroTabs.List {...rest} className={cn("workspace-tabs__list", className)} />;
}

export function TabsTrigger({ value, className, children, ...rest }: ComponentProps<typeof HeroTabs.Tab> & { value: string }) {
  return (
    <HeroTabs.Tab {...rest} id={value} className={cn("workspace-tabs__tab", className)}>
      {children}
    </HeroTabs.Tab>
  );
}

export function TabsContent({ value, className, children, ...rest }: ComponentProps<typeof HeroTabs.Panel> & { value: string }) {
  return (
    <HeroTabs.Panel {...rest} id={value} className={cn("outline-none", className)}>
      {children}
    </HeroTabs.Panel>
  );
}
