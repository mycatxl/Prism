import { useI18n } from "../../i18n";
import { purityBands } from "./presentation";

export function PurityGuide() {
  const { t } = useI18n();
  return <details className="purity-guide">
    <summary>{t("评分来源与分级")}</summary>
    <p>{t("节点纯净度 = 100 − IPPure 原始风险分。分数来自目标节点请求 IPPure 的结果，数值越高，IPPure 判定的风险越低。")}</p>
    <ol className="purity-bands">{purityBands.map(band => <li key={band.id} data-band={band.id}>
      <strong>{band.min}–{band.max}</strong><span>{t(band.label)}</span>
    </li>)}</ol>
    <p>{t("五档名称是 Prism 的展示规则，不是 IPPure 官方评级或网站通过率。未获得 IPPure 结果时不生成主分数。")}</p>
    <p>{t("ProxyCheck 补充网络类型、代理、VPN、Tor 等特征。综合结论同时检查分数、风险标记和来源冲突，不平均两家分数。")}</p>
    <div className="quality-reference-links">
      <a href="https://proxycheck.io/api/" target="_blank" rel="noreferrer">ProxyCheck v3</a>
      <a href="https://ippure.com/faq.html" target="_blank" rel="noreferrer">IPPure FAQ</a>
    </div>
  </details>;
}
