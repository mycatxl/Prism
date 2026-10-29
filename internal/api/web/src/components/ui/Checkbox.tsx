import { Checkbox as HeroCheckbox } from "@heroui/react";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "../../lib/cn";

export type CheckboxProps = Omit<ComponentProps<typeof HeroCheckbox>, "children" | "className"> & {
  className?: string;
  label?: ReactNode;
};

export function Checkbox({ className, label, ...props }: CheckboxProps) {
  return (
    <HeroCheckbox {...props} className={cn("workspace-checkbox", className)}>
      <HeroCheckbox.Content>
        <HeroCheckbox.Control>
          <HeroCheckbox.Indicator />
        </HeroCheckbox.Control>
        {label ? <span className="workspace-checkbox__label">{label}</span> : null}
      </HeroCheckbox.Content>
    </HeroCheckbox>
  );
}
