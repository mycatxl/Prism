import { useI18n } from "../../../i18n";
import type { NodeSummary } from "../types";

const labels: Record<string, string> = {
  PLAINTEXT_TRANSPORT: "代理链路未加密",
  IDENTITY_HEADER_CONFIG: "节点配置携带身份头",
  OWN_ASSET_EGRESS: "出口属于运营方自有资产",
  IDENTITY_UNMEASURED: "匿名性尚无有效测量",
  EGRESS_UNMEASURED: "尚未测得出口",
  OWN_ASSET_DISCOVERY_FAILED: "本机公网地址发现失败，请检查探测设置并重启",
  ADMIN_QUARANTINE: "管理员手动隔离",
  TLS_VERIFICATION_DISABLED: "上游 TLS 未验证：存在节点冒充与流量读取风险",
  DIRECT_EGRESS: "直连出口禁止转售",
  INVALID_NODE_DOCUMENT: "节点配置无效",
};

export function NodePrivacyFacts({ node }: { node: NodeSummary }) {
  const { t } = useI18n();
  if (!node.privacy) return null;
  const observed = node.privacy.identity_evidence;
  return <div className="space-y-2" data-testid="node-privacy">
    <p>{node.privacy.status === "accepted" ? t("已通过节点准入规则") : node.privacy.status === "unmanaged" ? t("未应用隐私准入规则") : t("隐私条件未满足：不可销售")}</p>
    <p className="text-xs text-ink-soft">{t("配置检查只解析节点文档，不是匿名性测量。")}</p>
    {[...node.privacy.reasons, ...node.privacy.warnings].filter(code => code !== "IDENTITY_UNMEASURED").map((code) => {
      const finding = node.privacy?.findings?.[code];
      return <p key={code} className="break-words">{t(labels[code] ?? code)}{finding?.action && <> · {t(finding.action)}</>}</p>;
    })}
    {observed && <div className="space-y-1 text-xs">
      {observed.cause && <p>{t(observed.action || "重新运行出口探测")}</p>}
      {observed.checked_at && <p>{t("检测于")} {observed.checked_at}</p>}
      {observed.valid_until && <p>{t("有效至")} {observed.valid_until}</p>}
    </div>}
    <p className="text-muted-foreground">{node.privacy.identity_check === "measured" ? t("本次 TLS echo 未观察到身份泄露，仅覆盖该测试路径。") : t("下游匿名性未知；未通过有效 echo 测量。")}</p>
  </div>;
}
