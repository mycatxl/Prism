import { ChevronLeft, ChevronRight } from "lucide-react";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { Button } from "./Button";
import { Select } from "./Select";

/**
 * One pagination for the whole console.
 *
 * The console used to carry five of these, one of them a bare offset pair with
 * no total, and every one of them hid the count. This one always states the
 * range and the total, because "how many are there" is the first question an
 * operator asks of a filtered list.
 */
export function Pagination({
  total,
  limit,
  offset,
  onOffsetChange,
  onLimitChange,
  pageSizes = [20, 50, 100],
  className,
}: {
  total: number;
  limit: number;
  offset: number;
  onOffsetChange: (offset: number) => void;
  onLimitChange?: (limit: number) => void;
  pageSizes?: number[];
  className?: string;
}) {
  const { t } = useI18n();
  const pages = Math.max(1, Math.ceil(total / Math.max(1, limit)));
  const page = Math.min(pages, Math.floor(offset / Math.max(1, limit)) + 1);
  const from = total === 0 ? 0 : offset + 1;
  const to = Math.min(total, offset + limit);

  // A window of five around the current page, with the ends pinned.
  const first = Math.max(1, Math.min(page - 2, pages - 4));
  const window = Array.from({ length: Math.min(5, pages) }, (_, index) => first + index).filter(
    (value) => value >= 1 && value <= pages,
  );

  const goTo = (target: number) => onOffsetChange((Math.min(pages, Math.max(1, target)) - 1) * limit);

  return (
    <nav aria-label={t("分页")} className={cn("flex flex-wrap items-center justify-between gap-3 px-1 py-2", className)}>
      <p className="text-xs text-ink-soft">
        <span className="readout text-ink">
          {from}–{to}
        </span>{" "}
        <span>{t("共")}</span> <span className="readout text-ink">{total}</span> <span>{t("条")}</span>
      </p>

      <div className="flex flex-wrap items-center gap-2">
        {onLimitChange && (
          <label className="flex items-center gap-1.5 text-xs text-ink-soft">
            <span>{t("每页")}</span>
            <Select
              className="h-[var(--control-h-sm)] w-auto py-0 pr-6 pl-2 text-xs"
              value={String(limit)}
              onChange={(event) => onLimitChange(Number(event.target.value))}
              aria-label={t("每页")}
            >
              {pageSizes.map((size) => (
                <option key={size} value={size}>
                  {size}
                </option>
              ))}
            </Select>
          </label>
        )}

        <div className="flex items-center gap-0.5">
          <Button
            size="icon"
            variant="ghost"
            onClick={() => goTo(page - 1)}
            disabled={page <= 1}
            aria-label={t("上一页")}
            title={t("上一页")}
          >
            <ChevronLeft size={15} aria-hidden />
          </Button>

          {first > 1 && (
            <>
              <PageButton active={false} onClick={() => goTo(1)}>
                1
              </PageButton>
              {first > 2 && <span className="px-1 text-xs text-ink-faint">…</span>}
            </>
          )}

          {window.map((value) => (
            <PageButton key={value} active={value === page} onClick={() => goTo(value)}>
              {value}
            </PageButton>
          ))}

          {first + window.length - 1 < pages && (
            <>
              {first + window.length < pages && <span className="px-1 text-xs text-ink-faint">…</span>}
              <PageButton active={false} onClick={() => goTo(pages)}>
                {pages}
              </PageButton>
            </>
          )}

          <Button
            size="icon"
            variant="ghost"
            onClick={() => goTo(page + 1)}
            disabled={page >= pages}
            aria-label={t("下一页")}
            title={t("下一页")}
          >
            <ChevronRight size={15} aria-hidden />
          </Button>
        </div>
      </div>
    </nav>
  );
}

function PageButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <Button
      size="sm"
      variant={active ? "secondary" : "ghost"}
      onClick={onClick}
      aria-current={active ? "page" : undefined}
      className={cn("min-w-[var(--control-h-sm)] px-1.5 tabular-nums", active && "border-accent text-accent")}
    >
      {children}
    </Button>
  );
}
