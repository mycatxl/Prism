import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, RefreshCw } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Button } from "../../components/ui/Button";
import { DataFreshness } from "../../components/ui/DataFreshness";
import { PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { WorkSection } from "../../components/ui/WorkSection";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatBytes, formatCount, formatTimestamp } from "./format";
import { opsPaths } from "../../lib/paths";
import { listAuditLogs } from "../audit/api";
import type { AuditLogEntry } from "../audit/types";
import { getUsageAlerts, getUsageSummary, listUsers } from "../admin/api";
import type { AdminUser } from "../admin/types";
import { listIntelJobs } from "../jobs/api";
import { listPlatforms } from "../platforms/api";
import type { Platform } from "../platforms/types";
import { listSubscriptions } from "../subscriptions/api";
import { getDashboardGlobalSnapshotData, getPlatformPulse, listNodeExitFacts } from "./api";
import { aggregateExitCountries, useCountryName } from "./exitCountries";
import EgressMap from "./EgressMap";
import { NODE_EXITS_REFRESH_MS, SNAPSHOT_REFRESH_MS, getTimeWindow } from "./range";
import { PLACEHOLDER } from "./format";

type Attention = {
  id: string;
  level: "alert" | "warn" | "notice";
  area: string;
  subject: string;
  reason: string;
  to: string;
};

type PlatformRow = { platform: Platform; routable: number | null; egress: number | null; leases: number | null };
const LEVEL_ORDER: Record<Attention["level"], number> = { alert: 0, warn: 1, notice: 2 };

function StateMark({
  tone,
  children,
}: {
  tone: "signal" | "live" | "warn" | "alert" | "neutral";
  children: ReactNode;
}) {
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <span aria-hidden className={cn("state-dot", tone !== "neutral" && `state-dot--${tone}`)} />
      <span className="state-text">{children}</span>
    </span>
  );
}

function RegisterState({
  loading = false,
  error,
  empty,
  onRetry,
}: {
  loading?: boolean;
  error?: ReactNode;
  empty?: ReactNode;
  onRetry?: () => void;
}) {
  if (loading) return <LoadingState className="py-8" />;
  if (error) return <ErrorState message={error} onRetry={onRetry} className="m-3" />;
  if (empty) return <EmptyState title={empty} className="py-8" />;
  return null;
}


function formatNs(ns?: number | null): string {
  if (!ns || ns <= 0) return PLACEHOLDER;
  return formatTimestamp(ns / 1_000_000);
}

function subscriptionLabel(user: AdminUser): string {
  if (!user.subscription) return "—";
  return user.subscription.plan_name || user.subscription.status || "—";
}

