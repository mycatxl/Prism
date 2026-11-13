import type { ReactNode, TextareaHTMLAttributes } from "react";
import { useId } from "react";
import { cn } from "../../lib/cn";

type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement> & {
  invalid?: boolean;
  label?: ReactNode;
  hint?: ReactNode;
};

export function Textarea({ className, invalid, label, hint, disabled, id, ...props }: TextareaProps) {
  const generatedId = useId();
  const controlId = id ?? generatedId;
  return (
    <div className="workspace-field">
      {label ? <label className="workspace-field__label" htmlFor={controlId}>{label}</label> : null}
      <textarea {...props} id={controlId} className={cn("workspace-textarea", className)} disabled={disabled} aria-invalid={invalid || undefined} />
      {hint ? <span className={invalid ? "workspace-field__error" : "workspace-field__hint"}>{hint}</span> : null}
    </div>
  );
}
