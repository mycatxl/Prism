import * as DialogPrimitive from "@radix-ui/react-dialog";
import type { ComponentProps } from "react";
import { cn } from "../../lib/cn";

/*
 * Dialog states:
 *   default   the overlay's `bg-ink/45` scrim over the page, with the content's
 *             elevated glass sheet centred on top of it
 *   hover     per control: a `DialogClose` composed with `Button` carries the
 *             button's own hover, and the primitive adds none of its own
 *   focus     Radix traps focus inside the content and returns it to whatever
 *             opened the dialog when it closes; the surface itself draws no ring
 *             (the controls inside it do)
 *   open      the content is mounted by the portal only while the dialog is open,
 *             so anything expensive inside it is paid for on open and given back
 *             on close
 *   escape    `Escape`, a pointer-down outside the content and a click on the
 *             scrim all dismiss it through `onOpenChange(false)`
 *   disabled  not a state of this primitive: a dialog has no disabled form, so a
 *             caller that must not open one does not render its trigger
 *
 * No entrance animation is declared. Radix would happily animate the mount, but
 * the console's own motion rule is that motion states change and never decorates
 * an arrival (root `DESIGN.md`), so the surface appears.
 */

/**
 * The controlled root. `open` + `onOpenChange` is the contract every panel here
 * uses: the trigger lives somewhere else on the page, and the boolean lives in
 * the page's state.
 */
export const DialogRoot = DialogPrimitive.Root;

/** A control that opens the dialog. Compose it with `Button asChild`. */
export const DialogTrigger = DialogPrimitive.Trigger;

/**
 * The portal. The content exists in the DOM only while the dialog is open, which
 * is the whole reason a dashboard can mount a second chart inside a dialog
 * without paying for it on every page load.
 */
export const DialogPortal = DialogPrimitive.Portal;

/** Closes the dialog. Compose it with `Button asChild` and give it a label. */
export const DialogClose = DialogPrimitive.Close;

/**
 * The scrim: a dimmed, slightly blurred wash over the page, so the surface on top
 * of it reads as the only thing in focus. It is not a control — tapping it closes
 * the dialog through Radix's own outside-pointer handling, which is why it is not
 * a `Button`.
 */
export function DialogOverlay({ className, ...rest }: ComponentProps<typeof DialogPrimitive.Overlay>) {
  return (
    <DialogPrimitive.Overlay
      className={cn("fixed inset-0 z-40 bg-ink/45 backdrop-blur-sm", className)}
      {...rest}
    />
  );
}

/**
 * The surface: an elevated glass sheet, centred, with generous radius.
 *
 * It takes the width of its own body rather than naming one — a dialog that holds
 * a chart is as wide as that chart's frame, and a dialog that holds a sentence is
 * as wide as the sentence. The body decides, and the two ceilings here only keep
 * it inside a short or narrow viewport.
 */
export function DialogContent({ className, ...rest }: ComponentProps<typeof DialogPrimitive.Content>) {
  return (
    <DialogPrimitive.Content
      className={cn(
        "fixed top-1/2 left-1/2 z-50 max-h-[calc(100vh-2rem)] max-w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 overflow-hidden rounded-xl border border-glass-edge bg-paper-elevated shadow-lg focus:outline-none",
        className,
      )}
      {...rest}
    />
  );
}

/**
 * The title. Radix reads it out as the dialog's name and points the surface's
 * `aria-labelledby` at it, so it is the one string that has to be there.
 */
export function DialogTitle({ className, ...rest }: ComponentProps<typeof DialogPrimitive.Title>) {
  return (
    <DialogPrimitive.Title
      className={cn("truncate text-sm font-semibold tracking-tight text-ink", className)}
      {...rest}
    />
  );
}
