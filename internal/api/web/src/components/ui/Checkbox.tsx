import { Check } from "lucide-react";
import type { InputHTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/cn";

export type CheckboxProps = Omit<InputHTMLAttributes<HTMLInputElement>, "checked" | "onChange" | "type"> & {
  className?: string;
  label?: ReactNode;
  isSelected?: boolean;
  isDisabled?: boolean;
  onChange?: (checked: boolean) => void;
};

export function Checkbox({ className, label, isSelected, isDisabled, disabled, onChange, ...props }: CheckboxProps) {
  return (
    <label className={cn("workspace-checkbox", className)}>
      <span className="workspace-checkbox__content">
        <input {...props} type="checkbox" checked={Boolean(isSelected)} disabled={isDisabled || disabled} onChange={(event) => onChange?.(event.target.checked)} />
        <span className="workspace-checkbox__control" aria-hidden><Check size={12} /></span>
        {label ? <span className="workspace-checkbox__label">{label}</span> : null}
      </span>
    </label>
  );
}
