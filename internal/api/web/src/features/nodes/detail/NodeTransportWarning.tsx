import { useI18n } from "../../../i18n";
import type { NodeSummary } from "../types";

export function NodeTransportWarning({ node }: { node: NodeSummary }) {
  const { t } = useI18n();
  if (!node.privacy?.warnings.includes("TLS_VERIFICATION_DISABLED")) return null;
  return <span className="text-warn" data-testid="node-tls-warning">{t("上游 TLS 未验证：存在节点冒充与流量读取风险")}</span>;
}
