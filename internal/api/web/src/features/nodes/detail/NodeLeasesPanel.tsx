import { RefreshCw } from "lucide-react";
import { Link } from "react-router-dom";
import { Button } from "../../../components/ui/Button";
import { Panel, PanelBody, PanelHeader } from "../../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../../components/ui/QueryState";
import { Table, TableWrap, TBody, TD, TDClip, TH, THead, TR } from "../../../components/ui/Table";
import { useI18n } from "../../../i18n";
import { useNow } from "../../../lib/clock";
import { cn } from "../../../lib/cn";
import { opsPaths } from "../../../lib/paths";
import { formatDateTime } from "../../../lib/time";
import {
  formatRemainingDuration,
  leaseFillPercent,
  leaseRemainingTone,
  parseGoDurationMs,
} from "./nodeMeta";
import type { NodeLeaseCollection, NodeLeaseRow } from "./api";

/**
 * The leases this node currently holds, one row per lease.
 *
 * There is no per-node lease endpoint on the server: the rows are gathered from
 * every platform's lease page and filtered by `node_hash` (see `collectNodeLeases`),
 * which is why the platform each lease belongs to is a column here — it is the
 * one fact the row cannot state about itself.
 *
 * The remaining-life bar is measured against the platform's sticky TTL; when a
 * platform gives no TTL and the row's own stamps cannot produce a window, the
 * bar is left out rather than guessed at.
 */

const BAR_CLASS = {
  signal: "lease-fill",
  warn: "lease-fill lease-fill--warn",
  alert: "lease-fill lease-fill--alert",
} as const;

/**
 * The window the remaining time is measured against: the platform's sticky TTL
 * when it is a duration, otherwise the span from the lease's last access to its
 * expiry when that span is positive.
 */
function leaseWindowMs(lease: NodeLeaseRow): number | null {
  const ttl = parseGoDurationMs(lease.sticky_ttl);
  if (ttl !== null && ttl > 0) {
    return ttl;
  }
  const expiryMs = Date.parse(lease.expiry || "");
  const accessedMs = Date.parse(lease.last_accessed || "");
  if (Number.isFinite(expiryMs) && Number.isFinite(accessedMs)) {
    const span = expiryMs - accessedMs;
    if (span > 0) {
      return span;
    }
  }
  return null;
}

function RemainingCell({ lease }: { lease: NodeLeaseRow }) {
  const { t } = useI18n();
  const now = useNow();
  const expiryMs = Date.parse(lease.expiry || "");
  if (!Number.isFinite(expiryMs)) {
    return <TD className="text-ink-faint">--</TD>;
  }
  const remainingMs = expiryMs - now;
  const tone = leaseRemainingTone(remainingMs);
  const percent = leaseFillPercent(remainingMs, leaseWindowMs(lease));
  const expired = remainingMs <= 0;

  return (
    <TD>
      <div className="flex items-center gap-2.5">
        <span className={cn("readout whitespace-nowrap text-xs tabular-nums", expired && "text-alert")}>
          {expired ? t("已过期") : formatRemainingDuration(remainingMs)}
        </span>
        {percent !== null && (
          <span className="block min-w-16 flex-1" aria-hidden>
            <span className="lease-track block">
              <span
                className={cn("block", BAR_CLASS[tone])}
                style={{ width: `${expired ? 100 : percent}%` }}
              />
            </span>
          </span>
        )}
      </div>
    </TD>
  );
}

export function NodeLeasesPanel({
  collection,
  platformsReady,
  error,
  isFetching,
  onRefresh,
  onRetry,
}: {
  collection: NodeLeaseCollection | undefined;
  /** True once the platform list behind the scan has loaded. */
  platformsReady: boolean;
  error: unknown;
  isFetching: boolean;
  onRefresh: () => void;
  onRetry: () => void;
}) {
  const { t } = useI18n();
  const leases = collection?.leases ?? [];

  return (
    <Panel>
      <PanelHeader
        title={t("节点租约")}
        meta={collection ? t("共 {{count}} 条", { count: leases.length }) : undefined}
        actions={
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("刷新")}
            title={t("刷新")}
            disabled={isFetching}
            onClick={onRefresh}
          >
            <RefreshCw size={15} aria-hidden className={isFetching ? "animate-spin" : undefined} />
          </Button>
        }
      />
      <PanelBody className="space-y-3">
        {error ? (
          <ErrorState message={t("租约信息暂时不可用")} onRetry={onRetry} />
        ) : !platformsReady ? (
          <LoadingState />
        ) : leases.length === 0 ? (
          <EmptyState title={t("暂无租约")} hint={t("没有任何平台的租约正落在这个节点上。")} />
        ) : (
          <>
            {collection && collection.scanned < collection.totalPlatforms && (
              <p className="text-xs text-ink-faint">
                {t("平台数量超出扫描上限，已扫描 {{scanned}} / {{total}} 个平台。", {
                  scanned: collection.scanned,
                  total: collection.totalPlatforms,
                })}
              </p>
            )}
            <TableWrap>
              <Table density="compact">
                <THead>
                  <TR>
                    <TH>{t("租约号")}</TH>
                    <TH>{t("租户")}</TH>
                    <TH>{t("平台")}</TH>
                    <TH>{t("到期时间")}</TH>
                    <TH>{t("剩余时间")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {leases.map((lease) => (
                    <TR key={`${lease.platform_id}:${lease.account}`}>
                      <TDClip title={lease.account || undefined}>
                        <code className="readout">{lease.account || "--"}</code>
                      </TDClip>
                      <TDClip title={lease.user_id || undefined}>{lease.user_id || "--"}</TDClip>
                      <TDClip>
                        <Link
                          to={`${opsPaths.platforms}/${encodeURIComponent(lease.platform_id)}`}
                          className="link-quiet"
                        >
                          {lease.platform_name || lease.platform_id}
                        </Link>
                      </TDClip>
                      <TD className="whitespace-nowrap">{formatDateTime(lease.expiry)}</TD>
                      <RemainingCell lease={lease} />
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          </>
        )}
      </PanelBody>
    </Panel>
  );
}
