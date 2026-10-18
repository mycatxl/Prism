import { X } from "lucide-react";
import type { ReactNode } from "react";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { Button } from "./Button";

export type FilterChip = {
  label: string;
  value: ReactNode;
  onClear: () => void;
};

/**
 * The filter strip every list page shares.
 *
 * Filters live in the URL (see `useTableState`), so what is on screen can be
 * pasted into a ticket. The chips restate the active filters in words, because a
 * filled select is easy to miss and "why is this list short" is the commonest
 * confusion in a console.
 */
export function FilterBar({
  children,
  chips = [],
  isFiltered = false,
  onReset,
  trailing,
  className,
}: {
  children?: ReactNode;
  chips?: FilterChip[];
  isFiltered?: boolean;
  onReset?: () => void;
  trailing?: ReactNode;
  className?: string;
}) {
  const { t } = useI18n();
  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <div className="flex flex-wrap items-center gap-2">
        {children}
        <span className="flex-1" />
        {isFiltered && onReset && (
          <Button size="sm" variant="quiet" onClick={onReset}>
            {t("清除筛选")}
          </Button>
        )}
        {trailing}
      </div>

      {chips.length > 0 && (
        <ul className="flex flex-wrap items-center gap-1.5" aria-label={t("当前筛选")}>
          {chips.map((chip) => (
            <li key={chip.label}>
              <button
                type="button"
                onClick={chip.onClear}
                className="inline-flex items-center gap-1.5 rounded-chip border border-rule bg-paper-inset px-2 py-0.5 text-2xs text-ink-soft hover:border-rule-strong hover:text-ink"
              >
                <span className="text-ink-faint">{chip.label}</span>
                <span className="font-medium text-ink">{chip.value}</span>
                <X size={11} aria-hidden />
                <span className="sr-only">{t("移除筛选")}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
