import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Globe2, ShieldCheck, Zap } from "lucide-react";
import { Link, useParams } from "react-router-dom";
import { Badge } from "../../../components/ui/Badge";
import { Button } from "../../../components/ui/Button";
import { Page, PageHeader, PageMeta } from "../../../components/ui/PageHeader";
import { Panel, PanelBody } from "../../../components/ui/Panel";
import { ErrorState, LoadingState } from "../../../components/ui/QueryState";
import { ToastContainer } from "../../../components/ui/Toast";
import { useToast } from "../../../hooks/useToast";
import { useI18n } from "../../../i18n";
import { formatApiErrorMessage } from "../../../lib/error-message";
import { opsPaths } from "../../../lib/paths";
import { getQualityStatus, inspectNode, qualityPollingInterval } from "../quality/api";
import { getRegionName } from "../regions";
import { getNode, probeEgress, probeLatency } from "../api";
import { listPlatforms } from "../../platforms/api";
import { NodeEventsPanel } from "./NodeEventsPanel";
import { NodeAttributesPanel } from "./NodeAttributesPanel";
import { NodeLeasesPanel } from "./NodeLeasesPanel";
import { NodeOverviewPanel } from "./NodeOverviewPanel";
import { NodeTrafficPanel } from "./NodeTrafficPanel";
import { collectNodeLeases, getIntelNodeDetail } from "./api";
import { nodeName, nodeStatus } from "./nodeMeta";

/**
 * The node detail page: the full-page counterpart of the node drawer on the
 * pool. The header carries the node's name and page-level actions; the main
 * column states how the node is doing (overview, traffic pulse, leases) and the
 * side column records what happened to it (events, attributes).
 *
 * Query keys are shared with the drawer where the drawer reads the same object
 * (`["node", hash]`), so a probe run on one surface refreshes the other.
 */
