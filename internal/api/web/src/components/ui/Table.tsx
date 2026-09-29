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
  return <thead className={cn("bg-paper-sunk/70", className)} {...rest} />;
}

/**
 * A column head is micro-caps, tracked and quieter than any cell: that contrast
 * is what lets a 13px data row read as data instead of as a label.
 */
export function TH({ className, ...rest }: ThHTMLAttributes<HTMLTableCellElement>) {
  return (
    <th
      className={cn(
        "micro border-b border-rule px-3 py-1.5 text-left whitespace-nowrap",
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
        selected && "bg-accent-wash/70 hover:bg-accent-wash",
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
