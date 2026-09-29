import type { HTMLAttributes, ThHTMLAttributes, TdHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

/**
 * Table primitives.
 *
 * The grid is a token set, not a per-page decision: 32px header row, 32px body
 * rows (28 compact, 36 comfortable), 12px cell padding with the first and last
 * cells carrying the panel's own 16px so the columns line up with the panel
 * header. A table whose rows are 36px on one page and 80px on another has no grid,
 * which is exactly what made the earlier panel read as deformed.
 *
 * Rows are separated by a hairline, never by cell borders: column alignment is
 * what makes a table readable, and vertical rules fight the numbers.
 */

export type Density = "compact" | "comfortable";

export function TableWrap({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("w-full overflow-x-auto", className)} {...rest} />;
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
 * A column head is micro-caps, tracked and quieter than any cell. That contrast is
 * what lets a 14px data row read as data instead of as a label. It is sticky, so a
 * long inventory keeps its column names while it scrolls.
 */
export function TH({ className, ...rest }: ThHTMLAttributes<HTMLTableCellElement>) {
  return <th className={className} {...rest} />;
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
 * A one-line cell. Rows are a fixed height, so any cell that could wrap has to
 * truncate: a table where one long subscription URL doubles every row's height is
 * the other half of why the earlier panel looked out of shape.
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
