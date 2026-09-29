import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, ArrowUpRight, LoaderCircle, ShieldCheck } from "lucide-react";
import type { ReactNode } from "react";
import { Badge, type BadgeProps } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { ErrorState } from "../../components/ui/QueryState";
import { Readout } from "../../components/ui/Readout";
import { useI18n } from "../../i18n";
import { formatDateTime } from "../../lib/time";
import { getQualityStatus, reviewIPPure } from "./api";
import { purityBand, purityScore, useQualityTime } from "./presentation";

const failures: Record<string, string> = {
  IPPURE_LIMIT: "IPPure 复核正在冷却，请稍后重试",
  IPPURE_UNAVAILABLE: "此节点暂时无法访问 IPPure",
  IPPURE_RESPONSE: "IPPure 返回的数据不完整，未生成评分",
};

type Tone = NonNullable<BadgeProps["tone"]>;

// presentation.ts speaks the old variant vocabulary; the meanings are the tones.
const bandTones: Record<string, Tone> = {
  success: "signal",
  info: "live",
  warning: "warn",
  danger: "alert",
};

/** One measurement of the review result: quiet label, value beneath it. */
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
  // WP08 §9: the review waits up to 15 seconds for the evidence; a 202 means the
  // intel job is still running, so the panel says so instead of showing a score.
  const queued = Boolean(result?.queued && !evidence);
  const errorCode = review.error && "code" in review.error ? String(review.error.code) : "";
  return <section className="space-y-3 border-t border-rule pt-3" aria-label={t("IPPure 节点复核")}>
    <div className="flex flex-wrap items-center justify-between gap-2">
      <h4 className="flex items-center gap-1.5 text-sm font-semibold">
        <ShieldCheck size={16} aria-hidden className="text-ink-faint" />
        {t("IPPure 节点复核")}
      </h4>
      <Badge tone="neutral">{t("按需查询")}</Badge>
    </div>
    <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t("请求会经过此节点，查看 IPPure 对本次实际出口的独立结果。")}</p>
    <div className="flex flex-wrap items-center gap-3">
      <Button variant="secondary" size="sm" onClick={() => review.mutate()} disabled={!ready || !nodeIP || review.isPending || manual?.busy || waiting}>
        {review.isPending ? <LoaderCircle size={14} className="animate-spin" /> : <ShieldCheck size={14} />}
        {t(review.isPending ? "正在经节点查询" : "通过此节点复核")}
      </Button>
      {waiting && <span className="readout text-xs text-ink-soft" role="status">{t("{{seconds}} 秒后可再次复核", { seconds: Math.ceil((nextAt - now) / 1000) })}</span>}
    </div>
    {!nodeIP && <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t("请先完成出口探测，再通过节点查询 IPPure。")}</p>}
    {review.error && <ErrorState message={t(failures[errorCode] || "复核失败，请检查节点连接后重试")} />}
    {queued && <p className="flex flex-wrap items-center gap-2 border border-live/30 bg-live-wash px-3 py-2 text-sm text-live" role="status">
      <LoaderCircle size={14} aria-hidden className="animate-spin" />
      {t("已创建复核任务，仍在等待节点返回；稍后会自动刷新。")}
      {result?.job_id ? <span className="readout text-2xs">#{result.job_id.slice(0, 8)}</span> : null}
    </p>}
    {evidence && <div className="space-y-3 border border-rule bg-paper-raised px-3 py-3" aria-live="polite">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <span className="label block">{t("本次实际出口")}</span>
          <span className="readout text-base font-medium">{evidence.ip}</span>
        </div>
        <Badge tone={sameExit ? "signal" : "warn"} dot>{t(sameExit ? "与节点出口一致" : "与节点记录不同")}</Badge>
      </div>
      {!sameExit && <p className="flex items-start gap-2 text-xs leading-relaxed text-warn">
        <AlertCircle size={14} aria-hidden className="mt-0.5 shrink-0" />
        {t("本次出口与节点记录不同，结果仅用于本次目标，不覆盖节点评级。")}
      </p>}
      <div className="grid gap-4 border-y border-rule py-3 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Readout label={t("IPPure 纯净度参考")} value={score ?? "—"} unit="/ 100" />
          <div className="flex flex-wrap items-center gap-1.5">
            {band && !expired && <Badge tone={bandTones[band.variant] ?? "neutral"}>{t(band.label)}</Badge>}
            {expired && <Badge tone="warn">{t("已过期")}</Badge>}
          </div>
        </div>
        <Readout label={t("IPPure 原始风险")} value={evidence.risk_score ?? "—"} unit="/ 100" />
      </div>
      {!result?.score_supported && <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t("IPPure 未提供此地址的风险分；IPv6 暂不支持评分。")}</p>}
      <dl className="grid gap-x-8 gap-y-3 sm:grid-cols-2">
        <Fact label={t("住宅属性")}>{t(result?.is_residential === true ? "住宅网络" : result?.is_residential === false ? "非住宅网络" : "未知")}</Fact>
        <Fact label={t("地址归属")}>{t(evidence.native === true ? "原生 IP" : evidence.native === false ? "广播 IP" : "未知")}</Fact>
        <Fact label="ASN"><span className="readout">{evidence.asn || "—"}</span></Fact>
        <Fact label={t("网络组织")}>{evidence.organization || "—"}</Fact>
        <Fact label={t("检测于")}><span className="readout">{formatDateTime(evidence.observed_at)}</span></Fact>
      </dl>
    </div>}
    <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t("按需查询，每分钟最多一次。已核对出口的结果临时复用 24 小时，服务重启后清除。")}</p>
    <a className="inline-flex items-center gap-1 text-xs" href="https://ippure.com/MyIP-Info-API" target="_blank" rel="noreferrer">{t("IPPure 接口说明")}<ArrowUpRight size={12} aria-hidden /></a>
  </section>;
}