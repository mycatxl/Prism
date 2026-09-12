import { AlertCircle, ArrowUpRight, Building2, Check, Clock3, Home, LoaderCircle, Server, ShieldCheck, Smartphone, Wifi } from "lucide-react";
import { Link } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { useI18n } from "../../i18n";
import { formatDateTime, formatRelativeTime } from "../../lib/time";
import type { QualityEvidence, QualitySummary } from "./types";
import { assessmentReasons, evidenceFor, inspectionErrorLabel, providerName, purityBand, purityScore, riskLabels, sourceIsFresh, torRoleLabels, typeLabels, useQualityTime, verdictLabels } from "./presentation";
import { PurityGuide } from "./PurityGuide";
import { IPPureReviewPanel } from "./IPPureReview";

export function QualityBadge({ summary, showScore = true }: { summary?: QualitySummary | null; showScore?: boolean }) {
  const { t } = useI18n();
  const now = useQualityTime();
  const evidence = evidenceFor(summary, "ippure");
  if (evidence && !sourceIsFresh(summary, "ippure", now)) return <Badge><Clock3 size={11} />{t("IPPure 已过期")}</Badge>;
  if (sourceIsFresh(summary, "ippure", now)) {
    const score = purityScore(evidence!.risk_score);
    const band = purityBand(score);
    return <Badge variant={band?.variant || "neutral"}>
      {showScore && score !== null && <strong className="purity-badge-score">{score}</strong>}{t(band?.label || "IPPure 未提供评分")}
    </Badge>;
  }
  if (summary?.state === "unsupported") return <Badge>{t("不支持检测")}</Badge>;
  return <Badge variant="muted">{t("待 IPPure 复核")}</Badge>;
}

export function VerdictBadge({ summary }: { summary?: QualitySummary | null }) {
  const { t } = useI18n();
  const now = useQualityTime();
  const verdict = summary?.assessment?.verdict || "pending";
  const expired = ["ippure", "proxycheck"].some(id => { const evidence = evidenceFor(summary,id); return evidence && Date.parse(evidence.valid_until) <= now; });
  const adverse = ["high_risk","review","conflicting"].includes(verdict);
  if (expired && !adverse) return <Badge variant="warning">{t("部分证据过期")}</Badge>;
  return <span className="verdict-badges"><Badge variant={verdict === "favorable" ? "success" : verdict === "high_risk" ? "danger" : ["review","conflicting","caution"].includes(verdict) ? "warning" : "neutral"}>{t(verdictLabels[verdict])}</Badge>
    {expired && <Badge variant="warning">{t("部分证据过期")}</Badge>}
  </span>;
}

export function NetworkSignals({ summary }: { summary?: QualitySummary | null }) {
  const { t } = useI18n();
  const now = useQualityTime();
  const evidence = evidenceFor(summary,"proxycheck");
  const torRoles = sourceIsFresh(summary,"torproject",now) ? evidenceFor(summary,"torproject")?.tor_roles || [] : [];
  const roleTags = torRoles.filter(role => role !== "relay" || torRoles.length === 1).map(role => role === "guard" ? "Tor 守卫" : torRoleLabels[role]);
  if (!sourceIsFresh(summary,"proxycheck",now) || !evidence) return roleTags.length ? <span className="network-signal-tags">{roleTags.map(label => <Badge key={label} variant="warning">{t(label)}</Badge>)}</span> : <span className="muted">{t("特征待查")}</span>;
  const labels: string[] = ([["proxy","代理"],["vpn","VPN"],["tor","Tor"],["compromised","被入侵记录"],["scraper","爬取特征"]] as const)
    .filter(([key]) => evidence.signals[key] === true && !(key === "tor" && roleTags.length > 0)).map(([,label]) => label);
  labels.push(...roleTags);
  if (evidence.signals.anonymous === true && labels.length === 0) labels.push("匿名网络");
  if (labels.length) return <span className="network-signal-tags">{labels.slice(0,3).map(label => <Badge key={label} variant="warning">{t(label)}</Badge>)}{labels.length > 3 && <small>+{labels.length - 3}</small>}</span>;
  const complete = [evidence.signals.proxy,evidence.signals.vpn,evidence.signals.tor].every(value => value === false);
  return <span className={complete ? "network-signals-clear" : "muted"}>{t(complete ? "未见匿名标记" : "部分特征未知")}</span>;
}

