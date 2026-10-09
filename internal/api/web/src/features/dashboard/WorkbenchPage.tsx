import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Button } from "../../components/ui/Button";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatApiErrorMessage } from "../../lib/error-message";
import { listAuditLogs } from "../audit/api";
import { listIntelJobs } from "../jobs/api";
import type { IntelJob } from "../jobs/types";
import { listPlatforms } from "../platforms/api";
import { getEnvConfig } from "../systemConfig/api";
import { SYSTEM_INFO_QUERY_KEY, getSystemInfo } from "../systemInfo/api";
import { listSubscriptions, refreshSubscription } from "../subscriptions/api";
import {
  getDashboardGlobalSnapshotData,
  getGlobalLeaseSeries,
  getPlatformPulse,
  getTrafficAndProbes,
  listNodeExitFacts,
} from "./api";
import { alertPhrase } from "./auditPhrases";
import EgressMap from "./EgressMap";
import { aggregateExitCountries, useCountryName } from "./exitCountries";
import { PLACEHOLDER, formatBytes, formatCount, toEpochMs } from "./format";
import { NODE_EXITS_REFRESH_MS, SNAPSHOT_REFRESH_MS, getTimeWindow } from "./range";

const OFFLINE = "无法连接服务，请检查连接后重试。";
const HOUR_MS = 3_600_000;

const JOB_KIND_LABELS: Record<string, string> = {
  intel: "情报检测",
  full: "完整检测",
  egress: "出口探测",
  checks: "解锁检测",
};

/** IP types folded into the heat grid's four rows. */
const TYPE_ROWS: { label: string; match: (type: string) => boolean }[] = [
  { label: "住宅", match: (type) => type === "residential" },
  { label: "机房", match: (type) => type === "datacenter" },
  { label: "移动", match: (type) => type === "mobile" || type === "wireless" },
  { label: "其他", match: (type) => !["residential", "datacenter", "mobile", "wireless"].includes(type) },
];

/** Purity bands folded into the heat grid's four columns. */
const BAND_COLUMNS: { label: string; bands: string[] }[] = [
  { label: "纯净", bands: ["excellent", "clean"] },
  { label: "良好", bands: ["fair"] },
  { label: "一般", bands: ["mixed"] },
  { label: "较差", bands: ["poor"] },
];

type Tone = "ok" | "warn" | "bad" | "faint" | "accent";

function Dot({ tone }: { tone: Tone }) {
  return <span aria-hidden className={`dash-dot dash-dot--${tone}`} />;
}

/** A thin stacked bar: each part is a share in [0, 1] with a tone. */
function Bar({ parts, className }: { parts: { share: number; tone: Tone }[]; className?: string }) {
  return (
    <span className={cn("dash-bar", className)} aria-hidden>
      {parts.map((part, index) => (
        <i key={index} className={`dash-fill--${part.tone}`} style={{ width: `${Math.max(0, Math.min(1, part.share)) * 100}%` }} />
      ))}
    </span>
  );
}

function Card({
  className,
  title,
  meta,
  action,
  footer,
  children,
}: {
  className: string;
  title: string;
  meta?: ReactNode;
  action?: ReactNode;
  footer?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className={cn("dash-card", className)} aria-label={title}>
      <header className="dash-card__head">
        <h2>{title}</h2>
        {meta !== undefined && meta !== null && <span className="dash-card__meta">{meta}</span>}
        {action && <span className="dash-card__action">{action}</span>}
      </header>
      {children}
      {footer && <div className="dash-card__foot">{footer}</div>}
    </section>
  );
}

