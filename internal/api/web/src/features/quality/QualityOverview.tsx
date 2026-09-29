import { ShieldCheck } from "lucide-react";
import { Link } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { useI18n } from "../../i18n";
import type { QualityStatus } from "./types";

/**
 * The quality instrument on the overview screen: four readings on one baseline,
 * framed as one region.
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
  return <Panel className="min-w-0">
    <PanelHeader
      title={
        <span className="flex min-w-0 items-center gap-1.5">
          <ShieldCheck size={15} aria-hidden className="shrink-0 text-ink-faint" />
          <span className="truncate">{t("质量概况")}</span>
        </span>
      }
      description={t("IPPure 提供评分，ProxyCheck 补充网络特征，同 IP 线路共享证据。")}
      actions={status
        ? status.enabled
          ? <Badge tone="signal" dot>{t("网络特征自动更新")}</Badge>
          : <Badge tone="neutral" dot>{t("已停用")}</Badge>
        : <Badge tone="neutral" dot>{t("等待服务数据")}</Badge>}
    />

    <PanelBody className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 className="text-sm font-medium">{t("出口类型与风险记录")}</h3>
        <Link className="shrink-0 text-sm" to="/nodes?view=exits">{t("查看出口记录")}</Link>
      </div>

      <ReadoutStrip>
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
    </PanelBody>
  </Panel>;
}
