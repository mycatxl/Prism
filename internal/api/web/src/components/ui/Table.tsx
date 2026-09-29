import type { HTMLAttributes, ThHTMLAttributes, TdHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

/**
 * Table primitives.
 *
 * A data table is separated by row rules only, never by borders around cells: the
 * column alignment is what makes it readable, and vertical rules fight the
 * numbers.
 */
export function TableWrap({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("w-full overflow-x-auto", className)} {...rest} />;
}

export function Table({ className, ...rest }: HTMLAttributes<HTMLTableElement>) {
  return <table className={cn("w-full border-collapse text-sm", className)} {...rest} />;
}

export function THead({ className, ...rest }: HTMLAttributes<HTMLTableSectionElement>) {
  return <thead className={cn("bg-paper-sunk/60", className)} {...rest} />;
}

export function TH({ className, ...rest }: ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <th
      className={cn(
        "border-b border-rule px-3 py-2 text-left text-xs font-medium whitespace-nowrap text-ink-soft",
        className,
      )}
      {...rest}
    />
  );
}

export function TBody({ className, ...rest }: HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody className={cn("divide-y divide-rule", className)} {...rest} />;
}

export function TR({
  className,
  selected,
  ...rest
}: HTMLAttributes<HTMLTableRowElement> & { selected?: boolean }) {
  return (
    <tr
      data-selected={selected ? "true" : undefined}
      className={cn(
        "transition-colors hover:bg-paper-sunk/50",
        selected && "bg-signal-wash/60 hover:bg-signal-wash",
        className,
      )}
      {...rest}
    />
  );
}

export function TD({ className, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={cn("px-3 py-2 align-middle", className)} {...rest} />;
}

/** Marks a cell whose content is a value to be compared: mono, tabular, right. */
export function TDNum({ className, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <TD className={cn("readout text-right", className)} {...rest} />;
}
