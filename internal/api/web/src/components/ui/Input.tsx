import {
  Description as HeroDescription,
  FieldError as HeroFieldError,
  Input as HeroInput,
  Label as HeroLabel,
  TextField as HeroTextField,
} from "@heroui/react";
import type { InputHTMLAttributes, ReactNode } from "react";
import { forwardRef } from "react";
import { cn } from "../../lib/cn";

export const Input = forwardRef<
  HTMLInputElement,
  InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }
>(function Input({ className, invalid, disabled, ...rest }, ref) {
  return (
    <HeroTextField className="workspace-field" isInvalid={invalid} isDisabled={disabled}>
      <HeroInput
        ref={ref}
        {...rest}
        className={cn("workspace-input min-h-[var(--control-h)]", className)}
        disabled={disabled}
      />
      {invalid ? <HeroFieldError /> : null}
    </HeroTextField>
  );
});

export { Textarea } from "./Textarea";

export function Fieldset({
  label,
  hint,
  htmlFor,
  children,
  className,
}: {
  label: string;
  hint?: string;
  htmlFor?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <HeroTextField className={cn("workspace-field", className)}>
      <HeroLabel htmlFor={htmlFor}>{label}</HeroLabel>
      {children}
      {hint ? <HeroDescription>{hint}</HeroDescription> : null}
    </HeroTextField>
  );
}
