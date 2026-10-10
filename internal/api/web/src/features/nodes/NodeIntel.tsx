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
import { IPTypeBadge } from "./quality/QualityDetails";
import { purityBands, typeLabels } from "./quality/presentation";
import type { QualitySummary } from "./quality/types";
import { getRegionName } from "./regions";
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
  dnsbl: "黑名单命中",
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

/**
 * Compact purity cell of the node list table.
 *
 * 单元格只有一行：主值是纯度 Badge，置信度/评估时间等限定信息跟在后面并被
 * truncate 截断，不再让一行内容撑高整张表。
 */
export function NodeIntelCell({ intel }: { intel?: NodeIntel | null }) {
  const { t } = useI18n();
  const state = intel?.state || "unassessed";
  if (state === "unassessed") {
    return (
      <span className="flex min-w-0 items-center gap-1.5" title={t("等待纯净度评估")}>
        <Badge tone="neutral" dot>
          {t(stateLabels.unassessed)}
        </Badge>
      </span>
    );
  }
  if (state === "pending" || state === "unsupported") {
    const pending = state === "pending";
    return (
      <Badge tone={pending ? "live" : "neutral"} dot={pending}>
        {t(stateLabels[state])}
      </Badge>
    );
  }
  const band = purityBands.find((item) => item.id === intel?.purity_band);
  const score = typeof intel?.purity_score === "number" ? intel.purity_score : null;
  const qualifier = [
    t(confidenceLabels[intel?.confidence || "none"]),
    intel?.assessed_at ? formatRelativeTime(intel.assessed_at) : "",
    state === "stale" ? t(stateLabels.stale) : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <Badge tone={bandTones[band?.variant ?? ""] ?? "neutral"}>
        {score !== null && <span className="readout font-semibold">{score}</span>}
        {t(band?.label || "评级未知")}
      </Badge>
      <span
        className={`min-w-0 truncate text-2xs ${state === "stale" ? "text-warn" : "text-ink-faint"}`}
        title={qualifier}
      >
        {qualifier}
      </span>
    </span>
  );
}

/**
 * Where a node's exit is and what kind of network it is: the two row labels the
 * list carries besides its measurements.
 *
 * The location comes from the assessment — its country plus the city, or the
 * colo when no city was resolved (`JP · Tokyo`, `JP · NRT`). A node that has no
 * assessment yet falls back to the region its egress probe reported, so a node
 * with an egress carries a location label even before it is assessed.
 *
 * The network type is the assessment's own `ip_type`, which the backend votes
 * on: residential, mobile, business, wireless, datacenter, non_residential or
 * conflicting (`internal/intel/assess/assess.go`). When the assessment carries
 * no type of its own the quality evidence answers instead (`IPTypeBadge`), so the
 * row never claims "类型未知" while a provider does know the answer.
 *
 * This is one cell of one line by construction: the grid's rows are a fixed
 * 28px, so nothing here may wrap.
 */
export function NodeLabels({
  intel,
  region,
  quality,
}: {
  intel?: NodeIntel | null;
  region?: string;
  quality?: QualitySummary | null;
}) {
  const { t } = useI18n();
  const code = (intel?.country || region || "").toUpperCase();
  const place = intel?.city || intel?.colo || "";
  const ipType = intel?.ip_type || "";
  const type =
    ipType && ipType !== "unknown"
      ? typeLabels[ipType as keyof typeof typeLabels]
      : "";
  const title =
    [code ? getRegionName(code) : undefined, place, type ? t(type) : ""]
      .filter(Boolean)
      .join(" · ") || undefined;
  return (
    <span className="flex min-w-0 items-center gap-1.5" title={title}>
      <span className="readout shrink-0 font-medium text-ink">{code || "—"}</span>
      {place && (
        <>
          <span aria-hidden className="shrink-0 text-ink-faint">
            ·
          </span>
          <span className="min-w-0 truncate text-xs text-ink-soft">{place}</span>
        </>
      )}
      {type ? (
        <Badge tone={ipType === "conflicting" ? "warn" : "outline"}>{t(type)}</Badge>
      ) : (
        <IPTypeBadge summary={quality} />
      )}
    </span>
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
        <p className="max-w-[68ch] text-sm leading-relaxed text-ink-soft">
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