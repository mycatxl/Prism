import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RefreshCw, Unlink } from "lucide-react";
import { useState } from "react";
import { Button } from "../../components/ui/Button";
import {
  DialogContent,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "../../components/ui/Dialog";
import { PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Table, TableWrap, TBody, TD, TDClip, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { cn } from "../../lib/cn";
import { useNow } from "../../lib/clock";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatGoDuration } from "../../lib/time";
import { getLeases, releaseLease } from "./api";
import type { SelfLease } from "./types";

/** Leases carry RFC 3339 strings, which is what the routing table holds. */
function formatTimestamp(value: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}

/** The console-wide "soon" line: the last two days of a lease's life warn. */
const LEASE_WARN_MS = 48 * 3_600_000;

/** The state ramp of a lease's remaining life: signal, then warn, then alert. */
function remainingTone(remainingMs: number): "signal" | "warn" | "alert" {
  if (remainingMs <= 0) return "alert";
  if (remainingMs <= LEASE_WARN_MS) return "warn";
  return "signal";
}

/** The share of the lease's window that is still ahead, 0..100. */
function remainingPercent(remainingMs: number, windowMs: number): number {
  if (windowMs <= 0 || remainingMs <= 0) return 0;
  return Math.max(0, Math.min(100, (remainingMs / windowMs) * 100));
}

/** A remaining span in the house duration voice, re-encoded through formatGoDuration. */
function formatRemainingDuration(remainingMs: number): string {
  const totalSeconds = Math.max(0, Math.floor(remainingMs / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const parts: string[] = [];
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0) parts.push(`${minutes}m`);
  if (hours === 0 && seconds > 0) parts.push(`${seconds}s`);
  return formatGoDuration(parts.join("") || "0s");
}

/** The fill class of the remaining-life bar, one per state tone. */
const BAR_CLASS = {
  signal: "lease-fill",
  warn: "lease-fill lease-fill--warn",
  alert: "lease-fill lease-fill--alert",
} as const;

/**
 * The remaining-life cell: the figure states how long is left, and the bar
 * draws the same share of the window the row's own stamps produce (last
 * access to expiry). A window the row cannot produce is a bar that would
 * guess at its denominator, so the bar is left out rather than drawn.
 */
function RemainingCell({ lease }: { lease: SelfLease }) {
  const { t } = useI18n();
  const now = useNow();
  const expiryMs = Date.parse(lease.expiry || "");
  if (!Number.isFinite(expiryMs)) {
    return <TD className="text-ink-faint">--</TD>;
  }
  const remainingMs = expiryMs - now;
  const expired = remainingMs <= 0;
  const accessedMs = Date.parse(lease.last_accessed || "");
  const windowMs =
    Number.isFinite(accessedMs) && expiryMs - accessedMs > 0 ? expiryMs - accessedMs : 0;
  return (
    <TD>
      <div className="flex items-center gap-2.5">
        <span
          className={cn(
            "readout whitespace-nowrap text-xs tabular-nums",
            expired ? "text-alert" : "text-ink-soft",
          )}
        >
          {expired ? t("已过期") : formatRemainingDuration(remainingMs)}
        </span>
        {windowMs > 0 && (
          <span className="block min-w-16 flex-1" aria-hidden>
            <span className="lease-track block">
              <span
                className={cn("block", BAR_CLASS[remainingTone(remainingMs)])}
                style={{ width: `${expired ? 100 : remainingPercent(remainingMs, windowMs)}%` }}
              />
            </span>
          </span>
        )}
      </div>
    </TD>
  );
}

/**
 * LeasesPage shows a customer the sticky exits their account currently holds.
 *
 * Leases are the data plane's own state: an account stays pinned to one exit for
 * as long as the lease lasts, and releasing one is how a customer asks for a
 * different exit before it expires. The list is per account rather than per
 * connection, which is why the release is addressed by platform and account.
 */
export function LeasesPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [releaseTarget, setReleaseTarget] = useState<SelfLease | null>(null);

  const leasesQuery = useQuery({
    queryKey: ["me-leases"],
    queryFn: getLeases,
    // A lease expires on its own, so a list left open goes stale by itself.
    refetchInterval: 60_000,
  });

  const leases = leasesQuery.data?.items ?? [];

  const releaseMutation = useMutation({
    mutationFn: (lease: SelfLease) => releaseLease(lease.platform_id, lease.account),
    onSuccess: () => {
      showToast("success", t("租约已释放"));
      setReleaseTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["me-leases"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("我的租约")}
        description={t("查看当前粘定的出口，并在到期前主动释放。")}
        actions={
          <Button
            variant="secondary"
            onClick={() => void leasesQuery.refetch()}
            disabled={leasesQuery.isFetching}
            title={t("刷新")}
          >
            <RefreshCw
              size={14}
              className={leasesQuery.isFetching ? "animate-spin" : undefined}
            />
            <span>{t("刷新")}</span>
          </Button>
        }
      />

      <Panel>
        <PanelHeader
          title={t("当前租约")}
          description={t("同一账号在租约有效期内保持同一个出口 IP。")}
          meta={t("共 {{count}} 条", { count: leases.length })}
        />
        <PanelBody className="p-0">
          {leasesQuery.isLoading ? (
            <LoadingState label={t("正在加载租约...")} className="py-12" />
          ) : leasesQuery.isError ? (
            <ErrorState
              message={formatApiErrorMessage(leasesQuery.error, t)}
              onRetry={() => void leasesQuery.refetch()}
              className="py-12"
            />
          ) : leases.length === 0 ? (
            <EmptyState
              title={t("暂无租约")}
              hint={t("建立代理连接后，粘定的出口会显示在这里。")}
              className="py-12"
            />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("平台")}</TH>
                    <TH>{t("账号")}</TH>
                    <TH>{t("节点")}</TH>
                    <TH>{t("出口 IP")}</TH>
                    <TH>{t("到期时间")}</TH>
                    <TH>{t("剩余时间")}</TH>
                    <TH>{t("最后活跃")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {leases.map((lease) => (
                    <TR key={`${lease.platform_id}/${lease.account}`}>
                      <TD className="font-medium text-ink">
                        <TDClip>{lease.platform_name || lease.platform_id}</TDClip>
                      </TD>
                      <TD className="font-mono text-xs text-ink-soft">
                        <TDClip>{lease.account}</TDClip>
                      </TD>
                      <TD className="text-xs text-ink-soft">
                        <TDClip>{lease.node_tag || "-"}</TDClip>
                      </TD>
                      <TD className="readout font-mono text-xs text-ink">
                        {lease.egress_ip || "-"}
                      </TD>
                      <TD className="readout font-mono text-xs text-ink-soft">
                        {formatTimestamp(lease.expiry)}
                      </TD>
                      <RemainingCell lease={lease} />
                      <TD className="readout font-mono text-xs text-ink-soft">
                        {formatTimestamp(lease.last_accessed)}
                      </TD>
                      <TD className="text-right">
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setReleaseTarget(lease)}
                          className="text-alert hover:text-alert"
                          title={t("释放此租约")}
                        >
                          <Unlink size={14} />
                          <span>{t("释放")}</span>
                        </Button>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          )}
        </PanelBody>
        <PanelFooter className="flex items-center justify-between">
          <span className="readout text-xs text-ink-soft">
            {t("释放后下一次连接会重新选择出口。")}
          </span>
        </PanelFooter>
      </Panel>

      <DialogRoot
        open={Boolean(releaseTarget)}
        onOpenChange={(open) => !open && setReleaseTarget(null)}
      >
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md p-6">
            <DialogTitle className="text-base font-semibold text-ink">
              {t("确认释放租约")}
            </DialogTitle>
            <p className="mt-3 text-sm text-ink-soft">
              {t(
                "确定要释放账号 {{account}} 的租约吗？释放后该账号下一次连接会重新选择出口。",
                { account: releaseTarget?.account ?? "" },
              )}
            </p>
            <div className="mt-6 flex items-center justify-end gap-3">
              <Button
                variant="secondary"
                onClick={() => setReleaseTarget(null)}
                disabled={releaseMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                variant="danger"
                onClick={() => {
                  if (releaseTarget) releaseMutation.mutate(releaseTarget);
                }}
                loading={releaseMutation.isPending}
              >
                {t("确认释放")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
  );
}
