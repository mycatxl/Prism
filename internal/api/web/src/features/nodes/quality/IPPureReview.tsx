import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, LoaderCircle, ShieldCheck } from "lucide-react";
import type { ReactNode } from "react";
import { Badge, type BadgeProps } from "../../../components/ui/Badge";
import { Button } from "../../../components/ui/Button";
import { Panel, PanelBody, PanelHeader } from "../../../components/ui/Panel";
import { ErrorState } from "../../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../../components/ui/Readout";
import { useI18n } from "../../../i18n";
import { formatDateTime } from "../../../lib/time";
import { getQualityStatus, reviewIPPure } from "./api";
import { purityBand, purityScore, useQualityTime } from "./presentation";

const failures: Record<string, string> = {
  IPPURE_LIMIT: "复核正在冷却，请稍后重试",
  IPPURE_UNAVAILABLE: "此节点暂时无法访问评分来源",
  IPPURE_RESPONSE: "来源返回的数据不完整，未生成评分",
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
  return <Panel className="min-w-0" aria-label={t("纯净度节点复核")}>
    <PanelHeader
      title={
        <span className="flex min-w-0 items-center gap-1.5">
          <ShieldCheck size={16} aria-hidden className="shrink-0 text-ink-faint" />
          <span className="truncate">{t("纯净度节点复核")}</span>
        </span>
      }
      description={t("请求会经过此节点，查看对本次实际出口的独立评分结果。")}
      actions={<Badge tone="neutral">{t("按需查询")}</Badge>}
    />
    <PanelBody className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        <Button
          variant="secondary"
          size="sm"
          onClick={() => review.mutate()}
          disabled={!ready || !nodeIP || review.isPending || manual?.busy || waiting}
          loading={review.isPending}
        >
          <ShieldCheck size={14} />
          {t("通过此节点复核")}
        </Button>
        {waiting && <span className="readout text-xs text-ink-soft" role="status">{t("{{seconds}} 秒后可再次复核", { seconds: Math.ceil((nextAt - now) / 1000) })}</span>}
      </div>
      {!nodeIP && <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t("请先完成出口探测，再通过节点查询评分。")}</p>}
      {review.error && <ErrorState message={t(failures[errorCode] || "复核失败，请检查节点连接后重试")} />}
      {queued && <p className="flex flex-wrap items-center gap-2 border border-live/30 bg-live-wash px-3 py-2 text-sm text-live" role="status">
        <LoaderCircle size={14} aria-hidden className="animate-spin" />
        {t("已创建复核任务，仍在等待节点返回；稍后会自动刷新。")}
        {result?.job_id ? <span className="readout text-2xs">#{result.job_id.slice(0, 8)}</span> : null}
      </p>}
      {evidence && <div className="space-y-3 border-t border-rule pt-3" aria-live="polite">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0">
            <span className="label block">{t("本次实际出口")}</span>
            <span className="readout truncate text-base font-medium" title={evidence.ip}>{evidence.ip}</span>
          </div>
          <Badge tone={sameExit ? "signal" : "warn"} dot>{t(sameExit ? "与节点出口一致" : "与节点记录不同")}</Badge>
        </div>
        {!sameExit && <p className="flex items-start gap-2 text-xs leading-relaxed text-warn">
          <AlertCircle size={14} aria-hidden className="mt-0.5 shrink-0" />
          {t("本次出口与节点记录不同，结果仅用于本次目标，不覆盖节点评级。")}
        </p>}
        <ReadoutStrip>
          <ReadoutCell>
            <Readout label={t("纯净度参考")} value={score ?? "—"} unit="/ 100" />
            <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
              {band && !expired && <Badge tone={bandTones[band.variant] ?? "neutral"}>{t(band.label)}</Badge>}
              {expired && <Badge tone="warn">{t("已过期")}</Badge>}
            </div>
          </ReadoutCell>
          <ReadoutCell>
            <Readout label={t("来源原始风险")} value={evidence.risk_score ?? "—"} unit="/ 100" />
          </ReadoutCell>
        </ReadoutStrip>
        {!result?.score_supported && <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t("来源未提供此地址的风险分；IPv6 暂不支持评分。")}</p>}
        <dl className="grid gap-x-8 gap-y-3 sm:grid-cols-2">
          <Fact label={t("住宅属性")}>{t(result?.is_residential === true ? "住宅网络" : result?.is_residential === false ? "非住宅网络" : "未知")}</Fact>
          <Fact label={t("地址归属")}>{t(evidence.native === true ? "原生 IP" : evidence.native === false ? "广播 IP" : "未知")}</Fact>
          <Fact label="ASN"><span className="readout">{evidence.asn || "—"}</span></Fact>
          <Fact label={t("网络组织")}><span className="block max-w-[46ch] truncate" title={evidence.organization || undefined}>{evidence.organization || "—"}</span></Fact>
          <Fact label={t("检测于")}><span className="readout">{formatDateTime(evidence.observed_at)}</span></Fact>
        </dl>
      </div>}
      <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t("按需查询，每分钟最多一次。已核对出口的结果临时复用 24 小时，服务重启后清除。")}</p>
    </PanelBody>
  </Panel>;
}
