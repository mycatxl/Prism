import { AlertCircle, ArrowUpRight, Building2, Check, Clock3, Home, LoaderCircle, Server, ShieldCheck, Smartphone, Wifi } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { ControlButton, SectionHeading, WorkspaceCard } from "../../../components/control-plane";
import { Badge, type BadgeProps } from "../../../components/ui/Badge";
import { Readout, ReadoutCell, ReadoutStrip } from "../../../components/ui/Readout";
import { useI18n } from "../../../i18n";
import { opsPaths } from "../../../lib/paths";
import { formatDateTime, formatRelativeTime } from "../../../lib/time";
import type { QualityEvidence, QualitySummary } from "./types";
import { useNow } from "../../../lib/clock";
import { assessmentReasons, evidenceFor, inspectionErrorLabel, canonicalPurity, riskLabels, sourceIsFresh, torRoleLabels, typeLabels, verdictLabels } from "./presentation";
import { UnratedPurity, MeasurementTime } from "./PurityStatus";
import { PurityGuide } from "./PurityGuide";
import { IPPureReviewPanel } from "./IPPureReview";

type Tone = NonNullable<BadgeProps["tone"]>;

// The bands in presentation.ts still carry the old variant vocabulary; the tones
// are the same meanings under the names the kit uses.
type ReadoutTone = "ink" | "signal" | "live" | "warn" | "alert" | "muted";

const bandTones: Record<string, Tone> = {
  success: "signal",
  info: "live",
  warning: "warn",
  danger: "alert",
};

// The same mapping for a readout, which has no outline or neutral state.
const bandReadoutTones: Record<string, ReadoutTone> = {
  success: "signal",
  info: "live",
  warning: "warn",
  danger: "alert",
};

const verdictTones: Record<string, Tone> = {
  favorable: "signal",
  high_risk: "alert",
  review: "warn",
  conflicting: "warn",
  caution: "warn",
  incomplete: "neutral",
  pending: "neutral",
};

/** One measurement of a definition list: quiet label, value on the same baseline. */
function Fact({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="label">{label}</dt>
      <dd className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-ink">
        {children}
      </dd>
    </div>
  );
}

export function QualityBadge({ summary, showScore = true }: { summary?: QualitySummary | null; showScore?: boolean }) {
  const { t } = useI18n();
  const { score, band } = canonicalPurity(summary);
  if (score === null) return <UnratedPurity fact={summary?.assessment?.facts?.purity}/>;
  return <span className="flex flex-wrap items-center gap-1.5">
    <Badge tone={bandTones[band?.variant ?? ""] ?? "neutral"}>
      {showScore && score !== null && <span className="readout font-semibold">{score}</span>}{t(band?.label || "未提供纯净度评分")}
    </Badge>
    {summary?.state === "stale" && <Badge tone="warn">{t("纯净度评分已过期")}</Badge>}
  </span>;
}

export function VerdictBadge({ summary }: { summary?: QualitySummary | null }) {
  const { t } = useI18n();
  const now = useNow();
  const verdict = summary?.assessment?.verdict || "pending";
  const expired = summary?.state === "stale" || ["ippure", "proxycheck"].some(id => { const evidence = evidenceFor(summary,id); return evidence && Date.parse(evidence.valid_until) <= now; });
  const adverse = ["high_risk","review","conflicting"].includes(verdict);
  if (expired && !adverse) return <Badge tone="warn">{t("部分证据过期")}</Badge>;
  return <span className="flex flex-wrap items-center gap-1.5">
    <Badge tone={verdictTones[verdict] ?? "neutral"}>{t(verdictLabels[verdict])}</Badge>
    {expired && <Badge tone="warn">{t("部分证据过期")}</Badge>}
  </span>;
}

