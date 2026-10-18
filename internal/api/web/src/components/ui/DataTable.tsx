import { ArrowDown, ArrowUp, ChevronsUpDown } from "lucide-react";
import type { ReactNode } from "react";
import { useEffect, useRef } from "react";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { Checkbox } from "./Checkbox";
import { EmptyState, ErrorState } from "./QueryState";

export type Column<T> = {
  id: string;
  header: string;
  cell: (row: T) => ReactNode;
  align?: "start" | "end";
  /** A CSS width for the column, e.g. `"12rem"` or `"minmax(0,2fr)"`. */
  width?: string;
  sortable?: boolean;
  /** Figures and identifiers are set in the mono face and flush to the end. */
  mono?: boolean;
  className?: string;
};

export type DataTableProps<T> = {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  caption: string;
  loading?: boolean;
  error?: { message: ReactNode; retry?: () => void } | null;
  empty?: { title: string; hint?: ReactNode; action?: ReactNode };
  sort?: string;
  dir?: "asc" | "desc";
  onSort?: (id: string) => void;
  selected?: Set<string>;
  onSelectedChange?: (next: Set<string>) => void;
  bulkActions?: (ids: string[]) => ReactNode;
  rowActions?: (row: T) => ReactNode;
  /** Compact rows for reference tables; 40px by default. */
  dense?: boolean;
  className?: string;
};

/**
 * The console's one table.
 *
 * It owns the four states a list actually has — loading, failed, empty, filled —
 * plus selection, sorting and per-row actions, so no page re-invents them. A
 * sortable header is a real button inside its `th`, carrying `aria-sort`, because
 * a clickable `div` is invisible to the keyboard.
 */
