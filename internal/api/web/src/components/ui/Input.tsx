import type { InputHTMLAttributes, ReactNode } from "react";
import { forwardRef } from "react";
import { cn } from "../../lib/cn";

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean }>(function Input(
  { className, invalid, disabled, ...rest },
  ref,
) {
  return <input ref={ref} {...rest} className={cn("workspace-input min-h-[var(--control-h)]", className)} disabled={disabled} aria-invalid={invalid || rest["aria-invalid"] || undefined} />;
});

export { Textarea } from "./Textarea";

export function Fieldset({ label, hint, htmlFor, children, className }: { label: string; hint?: string; htmlFor?: string; children: ReactNode; className?: string }) {
  return (
    <div className={cn("workspace-field", className)}>
      <label className="workspace-field__label" htmlFor={htmlFor}>{label}</label>
      {children}
      {hint ? <span className="workspace-field__hint">{hint}</span> : null}
    </div>
  );
}