export function NetworkSignals({ summary }: { summary?: QualitySummary | null }) {
  const { t } = useI18n();
  const now = useNow();
  const evidence = evidenceFor(summary,"proxycheck");
  const torRoles = sourceIsFresh(summary,"torproject",now) ? evidenceFor(summary,"torproject")?.tor_roles || [] : [];
  const roleTags = torRoles.filter(role => role !== "relay" || torRoles.length === 1).map(role => role === "guard" ? "Tor 守卫" : torRoleLabels[role]);
  if (!sourceIsFresh(summary,"proxycheck",now) || !evidence) return roleTags.length
    ? <span className="flex flex-wrap items-center gap-1.5">{roleTags.map(label => <Badge key={label} tone="warn">{t(label)}</Badge>)}</span>
    : <span className="text-xs text-ink-faint">{t("暂无有效风险检测结果，请更新网络特征")}</span>;
  const labels: string[] = ([["proxy","代理"],["vpn","VPN"],["tor","Tor"],["compromised","被入侵记录"],["scraper","爬取特征"]] as const)
    .filter(([key]) => evidence.signals[key] === true && !(key === "tor" && roleTags.length > 0)).map(([,label]) => label);
  labels.push(...roleTags);
  if (evidence.signals.anonymous === true && labels.length === 0) labels.push("匿名网络");
  if (labels.length) return <span className="flex flex-wrap items-center gap-1.5">{labels.slice(0,3).map(label => <Badge key={label} tone="warn">{t(label)}</Badge>)}{labels.length > 3 && <span className="readout text-2xs text-ink-faint">+{labels.length - 3}</span>}</span>;
  const complete = [evidence.signals.proxy,evidence.signals.vpn,evidence.signals.tor].every(value => value === false);
  return complete
    ? <Badge tone="signal" dot>{t("未标记代理、VPN 或 Tor")}</Badge>
    : <span className="text-xs text-ink-faint">{t("部分风险特征尚未检测，请更新网络特征")}</span>;
}

export function IPTypeBadge({ summary }: { summary?: QualitySummary | null }) {
  const { t } = useI18n();
  const type = summary?.assessment?.network_type || "unknown";
  const Icon = type === "residential" ? Home : type === "mobile" ? Smartphone : type === "datacenter" ? Server : type === "business" ? Building2 : type === "wireless" ? Wifi : AlertCircle;
  return <Badge
    tone={type === "conflicting" ? "warn" : type === "unknown" ? "neutral" : "outline"}
    title={type === "wireless" ? t("无线接入，包括蜂窝和卫星；不等同于移动蜂窝") : undefined}
  >
    <Icon size={14} aria-hidden />
    {t(typeLabels[type] || "类型未知")}
  </Badge>;
}

function EvidenceTime({ evidence }: { evidence: QualityEvidence }) {
  const { t } = useI18n();
  return <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-2xs text-ink-faint">
    <span className="flex items-center gap-1.5">
      <Clock3 size={14} aria-hidden />
      {t("检测于")}
      <span className="readout">{formatRelativeTime(evidence.observed_at)}</span>
    </span>
    <span title={formatDateTime(evidence.valid_until)}>
      {t("有效至")} <span className="readout">{formatDateTime(evidence.valid_until)}</span>
    </span>
  </div>;
}

