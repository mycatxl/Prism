import type { HTMLAttributes, ThHTMLAttributes, TdHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

/**
 * Table primitives: a sticky head row, hairline row rules (`--color-row-rule`),
 * hover wash, and no zebra striping.
 */

export type Density = "compact" | "comfortable";

export function TableWrap({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("table-wrap w-full min-w-0 overflow-x-auto overscroll-x-contain", className)} {...rest} />;
}

export function Table({
  className,
  density = "compact",
  ...rest
}: HTMLAttributes<HTMLTableElement> & { density?: Density }) {
  return (
    <table
      data-density={density}
      className={cn(
        "data-grid",
        density === "compact" && "data-grid--compact",
        density === "comfortable" && "data-grid--comfortable",
        className,
      )}
      {...rest}
    />
  );
}

export function THead({ className, ...rest }: HTMLAttributes<HTMLTableSectionElement>) {
  return <thead className={className} {...rest} />;
}

/**
 * Sticky column head in micro-caps; opaque, because a head that shows rows through
 * itself is a head nobody can read.
 *
 * `scope="col"` is the default: every head in this console heads a column, and a
 * cell a screen reader cannot tie to a column is a cell read as a naked value. A
 * caller that really heads a row passes its own `scope` and wins.
 */
export function TH({ className, scope = "col", ...rest }: ThHTMLAttributes<HTMLTableCellElement>) {
  return <th scope={scope} className={className} {...rest} />;
}

export function TBody({ className, ...rest }: HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody className={className} {...rest} />;
}

export function TR({
  className,
  selected,
  ...rest
}: HTMLAttributes<HTMLTableRowElement> & { selected?: boolean }) {
  return (
    <tr data-selected={selected ? "true" : undefined} className={className} {...rest} />
  );
}

export function TD({ className, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={className} {...rest} />;
}

/** A cell whose content is a value to be compared: mono, tabular, right-aligned. */
export function TDNum({ className, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <TD className={cn("cell-num", className)} {...rest} />;
}

/**
 * A one-line cell that truncates cleanly without wrapping row height.
 */
export function TDClip({ className, title, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return (
    <TD
      className={cn("max-w-0 truncate", className)}
      title={title ?? (typeof rest.children === "string" ? rest.children : undefined)}
      {...rest}
    />
  );
}
