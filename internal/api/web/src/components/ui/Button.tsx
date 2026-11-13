import { Slot } from "@radix-ui/react-slot";
import { LoaderCircle } from "lucide-react";
import type { ButtonHTMLAttributes, MouseEventHandler, ReactNode } from "react";
import { cn } from "../../lib/cn";

export type ButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "disabled" | "onClick"> & {
  children?: ReactNode;
  className?: string;
  variant?: "primary" | "secondary" | "ghost" | "danger" | "destructive" | "quiet";
  size?: "sm" | "md" | "lg" | "xl" | "icon";
  disabled?: boolean;
  isDisabled?: boolean;
  isIconOnly?: boolean;
  onClick?: MouseEventHandler<HTMLButtonElement>;
  onPress?: MouseEventHandler<HTMLButtonElement> | (() => void);
} & ({ asChild: true; loading?: never } | { asChild?: false; loading?: boolean });

export function Button({
  className,
  variant = "secondary",
  size = "md",
  asChild,
  loading,
  disabled,
  isDisabled,
  isIconOnly,
  onClick,
  onPress,
  children,
  type = "button",
  ...rest
}: ButtonProps) {
  const classes = cn(
    "workspace-button min-h-[var(--control-h)]",
    (size === "icon" || isIconOnly) && "workspace-button--icon",
    className,
  );
  const content = children;
  const handleClick: MouseEventHandler<HTMLButtonElement> | undefined = onClick || onPress
    ? (event) => {
      onClick?.(event);
      if (!event.defaultPrevented) (onPress as MouseEventHandler<HTMLButtonElement> | undefined)?.(event);
    }
    : undefined;

  if (asChild) {
    return <Slot {...rest} data-variant={variant} data-size={size} className={classes}>{content}</Slot>;
  }

  return (
    <button
      {...rest}
      type={type}
      data-variant={variant === "destructive" ? "danger" : variant}
      data-size={size}
      className={classes}
      disabled={disabled || isDisabled || loading}
      onClick={handleClick}
      aria-busy={loading || undefined}
    >
      {loading ? <LoaderCircle size={14} aria-hidden className="workspace-spinner" /> : null}
      {content}
    </button>
  );
}