function clock(ms: number): string {
  const date = new Date(ms);
  return `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
}

function jobProgress(job: IntelJob): number {
  if (job.total <= 0) return 0;
  return (job.done + job.failed + job.skipped) / job.total;
}

/**
 * The overview board: one viewport of twelve-column glass cards. Every figure is
 * read from the API; a card whose source is loading, failed or empty says so
 * instead of showing a number.
 */
export function WorkbenchPage() {
  const { t } = useI18n();
  const { toasts, showToast, dismissToast } = useToast();
  const queryClient = useQueryClient();
  const countryName = useCountryName();
  const offline = t(OFFLINE);

  const info = useQuery({ queryKey: SYSTEM_INFO_QUERY_KEY, queryFn: getSystemInfo, refetchInterval: 30_000, retry: false });
  const env = useQuery({ queryKey: ["system-config-env", "shell"], queryFn: getEnvConfig, staleTime: 30_000 });
  const snapshot = useQuery({
    queryKey: ["dashboard-global-snapshot"],
    queryFn: getDashboardGlobalSnapshotData,
    refetchInterval: SNAPSHOT_REFRESH_MS,
    placeholderData: (previous) => previous,
  });
  const nodes = useQuery({
    queryKey: ["workbench", "node-exits"],
    queryFn: ({ signal }) => listNodeExitFacts(signal),
    refetchInterval: NODE_EXITS_REFRESH_MS,
    placeholderData: (previous) => previous,
  });
  const day = useQuery({
    queryKey: ["workbench", "traffic-probes-24h"],
    queryFn: () => getTrafficAndProbes(getTimeWindow("24h")),
    refetchInterval: 60_000,
    placeholderData: (previous) => previous,
  });
  const leases = useQuery({
    queryKey: ["workbench", "leases-24h"],
    queryFn: () => getGlobalLeaseSeries(getTimeWindow("24h")),
    refetchInterval: 30_000,
    placeholderData: (previous) => previous,
  });
  const platforms = useQuery({
    queryKey: ["workbench", "platforms"],
    queryFn: () => listPlatforms({ limit: 100 }),
    staleTime: 60_000,
    refetchInterval: 120_000,
  });
  const subscriptions = useQuery({
    queryKey: ["workbench", "subscriptions"],
    queryFn: () => listSubscriptions({ limit: 200 }),
    staleTime: 30_000,
    refetchInterval: 60_000,
  });
  const events = useQuery({ queryKey: ["workbench", "events"], queryFn: () => listAuditLogs({ limit: 3 }), refetchInterval: 60_000 });
  const jobs = useQuery({ queryKey: ["workbench", "jobs"], queryFn: () => listIntelJobs("", 6), refetchInterval: 15_000 });
  const running = useQuery({ queryKey: ["workbench", "jobs", "running"], queryFn: () => listIntelJobs("running", 1), refetchInterval: 15_000 });
  const queued = useQuery({ queryKey: ["workbench", "jobs", "queued"], queryFn: () => listIntelJobs("queued", 1), refetchInterval: 15_000 });

  const platformItems = useMemo(() => platforms.data?.items ?? [], [platforms.data]);
  const pulses = useQueries({
    queries: platformItems.map((platform) => ({
      queryKey: ["workbench", "platform-pulse", platform.id],
      queryFn: () => getPlatformPulse(platform.id, getTimeWindow("1h")),
      refetchInterval: 30_000,
      placeholderData: (previous: Awaited<ReturnType<typeof getPlatformPulse>> | undefined) => previous,
    })),
  });

  const retry = useMutation({
    mutationFn: (id: string) => refreshSubscription(id),
    onSuccess: () => {
      showToast("success", t("已开始刷新"));
      void queryClient.invalidateQueries({ queryKey: ["workbench", "subscriptions"] });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  /* ---- derived facts ---- */
  const facts = useMemo(() => nodes.data ?? [], [nodes.data]);
  const countries = useMemo(() => aggregateExitCountries(facts), [facts]);
  const pool = snapshot.data?.snapshot_node_pool;
  const egress24h = useMemo(() => (day.data?.traffic.items ?? []).reduce((sum, item) => sum + item.egress_bytes, 0), [day.data]);
  const probes24h = useMemo(() => (day.data?.probes.items ?? []).reduce((sum, item) => sum + item.total_count, 0), [day.data]);
  const circuitOpen = useMemo(() => facts.filter((fact) => fact.circuitOpen).length, [facts]);

  const quality = useMemo(() => {
    const byIp = new Map<string, { type: string; band: string }>();
    for (const fact of facts) {
      if (fact.egressIp && !byIp.has(fact.egressIp)) byIp.set(fact.egressIp, { type: fact.ipType, band: fact.purityBand });
    }
    const grid = TYPE_ROWS.map(() => BAND_COLUMNS.map(() => 0));
    let unassessed = 0;
    for (const { type, band } of byIp.values()) {
      const column = BAND_COLUMNS.findIndex((entry) => entry.bands.includes(band));
      if (column < 0) {
        unassessed += 1;
        continue;
      }
      const row = TYPE_ROWS.findIndex((entry) => entry.match(type));
      grid[row][column] += 1;
    }
    const max = Math.max(0, ...grid.flat());
    return { grid, max, unassessed, assessed: byIp.size - unassessed };
  }, [facts]);

  const hourly = useMemo(() => {
    const items = leases.data?.items ?? [];
    const end = leases.dataUpdatedAt;
    const start = end - 24 * HOUR_MS;
    const bars: (number | null)[] = Array.from({ length: 24 }, () => null);
    let peak = 0;
    for (const item of items) {
      const stamp = toEpochMs(item.ts);
      if (stamp === null || stamp < start) continue;
      const index = Math.min(23, Math.floor((stamp - start) / HOUR_MS));
      bars[index] = Math.max(bars[index] ?? 0, item.active_leases);
      peak = Math.max(peak, item.active_leases);
    }
    return { bars, peak, latest: items.at(-1)?.active_leases ?? null };
  }, [leases.data, leases.dataUpdatedAt]);

  const platformRows = useMemo(
    () =>
      platformItems
        .map((platform, index) => {
          const pulse = pulses[index]?.data;
          const routable = pulse?.pool.routable_node_count ?? platform.routable_node_count;
          return {
            platform,
            routable,
            exitIps: pulse ? pulse.pool.egress_ip_count : null,
            sessions: pulse ? pulse.activeLeases : null,
            problem: routable === 0,
          };
        })
        .sort(
          (a, b) =>
            Number(b.problem) - Number(a.problem) ||
            (b.sessions ?? -1) - (a.sessions ?? -1) ||
            b.routable - a.routable ||
            a.platform.name.localeCompare(b.platform.name),
        ),
    [platformItems, pulses],
  );
  const maxRoutable = Math.max(1, ...platformRows.map((row) => row.routable));

  const subscriptionRows = useMemo(
    () => [...(subscriptions.data?.items ?? [])].sort((a, b) => b.node_count - a.node_count || a.name.localeCompare(b.name)),
    [subscriptions.data],
  );

  type Issue = { key: string; tone: Tone; title: string; detail: string; action: ReactNode };
  const issues: Issue[] = [];
  for (const source of subscriptionRows) {
    if (source.enabled && source.last_error) {
      issues.push({
        key: `sub-${source.id}`,
        tone: "bad",
        title: t("{{name}} 刷新失败", { name: source.name }),
        detail: source.last_error,
        action: (
          <Button size="sm" loading={retry.isPending && retry.variables === source.id} onClick={() => retry.mutate(source.id)}>
            {t("重试")}
          </Button>
        ),
      });
    }
  }
  for (const row of platformRows) {
    if (row.problem) {
      issues.push({
        key: `platform-${row.platform.id}`,
        tone: "warn",
        title: t("{{name}} 没有可路由节点", { name: row.platform.name }),
        detail: t("请求会按平台的未命中策略处理"),
        action: (
          <Button asChild size="sm">
            <Link to={`/platforms/${encodeURIComponent(row.platform.id)}`}>{t("查看")}</Link>
          </Button>
        ),
      });
    }
  }
  if (circuitOpen > 0) {
    issues.push({
      key: "circuit",
      tone: "warn",
      title: t("{{count}} 个节点熔断", { count: formatCount(circuitOpen) }),
      detail: t("已暂停分配，恢复前不会接收请求"),
      action: (
        <Button asChild size="sm">
          <Link to="/nodes?circuit_open=true">{t("查看")}</Link>
        </Button>
      ),
    });
  }
  if (quality.unassessed > 0) {
    issues.push({
      key: "unassessed",
      tone: "faint",
      title: t("{{count}} 个 IP 未做质量检测", { count: formatCount(quality.unassessed) }),
      detail: t("不会命中纯净度筛选"),
      action: (
        <Button asChild size="sm">
          <Link to="/jobs">{t("检测")}</Link>
        </Button>
      ),
    });
  }
  if (env.data && (!env.data.admin_token_set || !env.data.proxy_token_set)) {
    issues.push({
      key: "tokens",
      tone: "warn",
      title: t("令牌未设置"),
      detail: t("部分令牌未设置"),
      action: (
        <Button asChild size="sm">
          <Link to="/system-config">{t("设置")}</Link>
        </Button>
      ),
    });
  }
  const issueSourcesReady = Boolean(subscriptions.data && platforms.data && nodes.data);
  const issueSourcesFailed = subscriptions.isError || platforms.isError || nodes.isError;

  const origin = info.data?.panel_egress_region?.trim().toUpperCase() ?? "";
  const loading = <LoadingState className="dash-state" label={t("正在加载")} />;
  const failed = (onRetry: () => void) => <ErrorState className="dash-state" message={offline} onRetry={onRetry} />;

  return (
    <div className="dash">
      <h1 className="sr-only">{t("总览")}</h1>

      {/* ---- exit network ---- */}
      <Card
        className="dash-map"
        title={t("出口网络")}
        meta={origin ? t("本机在{{place}}", { place: countryName(origin) }) : undefined}
      >
        <div className="dash-map__stats">
          <span>
            <b className="readout">{pool ? formatCount(pool.healthy_egress_ip_count) : PLACEHOLDER}</b> / {pool ? formatCount(pool.egress_ip_count) : PLACEHOLDER} {t("可用出口 IP")}
          </span>
          <span>
            <b className="readout">{nodes.data ? formatCount(countries.length) : PLACEHOLDER}</b> {t("个地区")}
          </span>
          <span>
            {t("出站")} <b className="readout">{day.data ? formatBytes(egress24h) : PLACEHOLDER}</b> · 24h
          </span>
          <span className="dash-map__legend">
            <span><Dot tone="accent" /> {t("节点出口")}</span>
            {origin && <span><span aria-hidden className="dash-dot dash-dot--origin" /> {t("本机")}</span>}
          </span>
        </div>
        <div className="dash-map__plate">
          {nodes.isError && !nodes.data ? (
            failed(() => void nodes.refetch())
          ) : !nodes.data ? (
            loading
          ) : facts.length === 0 ? (
            <EmptyState
              className="dash-state"
              title={t("建立你的第一个节点池")}
              hint={t("添加订阅链接或导入本地节点，开始查看线路状态。")}
              action={<Button asChild variant="primary"><Link to="/subscriptions?create=1">{t("开始导入")}</Link></Button>}
            />
          ) : (
            <EgressMap countries={countries} origin={origin} />
          )}
        </div>
      </Card>

      {/* ---- needs attention ---- */}
      <Card
        className="dash-issues"
        title={t("需要处理")}
        meta={issueSourcesReady ? t("{{count}} 项", { count: issues.length }) : undefined}
        footer={
          <div className="dash-changes">
            <div className="dash-changes__head">
              <span>{t("最近变更")}</span>
              <Link to="/audit" className="dash-link">{t("审计日志")}</Link>
            </div>
            {events.isError && !events.data ? (
              <span className="dash-muted">{offline}</span>
            ) : !events.data ? (
              <span className="dash-muted">{t("正在加载")}</span>
            ) : events.data.items.length === 0 ? (
              <span className="dash-muted">{t("暂无变更记录")}</span>
            ) : (
              events.data.items.map((entry) => {
                const { method, phrase } = alertPhrase(entry.action);
                return (
                  <div key={entry.id} className="dash-change" title={entry.action}>
                    <b className={cn("readout dash-change__method", method === "DELETE" ? "text-alert" : method === "PATCH" ? "text-accent" : "text-signal")}>
                      {method || PLACEHOLDER}
                    </b>
                    <span className="dash-change__text">{t(phrase)}{entry.target ? ` · ${entry.target}` : ""}</span>
                    <span className="readout">{clock(entry.at_ns / 1_000_000)}</span>
                  </div>
                );
              })
            )}
          </div>
        }
      >
        <div className="dash-scroll">
          {issueSourcesFailed && !issueSourcesReady ? (
            failed(() => {
              void subscriptions.refetch();
              void platforms.refetch();
              void nodes.refetch();
            })
          ) : !issueSourcesReady ? (
            loading
          ) : issues.length === 0 ? (
            <EmptyState className="dash-state" title={t("一切正常")} hint={t("没有需要处理的问题")} />
          ) : (
            issues.map((issue) => (
              <div key={issue.key} className="dash-issue">
                <Dot tone={issue.tone} />
                <span className="dash-issue__text">
                  <b>{issue.title}</b>
                  <span title={issue.detail}>{issue.detail}</span>
                </span>
                {issue.action}
              </div>
            ))
          )}
        </div>
      </Card>

      {/* ---- platforms ---- */}
      <Card
        className="dash-platforms"
        title={t("平台")}
        meta={t("有问题的排在前面")}
        action={<Link to="/platforms" className="dash-link">{t("管理平台")}</Link>}
      >
        {platforms.isError && !platforms.data ? (
          failed(() => void platforms.refetch())
        ) : !platforms.data ? (
          loading
        ) : platformRows.length === 0 ? (
          <EmptyState
            className="dash-state"
            title={t("无平台")}
            hint={t("创建平台以聚合节点")}
            action={<Button asChild variant="primary"><Link to="/platforms">{t("创建平台")}</Link></Button>}
          />
        ) : (
          <div className="dash-table" tabIndex={0} role="region" aria-label={t("平台")}>
            <table>
              <thead>
                <tr>
                  <th scope="col">{t("平台")}</th>
                  <th scope="col">{t("可路由节点")}</th>
                  <th scope="col" className="is-num">{t("出口 IP")}</th>
                  <th scope="col" className="is-num">{t("活跃会话")}</th>
                  <th scope="col" className="is-num">{t("更新时间")}</th>
                </tr>
              </thead>
              <tbody>
                {platformRows.map((row) => (
                  <tr key={row.platform.id}>
                    <td>
                      <Link to={`/platforms/${encodeURIComponent(row.platform.id)}`} className="dash-platform-name">
                        <Dot tone={row.problem ? "bad" : "ok"} />
                        <b>{row.platform.name}</b>
                      </Link>
                    </td>
                    <td>
                      <span className="dash-rate">
                        <Bar parts={[{ share: row.routable / maxRoutable, tone: row.problem ? "bad" : "ok" }]} />
                        <span className={cn("readout", row.problem && "text-alert")}>{formatCount(row.routable)}</span>
                      </span>
                    </td>
                    <td className="is-num readout">{row.exitIps === null ? PLACEHOLDER : formatCount(row.exitIps)}</td>
                    <td className="is-num readout">{row.sessions === null ? PLACEHOLDER : formatCount(row.sessions)}</td>
                    <td className="is-num readout dash-muted">
                      {row.platform.updated_at ? new Date(row.platform.updated_at).toLocaleDateString() : PLACEHOLDER}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {/* ---- exit IP quality ---- */}
      <Card
        className="dash-quality"
        title={t("出口 IP 质量")}
        meta={nodes.data ? `${t("类型 × 纯净度")}${quality.unassessed > 0 ? ` · ${t("未检测")} ${formatCount(quality.unassessed)}` : ""}` : undefined}
      >
        {nodes.isError && !nodes.data ? (
          failed(() => void nodes.refetch())
        ) : !nodes.data ? (
          loading
        ) : quality.assessed === 0 ? (
          <EmptyState
            className="dash-state"
            title={t("暂无质量检测结果")}
            action={<Button asChild size="sm"><Link to="/jobs">{t("新建任务")}</Link></Button>}
          />
        ) : (
          <div className="dash-heat" role="table" aria-label={t("出口 IP 质量")}>
            <div role="row" className="dash-heat__row">
              <span role="columnheader" />
              {BAND_COLUMNS.map((column) => (
                <span key={column.label} role="columnheader" className="dash-heat__hd">{t(column.label)}</span>
              ))}
              <span role="columnheader" className="dash-heat__total">{t("合计")}</span>
            </div>
            {TYPE_ROWS.map((row, rowIndex) => (
              <div role="row" key={row.label} className="dash-heat__row">
                <span role="rowheader" className="dash-muted">{t(row.label)}</span>
                {quality.grid[rowIndex].map((value, column) => {
                  const weight = quality.max > 0 ? value / quality.max : 0;
                  const alpha = 0.06 + weight * 0.94;
                  return (
                    <span
                      role="cell"
                      key={column}
                      className={cn("dash-heat__cell readout", alpha > 0.62 && "is-strong")}
                      style={{ backgroundColor: `rgba(var(--p-heat-rgb), ${alpha.toFixed(2)})` }}
                    >
                      {formatCount(value)}
                    </span>
                  );
                })}
                <span role="cell" className="dash-heat__total readout">
                  {formatCount(quality.grid[rowIndex].reduce((sum, value) => sum + value, 0))}
                </span>
              </div>
            ))}
          </div>
        )}
      </Card>

      {/* ---- sessions ---- */}
      <Card className="dash-sessions" title={t("活跃会话")} meta={t("实时")}>
        {leases.isError && !leases.data ? (
          failed(() => void leases.refetch())
        ) : !leases.data ? (
          loading
        ) : (
          <>
            <div className="dash-kv">
              <span className="readout dash-big">{hourly.latest === null ? PLACEHOLDER : formatCount(hourly.latest)}</span>
              <span className="dash-muted">{t("24 小时峰值")} {formatCount(hourly.peak)} · {t("每小时")}</span>
            </div>
            <svg className="dash-spark" viewBox="0 0 100 40" preserveAspectRatio="none" aria-hidden>
              {hourly.bars.map((value, index) => {
                const height = hourly.peak > 0 && value !== null ? Math.max(1, (value / hourly.peak) * 38) : 0;
                return (
                  <rect key={index} x={index * (100 / 24) + 0.6} y={40 - height} width={100 / 24 - 1.2} height={height} rx={0.6} className={index === 23 ? "is-now" : undefined} />
                );
              })}
              <line x1="0" x2="100" y1="39.75" y2="39.75" />
            </svg>
          </>
        )}
      </Card>

      {/* ---- subscription sources ---- */}
      <Card
        className="dash-sources"
        title={t("订阅源")}
        meta={subscriptions.data ? t("{{count}} 个", { count: formatCount(subscriptions.data.total) }) : undefined}
        action={<Link to="/subscriptions" className="dash-link">{t("管理")}</Link>}
        footer={
          <>
            <span>{t("健康节点")}</span>
            <span className="readout dash-strong">{pool ? `${formatCount(pool.healthy_nodes)} / ${formatCount(pool.total_nodes)}` : PLACEHOLDER}</span>
          </>
        }
      >
        <div className="dash-scroll dash-list">
          {subscriptions.isError && !subscriptions.data ? (
            failed(() => void subscriptions.refetch())
          ) : !subscriptions.data ? (
            loading
          ) : subscriptionRows.length === 0 ? (
            <EmptyState className="dash-state" title={t("还没有订阅")} action={<Button asChild size="sm" variant="primary"><Link to="/subscriptions?create=1">{t("开始导入")}</Link></Button>} />
          ) : (
            subscriptionRows.map((source) => {
              const failedSource = source.enabled && Boolean(source.last_error);
              const share = source.node_count > 0 ? source.healthy_node_count / source.node_count : 0;
              return (
                <div key={source.id} className="dash-line" title={source.last_error || undefined}>
                  <span className="dash-line__name">{source.name}</span>
                  <Bar
                    className="flex-1"
                    parts={
                      !source.enabled ? [] : failedSource ? [{ share: 1, tone: "bad" }] : [{ share, tone: "ok" }, { share: source.node_count > 0 ? 1 - share : 0, tone: "warn" }]
                    }
                  />
                  <span className={cn("readout dash-line__value", failedSource && "text-alert")}>
                    {!source.enabled ? t("已停用") : failedSource ? t("失败") : formatCount(source.node_count)}
                  </span>
                </div>
              );
            })
          )}
        </div>
      </Card>

      {/* ---- jobs ---- */}
      <Card
        className="dash-jobs"
        title={t("检测任务")}
        meta={running.data && queued.data ? `${t("运行")} ${formatCount(running.data.total)} · ${t("排队")} ${formatCount(queued.data.total)}` : undefined}
        action={<Link to="/jobs" className="dash-link">{t("全部")}</Link>}
        footer={
          <>
            <span>{t("24h 探测")}</span>
            <span className="readout dash-strong">{day.data ? t("{{count}} 次", { count: formatCount(probes24h) }) : PLACEHOLDER}</span>
          </>
        }
      >
        <div className="dash-scroll dash-list">
          {jobs.isError && !jobs.data ? (
            failed(() => void jobs.refetch())
          ) : !jobs.data ? (
            loading
          ) : jobs.data.items.length === 0 ? (
            <EmptyState className="dash-state" title={t("暂无任务")} action={<Button asChild size="sm"><Link to="/jobs">{t("新建任务")}</Link></Button>} />
          ) : (
            jobs.data.items.map((job) => {
              const progress = jobProgress(job);
              const tone: Tone =
                job.status === "succeeded" ? "ok" : job.status === "failed" || job.status === "partial" ? "warn" : job.status === "canceled" ? "faint" : "accent";
              const label =
                job.status === "succeeded"
                  ? t("完成")
                  : job.status === "failed" || job.status === "partial"
                    ? t("{{count}} 败", { count: formatCount(job.failed) })
                    : job.status === "canceled"
                      ? t("已取消")
                      : job.status === "queued"
                        ? t("排队")
                        : `${Math.round(progress * 100)}%`;
              return (
                <Link key={job.id} to="/jobs" className="dash-line dash-line--link">
                  <span className="dash-line__grow">
                    {t(JOB_KIND_LABELS[job.kind] ?? job.kind)} · <span className="readout dash-muted">{formatCount(job.total)}</span>
                  </span>
                  <Bar className="dash-bar--short" parts={[{ share: job.status === "succeeded" ? 1 : progress, tone }]} />
                  <span className={cn("readout dash-line__value", `dash-text--${tone}`)}>{label}</span>
                </Link>
              );
            })
          )}
        </div>
      </Card>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
  );
}