export function WorkbenchPage() {
  const { t } = useI18n();
  const countryName = useCountryName();
  const queryClient = useQueryClient();
  const [refreshing, setRefreshing] = useState(false);

  const snapshot = useQuery({
    queryKey: ["dashboard-global-snapshot"],
    queryFn: getDashboardGlobalSnapshotData,
    refetchInterval: SNAPSHOT_REFRESH_MS,
    placeholderData: (previous) => previous,
  });
  const exits = useQuery({
    queryKey: ["workbench", "node-exits"],
    queryFn: ({ signal }) => listNodeExitFacts(signal),
    refetchInterval: NODE_EXITS_REFRESH_MS,
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
  const events = useQuery({ queryKey: ["workbench", "events"], queryFn: () => listAuditLogs({ limit: 8 }), refetchInterval: 60_000 });
  const jobs = useQuery({ queryKey: ["workbench", "jobs"], queryFn: () => listIntelJobs("", 6), refetchInterval: 15_000 });
  const users = useQuery({ queryKey: ["workbench", "users"], queryFn: () => listUsers({ limit: 6, offset: 0 }), refetchInterval: 60_000 });
  const usage = useQuery({ queryKey: ["workbench", "usage-summary"], queryFn: getUsageSummary, refetchInterval: 60_000 });
  const alerts = useQuery({ queryKey: ["workbench", "usage-alerts"], queryFn: () => getUsageAlerts({ limit: 5, unacknowledged: true }), refetchInterval: 60_000 });

  const facts = useMemo(() => exits.data ?? [], [exits.data]);
  const countries = useMemo(() => aggregateExitCountries(facts), [facts]);
  const pool = snapshot.data?.snapshot_node_pool;
  const platformItems = useMemo(() => platforms.data?.items ?? [], [platforms.data]);
  const circuitOpen = useMemo(() => facts.filter((fact) => fact.circuitOpen).length, [facts]);
  const unassessed = useMemo(() => facts.filter((fact) => fact.egressIp && !fact.ipType).length, [facts]);

  const pulses = useQueries({
    queries: platformItems.map((platform) => ({
      queryKey: ["workbench", "platform-pulse", platform.id],
      queryFn: () => getPlatformPulse(platform.id, getTimeWindow("1h")),
      refetchInterval: 30_000,
      placeholderData: (previous: Awaited<ReturnType<typeof getPlatformPulse>> | undefined) => previous,
    })),
  });
  const platformRows: PlatformRow[] = useMemo(
    () => platformItems.map((platform, index) => {
      const pulse = pulses[index]?.data;
      return {
        platform,
        routable: pulse ? pulse.pool.routable_node_count : null,
        egress: pulse ? pulse.pool.egress_ip_count : null,
        leases: pulse ? pulse.activeLeases : null,
      };
    }),
    [platformItems, pulses],
  );

  const attention = useMemo(() => {
    const items: Attention[] = [];
    for (const platform of platformItems) {
      if (platform.routable_node_count === 0) {
        items.push({ id: `platform-${platform.id}`, level: "alert", area: t("平台"), subject: platform.name, reason: t("没有可路由节点，请求会按未命中策略处理"), to: `${opsPaths.platforms}/${platform.id}` });
      }
    }
    for (const subscription of subscriptions.data?.items ?? []) {
      if (subscription.last_error) {
        items.push({ id: `subscription-${subscription.id}`, level: "warn", area: t("订阅源"), subject: subscription.name, reason: subscription.last_error, to: opsPaths.subscriptions });
      }
    }
    for (const job of jobs.data?.items ?? []) {
      if (job.status === "failed" || job.failed > 0) {
        items.push({ id: `job-${job.id}`, level: "warn", area: t("检测任务"), subject: job.id.slice(0, 8), reason: t("{{count}} 项检测失败", { count: job.failed }), to: opsPaths.jobs });
      }
    }
    if (circuitOpen > 0) items.push({ id: "circuit-open", level: "warn", area: t("节点"), subject: t("{{count}} 个节点", { count: circuitOpen }), reason: t("被动熔断已暂停这些节点"), to: opsPaths.nodes });
    if (unassessed > 0) items.push({ id: "unassessed", level: "notice", area: t("出口"), subject: t("{{count}} 个出口未评估", { count: unassessed }), reason: t("没有 IP 类型与纯净度，按条件的平台不会选中它"), to: opsPaths.nodes });
    for (const alert of alerts.data?.items ?? []) items.push({ id: `alert-${alert.id}`, level: "warn", area: t("用量"), subject: alert.username || alert.user_id, reason: alert.note || t("检测到用量异常"), to: `${opsPaths.users}/${alert.user_id}` });
    return items.sort((a, b) => LEVEL_ORDER[a.level] - LEVEL_ORDER[b.level]);
  }, [alerts.data, circuitOpen, jobs.data, platformItems, subscriptions.data, t, unassessed]);

  const refreshAll = async () => {
    if (refreshing) return;
    setRefreshing(true);
    try {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["dashboard-global-snapshot"] }),
        queryClient.invalidateQueries({ queryKey: ["workbench"] }),
      ]);
    } finally {
      setRefreshing(false);
    }
  };

  const jobRows = jobs.data?.items ?? [];
  const userRows = users.data?.items ?? [];
  const regions = [...countries].sort((a, b) => b.exits - a.exits || a.iso.localeCompare(b.iso));
  const regionPeak = regions.reduce((peak, region) => Math.max(peak, region.exits), 0);
  const egressError = exits.isError && !exits.data;
  const platformError = platforms.isError && !platforms.data;
  const customerLoading = users.isLoading || usage.isLoading;

  return (
    <>
      <PageHeader
        title={t("控制平面")}
        meta={<PageMeta label={t("节点总数")} value={pool ? formatCount(pool.total_nodes) : PLACEHOLDER} />}
        actions={<Button size="sm" variant="secondary" onClick={() => void refreshAll()} loading={refreshing}><RefreshCw size={14} aria-hidden />{t("刷新")}</Button>}
      />

      <div className="page-content">
        <div className="workbench-statusline" aria-label={t("全局状态")}>
          <div className="workbench-statusline__cell"><span className="workbench-statusline__label">{t("健康独立出口 IP")}</span><strong className="workbench-statusline__value">{pool ? formatCount(pool.healthy_egress_ip_count) : PLACEHOLDER}</strong><span className="workbench-statusline__hint"><DataFreshness tone={snapshot.isSuccess ? "live" : "neutral"} label={snapshot.isSuccess ? t("已更新") : t("未加载")} /></span></div>
          <div className="workbench-statusline__cell"><span className="workbench-statusline__label">{t("活跃平台")}</span><strong className="workbench-statusline__value">{platforms.data ? formatCount(platformItems.length) : PLACEHOLDER}</strong><span className="workbench-statusline__hint">{platformError ? t("读取失败") : t("平台配线")}</span></div>
          <div className="workbench-statusline__cell"><span className="workbench-statusline__label">{t("活跃用户")}</span><strong className="workbench-statusline__value">{usage.data ? `${formatCount(usage.data.active_users)} / ${formatCount(usage.data.total_users)}` : PLACEHOLDER}</strong><span className="workbench-statusline__hint">{usage.data ? t("正常 / 总数") : t("未加载")}</span></div>
          <div className="workbench-statusline__cell"><span className="workbench-statusline__label">{t("需要处理")}</span><strong className={cn("workbench-statusline__value", attention.length > 0 && "text-alert")}>{platforms.data || subscriptions.data || jobs.data || alerts.data ? formatCount(attention.length) : PLACEHOLDER}</strong><span className="workbench-statusline__hint">{attention.length > 0 ? t("打开队列") : t("没有已知事项")}</span></div>
        </div>

        <div className="workbench-layout mt-6">
          <div className="workbench-column">
            <WorkSection title={t("交付")} actions={<Link className="link-quiet text-xs text-accent" to={opsPaths.platforms}>{t("平台管理")} <ArrowUpRight size={13} className="inline" /></Link>}>
              <div className="workbench-register">
                <div className="workbench-register__head"><strong>{t("需要处理")}</strong><span>{platforms.data && subscriptions.data ? t("{{count}} 项", { count: attention.length }) : t("未加载")}</span></div>
                {attention.length === 0 && (platforms.isLoading || subscriptions.isLoading || jobs.isLoading || alerts.isLoading) ? <RegisterState loading /> : null}
                {attention.length === 0 && !platforms.isLoading && !subscriptions.isLoading && !jobs.isLoading && !alerts.isLoading ? <RegisterState empty={t("没有需要处理的事项")} /> : null}
                {attention.slice(0, 7).map((item) => <div className="workbench-register__row" key={item.id}><StateMark tone={item.level === "alert" ? "alert" : item.level === "warn" ? "warn" : "neutral"}>{item.level === "alert" ? t("阻断") : item.level === "warn" ? t("需处理") : t("待补")}</StateMark><div><strong>{item.area} · {item.subject}</strong><p>{item.reason}</p></div><Link className="workbench-register__row-action" to={item.to}>{t("查看")} <ArrowUpRight size={13} className="inline" /></Link></div>)}
              </div>
            </WorkSection>

            <WorkSection title={t("平台配线")} meta={platforms.data ? t("{{count}} 个平台", { count: platformItems.length }) : PLACEHOLDER}>
              <div className="workbench-register">
                <div className="workbench-object-list__head"><span>{t("平台")}</span><span>{t("可路由")}</span><span>{t("出口 IP")}</span><span>{t("活跃租约")}</span></div>
                {platformError ? <RegisterState error={t("无法连接服务，请检查连接后重试。")} onRetry={() => void platforms.refetch()} /> : null}
                {platforms.isLoading && !platforms.data ? <RegisterState loading /> : null}
                {platformRows.length === 0 && platforms.data ? <RegisterState empty={t("还没有平台")} /> : null}
                {platformRows.slice(0, 8).map((row) => <Link className="workbench-object-list__row" key={row.platform.id} to={`${opsPaths.platforms}/${row.platform.id}`}><span><strong>{row.platform.name}</strong><small>{row.platform.sticky_ttl || PLACEHOLDER}</small></span><span className="workbench-object-list__value">{row.routable === null ? PLACEHOLDER : formatCount(row.routable)}</span><span className="workbench-object-list__value">{row.egress === null ? PLACEHOLDER : formatCount(row.egress)}</span><span className="workbench-object-list__value">{row.leases === null ? PLACEHOLDER : formatCount(row.leases)}</span></Link>)}
              </div>
            </WorkSection>

            <WorkSection title={t("检测任务")} meta={jobs.data ? t("{{count}} 项", { count: jobRows.length }) : PLACEHOLDER}>
              <div className="workbench-register">
                {jobs.isError && !jobs.data ? <RegisterState error={t("无法连接服务，请检查连接后重试。")} onRetry={() => void jobs.refetch()} /> : null}
                {!jobs.data && !jobs.isError ? <RegisterState loading /> : null}
                {jobs.data && jobRows.length === 0 ? <RegisterState empty={t("还没有检测任务。")} /> : null}
                {jobRows.map((job) => <div className="workbench-register__row" key={job.id}><StateMark tone={jobTone(job.status)}>{jobStatusLabel(job.status, t)}</StateMark><div><strong className="mono">{job.id.slice(0, 8)} · {job.kind}</strong><p>{formatCount(job.done + job.failed + job.skipped)} / {formatCount(job.total)} · {job.failed > 0 ? t("{{count}} 项失败", { count: job.failed }) : t("没有失败项")}</p></div><Link className="workbench-register__row-action" to={opsPaths.jobs}>{t("打开")} <ArrowUpRight size={13} className="inline" /></Link></div>)}
              </div>
            </WorkSection>
          </div>

          <div className="workbench-column">
            <WorkSection title={t("客户")} actions={<Link className="link-quiet text-xs text-accent" to={opsPaths.users}>{t("用户管理")} <ArrowUpRight size={13} className="inline" /></Link>}>
              <div className="workbench-register">
                <div className="workbench-register__head"><strong>{t("用户")}</strong><span>{users.data ? t("前 {{count}} 条", { count: userRows.length }) : t("未加载")}</span></div>
                {users.isError ? <RegisterState error={t("无法连接服务，请检查连接后重试。")} onRetry={() => void users.refetch()} /> : null}
                {customerLoading && !users.data && !usage.data ? <RegisterState loading /> : null}
                {users.data && userRows.length === 0 ? <RegisterState empty={t("暂无用户")} /> : null}
                {userRows.map((user) => <Link className="workbench-customer-row" key={user.id} to={`${opsPaths.users}/${user.id}`}><span><strong>{user.username}</strong><small>{subscriptionLabel(user)} · {formatCount(user.key_count)} {t("个密钥")}</small></span><span className="workbench-customer-row__metric">{user.subscription ? formatNs(user.subscription.expires_at_ns) : t("无订阅")}</span><StateMark tone={user.status === "active" ? "signal" : "warn"}>{user.status === "active" ? t("正常") : t("已停用")}</StateMark></Link>)}
              </div>
              <div className="workbench-register mt-3"><div className="workbench-register__head"><strong>{t("用量")}</strong><span>{usage.data ? t("当前周期") : t("未加载")}</span></div>{usage.data ? <div className="workbench-customer-row"><span><strong>{t("今日全站流量")}</strong><small>{t("活跃用户 {{count}} 人", { count: usage.data.active_users })}</small></span><span className="workbench-customer-row__metric">{formatBytes(usage.data.today_bytes)}</span><Link className="workbench-register__row-action" to={opsPaths.usage}>{t("打开")} <ArrowUpRight size={13} className="inline" /></Link></div> : <RegisterState loading={usage.isLoading} error={usage.isError ? t("无法连接服务，请检查连接后重试。") : undefined} onRetry={() => void usage.refetch()} />}</div>
            </WorkSection>

            <WorkSection title={t("治理")} actions={<Link className="link-quiet text-xs text-accent" to={opsPaths.audit}>{t("审计日志")} <ArrowUpRight size={13} className="inline" /></Link>}>
              <div className="workbench-register"><div className="workbench-register__head"><strong>{t("最近变更")}</strong><span>{events.data ? t("{{count}} 条", { count: events.data.items.length }) : PLACEHOLDER}</span></div>{events.isError && !events.data ? <RegisterState error={t("无法连接服务，请检查连接后重试。")} onRetry={() => void events.refetch()} /> : null}{!events.data && !events.isError ? <RegisterState loading /> : null}{events.data && events.data.items.length === 0 ? <RegisterState empty={t("还没有写操作。")} /> : null}{events.data?.items.map((entry) => <AuditRow key={entry.id} entry={entry} />)}</div>
            </WorkSection>
          </div>
        </div>

        <div className="mt-7">
          <WorkSection title={t("覆盖地区")} meta={exits.data ? t("{{count}} 个地区", { count: regions.length }) : PLACEHOLDER}>
            <div className="workbench-register p-3">
              {egressError ? <RegisterState error={t("无法连接服务，请检查连接后重试。")} onRetry={() => void exits.refetch()} /> : null}
              {!exits.data ? <RegisterState loading={exits.isLoading} /> : null}
              {exits.data && facts.length === 0 ? <RegisterState empty={t("还没有出口，导入订阅后这里会按国家列出。")} /> : null}
              {exits.data && facts.length > 0 ? <EgressMap countries={countries} /> : null}
              {regions.length > 0 ? (
                <ul className="mt-3 flex flex-col">
                  {regions.slice(0, 8).map((region) => (
                    <li key={region.iso} className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)_3.5rem] items-center gap-2 border-b border-row-rule py-1.5 last:border-0">
                      <span className="truncate text-xs text-ink">{countryName(region.iso)}</span>
                      <span aria-hidden className="h-1.5 overflow-hidden rounded-[2px] bg-sunk">
                        <span className={cn("block h-full", region.healthy === region.exits ? "bg-signal" : "bg-warn")} style={{ width: `${regionPeak === 0 ? 0 : Math.max(4, (region.exits / regionPeak) * 100)}%` }} />
                      </span>
                      <span className="readout text-right text-2xs text-ink-soft">{formatCount(region.healthy)} / {formatCount(region.exits)}</span>
                    </li>
                  ))}
                </ul>
              ) : null}
            </div>
          </WorkSection>
        </div>
      </div>
    </>
  );
}

