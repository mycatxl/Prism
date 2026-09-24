import { ArrowUpRight, ShieldCheck } from "lucide-react";
import type { CSSProperties } from "react";
import { Link } from "react-router-dom";
import { useI18n } from "../../i18n";
import type { QualityStatus } from "./types";

export function QualityOverview({ status }: { status?: QualityStatus }) {
  const { t } = useI18n();
  const checked = status?.manual_sources?.find(source => source.id === "ippure")?.current_ips;
  const coverage = status?.known_ips ? Math.min(100, (checked || 0) / status.known_ips * 100) : 0;
  const queued = status?.sources.reduce((total, source) => total + source.queued + source.running, 0);
  const count = (value?: number) => value === undefined ? "—" : value.toLocaleString();
  return <section className="quality-overview">
    <div className="quality-overview-copy">
      <div className="quality-overview-label"><ShieldCheck size={16} />{t("质量概况")}
        <span>{t(status ? status.enabled ? "网络特征自动更新" : "已停用" : "等待服务数据")}</span>
      </div>
      <h2>{t("出口类型与风险记录")}</h2>
      <p>{t("IPPure 提供评分，ProxyCheck 补充网络特征，同 IP 线路共享证据。")}</p>
      <Link to="/nodes?view=exits">{t("查看出口记录")}<ArrowUpRight size={15} /></Link>
    </div>
    <div className="quality-overview-facts">
      <div><strong>{count(status?.checked_ips)}</strong><span>{t("网络证据")}</span></div>
      <div><strong>{count(queued)}</strong><span>{t("特征检测排队")}</span></div>
    </div>
    <div className="quality-orbit" style={{ "--quality-coverage": coverage + "%" } as CSSProperties}
      role="img" aria-label={t("IPPure 有效评分") + " " + count(checked)}>
      <div><strong>{count(checked)}</strong><span>{t("IPPure 已复核")}</span></div>
    </div>
  </section>;
}
