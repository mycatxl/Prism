import type { ReactNode } from "react";
import { LoaderCircle } from "lucide-react";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatDateTime, formatRelativeTime } from "../../lib/time";
import { Button } from "./Button";

export function DataFreshnessBar({
  range,
  updatedAt,
  isFetching,
  error,
  onRetry,
  children,
  className,
}: {
  range?: ReactNode;
  updatedAt?: string | number | Date | null;
  isFetching?: boolean;
  error?: ReactNode;
  onRetry?: () => void;
  children?: ReactNode;
  className?: string;
}) {
  const { t } = useI18n();
  const stamp = updatedAt instanceof Date ? updatedAt.getTime() : updatedAt;
  const timestamp = stamp === undefined || stamp === null ? Number.NaN : new Date(stamp).getTime();
  const hasValidTimestamp = Number.isFinite(timestamp) && timestamp > 0;
  const isoTimestamp = hasValidTimestamp ? new Date(timestamp).toISOString() : undefined;
  const updatedLabel = isoTimestamp ? formatRelativeTime(isoTimestamp) : "—";
  const updatedTitle = isoTimestamp ? formatDateTime(isoTimestamp) : undefined;

  return (
    <div className={cn("data-freshness-bar", className)} role={error ? "alert" : undefined}>
      <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
        {range && <span className="data-freshness-bar__range">{range}</span>}
        <span className="data-freshness-bar__age" title={updatedTitle}>
          <span className="micro">{t("最近更新")}</span>
          <span className="readout">{updatedLabel}</span>
        </span>
        {isFetching && (
          <span className="data-freshness-bar__fetching" role="status">
            <LoaderCircle size={13} aria-hidden className="animate-spin" />
            {t("更新中")}
          </span>
        )}
        {children}
      </div>
      {error && (
        <div className="data-freshness-bar__error">
          <span className="text-xs text-alert">{error}</span>
          {onRetry && (
            <Button type="button" size="sm" variant="secondary" onClick={onRetry}>
              {t("重试")}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
