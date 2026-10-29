import { Surface as HeroSurface, Table as HeroTable } from "@heroui/react";
import type { ComponentProps, HTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/cn";

export type Density = "compact" | "comfortable";

type TableRootProps = Omit<ComponentProps<typeof HeroTable>, "children" | "className"> & {
  className?: string;
  density?: Density;
  children?: ReactNode;
};
type TableHeaderProps = Omit<ComponentProps<typeof HeroTable.Header>, "children" | "className"> & { className?: string; children?: ReactNode };
type ColumnProps = Omit<ComponentProps<typeof HeroTable.Column>, "children" | "className"> & { className?: string; children?: ReactNode };
type BodyProps = Omit<ComponentProps<typeof HeroTable.Body>, "children" | "className"> & { className?: string; children?: ReactNode };
type RowProps = Omit<ComponentProps<typeof HeroTable.Row>, "children" | "className"> & { className?: string; children?: ReactNode; selected?: boolean };
type CellProps = Omit<ComponentProps<typeof HeroTable.Cell>, "children" | "className"> & { className?: string; children?: ReactNode; title?: string };

export function TableWrap({ className, children, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return (
    <HeroSurface
      {...rest}
      variant="secondary"
      className={cn("workspace-table-region workspace-table__scroll", className)}
    >
      {children}
    </HeroSurface>
  );
}

export function Table({ className, density = "compact", children, ...rest }: TableRootProps) {
  return (
    <HeroTable
      {...rest}
      variant="primary"
      className={cn(
        "workspace-table",
        density === "compact" && "workspace-table--compact",
        density === "comfortable" && "workspace-table--comfortable",
        className,
      )}
    >
      <HeroTable.Content>{children}</HeroTable.Content>
    </HeroTable>
  );
}

export function THead({ className, children, ...rest }: TableHeaderProps) {
  return <HeroTable.Header {...rest} className={className}>{children}</HeroTable.Header>;
}

export function TH({ className, children, ...rest }: ColumnProps) {
  return <HeroTable.Column {...rest} className={cn("workspace-table__column", className)}>{children}</HeroTable.Column>;
}

export function TBody({ className, children, ...rest }: BodyProps) {
  return <HeroTable.Body {...rest} className={className}>{children}</HeroTable.Body>;
}

export function TR({ className, selected, children, ...rest }: RowProps) {
  return <HeroTable.Row {...rest} data-selected={selected ? "true" : undefined} className={cn("workspace-table__row", className)}>{children}</HeroTable.Row>;
}

export function TD({ className, title, children, ...rest }: CellProps) {
  return <HeroTable.Cell {...rest} className={cn("workspace-table__cell", className)}>{title ? <span title={title}>{children}</span> : children}</HeroTable.Cell>;
}

export function TDNum({ className, children, ...rest }: CellProps) {
  return <TD {...rest} className={cn("workspace-table__cell workspace-table__cell--end", className)}>{children}</TD>;
}

export function TDClip({ className, title, children, ...rest }: CellProps) {
  return (
    <TD
      {...rest}
      className={cn("workspace-table__cell max-w-0 truncate", className)}
      title={title ?? (typeof children === "string" ? children : undefined)}
    >
      {children}
    </TD>
  );
}
