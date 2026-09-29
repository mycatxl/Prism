import { useMutation, useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, RefreshCw } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Badge, type BadgeProps } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatRelativeTime } from "../../lib/time";
import { createIntelJob } from "../jobs/api";
import { purityBands } from "../quality/presentation";
import type { NodeIntel } from "./types";

// WP10 §4 node intel surface. The API field `intel` carries the prism-purity-v2
// assessment of the node's egress IP; an unassessed node reports explicit empty
// values and state "unassessed", which is rendered as such — never as a score.

const stateLabels: Record<string, string> = {
  unassessed: "未评估",
  pending: "等待评估",
  unsupported: "不支持评估",
  stale: "评估已过期",
};

const confidenceLabels: Record<string, string> = {
  high: "置信度高",
  medium: "置信度中",
  low: "置信度低",
  none: "置信度未知",
};

const flagLabels: Record<string, string> = {
  compromised: "被入侵记录",
  tor: "Tor",
  vpn: "VPN",
  proxy: "代理",
  scraper: "爬取特征",
  abuse: "滥用记录",
  anonymous: "匿名网络",
  hosting: "托管网络",
  dnsbl: "DNSBL 命中",
  attack_history: "攻击历史",
};

const outcomeLabels: Record<string, string> = {
  unknown: "未知",
  available: "可用",
  blocked: "被拦截",
  region_limited: "地区受限",
  captcha: "验证码",
  error: "检测失败",
};

type Tone = NonNullable<BadgeProps["tone"]>;

// The purity bands in presentation.ts still carry the old variant vocabulary;
// the tones are the same three meanings under the names the kit uses.
const bandTones: Record<string, Tone> = {
  success: "signal",
  info: "live",
  warning: "warn",
  danger: "alert",
};

const outcomeTones: Record<string, Tone> = {
  available: "signal",
  blocked: "alert",
  region_limited: "warn",
  captcha: "warn",
  error: "alert",
  unknown: "neutral",
};

/** One measurement in a definition list: quiet label above, value on the sheet. */
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

/** Compact purity cell of the node list table. */
export function NodeIntelCell({ intel }: { intel?: NodeIntel | null }) {
  const { t } = useI18n();
  const state = intel?.state || "unassessed";
  if (state === "unassessed") {
    return (
      <div className="flex min-w-0 flex-col items-start gap-1">
        <Badge tone="neutral" dot>
          {t(stateLabels.unassessed)}
        </Badge>
        <span className="text-2xs text-ink-faint">{t("等待纯净度评估")}</span>
      </div>
    );
  }
  if (state === "pending" || state === "unsupported") {
    const pending = state === "pending";
    return (
      <div className="flex min-w-0 flex-col items-start gap-1">
        <Badge tone={pending ? "live" : "neutral"} dot={pending}>
          {t(stateLabels[state])}
        </Badge>
      </div>
    );
  }
  const band = purityBands.find((item) => item.id === intel?.purity_band);
  const score = typeof intel?.purity_score === "number" ? intel.purity_score : null;
  return (
    <div className="flex min-w-0 flex-col items-start gap-1">
      <Badge tone={bandTones[band?.variant ?? ""] ?? "neutral"}>
        {score !== null && <span className="readout font-semibold">{score}</span>}
        {t(band?.label || "评级未知")}
      </Badge>
      <span className="flex flex-wrap items-center gap-1.5 text-2xs text-ink-faint">
        <span>{t(confidenceLabels[intel?.confidence || "none"])}</span>
        {intel?.assessed_at && (
          <span className="readout">{formatRelativeTime(intel.assessed_at)}</span>
        )}
        {state === "stale" && <span className="text-warn">{t(stateLabels.stale)}</span>}
      </span>
    </div>
  );
}

/**
 * Full assessment block of the node detail drawer.
 *
 * Besides the intel values it owns the one per-node action: "重新检测" creates a
 * single-node `full` job.
 * `ready` is the node's outbound state and gates that button.
 *
 * The IPPure review is deliberately not reachable from here: QualityDetails
 * already renders the existing IPPureReviewPanel for this same node in this
 * drawer, so a second entry point would duplicate the identical widget.
 */
