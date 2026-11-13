import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, LoaderCircle, ShieldCheck } from "lucide-react";
import type { ReactNode } from "react";
import { ControlButton, WorkspaceCard } from "../../../components/control-plane";
import { Badge } from "../../../components/ui/Badge";
import { ErrorState } from "../../../components/ui/QueryState";
import { useI18n } from "../../../i18n";
import { formatDateTime } from "../../../lib/time";
import { getQualityStatus, reviewIPPure } from "./api";
import { useNow } from "../../../lib/clock";
const failures: Record<string, string> = {
  IPPURE_LIMIT: "复核正在冷却，请稍后重试",
  IPPURE_UNAVAILABLE: "检测暂时不可用，请稍后重试",
  IPPURE_RESPONSE: "检测结果不完整，请重新检测",
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
  const now = useNow();
  const client = useQueryClient();
  const status = useQuery({ queryKey: ["quality", "status"], queryFn: getQualityStatus, staleTime: 5000 });
  const review = useMutation({ mutationFn: () => reviewIPPure(nodeHash), retry: false, gcTime: 0,
    onSettled: () => { void client.invalidateQueries({ queryKey: ["quality"] }); void client.invalidateQueries({ queryKey: ["nodes"] }); void client.invalidateQueries({ queryKey: ["node"] }); } });
  const manual = status.data?.manual_sources?.find(source => source.id === "ippure");
  const result = review.data;
  const evidence = result?.evidence;
  const nextAt = Math.max(Date.parse(manual?.next_allowed_at || "") || 0, Date.parse(result?.next_allowed_at || "") || 0);
  const waiting = nextAt > now;
  const expired = evidence && Date.parse(evidence.valid_until) <= now;
  const sameExit = result?.matches_node_ip && evidence?.ip === nodeIP;
 // the review waits up to 15 seconds for the evidence; a 202 means the
  // intel job is still running, so the panel says so instead of showing a score.
  const queued = Boolean(result?.queued && !evidence);
  const errorCode = review.error && "code" in review.error ? String(review.error.code) : "";
  return (
    <WorkspaceCard
      className="min-w-0"
      title={
        <span className="flex min-w-0 items-center gap-1.5">
          <ShieldCheck size={16} aria-hidden className="shrink-0 text-ink-faint" />
          <span className="truncate">{t("纯净度复核")}</span>
        </span>
      }
      description={t("通过此节点重新检测当前出口，完成后更新评分。")}
      actions={<Badge tone="neutral">{t("按需查询")}</Badge>}
    >
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-3">
          <ControlButton
            variant="secondary"
            size="sm"
            onPress={() => review.mutate()}
            isDisabled={!ready || !nodeIP || review.isPending || manual?.busy || waiting}
            loading={review.isPending}
          >
            <ShieldCheck size={14} />
            {t("通过此节点复核")}
          </ControlButton>
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
          {expired && <Badge tone="warn">{t("已过期")}</Badge>}
          {!result?.score_supported && <p className="max-w-[68ch] text-xs leading-relaxed text-ink-soft">{t(evidence.ip.includes(":") ? "当前 IPv6 出口暂不支持评分" : "暂未获得有效评分，请重新检测")}</p>}
          <dl className="grid gap-x-8 gap-y-3 sm:grid-cols-2">
            <Fact label={t("住宅属性")}>{t(result?.is_residential === true ? "住宅网络" : result?.is_residential === false ? "非住宅网络" : "未知")}</Fact>
            <Fact label={t("地址归属")}>{t(evidence.native === true ? "原生 IP" : evidence.native === false ? "广播 IP" : "未知")}</Fact>
            <Fact label="ASN"><span className="readout">{evidence.asn || "—"}</span></Fact>
            <Fact label={t("网络组织")}><span className="block max-w-[46ch] truncate" title={evidence.organization || undefined}>{evidence.organization || "—"}</span></Fact>
            <Fact label={t("有效至")}><span className="readout">{formatDateTime(evidence.valid_until)}</span></Fact>
            <Fact label={t("检测于")}><span className="readout">{formatDateTime(evidence.observed_at)}</span></Fact>
          </dl>
        </div>}
      </div>
    </WorkspaceCard>
  );
}