export function IPTypeBadge({ summary }: { summary?: QualitySummary | null }) {
  const { t } = useI18n();
  const now = useQualityTime();
  const proxyFresh = sourceIsFresh(summary,"proxycheck",now);
  const pureFresh = sourceIsFresh(summary,"ippure",now);
  let type = proxyFresh ? evidenceFor(summary,"proxycheck")!.ip_type : "unknown";
  if (pureFresh && proxyFresh) type = summary?.assessment?.network_type || type;
  else if (pureFresh) {
    const pure = evidenceFor(summary,"ippure");
    type = pure?.source_type === "Residential" ? "residential" : pure?.source_type === "Non-residential" ? "non_residential" : "unknown";
  }
  const Icon = type === "residential" ? Home : type === "mobile" ? Smartphone : type === "datacenter" ? Server : type === "business" ? Building2 : type === "wireless" ? Wifi : AlertCircle;
  return <span className={"ip-type ip-type-" + type} title={type === "wireless" ? t("无线接入，包括蜂窝和卫星；不等同于移动蜂窝") : undefined}><Icon size={13} />{t(typeLabels[type] || "类型未知")}</span>;
}

function EvidenceTime({ evidence }: { evidence: QualityEvidence }) {
  const { t } = useI18n();
  return <div className="evidence-time">
    <span><Clock3 size={12} />{t("检测于")} {formatRelativeTime(evidence.observed_at)}</span>
    <span title={formatDateTime(evidence.valid_until)}>{t("有效至")} {formatDateTime(evidence.valid_until)}</span>
  </div>;
}

