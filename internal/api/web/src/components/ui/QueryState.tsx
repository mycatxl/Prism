import { AlertCircle, Inbox, RefreshCw } from "lucide-react";
import { useI18n } from "../../i18n";
import { Button } from "./Button";

type QueryStateProps = {
  loading?: boolean;
  error?: unknown;
  empty?: boolean;
  emptyText?: string;
  onRetry?: () => void;
};

export function QueryState({
  loading,
  error,
  empty,
  emptyText,
  onRetry,
}: QueryStateProps) {
  const { t } = useI18n();
  if (loading)
    return (
      <div className="query-skeleton" role="status" aria-label={t("正在加载")}>
        {[1, 2, 3].map((i) => (
          <span key={i} />
        ))}
      </div>
    );
  if (error)
    return (
      <div className="query-state query-error" role="alert">
        <AlertCircle size={20} />
        <div>
          <strong>{t("数据暂时不可用")}</strong>
          <p>{t("请检查连接后重试。")}</p>
        </div>
        {onRetry && (
          <Button variant="secondary" onClick={onRetry}>
            <RefreshCw size={14} />
            {t("重试")}
          </Button>
        )}
      </div>
    );
  if (empty)
    return (
      <div className="query-state">
        <Inbox size={22} />
        <span>{emptyText ?? t("暂无记录")}</span>
      </div>
    );
  return null;
}
