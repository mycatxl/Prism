import { useQuery } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { useMemo, useState } from "react";
import { Button } from "../../../components/ui/Button";
import { Panel, PanelBody, PanelHeader, PanelToolbar } from "../../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../../components/ui/Readout";
import { Select } from "../../../components/ui/Select";
import { Sparkline } from "../../../components/ui/Sparkline";
import { useI18n } from "../../../i18n";
import { formatApiErrorMessage } from "../../../lib/error-message";
import { listRequestLogs } from "../../requestLogs/api";
import type { RequestLogItem } from "../../requestLogs/types";

/**
 * The node's traffic pulse. There is no per-node metric on the server: the
 * request log is the one place a node's traffic is recorded, and it can be
 * filtered by the node's egress IP. So this panel aggregates request-log rows
 * into time buckets and says plainly that it is a sample of the retained log,
 * never a claim about the platform's traffic as a whole.
 */

const TRAFFIC_SAMPLE_LIMIT = 5_000;

type TrafficWindow = {
  key: string;
  label: string;
  ms: number;
  /** How many buckets the window is divided into. */
  buckets: number;
};

const TRAFFIC_WINDOWS: TrafficWindow[] = [
  { key: "48h", label: "最近 48 小时", ms: 48 * 3_600_000, buckets: 48 },
  { key: "7d", label: "最近 7 天", ms: 7 * 24 * 3_600_000, buckets: 28 },
];

type TrafficSummary = {
  counts: number[];
  requests: number;
  failed: number;
  sampled: number;
  hasMore: boolean;
};

/** Buckets the sampled rows into `window.buckets` counts, oldest first. */
function summarise(items: RequestLogItem[], windowMs: number, buckets: number): TrafficSummary {
  const now = Date.now();
  const startMs = now - windowMs;
  const width = windowMs / buckets;
  const counts = new Array<number>(buckets).fill(0);
  let requests = 0;
  let failed = 0;
  for (const item of items) {
    const ts = Date.parse(item.ts);
    if (!Number.isFinite(ts) || ts < startMs) continue;
    const index = Math.min(buckets - 1, Math.floor((ts - startMs) / width));
    counts[index] += 1;
    requests += 1;
    if (!item.net_ok) failed += 1;
  }
  return { counts, requests, failed, sampled: items.length, hasMore: false };
}

export function NodeTrafficPanel({ egressIp }: { egressIp: string }) {
  const { t } = useI18n();
  const [windowKey, setWindowKey] = useState(TRAFFIC_WINDOWS[0].key);
  const option = TRAFFIC_WINDOWS.find((entry) => entry.key === windowKey) ?? TRAFFIC_WINDOWS[0];

  const query = useQuery({
    queryKey: ["node-traffic", egressIp, option.key],
    queryFn: () =>
      listRequestLogs({
        egress_ip: egressIp,
        from: new Date(Date.now() - option.ms).toISOString(),
        limit: TRAFFIC_SAMPLE_LIMIT,
      }),
    enabled: Boolean(egressIp),
    staleTime: 60_000,
  });

  const summary = useMemo<TrafficSummary | null>(() => {
    if (!query.data) return null;
    const result = summarise(query.data.items, option.ms, option.buckets);
    return { ...result, hasMore: query.data.has_more };
  }, [query.data, option]);

  return (
    <Panel>
      <PanelHeader
        title={t("流量脉冲")}
        description={t("按出口 IP 聚合的请求日志采样，不是平台级流量。")}
      />
      {!egressIp ? (
        <PanelBody>
          <EmptyState
            title={t("该节点暂无出口 IP")}
            hint={t("取得出口 IP 后，这里按请求日志统计该节点的流量。")}
          />
        </PanelBody>
      ) : (
        <>
          <PanelToolbar>
            <label className="flex items-center gap-1.5 text-xs text-ink-soft" htmlFor="node-traffic-window">
              <span>{t("时间范围")}</span>
              <Select
                id="node-traffic-window"
                className="w-auto"
                value={option.key}
                onChange={(event) => setWindowKey(event.target.value)}
              >
                {TRAFFIC_WINDOWS.map((entry) => (
                  <option key={entry.key} value={entry.key}>
                    {t(entry.label)}
                  </option>
                ))}
              </Select>
            </label>
            <Button
              variant="ghost"
              size="icon"
              aria-label={t("刷新")}
              title={t("刷新")}
              disabled={query.isFetching}
              onClick={() => void query.refetch()}
            >
              <RefreshCw size={15} aria-hidden className={query.isFetching ? "animate-spin" : undefined} />
            </Button>
          </PanelToolbar>
          <PanelBody className="space-y-3">
            {query.isError && (
              <ErrorState
                message={formatApiErrorMessage(query.error, t)}
                onRetry={() => void query.refetch()}
              />
            )}
            {query.isLoading && <LoadingState />}
            {summary &&
              (summary.requests === 0 ? (
                <EmptyState
                  title={t("窗口内没有请求记录")}
                  hint={t("该出口 IP 在所选时间范围内没有留下请求日志。")}
                />
              ) : (
                <>
                  {summary.hasMore && (
                    <p className="text-xs text-ink-faint">
                      {t("日志量超出单次上限，以下为最近 {{count}} 条的采样", { count: summary.sampled })}
                    </p>
                  )}
                  <ReadoutStrip className="grid grid-cols-2 sm:grid-cols-3">
                    <ReadoutCell>
                      <Readout
                        label={t("请求数")}
                        value={summary.requests.toLocaleString()}
                        hint={t("按出口 IP 过滤的日志行")}
                      />
                    </ReadoutCell>
                    <ReadoutCell>
                      <Readout
                        label={t("失败请求")}
                        value={summary.failed.toLocaleString()}
                        tone={summary.failed > 0 ? "warn" : "ink"}
                      />
                    </ReadoutCell>
                    <ReadoutCell className="hidden sm:block">
                      <Readout
                        label={t("采样窗口")}
                        value={t(option.label)}
                        hint={t("每段为一格，从左到右由旧到新")}
                      />
                    </ReadoutCell>
                  </ReadoutStrip>
                  <Sparkline values={summary.counts} tone="accent" height={44} />
                </>
              ))}
          </PanelBody>
        </>
      )}
    </Panel>
  );
}
