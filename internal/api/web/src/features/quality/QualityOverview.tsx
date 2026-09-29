import { ShieldCheck } from "lucide-react";
import { Link } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { SectionTitle } from "../../components/ui/Panel";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { useI18n } from "../../i18n";
import type { QualityStatus } from "./types";

/**
 * The quality instrument on the overview screen: four readings on one baseline.
 *
 * The coverage of the IPPure source used to be drawn as a ring; it is a share of
 * the known exits, so it belongs beside the count it divides, as a quiet hint.
 */
export function QualityOverview({ status }: { status?: QualityStatus }) {
  const { t } = useI18n();
  const checked = status?.manual_sources?.find(source => source.id === "ippure")?.current_ips;
  const coverage = status?.known_ips ? Math.min(100, (checked || 0) / status.known_ips * 100) : 0;
  const queued = status?.sources.reduce((total, source) => total + source.queued + source.running, 0);
  const count = (value?: number) => value === undefined ? "—" : value.toLocaleString();
  return <section className="space-y-3">
    <SectionTitle
      trailing={status
        ? status.enabled
          ? <Badge tone="signal" dot>{t("网络特征自动更新")}</Badge>
          : <Badge tone="neutral" dot>{t("已停用")}</Badge>
        : <Badge tone="neutral" dot pulse>{t("等待服务数据")}</Badge>}
    >
      <span className="flex items-center gap-1.5">
        <ShieldCheck size={15} aria-hidden className="text-ink-faint" />
        {t("质量概况")}
      </span>
    </SectionTitle>

    <div className="flex flex-wrap items-end justify-between gap-3">
      <div className="min-w-0">
        <h3 className="text-base font-medium">{t("出口类型与风险记录")}</h3>
        <p className="mt-1 max-w-[80ch] text-sm text-ink-soft">{t("IPPure 提供评分，ProxyCheck 补充网络特征，同 IP 线路共享证据。")}</p>
      </div>
      <Link className="shrink-0 text-sm" to="/nodes?view=exits">{t("查看出口记录")}</Link>
    </div>

    <ReadoutStrip className="grid-cols-2 lg:grid-cols-4">
      <ReadoutCell>
        <Readout label={t("已查询 IP")} value={count(status?.known_ips)} />
      </ReadoutCell>
      <ReadoutCell>
        <Readout label={t("网络证据")} value={count(status?.checked_ips)} />
      </ReadoutCell>
      <ReadoutCell>
        <Readout
          label={t("IPPure 已复核")}
          value={count(checked)}
          hint={coverage ? Math.round(coverage) + "%" : undefined}
        />
      </ReadoutCell>
      <ReadoutCell>
        <Readout label={t("特征检测排队")} value={count(queued)} />
      </ReadoutCell>
    </ReadoutStrip>
  </section>;
}