export function NodeDetailPage() {
  const { nodeId } = useParams<{ nodeId: string }>();
  const hash = (nodeId ?? "").trim();
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  // The quality service's own state drives the polling of the node record: an
  // inspection in flight means the record is about to change under us.
  const qualityStatus = useQuery({
    queryKey: ["quality", "status"],
    queryFn: getQualityStatus,
    refetchInterval: (query) => qualityPollingInterval(query.state.data),
  });

  const detailQuery = useQuery({
    queryKey: ["node", hash],
    queryFn: () => getNode(hash),
    enabled: Boolean(hash),
    refetchInterval: qualityPollingInterval(qualityStatus.data),
  });

  const detail = detailQuery.data;
  const state = detail ? nodeStatus(detail) : undefined;
  const regionName = detail?.region ? getRegionName(detail.region.toUpperCase()) : undefined;

  // The intel report is an addition to the record, not its basis: when it
  // fails, the page keeps the record's own events and says so.
  const intelQuery = useQuery({
    queryKey: ["intel-node", hash],
    queryFn: ({ signal }) => getIntelNodeDetail(hash, signal),
    enabled: Boolean(hash),
    staleTime: 30_000,
    retry: 1,
  });

  const platformsQuery = useQuery({
    queryKey: ["platforms", "node-filter"],
    queryFn: () => listPlatforms({ limit: 1000 }),
    enabled: Boolean(hash),
    staleTime: 60_000,
  });
  const platforms = platformsQuery.data?.items ?? [];
  const platformIdsKey = platforms.map((platform) => platform.id).join("|");

  const leasesQuery = useQuery({
    queryKey: ["node-leases", hash, platformIdsKey],
    queryFn: ({ signal }) => collectNodeLeases(hash, platforms, signal),
    enabled: Boolean(hash) && platforms.length > 0,
    staleTime: 30_000,
  });

  const probe = useMutation({
    mutationFn: ({ hash, kind }: { hash: string; kind: "egress" | "latency" }) =>
      kind === "egress" ? probeEgress(hash) : probeLatency(hash),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["nodes"] });
      void queryClient.invalidateQueries({ queryKey: ["node"] });
      void queryClient.invalidateQueries({ queryKey: ["intel-node"] });
      void queryClient.invalidateQueries({ queryKey: ["quality"] });
      showToast("success", t("探测已完成"));
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const runProbe = (kind: "egress" | "latency") => {
    if (!detail) return;
    probe.mutate({ hash: detail.node_hash, kind });
  };

  const qualityProbe = useMutation({
    mutationFn: inspectNode,
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: ["nodes"] });
      void queryClient.invalidateQueries({ queryKey: ["node"] });
      void queryClient.invalidateQueries({ queryKey: ["quality"] });
      void queryClient.invalidateQueries({ queryKey: ["intel-node"] });
      showToast("success", t(result.queued ? "检测已排队" : "已复用最近的检测结果"));
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const refresh = () => {
    void detailQuery.refetch();
    void intelQuery.refetch();
    void queryClient.invalidateQueries({ queryKey: ["node-leases", hash] });
    void queryClient.invalidateQueries({ queryKey: ["node-traffic"] });
  };

  const copy = async (value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      showToast("success", t("已复制"));
    } catch {
      showToast("error", t("无法复制，请手动选择文本"));
    }
  };

  const probeDisabled = !detail || !detail.has_outbound || probe.isPending;

  return (
    <Page bleed>
      <PageHeader
        title={detail ? nodeName(detail) : hash.slice(0, 12)}
        description={<code className="readout text-xs text-ink-faint">{hash || "--"}</code>}
        meta={
          detail && state ? (
            <>
              <Badge tone={state.tone} dot>
                {t(state.label)}
              </Badge>
              <PageMeta label={t("地区")} value={regionName ?? "--"} />
              <PageMeta label={t("出口 IP")} value={detail.egress_ip || "--"} />
            </>
          ) : undefined
        }
        actions={
          <>
            <Button variant="secondary" size="sm" asChild>
              <Link to={opsPaths.nodes}>
                <ArrowLeft size={15} aria-hidden />
                {t("返回列表")}
              </Link>
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={refresh}
              disabled={detailQuery.isFetching}
            >
              {t("刷新")}
            </Button>
            <Button
              size="sm"
              disabled={qualityProbe.isPending || !detail?.has_outbound || qualityStatus.data?.enabled === false}
              onClick={() => detail && qualityProbe.mutate(detail.node_hash)}
            >
              <ShieldCheck size={15} aria-hidden />
              {t("更新网络特征")}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={probeDisabled}
              onClick={() => runProbe("egress")}
            >
              <Globe2 size={15} aria-hidden />
              {t("出口探测")}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              disabled={probeDisabled}
              onClick={() => runProbe("latency")}
            >
              <Zap size={15} aria-hidden />
              {t("延迟探测")}
            </Button>
          </>
        }
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="page-content page-content--split">
        {!hash ? (
          <Panel>
            <PanelBody>
              <ErrorState message={t("节点 ID 缺失，无法加载详情。")} />
            </PanelBody>
          </Panel>
        ) : detailQuery.isError && !detail ? (
          <Panel>
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(detailQuery.error, t)}
                onRetry={() => void detailQuery.refetch()}
              />
            </PanelBody>
          </Panel>
        ) : detailQuery.isLoading && !detail ? (
          <Panel>
            <PanelBody>
              <LoadingState label={t("正在加载节点详情...")} />
            </PanelBody>
          </Panel>
        ) : detail ? (
          <>
            <div className="flex min-w-0 flex-col gap-4">
              <NodeOverviewPanel
                node={detail}
                purityScore={typeof detail.intel?.purity_score === "number" ? detail.intel.purity_score : null}
              />
              <NodeTrafficPanel egressIp={detail.egress_ip || ""} />
              <NodeLeasesPanel
                collection={leasesQuery.data}
                platformsReady={platformsQuery.isSuccess}
                error={platformsQuery.isError ? platformsQuery.error : leasesQuery.isError ? leasesQuery.error : null}
                isFetching={leasesQuery.isFetching}
                onRefresh={() => void leasesQuery.refetch()}
                onRetry={() => {
                  if (platformsQuery.isError) void platformsQuery.refetch();
                  else void leasesQuery.refetch();
                }}
              />
            </div>
            <div className="flex min-w-0 flex-col gap-4">
              <NodeEventsPanel
                node={detail}
                intel={intelQuery.data}
                intelUnavailable={intelQuery.isError}
                isFetchingIntel={intelQuery.isFetching}
                onRetryIntel={() => void intelQuery.refetch()}
              />
              <NodeAttributesPanel
                node={detail}
                intelDetail={intelQuery.data}
                onCopy={(value) => void copy(value)}
              />
            </div>
          </>
        ) : null}
      </div>
    </Page>
  );
}
