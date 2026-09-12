import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, ArrowUpRight, LoaderCircle, ShieldCheck } from "lucide-react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { useI18n } from "../../i18n";
import { formatDateTime } from "../../lib/time";
import { getQualityStatus, reviewIPPure } from "./api";
import { purityBand, purityScore, useQualityTime } from "./presentation";

const failures: Record<string, string> = {
  IPPURE_LIMIT: "IPPure 复核正在冷却，请稍后重试",
  IPPURE_UNAVAILABLE: "此节点暂时无法访问 IPPure",
  IPPURE_RESPONSE: "IPPure 返回的数据不完整，未生成评分",
};

export function IPPureReviewPanel({ nodeHash, nodeIP, ready }: { nodeHash: string; nodeIP?: string; ready: boolean }) {
  const { t } = useI18n();
  const now = useQualityTime();
  const client = useQueryClient();
  const status = useQuery({ queryKey: ["quality", "status"], queryFn: getQualityStatus, staleTime: 5000 });
  const review = useMutation({ mutationFn: () => reviewIPPure(nodeHash), retry: false, gcTime: 0,
    onSettled: () => { void client.invalidateQueries({ queryKey: ["quality"] }); void client.invalidateQueries({ queryKey: ["nodes"] }); void client.invalidateQueries({ queryKey: ["node"] }); } });
  const manual = status.data?.manual_sources?.find(source => source.id === "ippure");
  const result = review.data;
  const evidence = result?.evidence;
  const nextAt = Math.max(Date.parse(manual?.next_allowed_at || "") || 0, Date.parse(result?.next_allowed_at || "") || 0);
  const waiting = nextAt > now;
  const score = result?.score_supported ? purityScore(evidence?.risk_score) : null;
  const band = purityBand(score);
  const expired = evidence && Date.parse(evidence.valid_until) <= now;
  const sameExit = result?.matches_node_ip && evidence?.ip === nodeIP;
  const errorCode = review.error && "code" in review.error ? String(review.error.code) : "";
  return <section className="ippure-review" aria-label={t("IPPure 节点复核")}>
    <div className="quality-detail-heading">
      <div><ShieldCheck size={16} /><h4>{t("IPPure 节点复核")}</h4></div>
      <Badge>{t("按需查询")}</Badge>
    </div>
    <p className="quality-explanation">{t("请求会经过此节点，查看 IPPure 对本次实际出口的独立结果。")}</p>
    <div className="ippure-review-actions">
      <Button variant="secondary" size="sm" onClick={() => review.mutate()} disabled={!ready || !nodeIP || review.isPending || manual?.busy || waiting}>
        {review.isPending ? <LoaderCircle size={14} className="spin" /> : <ShieldCheck size={14} />}
        {t(review.isPending ? "正在经节点查询" : "通过此节点复核")}
      </Button>
      {waiting && <span role="status">{t("{{seconds}} 秒后可再次复核", { seconds: Math.ceil((nextAt - now) / 1000) })}</span>}
    </div>
    {!nodeIP && <p className="quality-explanation">{t("请先完成出口探测，再通过节点查询 IPPure。")}</p>}
    {review.error && <p className="quality-inline-warning" role="alert"><AlertCircle size={14} />{t(failures[errorCode] || "复核失败，请检查节点连接后重试")}</p>}
    {evidence && <div className="ippure-result" aria-live="polite">
      <div className="ippure-result-top"><div><span>{t("本次实际出口")}</span><strong>{evidence.ip}</strong></div>
        <Badge variant={sameExit ? "success" : "warning"}>{t(sameExit ? "与节点出口一致" : "与节点记录不同")}</Badge></div>
      {!sameExit && <p className="quality-inline-warning">{t("本次出口与节点记录不同，结果仅用于本次目标，不覆盖节点评级。")}</p>}
      <div className="ippure-score-row"><div><span>{t("IPPure 纯净度参考")}</span><strong>{score ?? "—"}<small>/ 100</small></strong>
        {band && !expired && <Badge variant={band.variant}>{t(band.label)}</Badge>}
        {expired && <Badge variant="warning">{t("已过期")}</Badge>}</div>
        <div><span>{t("IPPure 原始风险")}</span><strong>{evidence.risk_score ?? "—"}<small>/ 100</small></strong></div></div>
      {!result?.score_supported && <p className="quality-explanation">{t("IPPure 未提供此地址的风险分；IPv6 暂不支持评分。")}</p>}
      <dl className="quality-network-facts">
        <div><dt>{t("住宅属性")}</dt><dd>{t(result?.is_residential === true ? "住宅网络" : result?.is_residential === false ? "非住宅网络" : "未知")}</dd></div>
        <div><dt>{t("地址归属")}</dt><dd>{t(evidence.native === true ? "原生 IP" : evidence.native === false ? "广播 IP" : "未知")}</dd></div>
        <div><dt>ASN</dt><dd>{evidence.asn || "—"}</dd></div>
        <div><dt>{t("网络组织")}</dt><dd>{evidence.organization || "—"}</dd></div>
        <div><dt>{t("检测于")}</dt><dd>{formatDateTime(evidence.observed_at)}</dd></div>
      </dl>
    </div>}
    <p className="quality-explanation">{t("按需查询，每分钟最多一次。已核对出口的结果临时复用 24 小时，服务重启后清除。")}</p>
    <a className="source-documentation" href="https://ippure.com/MyIP-Info-API" target="_blank" rel="noreferrer">{t("IPPure 接口说明")}<ArrowUpRight size={12} /></a>
  </section>;
}
