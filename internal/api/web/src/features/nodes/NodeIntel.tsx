import { useMutation, useQueryClient } from "@tanstack/react-query";
import { LoaderCircle, RefreshCw } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
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

/** Compact purity cell of the node list table. */
export function NodeIntelCell({ intel }: { intel?: NodeIntel | null }) {
  const { t } = useI18n();
  const state = intel?.state || "unassessed";
  if (state === "unassessed") {
    return (
      <div className="node-quality-cell">
        <Badge variant="muted">{t(stateLabels.unassessed)}</Badge>
        <small>{t("等待纯净度评估")}</small>
      </div>
    );
  }
  if (state === "pending" || state === "unsupported") {
    return (
      <div className="node-quality-cell">
        <Badge>{t(stateLabels[state])}</Badge>
      </div>
    );
  }
  const band = purityBands.find((item) => item.id === intel?.purity_band);
  const score = typeof intel?.purity_score === "number" ? intel.purity_score : null;
  return (
    <div className="node-quality-cell">
      <Badge variant={band?.variant || "neutral"}>
        {score !== null && <strong className="purity-badge-score">{score}</strong>}
        {t(band?.label || "评级未知")}
      </Badge>
      <small>
        {t(confidenceLabels[intel?.confidence || "none"])}
        {intel?.assessed_at ? " · " + formatRelativeTime(intel.assessed_at) : ""}
      </small>
      {state === "stale" && <small>{t(stateLabels.stale)}</small>}
    </div>
  );
}

/**
 * Full assessment block of the node detail drawer.
 *
 * Besides the intel values it owns the one per-node action the plan requires
 * (docs/plan/12-frontend.md:57): "重新检测" creates a single-node `full` job.
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
    <div className="node-intel-actions">
      <Button
        size="sm"
        variant="secondary"
        disabled={!ready || reinspect.isPending}
        title={t("为该节点创建完整检测任务，完成后刷新纯净度评估。")}
        onClick={() => reinspect.mutate()}
      >
        {reinspect.isPending ? <LoaderCircle size={14} className="spin" /> : <RefreshCw size={14} />}
        {t("重新检测")}
      </Button>
      {jobCreated && <Link className="btn btn-ghost btn-sm" to="/jobs">{t("查看检测任务")}</Link>}
    </div>
  );
  const state = intel?.state || "unassessed";
  if (state === "unassessed") {
    return (
      <>
        {actions}
        <p className="muted">{t("该节点还没有纯净度评估，运行一次节点检测后即可看到评分。")}</p>
      </>
    );
  }
  const band = purityBands.find((item) => item.id === intel?.purity_band);
  const checks = Object.entries(intel?.checks || {});
  return (
    <>
      {actions}
      <dl className="detail-facts">
        <div>
          <dt>{t("纯净度评分")}</dt>
          <dd>
            {typeof intel?.purity_score === "number" ? intel.purity_score + " / 100" : "--"}
            {band && " · " + t(band.label)}
          </dd>
        </div>
        <div>
          <dt>{t("评估状态")}</dt>
          <dd>{t(stateLabels[state] || state)}</dd>
        </div>
        <div>
          <dt>{t("置信度")}</dt>
          <dd>{t(confidenceLabels[intel?.confidence || "none"])}</dd>
        </div>
        <div>
          <dt>{t("判定")}</dt>
          <dd>{intel?.verdict || "--"}</dd>
        </div>
        <div>
          <dt>{t("网络类型")}</dt>
          <dd>{intel?.ip_type || "--"}</dd>
        </div>
        <div>
          <dt>{t("原生 IP")}</dt>
          <dd>{intel?.native === null || intel?.native === undefined ? "--" : t(intel.native ? "是" : "否")}</dd>
        </div>
        <div>
          <dt>ASN</dt>
          <dd>{intel?.asn ? `AS${intel.asn}${intel.as_org ? " · " + intel.as_org : ""}` : "--"}</dd>
        </div>
        <div>
          <dt>{t("国家 / 城市")}</dt>
          <dd>{[intel?.country, intel?.city].filter(Boolean).join(" · ") || "--"}</dd>
        </div>
        <div>
          <dt>{t("出口地址")}</dt>
          <dd>{[intel?.egress_ipv4, intel?.egress_ipv6].filter(Boolean).join(" · ") || "--"}{intel?.colo ? ` · ${intel.colo}` : ""}</dd>
        </div>
        <div>
          <dt>{t("评估时间")}</dt>
          <dd>{intel?.assessed_at ? formatRelativeTime(intel.assessed_at) : "--"}</dd>
        </div>
        <div>
          <dt>{t("风险标记")}</dt>
          <dd>
            {(intel?.flags || []).length
              ? (intel?.flags || []).map((flag) => <Badge key={flag} variant="warning">{t(flagLabels[flag] || flag)}</Badge>)
              : t("未见明显标记")}
          </dd>
        </div>
        {checks.length > 0 && (
          <div>
            <dt>{t("检测结果")}</dt>
            <dd>
              {checks.map(([id, outcome]) => (
                <Badge key={id} variant={outcome === "available" ? "success" : "neutral"}>
                  {id}: {t(outcomeLabels[outcome] || outcome)}
                </Badge>
              ))}
            </dd>
          </div>
        )}
      </dl>
    </>
  );
}
