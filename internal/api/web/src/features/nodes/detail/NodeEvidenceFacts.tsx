import { useI18n } from "../../../i18n";
import type { NodeIntel } from "../types";
import { measurementStatusLabel } from "../quality/presentation";

const fieldLabels: Record<string, string> = {
  purity: "纯净度", network_type: "网络类型", egress_ipv4: "IPv4 出口", egress_ipv6: "IPv6 出口",
  colo: "接入点", asn: "ASN", as_org: "运营组织", country: "国家", city: "城市", native: "原生 IP",
  proxy: "代理", vpn: "VPN", tor: "Tor", hosting: "机房特征", compromised: "被入侵记录", scraper: "爬取特征", anonymous: "匿名网络",
  confidence: "置信度", assessed_at: "评估时间", verdict: "评级", checks: "检测结果", latency: "参考延迟",
};

// Group incomplete business measurements without exposing diagnostic payloads.
export function NodeEvidenceFacts({ intel }: { intel?: NodeIntel | null }) {
  const { t } = useI18n();
  const groups = new Map<string, string[]>();
  for (const [field, fact] of Object.entries(intel?.facts ?? {})) {
    if (fact.state === "known" || !fieldLabels[field]) continue;
    const message = t(measurementStatusLabel(fact));
    const fields = groups.get(message) ?? [];
    fields.push(t(fieldLabels[field]));
    groups.set(message, fields);
  }
  if (groups.size === 0) return null;
  return <div className="space-y-2 text-xs text-ink-soft" data-testid="node-evidence">
    <details>
      <summary className="cursor-pointer">{t("待完善的检测结果")}</summary>
      <ul className="mt-2 space-y-3">{[...groups].map(([message, fields]) => <li key={message}>
        <p>{fields.join(" · ")}</p>
        <p>{message}</p>
      </li>)}</ul>
    </details>
  </div>;
}
