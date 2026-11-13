import type { HTMLAttributes, ReactNode, TableHTMLAttributes, TdHTMLAttributes, ThHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

export type Density = "compact" | "comfortable";

export function TableWrap({ className, children, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div {...rest} data-scrollable="true" className={cn("workspace-table-region workspace-table__scroll", className)}>{children}</div>;
}

export function Table({ className, density = "comfortable", children, ...rest }: TableHTMLAttributes<HTMLTableElement> & { density?: Density }) {
  return <table {...rest} className={cn("workspace-table", `workspace-table--${density}`, className)}>{children}</table>;
}

export function THead({ className, children, ...rest }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <thead {...rest} className={className}><tr>{children}</tr></thead>;
}

export function TH({ className, children, ...rest }: ThHTMLAttributes<HTMLTableCellElement>) {
  return <th {...rest} scope="col" className={cn("workspace-table__column", className)}>{children}</th>;
}

export function TBody({ className, children, ...rest }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody {...rest} className={className}>{children}</tbody>;
}

export function TR({ className, selected, children, ...rest }: HTMLAttributes<HTMLTableRowElement> & { selected?: boolean }) {
  return <tr {...rest} data-selected={selected ? "true" : undefined} className={cn("workspace-table__row", className)}>{children}</tr>;
}

export function TD({ className, title, children, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <td {...rest} className={cn("workspace-table__cell", className)}>{title ? <span title={title}>{children}</span> : children}</td>;
}

export function TDNum({ className, children, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <TD {...rest} className={cn("workspace-table__cell workspace-table__cell--end", className)}>{children}</TD>;
}

export function TDClip({ className, title, children, ...rest }: TdHTMLAttributes<HTMLTableCellElement> & { children?: ReactNode }) {
  return <TD {...rest} className={cn("workspace-table__cell max-w-0 truncate", className)} title={title ?? (typeof children === "string" ? children : undefined)}>{children}</TD>;
}
