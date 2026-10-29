import {
  Description as HeroDescription,
  FieldError as HeroFieldError,
  Label as HeroLabel,
  TextArea as HeroTextArea,
  TextField as HeroTextField,
} from "@heroui/react";
import type { ReactNode, TextareaHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement> & {
  invalid?: boolean;
  label?: ReactNode;
  hint?: ReactNode;
};

export function Textarea({ className, invalid, label, hint, disabled, ...props }: TextareaProps) {
  return (
    <HeroTextField className="workspace-field" isInvalid={invalid} isDisabled={disabled}>
      {label ? <HeroLabel>{label}</HeroLabel> : null}
      <HeroTextArea {...props} className={cn("workspace-textarea", className)} disabled={disabled} />
      {hint ? <HeroDescription>{hint}</HeroDescription> : null}
      {invalid ? <HeroFieldError>{typeof hint === "string" ? hint : undefined}</HeroFieldError> : null}
    </HeroTextField>
  );
}
