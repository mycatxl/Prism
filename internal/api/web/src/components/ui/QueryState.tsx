import { AlertTriangle } from "lucide-react";
import type { ReactNode } from "react";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { ControlButton, EmptyWorkState } from "../control-plane";

export function LoadingState({ label, className }: { label?: string; className?: string }) {
  const { t } = useI18n();
  return (
    <div className={cn("workspace-state workspace-state--loading", className)} role="status">
      <div aria-hidden className="workspace-state__skeleton">
        <span />
        <span />
        <span />
      </div>
      <span>{label ? t(label) : t("读取中")}</span>
    </div>
  );
}

export function ErrorState({ message, onRetry, className }: { message: ReactNode; onRetry?: () => void; className?: string }) {
  const { t } = useI18n();
  return (
    <div className={cn("workspace-state workspace-state--error", className)} role="alert">
      <div className="workspace-state__message">
        <AlertTriangle size={16} aria-hidden />
        <div>{message}</div>
      </div>
      {onRetry ? <ControlButton size="sm" variant="secondary" onPress={onRetry}>{t("重试")}</ControlButton> : null}
    </div>
  );
}

export function EmptyState({ title, hint, action, className }: { title: ReactNode; hint?: ReactNode; action?: ReactNode; className?: string }) {
  return <EmptyWorkState embedded title={title} detail={hint} action={action} className={className} />;
}
export type QueryPhase =
  | "loading"
  | "ready"
  | "revalidating"
  | "stale"
  | "empty"
  | "partial"
  | "offline"
  | "reconnecting"
  | "permission-denied"
  | "error";

type QueryStateProps = {
  phase: QueryPhase;
  children?: ReactNode;
  title?: ReactNode;
  message?: ReactNode;
  action?: ReactNode;
  onRetry?: () => void;
  className?: string;
};

export function QueryState({ phase, children, title, message, action, onRetry, className }: QueryStateProps) {
  const { t } = useI18n();

  if (phase === "ready") return <>{children}</>;
  if (phase === "loading") return <LoadingState label={typeof message === "string" ? message : undefined} className={className} />;
  if (phase === "error" || phase === "permission-denied") {
    return <ErrorState message={message ?? (phase === "permission-denied" ? t("无权查看此内容") : t("读取失败"))} onRetry={onRetry} className={className} />;
  }
  if (phase === "empty") {
    return <EmptyState title={title ?? t("暂无数据")} hint={message} action={action} className={className} />;
  }

  const phaseMessage: Record<Exclude<QueryPhase, "loading" | "ready" | "empty" | "error" | "permission-denied">, ReactNode> = {
    revalidating: t("正在刷新"),
    stale: t("数据可能已过期"),
    partial: t("部分数据可用"),
    offline: t("网络不可用"),
    reconnecting: t("正在重新连接"),
  };

  return (
    <div className={cn("workspace-query-state", `workspace-query-state--${phase}`, className)} role="status">
      <span>{message ?? phaseMessage[phase]}</span>
      {action ?? (onRetry ? <ControlButton size="sm" variant="secondary" onPress={onRetry}>{t("重试")}</ControlButton> : null)}
    </div>
  );
}