export function DataTable<T>({
  columns,
  rows,
  rowKey,
  caption,
  loading = false,
  error = null,
  empty,
  sort,
  dir = "desc",
  onSort,
  selected,
  onSelectedChange,
  bulkActions,
  rowActions,
  dense = false,
  className,
}: DataTableProps<T>) {
  const { t } = useI18n();
  const selectable = Boolean(selected && onSelectedChange);
  const columnCount = columns.length + (selectable ? 1 : 0) + (rowActions ? 1 : 0);
  const selectedIds = selected ? [...selected] : [];
  const allOnPageSelected = rows.length > 0 && rows.every((row) => selected?.has(rowKey(row)));

  const headerCheckbox = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (headerCheckbox.current) {
      headerCheckbox.current.indeterminate = selectedIds.length > 0 && !allOnPageSelected;
    }
  }, [allOnPageSelected, selectedIds.length]);

  const toggleAll = () => {
    if (!onSelectedChange) return;
    const next = new Set(selected);
    if (allOnPageSelected) {
      for (const row of rows) next.delete(rowKey(row));
    } else {
      for (const row of rows) next.add(rowKey(row));
    }
    onSelectedChange(next);
  };

  const toggleRow = (id: string) => {
    if (!onSelectedChange) return;
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    onSelectedChange(next);
  };

  const rowHeight = dense ? "h-[var(--row-h-compact)]" : "h-[var(--row-h-comfortable)]";

  return (
    <div className={cn("flex min-w-0 flex-col", className)}>
      {selectable && selectedIds.length > 0 && (
        <div className="flex flex-wrap items-center gap-3 border-b border-rule bg-accent-wash px-3 py-2 text-xs text-ink">
          <span>
            {t("已选")} <span className="readout font-semibold">{selectedIds.length}</span> {t("条")}
          </span>
          <span className="flex-1" />
          {bulkActions?.(selectedIds)}
          <button
            type="button"
            className="text-accent underline underline-offset-2 hover:text-accent-deep"
            onClick={() => onSelectedChange?.(new Set())}
          >
            {t("清除选择")}
          </button>
        </div>
      )}

      <div className="overflow-x-auto">
        <table className="w-full min-w-[40rem] border-collapse text-left text-sm">
          <caption className="sr-only">{caption}</caption>
          <colgroup>
            {selectable && <col style={{ width: "2.25rem" }} />}
            {columns.map((column) => (
              <col key={column.id} style={column.width ? { width: column.width } : undefined} />
            ))}
            {rowActions && <col style={{ width: "1%" }} />}
          </colgroup>

          <thead>
            <tr className="border-b border-rule bg-paper-inset">
              {selectable && (
                <th scope="col" className="px-3 py-0">
                  <Checkbox
                    ref={headerCheckbox}
                    checked={allOnPageSelected}
                    onChange={toggleAll}
                    aria-label={t("选择本页全部")}
                  />
                </th>
              )}
              {columns.map((column) => {
                const active = sort === column.id;
                const ariaSort = active ? (dir === "asc" ? "ascending" : "descending") : column.sortable ? "none" : undefined;
                return (
                  <th
                    key={column.id}
                    scope="col"
                    aria-sort={ariaSort}
                    className={cn(
                      "h-[var(--control-h-lg)] px-3 text-2xs font-semibold tracking-[0.04em] whitespace-nowrap text-ink-faint uppercase",
                      column.align === "end" && "text-right",
                      column.className,
                    )}
                  >
                    {column.sortable && onSort ? (
                      <button
                        type="button"
                        onClick={() => onSort(column.id)}
                        className={cn(
                          "inline-flex items-center gap-1 rounded-control px-1 py-0.5 -mx-1 hover:text-ink",
                          active && "text-ink",
                          column.align === "end" && "flex-row-reverse",
                        )}
                      >
                        {column.header}
                        {active ? (
                          dir === "asc" ? (
                            <ArrowUp size={12} aria-hidden />
                          ) : (
                            <ArrowDown size={12} aria-hidden />
                          )
                        ) : (
                          <ChevronsUpDown size={12} aria-hidden className="text-ink-faint" />
                        )}
                      </button>
                    ) : (
                      column.header
                    )}
                  </th>
                );
              })}
              {rowActions && <th scope="col" className="px-3" aria-label={t("行操作")} />}
            </tr>
          </thead>

          <tbody>
            {error ? (
              <tr>
                <td colSpan={columnCount} className="px-3 py-4">
                  <ErrorState message={error.message} onRetry={error.retry} />
                </td>
              </tr>
            ) : loading ? (
              Array.from({ length: 6 }, (_, index) => (
                <tr key={index} className="border-b border-row-rule last:border-0">
                  {selectable && (
                    <td className="px-3">
                      <div className="size-4 rounded-[2px] border border-rule-faint" aria-hidden />
                    </td>
                  )}
                  {columns.map((column, columnIndex) => (
                    <td key={column.id} className="px-3 py-3">
                      <div
                        aria-hidden
                        className="h-2.5 rounded-control border border-rule-faint bg-paper-inset"
                        style={{ width: `${72 - columnIndex * 9}%` }}
                      />
                    </td>
                  ))}
                  {rowActions && <td className="px-3" />}
                </tr>
              ))
            ) : rows.length === 0 ? (
              <tr>
                <td colSpan={columnCount} className="px-3">
                  <EmptyState
                    title={empty?.title ?? t("没有记录")}
                    hint={empty?.hint}
                    action={empty?.action}
                  />
                </td>
              </tr>
            ) : (
              rows.map((row) => {
                const id = rowKey(row);
                const isSelected = selected?.has(id) ?? false;
                return (
                  <tr
                    key={id}
                    className={cn(
                      "border-b border-row-rule transition-colors last:border-0 hover:bg-hover",
                      isSelected && "bg-accent-wash/60",
                    )}
                  >
                    {selectable && (
                      <td className={cn("px-3", rowHeight)}>
                        <Checkbox checked={isSelected} onChange={() => toggleRow(id)} aria-label={t("选择行")} />
                      </td>
                    )}
                    {columns.map((column) => (
                      <td
                        key={column.id}
                        className={cn(
                          rowHeight,
                          "px-3 align-middle",
                          column.align === "end" && "text-right",
                          column.mono && "readout text-xs",
                          column.className,
                        )}
                      >
                        {column.cell(row)}
                      </td>
                    ))}
                    {rowActions && (
                      <td className={cn(rowHeight, "px-3 text-right whitespace-nowrap")}>{rowActions(row)}</td>
                    )}
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
