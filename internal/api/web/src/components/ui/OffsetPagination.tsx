import { ChevronLeft, ChevronRight } from "lucide-react";
import { Button } from "./Button";
import { Input } from "./Input";
import { Select } from "./Select";
import { useI18n } from "../../i18n";

type OffsetPaginationProps = {
  page: number;
  totalPages: number;
  totalItems: number;
  pageSize: number;
  pageSizeOptions: readonly number[];
  disabled?: boolean;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
};

export function OffsetPagination({
  page,
  totalPages,
  totalItems,
  pageSize,
  pageSizeOptions,
  disabled = false,
  onPageChange,
  onPageSizeChange,
}: OffsetPaginationProps) {
  const { t } = useI18n();
  const pages = Math.max(1, totalPages);
  const current = Math.min(Math.max(0, page), pages - 1);
  const jump = (raw: string) => {
    const value = Number(raw);
    if (Number.isInteger(value) && value > 0)
      onPageChange(Math.max(0, Math.min(pages - 1, value - 1)));
  };
  return (
    <div className="nodes-pagination">
      <p className="nodes-pagination-meta">
        {t("第 {{page}} / {{pages}} 页 · 显示 {{start}}-{{end}} / {{total}}", {
          page: current + 1,
          pages,
          start: totalItems ? current * pageSize + 1 : 0,
          end: Math.min((current + 1) * pageSize, totalItems),
          total: totalItems,
        })}
      </p>
      <div className="nodes-pagination-controls">
        <label className="nodes-page-size">
          <span>{t("每页")}</span>
          <Select
            value={pageSize}
            disabled={disabled}
            onChange={(event) => onPageSizeChange(Number(event.target.value))}
          >
            {pageSizeOptions.map((size) => (
              <option key={size} value={size}>
                {size}
              </option>
            ))}
          </Select>
        </label>
        <label className="nodes-page-jump">
          <span>{t("跳至")}</span>
          <Input
            key={current}
            type="number"
            inputMode="numeric"
            min={1}
            max={pages}
            defaultValue={current + 1}
            aria-label={t("选择页码")}
            disabled={disabled}
            onKeyDown={(event) => {
              if (event.key === "Enter") jump(event.currentTarget.value);
            }}
            onBlur={(event) => {
              jump(event.currentTarget.value);
            }}
          />
        </label>
        <Button
          variant="ghost"
          size="sm"
          className="icon-button"
          aria-label={t("上一页")}
          title={t("上一页")}
          disabled={disabled || current === 0}
          onClick={() => onPageChange(current - 1)}
        >
          <ChevronLeft size={16} />
        </Button>
        <Button
          variant="ghost"
          size="sm"
          className="icon-button"
          aria-label={t("下一页")}
          title={t("下一页")}
          disabled={disabled || current >= pages - 1}
          onClick={() => onPageChange(current + 1)}
        >
          <ChevronRight size={16} />
        </Button>
      </div>
    </div>
  );
}