function AuditRow({ entry }: { entry: AuditLogEntry }) {
  const [method, ...rest] = entry.action.split(" ");
  const path = rest.join(" ");
  return (
    <div className="workbench-evidence-row">
      <time>{formatTimestamp(entry.at_ns / 1_000_000)}</time>
      <b>{method}</b>
      <code title={path || entry.action}>{path || entry.action}</code>
      <span className="readout text-2xs text-ink-faint">{entry.actor || PLACEHOLDER}</span>
    </div>
  );
}

function jobStatusLabel(status: string, t: (text: string, options?: Record<string, unknown>) => string): string {
  switch (status) {
    case "queued":
      return t("排队中");
    case "running":
      return t("运行中");
    case "succeeded":
    case "done":
      return t("已完成");
    case "partial":
      return t("部分完成");
    case "failed":
      return t("失败");
    case "canceled":
    case "cancelled":
      return t("已取消");
    default:
      return status;
  }
}

function jobTone(status: string): "signal" | "live" | "warn" | "alert" | "neutral" {
  if (status === "running") return "live";
  if (status === "succeeded" || status === "done") return "signal";
  if (status === "failed") return "alert";
  if (status === "partial" || status === "canceled" || status === "cancelled") return "warn";
  if (status === "queued") return "neutral";
  return "warn";
}