export function NodeIntelPanel({ intel, nodeHash, ready, notify }: {
  intel?: NodeIntel | null;
  nodeHash: string;
  ready: boolean;
  notify: (tone: "success" | "error", text: string) => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [jobCreated, setJobCreated] = useState(false);
  const reinspect = useMutation({
    mutationFn: () => createIntelJob({ kind: "full", scope: { node_hashes: [nodeHash] }, force: true }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["intel-jobs"] });
      void queryClient.invalidateQueries({ queryKey: ["nodes"] });
      void queryClient.invalidateQueries({ queryKey: ["node"] });
      setJobCreated(true);
      notify("success", t("节点检测任务已创建。"));
    },
    onError: (error) => notify("error", formatApiErrorMessage(error, t)),
  });
  const actions = (
    <div className="flex flex-wrap items-center gap-2">
      <Button
        size="sm"
        variant="secondary"
        disabled={!ready || reinspect.isPending}
        title={t("为该节点创建完整检测任务，完成后刷新纯净度评估。")}
        onClick={() => reinspect.mutate()}
      >
        {reinspect.isPending ? <LoaderCircle size={14} className="animate-spin" /> : <RefreshCw size={14} />}
        {t("重新检测")}
      </Button>
      {jobCreated && (
        <Button asChild variant="ghost" size="sm">
          <Link to="/jobs">{t("查看检测任务")}</Link>
        </Button>
      )}
    </div>
  );
  const state = intel?.state || "unassessed";
  if (state === "unassessed") {
    return (
      <div className="space-y-3">
        {actions}
        <p className="max-w-[80ch] text-sm leading-relaxed text-ink-soft">
          {t("该节点还没有纯净度评估，运行一次节点检测后即可看到评分。")}
        </p>
      </div>
    );
  }
  const band = purityBands.find((item) => item.id === intel?.purity_band);
  const checks = Object.entries(intel?.checks || {});
  return (
    <div className="space-y-4">
      {actions}
      <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
        <Fact label={t("纯净度评分")}>
          <span className="readout text-base font-semibold">
            {typeof intel?.purity_score === "number" ? intel.purity_score : "--"}
          </span>
          <span className="text-2xs text-ink-faint">/ 100</span>
          {band && <Badge tone={bandTones[band.variant] ?? "neutral"}>{t(band.label)}</Badge>}
        </Fact>
        <Fact label={t("评估状态")}>{t(stateLabels[state] || state)}</Fact>
        <Fact label={t("置信度")}>{t(confidenceLabels[intel?.confidence || "none"])}</Fact>
        <Fact label={t("判定")}>
          <span className="readout">{intel?.verdict || "--"}</span>
        </Fact>
        <Fact label={t("网络类型")}>
          <span className="readout">{intel?.ip_type || "--"}</span>
        </Fact>
        <Fact label={t("原生 IP")}>
          {intel?.native === null || intel?.native === undefined ? "--" : t(intel.native ? "是" : "否")}
        </Fact>
        <Fact label="ASN">
          {intel?.asn ? <span className="readout">AS{intel.asn}</span> : "--"}
          {intel?.asn && intel.as_org ? <span className="text-ink-soft">{intel.as_org}</span> : null}
        </Fact>
        <Fact label={t("国家 / 城市")}>
          {intel?.country && <span>{intel.country}</span>}
          {intel?.city && <span className="text-ink-soft">{intel.city}</span>}
          {!intel?.country && !intel?.city && "--"}
        </Fact>
        <Fact label={t("出口地址")}>
          <span className="readout">{intel?.egress_ipv4 || "—"}</span>
          {intel?.egress_ipv6 && <span className="readout">{intel.egress_ipv6}</span>}
          {intel?.colo && <span className="text-2xs text-ink-faint">{intel.colo}</span>}
        </Fact>
        <Fact label={t("评估时间")}>
          <span className="readout">
            {intel?.assessed_at ? formatRelativeTime(intel.assessed_at) : "--"}
          </span>
        </Fact>
        <Fact label={t("风险标记")}>
          {(intel?.flags || []).length
            ? (intel?.flags || []).map((flag) => (
                <Badge key={flag} tone="warn">
                  {t(flagLabels[flag] || flag)}
                </Badge>
              ))
            : t("未见明显标记")}
        </Fact>
        {checks.length > 0 && (
          <Fact label={t("检测结果")}>
            {checks.map(([id, outcome]) => (
              <Badge key={id} tone={outcomeTones[outcome] ?? "neutral"}>
                <span className="readout">{id}</span>
                <span>{t(outcomeLabels[outcome] || outcome)}</span>
              </Badge>
            ))}
          </Fact>
        )}
      </dl>
    </div>
  );
}