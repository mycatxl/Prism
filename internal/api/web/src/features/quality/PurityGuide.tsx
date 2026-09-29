import { cn } from "../../lib/cn";
import { useI18n } from "../../i18n";
import { purityBands } from "./presentation";

// A band is read as a colour on the rule beside it, so the five steps of the
// scale are visible before the numbers are.
const bandRules: Record<string, string> = {
  success: "border-signal",
  info: "border-live",
  warning: "border-warn",
  danger: "border-alert",
};

/** The purity scale, folded away until it is asked for. */
export function PurityGuide() {
  const { t } = useI18n();
  return <details className="border-y border-rule bg-paper-sunk/40 px-3 py-2">
    <summary className="cursor-pointer text-sm font-medium text-ink-soft transition-colors hover:text-ink">{t("评分来源与分级")}</summary>
    <div className="mt-2 space-y-2">
      <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t("节点纯净度 = 100 − IPPure 原始风险分。分数来自目标节点请求 IPPure 的结果，数值越高，IPPure 判定的风险越低。")}</p>
      <ol className="grid gap-1 sm:grid-cols-5">
        {purityBands.map(band => <li key={band.id} className={cn("border-l-2 pl-2", bandRules[band.variant])}>
          <span className="readout block text-sm font-medium text-ink">{band.min}–{band.max}</span>
          <span className="block text-2xs text-ink-soft">{t(band.label)}</span>
        </li>)}
      </ol>
      <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t("五档名称是 Prism 的展示规则，不是 IPPure 官方评级或网站通过率。未获得 IPPure 结果时不生成主分数。")}</p>
      <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t("ProxyCheck 补充网络类型、代理、VPN、Tor 等特征。综合结论同时检查分数、风险标记和来源冲突，不平均两家分数。")}</p>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
        <a href="https://proxycheck.io/api/" target="_blank" rel="noreferrer">ProxyCheck v3</a>
        <a href="https://ippure.com/faq.html" target="_blank" rel="noreferrer">IPPure FAQ</a>
      </div>
    </div>
  </details>;
}