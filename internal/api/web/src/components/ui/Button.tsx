import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type { ButtonHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

const button = cva(
  "action inline-flex shrink-0 items-center justify-center gap-1.5 whitespace-nowrap rounded-control font-medium select-none disabled:pointer-events-none disabled:opacity-45",
  {
    variants: {
      variant: {
        // The accent owns interaction in every screen, so "clickable" and
        // "healthy" never look the same in a dense table.
        primary: "bg-accent text-on-accent hover:bg-accent-deep",
        secondary:
          "border border-rule-strong bg-paper-raised text-ink hover:border-ink-faint hover:bg-paper-sunk",
        ghost: "text-ink-soft hover:bg-paper-sunk hover:text-ink",
        danger: "bg-alert text-on-alert hover:bg-ink",
        quiet: "border border-transparent text-ink-soft hover:text-ink",
      },
      size: {
        sm: "h-6.5 px-2 text-xs",
        md: "h-8 px-3 text-sm",
        lg: "h-10 px-4 text-base",
        icon: "size-8 p-0",
      },
    },
    defaultVariants: { variant: "secondary", size: "md" },
  },
);

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof button> & { asChild?: boolean };

export function Button({ className, variant, size, asChild, ...rest }: ButtonProps) {
  const Comp = asChild ? Slot : "button";
  return <Comp className={cn(button({ variant, size }), className)} {...rest} />;
}
