import { Copy } from "lucide-react";
import type { ReactNode } from "react";
import { ControlButton, ControlTable, ControlTableBody, ControlTableCell, ControlTableColumn, ControlTableHead, ControlTableRow, ControlTableWrap, WorkspaceCard } from "../../../components/control-plane";
import { Badge } from "../../../components/ui/Badge";
import { useI18n } from "../../../i18n";
import { formatDateTime } from "../../../lib/time";
import { confidenceLabels, flagLabels, stateLabels } from "../quality/presentation";
import { purityBands, typeLabels, verdictLabels } from "../quality/presentation";
import type { NodeSummary } from "../types";
import { protocolLabel } from "./nodeMeta";
import type { IntelNodeDetail } from "./api";

/**
 * The node's read-only attributes: the identity and assessment facts of the
 * record, stated as a key–value table. The operational figures (latency,
 * failure count, probe stamps) live in the overview panel; this table is where
 * an operator looks up what the node *is* rather than how it is *doing*.
 */

/** `stateLabels` in NodeIntel.tsx predates the "valid" state; complete it here. */
const STATE_LABELS: Record<string, string> = { ...stateLabels, valid: "评估有效" };

type AttributeRow = { label: string; value: ReactNode };

export function NodeAttributesPanel({
  node,
  intelDetail,
  onCopy,
}: {
  node: NodeSummary;
  /** The intel report's egress record, when the report is available. */
  intelDetail: IntelNodeDetail | undefined;
  onCopy: (value: string) => void;
}) {
  const { t } = useI18n();
  const intel = node.intel ?? null;
  const score = typeof intel?.purity_score === "number" ? intel.purity_score : null;
  const band = score === null ? undefined : purityBands.find((entry) => score >= entry.min && score <= entry.max);
  const ipv4 = intelDetail?.egress.ipv4 || node.egress_ip || "";

  const copyButton = (value: string, label: string) => (
    <ControlButton
      variant="ghost"
      size="icon"
      className="size-5"
      aria-label={t("复制{{label}}", { label })}
      onPress={() => onCopy(value)}
    >
      <Copy size={14} aria-hidden />
    </ControlButton>
  );

  const rows: AttributeRow[] = [
    {
      label: t("节点哈希"),
      value: (
        <span className="flex items-center gap-1">
          <code className="readout text-xs">{node.node_hash}</code>
          {copyButton(node.node_hash, t("节点哈希"))}
        </span>
      ),
    },
    { label: t("连接协议"), value: protocolLabel(node.protocol) || "--" },
    {
      label: t("订阅来源"),
      value:
        node.tags.length > 0 ? (
          <span className="flex flex-col gap-0.5">
            {node.tags.map((tag) => (
              <span key={tag.subscription_id + tag.tag}>
                {tag.subscription_name}
                <span className="text-ink-faint"> · {tag.tag}</span>
              </span>
            ))}
          </span>
        ) : (
          "--"
        ),
    },
    {
      label: t("IPv4 出口"),
      value: (
        <span className="flex items-center gap-1">
          <code className="readout text-xs">{ipv4 || "--"}</code>
          {ipv4 && copyButton(ipv4, t("IPv4 出口"))}
        </span>
      ),
    },
    {
      label: t("IPv6 出口"),
      value: (
        <code className="readout text-xs">{intelDetail?.egress.ipv6 || intel?.egress_ipv6 || "--"}</code>
      ),
    },
    { label: t("接入点"), value: intelDetail?.egress.colo || intel?.colo || "--" },
    { label: t("城市"), value: intel?.city || "--" },
    { label: t("ASN"), value: intel?.asn ? String(intel.asn) : "--" },
    { label: t("运营组织"), value: intel?.as_org || "--" },
    {
      label: t("IP 类型"),
      value: intel?.ip_type ? t((typeLabels as Record<string, string>)[intel.ip_type] || intel.ip_type) : "--",
    },
    {
      label: t("原生 IP"),
      value: intel?.native === null || intel?.native === undefined ? "--" : t(intel.native ? "是" : "否"),
    },
    { label: t("纯净度评分"), value: score === null ? "--" : String(score) },
    { label: t("纯净度档位"), value: band ? t(band.label) : "--" },
    {
      label: t("评级"),
      value: intel?.verdict ? t((verdictLabels as Record<string, string>)[intel.verdict] || intel.verdict) : "--",
    },
    {
      label: t("置信度"),
      value: intel?.confidence ? t(confidenceLabels[intel.confidence] || intel.confidence) : "--",
    },
    { label: t("评估状态"), value: intel ? t(STATE_LABELS[intel.state] || intel.state) : t("未评估") },
    {
      label: t("评估时间"),
      value: intel?.assessed_at ? formatDateTime(intel.assessed_at) : "--",
    },
    {
      label: t("风险标记"),
      value:
        intel && intel.flags.length > 0 ? (
          <span className="flex flex-wrap gap-1">
            {intel.flags.map((flag) => (
              <Badge key={flag} tone="neutral">
                {t(flagLabels[flag] || flag)}
              </Badge>
            ))}
          </span>
        ) : (
          "--"
        ),
    },
    {
      label: t("熔断开始"),
      value: node.circuit_open_since ? formatDateTime(node.circuit_open_since) : "--",
    },
  ];

  return (
    <WorkspaceCard title={t("节点属性")}>
      <ControlTableWrap>
        <ControlTable density="compact">
          <ControlTableHead>
            <ControlTableColumn isRowHeader className="sr-only">{t("字段")}</ControlTableColumn>
            <ControlTableColumn className="sr-only">{t("详情")}</ControlTableColumn>
          </ControlTableHead>
          <ControlTableBody>
            {rows.map((row) => (
              <ControlTableRow key={row.label}>
                <ControlTableCell className="w-32 whitespace-nowrap text-left align-top">
                  <span className="label">{row.label}</span>
                </ControlTableCell>
                <ControlTableCell className="align-top">
                  <span className="block min-w-0 break-all text-sm text-ink">{row.value}</span>
                </ControlTableCell>
              </ControlTableRow>
            ))}
          </ControlTableBody>
        </ControlTable>
      </ControlTableWrap>
    </WorkspaceCard>
  );
}
