import { Badge } from "../../../components/ui/Badge";
import { Panel, PanelBody, PanelHeader } from "../../../components/ui/Panel";
import { ErrorState } from "../../../components/ui/QueryState";
import { Stat, StatStrip } from "../../../components/ui/StatStrip";
import { useI18n } from "../../../i18n";
import { cn } from "../../../lib/cn";
import { formatDateTime, formatRelativeTime } from "../../../lib/time";
import { purityBands } from "../quality/presentation";
import { getRegionName } from "../regions";
import type { NodeSummary } from "../types";
import { nodeStatus, protocolLabel } from "./nodeMeta";

/**
 * The operational summary of one node: its state, its egress, its last probe
 * activity and its purity score, in one band of figures. Identity facts (hash,
 * ASN, flags, subscription sources…) live in the attributes panel; this panel
 * answers "is the node working and when was it last checked".
 */
export function NodeOverviewPanel({
  node,
  purityScore,
}: {
  node: NodeSummary;
  /** The purity score the intel report carries, when one exists. */
  purityScore: number | null;
}) {
  const { t } = useI18n();
  const state = nodeStatus(node);
  const band = purityScore === null ? undefined : purityBands.find((entry) => purityScore >= entry.min && purityScore <= entry.max);
  const regionName = node.region ? getRegionName(node.region.toUpperCase()) : undefined;

  return (
    <Panel>
      <PanelHeader
        title={t("节点概要")}
        meta={
          <Badge tone={state.tone} dot>
            {t(state.label)}
          </Badge>
        }
      />
      <PanelBody className="space-y-3">
        <StatStrip className="sm:grid-cols-2 lg:grid-cols-4">
          <Stat
            label={t("状态")}
            value={
              <span className="flex items-center gap-2">
                <span
                  aria-hidden
                  className={cn("state-dot", state.tone !== "neutral" && `state-dot--${state.tone}`)}
                />
                <span className="state-text">{t(state.label)}</span>
              </span>
            }
          />
          <Stat
            label={t("参考延迟")}
            value={node.reference_latency_ms === undefined ? "--" : `${Math.round(node.reference_latency_ms)} ms`}
          />
          <Stat label={t("连续失败")} value={node.failure_count} />
          <Stat label={t("出口 IP")} value={node.egress_ip || "--"} hint={node.egress_ip ? undefined : t("未获取到出口 IP")} />

          <Stat label={t("国家 / 地区")} value={regionName ?? "--"} hint={node.region ? node.region.toUpperCase() : undefined} />
          <Stat label={t("连接协议")} value={protocolLabel(node.protocol) || "--"} />
          <Stat
            label={t("最近出口更新")}
            value={formatRelativeTime(node.last_egress_update || "")}
            hint={node.last_egress_update ? formatDateTime(node.last_egress_update) : undefined}
          />
          <Stat
            label={t("纯净度评分")}
            value={purityScore === null ? "--" : purityScore}
            hint={band ? t(band.label) : t("未评估")}
          />

          <Stat
            label={t("出口更新尝试")}
            value={formatRelativeTime(node.last_egress_update_attempt || "")}
            hint={node.last_egress_update_attempt ? formatDateTime(node.last_egress_update_attempt) : undefined}
          />
          <Stat
            label={t("延迟探测尝试")}
            value={formatRelativeTime(node.last_latency_probe_attempt || "")}
            hint={node.last_latency_probe_attempt ? formatDateTime(node.last_latency_probe_attempt) : undefined}
          />
          <Stat
            label={t("权威延迟探测")}
            value={formatRelativeTime(node.last_authority_latency_probe_attempt || "")}
            hint={node.last_authority_latency_probe_attempt ? formatDateTime(node.last_authority_latency_probe_attempt) : undefined}
          />
          <Stat label={t("创建时间")} value={formatDateTime(node.created_at)} />
        </StatStrip>

        {node.last_error && <ErrorState message={node.last_error} />}
      </PanelBody>
    </Panel>
  );
}