export function QualityDetails({ summary, onInspect, pending = false, disabled = false, nodeHash, nodeIP, nodeReady = false }: {
  summary?: QualitySummary | null; onInspect?: () => void; pending?: boolean; disabled?: boolean;
  nodeHash?: string; nodeIP?: string; nodeReady?: boolean;
}) {
  const { t } = useI18n();
  const now = useQualityTime();
  const evidence = summary?.evidence;
  const pure = evidenceFor(summary,"ippure");
  const pureFresh = sourceIsFresh(summary,"ippure",now);
  const abuse = summary?.sources?.find(source => source.provider === "abuseipdb");
  const tor = summary?.sources?.find(source => source.provider === "torproject");
  return <div className="quality-details">
    <div className="quality-detail-heading">
      <div><ShieldCheck size={17} /><h3>{t("纯净度与风险")}</h3></div>
      {onInspect && <Button size="sm" variant="secondary" onClick={onInspect} disabled={pending || disabled}>
        {pending ? <LoaderCircle size={13} className="spin" /> : <ShieldCheck size={13} />}
        {t("更新网络特征")}
      </Button>}
    </div>
    <div className="quality-result-banner">
      <div>
        <span className="quality-caption">{t("IPPure 纯净度参考")}</span>
        <strong className="purity-primary-value">{pureFresh ? purityScore(pure?.risk_score) ?? "—" : "—"}<small>/ 100</small></strong>
        <QualityBadge summary={summary} showScore={false} />
        <IPTypeBadge summary={summary} />
      </div>
      <div className="quality-risk-value">
        <span>IPPure</span>
        <span>{t("来源原始风险")}</span>
        <strong>{pure?.risk_score ?? "—"}<small>/ 100</small></strong>
        <span>{t("数值越低，来源判定风险越低")}</span>
      </div>
    </div>
    <div className="quality-verdict"><span>{t("综合判定")}</span><VerdictBadge summary={summary} />
      {summary?.assessment?.reasons.length ? <ul>{summary.assessment.reasons.map(reason => <li key={reason}>{t(assessmentReasons[reason] || reason)}</li>)}</ul> : null}
    </div>
    <PurityGuide />
    {pure && <>
      <dl className="quality-network-facts">
        <div><dt>{t("IPPure 分类")}</dt><dd>{t(pure.source_type === "Residential" ? "住宅网络" : pure.source_type === "Non-residential" ? "非住宅网络" : "未知")}</dd></div>
        <div><dt>{t("地址归属")}</dt><dd>{t(pure.native === true ? "原生 IP" : pure.native === false ? "广播 IP" : "未知")}</dd></div>
        <div><dt>ASN</dt><dd>{pure.asn || "—"}</dd></div>
      </dl><EvidenceTime evidence={pure} />
    </>}
    {!evidence && <p className="quality-explanation">{t(summary?.ip
      ? "查询此出口的网络类型、代理特征与风险记录。"
      : "确认节点出口 IP 后，即可检测质量。同一出口的线路共享结果。")}</p>}
    {summary?.task?.error_code && <p className="quality-inline-warning" role="status">
      <AlertCircle size={14} />{t(inspectionErrorLabel(summary.task.error_code))}
      {evidence && <span>{t("保留上次证据")}</span>}
    </p>}
    {evidence && <>
      <h4 className="quality-source-heading">{t("ProxyCheck 网络证据")}</h4>
      <dl className="quality-network-facts">
        <div><dt>{t("数据来源")}</dt><dd>{providerName(evidence.provider)}</dd></div>
        <div><dt>ASN</dt><dd>{evidence.asn || "—"}</dd></div>
        <div><dt>{t("网络组织")}</dt><dd>{evidence.organization || "—"}</dd></div>
        <div><dt>{t("来源分类")}</dt><dd>{evidence.source_type || "—"}</dd></div>
        {evidence.network_provider && <div><dt>{t("网络供应商")}</dt><dd>{evidence.network_provider}</dd></div>}
        {evidence.operator && <div><dt>{t("代理服务商")}</dt><dd>{evidence.operator}{evidence.operator_services?.length ? " · " + evidence.operator_services.join(", ") : ""}</dd></div>}
        <div><dt>{t("来源风险等级")}</dt><dd>{t(riskLabels[evidence.grade])}</dd></div>
        <div><dt>{t("来源原始风险")}</dt><dd>{evidence.risk_score ?? "—"} / 100</dd></div>
        {evidence.source_confidence !== null && <div><dt>{t("标记置信度")}</dt><dd>{evidence.source_confidence} / 100 <small>{t("不是安全概率")}</small></dd></div>}
      </dl>
      <div className="quality-signals">
        {([
          ["proxy", "公开代理"], ["vpn", "VPN"], ["tor", "Tor"], ["hosting", "机房特征"], ["compromised", "被入侵记录"], ["scraper", "爬取特征"], ["anonymous", "匿名网络"],
        ] as const).map(([key, label]) => {
          const value = evidence.signals[key];
          return <div key={key} className={value === true ? "signal-positive" : value === false ? "signal-negative" : "signal-unknown"}>
            <span>{t(label)}</span><strong>{value === true ? <AlertCircle size={13} /> : value === false ? <Check size={13} /> : null}
              {t(value === true ? "已标记" : value === false ? "未标记" : "未知")}</strong>
          </div>;
        })}
      </div>
      {evidence.attack_history && Object.keys(evidence.attack_history).length > 0 && <div className="quality-attack-history">
        <h4>{t("来源攻击记录")}</h4>
        {Object.entries(evidence.attack_history).map(([kind, count]) => <div key={kind}><span>{kind}</span><strong>{count}</strong></div>)}
        <p className="quality-explanation">{t("展示来源返回的攻击类型与次数，最多保留 8 类；不推断为节点使用者的行为。")}</p>
      </div>}
      <EvidenceTime evidence={evidence} />
    </>}
    {tor && <section className="tor-evidence">
      <div className="quality-detail-heading"><div><ShieldCheck size={15} /><h4>{t("Tor 角色核验")}</h4></div><span>Tor Project</span></div>
      {tor.evidence ? <>
        <div className="network-signal-tags">{tor.evidence.tor_roles?.length ? tor.evidence.tor_roles.map(role => <Badge key={role} variant="warning">{t(torRoleLabels[role])}</Badge>) : <Badge>{t("未列于公开中继资料")}</Badge>}</div>
        {Date.parse(tor.evidence.valid_until) <= now && <Badge variant="warning">{t("已过期")}</Badge>}
        <EvidenceTime evidence={tor.evidence} />
      </> : <p className="quality-explanation">{t("角色资料暂不可用，保留 Tor 标记，具体角色未知。")}</p>}
      <p className="quality-explanation">{t("出口依据最近 24 小时的实际出口记录；Guard 表示具备入口守卫资格。未列出不代表未使用 Tor，私有网桥不会公开地址。")}</p>
    </section>}
    {(abuse?.configured || abuse?.evidence) && <section className="abuse-evidence">
      <div className="quality-detail-heading"><div><ShieldCheck size={15} /><h4>{t("近期滥用记录")}</h4></div><span>AbuseIPDB</span></div>
      {abuse?.evidence ? <>
        <div className="abuse-facts">
          <div><span>{t("举报置信度")}</span><strong>{abuse.evidence.abuse_confidence ?? "—"}<small>/ 100</small></strong></div>
          <div><span>{t("近 30 天举报")}</span><strong>{abuse.evidence.total_reports ?? "—"}</strong></div>
          <div><span>{t("独立举报者")}</span><strong>{abuse.evidence.distinct_reporters ?? "—"}</strong></div>
        </div>
        {(abuse.state === "stale" || Date.parse(abuse.evidence.valid_until) <= now) && <Badge variant="warning">{t("已过期")}</Badge>}
        <EvidenceTime evidence={abuse.evidence} />
        <p className="quality-explanation">{t("没有举报不等于没有风险；此项与网络类型、代理识别分别展示。")}</p>
      </> : <p className="quality-explanation">{t(abuse?.configured
        ? (abuse.task?.error_code ? inspectionErrorLabel(abuse.task.error_code) : "等待近期滥用记录")
        : "可配置免费的 AbuseIPDB API Key，补充近 30 天的举报记录。")}</p>}
    </section>}
    {nodeHash ? <IPPureReviewPanel key={nodeHash + ":" + nodeIP} nodeHash={nodeHash} nodeIP={nodeIP} ready={nodeReady} /> : <div className="quality-external-check">
      <Link to={"/nodes?egress_ip=" + encodeURIComponent(summary?.ip || "")}>{t("查看此出口的节点")}<ArrowUpRight size={13} /></Link>
      <p>{t("IPPure 需要经目标节点查询，可在节点详情中直接复核。")}</p>
    </div>}
  </div>;
}
