import { Button as HeroButton } from "@heroui/react";
import { LoaderCircle } from "lucide-react";
import type { ButtonHTMLAttributes, MouseEventHandler, ReactElement, ReactNode } from "react";
import { Children, cloneElement } from "react";
import type { ComponentProps } from "react";
import { cn } from "../../lib/cn";

type HeroButtonProps = ComponentProps<typeof HeroButton>;

type ButtonBaseProps = Omit<
  ButtonHTMLAttributes<HTMLButtonElement>,
  "children" | "className" | "disabled" | "onClick"
> & {
  children?: ReactNode;
  className?: string;
  variant?: "primary" | "secondary" | "ghost" | "danger" | "quiet";
  size?: "sm" | "md" | "lg" | "xl" | "icon";
  disabled?: boolean;
  onClick?: MouseEventHandler<HTMLButtonElement>;
  onPress?: HeroButtonProps["onPress"];
};

export type ButtonProps = ButtonBaseProps &
  ({ asChild: true; loading?: never } | { asChild?: false; loading?: boolean });

const variantMap: Record<NonNullable<ButtonProps["variant"]>, NonNullable<HeroButtonProps["variant"]>> = {
  primary: "primary",
  secondary: "secondary",
  ghost: "ghost",
  danger: "danger",
  quiet: "ghost",
};

const sizeMap: Record<NonNullable<ButtonProps["size"]>, NonNullable<HeroButtonProps["size"]>> = {
  sm: "sm",
  md: "md",
  lg: "lg",
  xl: "lg",
  icon: "sm",
};

type SlotProps = Record<string, unknown>;

/*
 * `asChild` renders a link that looks like a button: one element, carrying the
 * button's classes, data attributes and ref alongside the child's own props.
 * Merging is all this needs, so the slot lives here rather than as a second
 * component library's private helper. Handlers compose child-first, which is why
 * a child's own `onClick` still runs after ours and can read `defaultPrevented`.
 */
function mergeSlotProps(child: SlotProps, slot: SlotProps): SlotProps {
  const merged: SlotProps = { ...slot, ...child };
  const className = [slot.className, child.className].filter(Boolean).join(" ");
  merged.className = className || undefined;

  for (const [key, slotValue] of Object.entries(slot)) {
    const childValue = child[key];
    if (!key.startsWith("on") || typeof slotValue !== "function" || typeof childValue !== "function") continue;
    merged[key] = (...args: unknown[]) => {
      (childValue as (...inner: unknown[]) => void)(...args);
      (slotValue as (...inner: unknown[]) => void)(...args);
    };
  }

  return merged;
}

export function Button({
  className,
  variant = "secondary",
  size = "md",
  asChild,
  loading,
  disabled,
  onClick,
  onPress,
  children,
  ...rest
}: ButtonProps) {
  const heroVariant = variantMap[variant];
  const heroSize = sizeMap[size];
  const isIconOnly = size === "icon";
  const classes = cn("workspace-button min-h-[var(--control-h)]", isIconOnly && "workspace-button--icon", className);
  const content = loading ? (
    <>
      <LoaderCircle size={14} aria-hidden className="workspace-spinner" />
      {children}
    </>
  ) : (
    children
  );

  if (asChild) {
    const child = Children.only(content) as ReactElement<SlotProps>;
    return cloneElement(
      child,
      mergeSlotProps(child.props, {
        className: classes,
        "data-variant": heroVariant,
        "data-size": heroSize,
        "aria-disabled": disabled || undefined,
        ...(rest as SlotProps),
      }),
    );
  }

  const handlePress: HeroButtonProps["onPress"] = onPress ?? (onClick
    ? (event) => onClick(event as unknown as React.MouseEvent<HTMLButtonElement>)
    : undefined);

  return (
    <HeroButton
      {...(rest as Omit<HeroButtonProps, "children" | "className">)}
      variant={heroVariant}
      size={heroSize}
      isIconOnly={isIconOnly}
      isDisabled={disabled || loading}
      isPending={loading}
      onPress={handlePress}
      className={classes}
      aria-busy={loading || undefined}
    >
      {content}
    </HeroButton>
  );
}