export function QualityDetails({ summary, onInspect, pending = false, disabled = false, nodeHash, nodeIP, nodeReady = false }: {
  summary?: QualitySummary | null; onInspect?: () => void; pending?: boolean; disabled?: boolean;
  nodeHash?: string; nodeIP?: string; nodeReady?: boolean;
}) {
  const { t } = useI18n();
  const now = useNow();
  const evidence = summary?.evidence;
  const pure = evidenceFor(summary,"ippure");
  const { score: pureScore, band } = canonicalPurity(summary);
  const abuse = summary?.sources?.find(source => source.provider === "abuseipdb");
  const tor = summary?.sources?.find(source => source.provider === "torproject");
  return <div className="space-y-3">
    {/* The two headline measurements of this exit, on one baseline rather than in
        two boxes, plus the verdict the operator reads first. */}
    <WorkspaceCard
      className="min-w-0"
      title={
        <span className="flex min-w-0 items-center gap-1.5">
          <ShieldCheck size={16} aria-hidden className="shrink-0 text-ink-faint" />
          <span className="truncate">{t("纯净度与风险")}</span>
        </span>
      }
      actions={onInspect && <ControlButton size="sm" variant="secondary" onPress={onInspect} isDisabled={pending || disabled} loading={pending}>
        {pending ? <LoaderCircle size={14} className="animate-spin" /> : <ShieldCheck size={14} />}
        {t("更新网络特征")}
      </ControlButton>}
    >
      <div className="space-y-3">
        <ReadoutStrip>
          <ReadoutCell>
            <Readout
              label={t("纯净度")}
              value={pureScore ?? "—"}
              unit="/ 100"
              size="lg"
              tone={bandReadoutTones[band?.variant ?? ""] ?? "muted"}
            />
          </ReadoutCell>
        </ReadoutStrip>

        {pureScore === null && <UnratedPurity fact={summary?.assessment?.facts?.purity}/>}
        <MeasurementTime fact={summary?.assessment?.facts?.purity}/>
        <div className="flex flex-wrap items-center gap-1.5">
          {pureScore !== null && <QualityBadge summary={summary} showScore={false} />}
          <IPTypeBadge summary={summary} />
        </div>
      </div>
    </WorkspaceCard>

    <WorkspaceCard title={t("综合判定")}>
      <div className="space-y-1.5">
        <VerdictBadge summary={summary} />
        {summary?.assessment?.reasons?.length ? <ul className="max-w-[68ch] space-y-0.5 text-xs leading-relaxed text-ink-soft">{summary.assessment.reasons.filter(reason => assessmentReasons[reason]).map(reason => <li key={reason}>{t(assessmentReasons[reason])}</li>)}</ul> : null}
      </div>
    </WorkspaceCard>

    <PurityGuide bands={summary?.assessment?.bands} />

    {pure && <div className="space-y-2">
      <dl className="grid gap-x-8 gap-y-3 sm:grid-cols-3">
        <Fact label={t("网络类型")}>{pure.source_type === "Residential" ? t("住宅网络") : pure.source_type === "Non-residential" ? t("非住宅网络") : t("未知")}</Fact>
        <Fact label={t("地址归属")}>{t(pure.native === true ? "原生 IP" : pure.native === false ? "广播 IP" : "未知")}</Fact>
        <Fact label="ASN"><span className="readout">{pure.asn || "—"}</span></Fact>
      </dl>
      <EvidenceTime evidence={pure} />
    </div>}

    {!evidence && <p className="max-w-[68ch] text-sm leading-relaxed text-ink-soft">{t(summary?.ip
      ? "查询此出口的网络类型、代理特征与风险记录。"
      : "确认节点出口 IP 后，即可检测质量。同一出口的线路共享结果。")}</p>}

    {summary?.task?.error_code && <p className="flex flex-wrap items-center gap-2 border border-warn/35 bg-warn-wash px-3 py-2 text-sm text-warn" role="status">
      <AlertCircle size={14} aria-hidden />
      {t(inspectionErrorLabel(summary.task.error_code))}
      {evidence && <span className="text-2xs">{t("保留上次证据")}</span>}
    </p>}

    {evidence && <section className="space-y-3 border-t border-rule pt-3">
      <SectionHeading>{t("网络特征")}</SectionHeading>
      <dl className="grid gap-x-8 gap-y-3 sm:grid-cols-3">
        <Fact label="ASN"><span className="readout">{evidence.asn || "—"}</span></Fact>
        <Fact label={t("网络组织")}><span className="block max-w-[46ch] truncate" title={evidence.organization || undefined}>{evidence.organization || "—"}</span></Fact>
        <Fact label={t("网络类型")}>{t(typeLabels[evidence.ip_type] || "类型未知")}</Fact>
        {evidence.network_provider && <Fact label={t("网络供应商")}>{evidence.network_provider}</Fact>}
        {evidence.operator && <Fact label={t("代理服务商")}>
          <span>{evidence.operator}</span>
          {evidence.operator_services?.length ? <span className="text-ink-soft">{evidence.operator_services.join(", ")}</span> : null}
        </Fact>}
        <Fact label={t("风险等级")}>{t(riskLabels[evidence.grade])}</Fact>
        {evidence.source_confidence !== null && <Fact label={t("标记置信度")}>
          <span className="readout">{evidence.source_confidence}</span>
          <span className="text-2xs text-ink-faint">/ 100</span>
          <span className="text-2xs text-ink-faint tracking-normal">{t("不是安全概率")}</span>
        </Fact>}
      </dl>

      <div className="grid grid-cols-2 gap-x-6 sm:grid-cols-4">
        {([
          ["proxy", "公开代理"], ["vpn", "VPN"], ["tor", "Tor"], ["hosting", "机房特征"], ["compromised", "被入侵记录"], ["scraper", "爬取特征"], ["anonymous", "匿名网络"],
        ] as const).map(([key, label]) => {
          const value = evidence.signals[key];
          if (value == null) return null;
          return <div key={key} className="flex min-w-0 items-center justify-between gap-2 border-b border-rule-faint py-1.5">
            <span className="truncate text-xs text-ink-soft">{t(label)}</span>
            <Badge tone={value === true ? "alert" : value === false ? "signal" : "neutral"}>
              {value === true ? <AlertCircle size={14} aria-hidden /> : value === false ? <Check size={14} aria-hidden /> : null}
              {t(value === true ? "已标记" : value === false ? "未标记" : "未知")}
            </Badge>
          </div>;
        })}
      </div>

      {evidence.attack_history && Object.keys(evidence.attack_history).length > 0 && <div className="space-y-1.5">
        <h4 className="text-xs font-medium text-ink-soft">{t("攻击记录")}</h4>
        <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
          {Object.entries(evidence.attack_history).map(([kind, count]) => <span key={kind} className="flex items-baseline gap-1.5 text-xs">
            <span className="text-ink-soft">{kind}</span>
            <span className="readout font-medium">{count}</span>
          </span>)}
        </div>
      </div>}

      <EvidenceTime evidence={evidence} />
    </section>}

    {tor && <section className="space-y-2 border-t border-rule pt-3">
      <SectionHeading>
        <span className="flex items-center gap-1.5">
          <ShieldCheck size={15} aria-hidden className="text-ink-faint" />
          {t("Tor 角色核验")}
        </span>
      </SectionHeading>
      {tor.evidence ? <>
        <div className="flex flex-wrap items-center gap-1.5">
          {tor.evidence.tor_roles?.length
            ? tor.evidence.tor_roles.map(role => <Badge key={role} tone="warn">{t(torRoleLabels[role])}</Badge>)
            : <Badge tone="neutral">{t("未列于公开中继资料")}</Badge>}
          {Date.parse(tor.evidence.valid_until) <= now && <Badge tone="warn">{t("已过期")}</Badge>}
        </div>
        <EvidenceTime evidence={tor.evidence} />
      </> : <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t("Tor 角色暂未检测，请更新网络特征。")}</p>}
      <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t("出口依据最近 24 小时的实际出口记录；Guard 表示具备入口守卫资格。未列出不代表未使用 Tor，私有网桥不会公开地址。")}</p>
    </section>}

    {(abuse?.configured || abuse?.evidence) && <section className="space-y-2 border-t border-rule pt-3">
      <SectionHeading>
        <span className="flex items-center gap-1.5">
          <ShieldCheck size={15} aria-hidden className="text-ink-faint" />
          {t("近期滥用记录")}
        </span>
      </SectionHeading>
      {abuse?.evidence ? <>
        <ReadoutStrip>
          <ReadoutCell>
            <Readout label={t("举报置信度")} value={abuse.evidence.abuse_confidence ?? "—"} unit="/ 100" size="sm" />
          </ReadoutCell>
          <ReadoutCell>
            <Readout label={t("近 30 天举报")} value={abuse.evidence.total_reports ?? "—"} size="sm" />
          </ReadoutCell>
          <ReadoutCell>
            <Readout label={t("独立举报者")} value={abuse.evidence.distinct_reporters ?? "—"} size="sm" />
          </ReadoutCell>
        </ReadoutStrip>
        {(abuse.state === "stale" || Date.parse(abuse.evidence.valid_until) <= now) && <Badge tone="warn">{t("已过期")}</Badge>}
        <EvidenceTime evidence={abuse.evidence} />
        <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t("没有举报不等于没有风险；此项与网络类型、代理识别分别展示。")}</p>
      </> : <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t(abuse?.configured
        ? (abuse.task?.error_code ? inspectionErrorLabel(abuse.task.error_code) : "等待近期滥用记录")
        : "暂无近期滥用检测结果。")}</p>}
    </section>}

    {nodeHash ? <IPPureReviewPanel key={nodeHash + ":" + nodeIP} nodeHash={nodeHash} nodeIP={nodeIP} ready={nodeReady} /> : <div className="space-y-1 border-t border-rule pt-3">
      <Link className="inline-flex items-center gap-1 text-sm" to={`${opsPaths.nodes}?egress_ip=${encodeURIComponent(summary?.ip || "")}`}>{t("查看此出口的节点")}<ArrowUpRight size={14} aria-hidden /></Link>
      <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t("纯净度评分需要经目标节点查询，可在节点详情中直接复核。")}</p>
    </div>}
  </div>;
}
