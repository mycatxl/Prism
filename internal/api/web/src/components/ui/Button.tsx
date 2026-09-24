import type { ButtonHTMLAttributes } from "react";
import { cn } from "../../lib/cn";
import * as Tooltip from "@radix-ui/react-tooltip";

type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
type ButtonSize = "sm" | "md";

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant;
  size?: ButtonSize;
};

const variantClass: Record<ButtonVariant, string> = {
  primary: "btn-primary",
  secondary: "btn-secondary",
  ghost: "btn-ghost",
  danger: "btn-danger",
};

const sizeClass: Record<ButtonSize, string> = {
  sm: "btn-sm",
  md: "btn-md",
};

export function Button({
  className,
  variant = "primary",
  size = "md",
  type = "button",
  title,
  ...props
}: ButtonProps) {
  const button = (
    <button
      type={type}
      className={cn("btn", variantClass[variant], sizeClass[size], className)}
      aria-label={props["aria-label"] || title}
      {...props}
    />
  );
  return title ? (
    <Tooltip.Root>
      <Tooltip.Trigger asChild>{button}</Tooltip.Trigger>
      <Tooltip.Portal>
        <Tooltip.Content className="tooltip-content" sideOffset={6}>
          {title}
        </Tooltip.Content>
      </Tooltip.Portal>
    </Tooltip.Root>
  ) : (
    button
  );
}
