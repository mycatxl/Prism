import { RefreshCw } from "lucide-react";
import { Button } from "../../../components/ui/Button";
import { Panel, PanelBody, PanelHeader } from "../../../components/ui/Panel";
import { useI18n } from "../../../i18n";
import { cn } from "../../../lib/cn";
import { formatDateTime } from "../../../lib/time";
import { outcomeLabels } from "../quality/presentation";
import type { IntelNodeDetail } from "./api";
import type { NodeSummary } from "../types";

/**
 * The node's recent events: what the intel report recorded (egress IP changes,
 * unlock checks) merged with the node record's own facts (creation, egress
 * updates, probe attempts, the open circuit). Newest first, capped, and every
 * event is a dot plus a word — a colour is never the whole message.
 */

const MAX_EVENTS = 12;

export type NodeEventLevel = "neutral" | "signal" | "live" | "warn" | "alert";

type NodeEvent = {
  atMs: number;
  level: NodeEventLevel;
  text: string;
};

const DOT_CLASS: Record<NodeEventLevel, string> = {
  neutral: "",
  signal: "state-dot--signal",
  live: "state-dot--live",
  warn: "state-dot--warn",
  alert: "state-dot--alert",
};

/** The check outcome's event level mirrors the badge tone the console already uses. */
const OUTCOME_LEVEL: Record<string, NodeEventLevel> = {
  available: "signal",
  blocked: "alert",
  region_limited: "warn",
  captcha: "warn",
  error: "alert",
  unknown: "neutral",
};

/**
 * buildNodeEvents assembles the merged, newest-first list from the two sources
 * that actually carry times: the intel report (history + checks) and the node
 * record's probe stamps. An absent source simply contributes nothing.
 */
function buildNodeEvents(
  node: NodeSummary,
  intel: IntelNodeDetail | undefined,
  translate: (key: string, options?: Record<string, unknown>) => string,
): NodeEvent[] {
  const events: NodeEvent[] = [];

  for (const entry of intel?.history ?? []) {
    const atMs = Math.round(entry.observed_at_ns / 1_000_000);
    if (!atMs || !entry.ip) continue;
    events.push({
      atMs,
      level: "live",
      text:
        entry.family === 6
          ? translate("出口 IPv6 记录为 {{ip}}", { ip: entry.ip })
          : translate("出口 IP 记录为 {{ip}}", { ip: entry.ip }),
    });
  }

  for (const check of intel?.checks ?? []) {
    const atMs = Math.round(check.observed_at_ns / 1_000_000);
    if (!atMs || !check.check_id) continue;
    events.push({
      atMs,
      level: OUTCOME_LEVEL[check.outcome] ?? "neutral",
      text: translate("检测 {{check}}：{{outcome}}", {
        check: check.check_id,
        outcome: translate(outcomeLabels[check.outcome] || check.outcome),
      }),
    });
  }

  if (node.circuit_open_since) {
    const atMs = Date.parse(node.circuit_open_since);
    if (Number.isFinite(atMs)) {
      events.push({ atMs, level: "warn", text: translate("熔断已触发") });
    }
  }
  if (node.last_egress_update) {
    const atMs = Date.parse(node.last_egress_update);
    if (Number.isFinite(atMs)) {
      events.push({ atMs, level: "live", text: translate("出口 IP 更新") });
    }
  }
  if (node.last_egress_update_attempt) {
    const atMs = Date.parse(node.last_egress_update_attempt);
    if (Number.isFinite(atMs)) {
      events.push({ atMs, level: "neutral", text: translate("尝试更新出口 IP") });
    }
  }
  if (node.last_latency_probe_attempt) {
    const atMs = Date.parse(node.last_latency_probe_attempt);
    if (Number.isFinite(atMs)) {
      events.push({ atMs, level: "neutral", text: translate("执行延迟探测") });
    }
  }
  if (node.last_authority_latency_probe_attempt) {
    const atMs = Date.parse(node.last_authority_latency_probe_attempt);
    if (Number.isFinite(atMs)) {
      events.push({ atMs, level: "neutral", text: translate("执行权威延迟探测") });
    }
  }
  if (node.created_at) {
    const atMs = Date.parse(node.created_at);
    if (Number.isFinite(atMs)) {
      events.push({ atMs, level: "neutral", text: translate("节点已登记") });
    }
  }

  events.sort((a, b) => b.atMs - a.atMs);
  return events.slice(0, MAX_EVENTS);
}

export function NodeEventsPanel({
  node,
  intel,
  intelUnavailable,
  isFetchingIntel,
  onRetryIntel,
}: {
  node: NodeSummary;
  intel: IntelNodeDetail | undefined;
  /** True when the intel report failed: the record events still show, with a retry. */
  intelUnavailable: boolean;
  isFetchingIntel: boolean;
  onRetryIntel: () => void;
}) {
  const { t } = useI18n();
  const events = buildNodeEvents(node, intel, t);

  return (
    <Panel>
      <PanelHeader
        title={t("最近事件")}
        actions={
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("刷新")}
            title={t("刷新")}
            disabled={isFetchingIntel}
            onClick={onRetryIntel}
          >
            <RefreshCw size={15} aria-hidden className={isFetchingIntel ? "animate-spin" : undefined} />
          </Button>
        }
      />
      <PanelBody className="space-y-2">
        <ul className="divide-y divide-rule-faint">
          {events.map((event, index) => (
            <li key={`${event.atMs}:${index}`} className="flex items-start gap-2.5 py-2.5">
              <span aria-hidden className={cn("state-dot mt-1.5", DOT_CLASS[event.level])} />
              <p className="min-w-0 flex-1 text-sm text-ink">{event.text}</p>
              <time className="numeral shrink-0 pt-0.5 text-2xs text-ink-faint">
                {formatDateTime(new Date(event.atMs).toISOString())}
              </time>
            </li>
          ))}
        </ul>
        {events.length === 0 && (
          <p className="py-2 text-sm text-ink-faint">{t("暂无事件记录")}</p>
        )}
        {intelUnavailable && (
          <p className="text-xs text-ink-faint">
            {t("检测记录暂不可用，已仅显示节点自身记录。")}
          </p>
        )}
      </PanelBody>
    </Panel>
  );
}
