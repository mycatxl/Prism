import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowUpRight, ClipboardCheck, Database, Network, RefreshCw, ShieldAlert, UsersRound, Waypoints } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { ControlButton, EmptyWorkState, ObjectList, ObjectRow, WorkspacePage, QueryBadge, StatusMark, WorkspaceCard } from "../../components/control-plane";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatBytes, formatCount, formatTimestamp, PLACEHOLDER } from "./format";
import { opsPaths } from "../../lib/paths";
import { listAuditLogs } from "../audit/api";
import type { AuditLogEntry } from "../audit/types";
import { getUsageAlerts, getUsageSummary, listUsers } from "../admin/api";
import type { AdminUser } from "../admin/types";
import { listIntelJobs } from "../jobs/api";
import { listPlatforms } from "../platforms/api";
import type { Platform } from "../platforms/types";
import { listSubscriptions } from "../subscriptions/api";
import { NODE_EXIT_FACT_SAMPLE_LIMIT, getDashboardGlobalSnapshotData, getPlatformPulse, listNodeExitFacts } from "./api";
import { aggregateExitCountries, useCountryName } from "./exitCountries";
import { NODE_EXITS_REFRESH_MS, SNAPSHOT_REFRESH_MS, getTimeWindow } from "./range";

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

/*
 * The delivery graph renders the first rows only, so only those rows read a pulse.
 * Reading one for every loaded platform was a fan-out the screen never showed; the
 * rows that are not rendered stay visible through the card's "shown / total" meta.
 */
const PLATFORM_GRAPH_ROWS = 8;

function stateTone(level: Attention["level"]): "alert" | "warn" | "neutral" {
  return level === "alert" ? "alert" : level === "warn" ? "warn" : "neutral";
}

function State({ tone, children }: { tone: "signal" | "live" | "warn" | "alert" | "neutral"; children: ReactNode }) {
  return <StatusMark tone={tone} label={children} />;
}

function jobLabel(status: string, t: (text: string, options?: Record<string, unknown>) => string): string {
  switch (status) {
    case "queued": return t("排队中");
    case "running": return t("运行中");
    case "succeeded":
    case "done": return t("已完成");
    case "partial": return t("部分完成");
    case "failed": return t("失败");
    case "canceled":
    case "cancelled": return t("已取消");
    default: return status;
  }
}

function jobTone(status: string): "signal" | "live" | "warn" | "alert" | "neutral" {
  if (status === "running") return "live";
  if (status === "succeeded" || status === "done") return "signal";
  if (status === "failed") return "alert";
  if (status === "partial" || status === "canceled" || status === "cancelled") return "warn";
  return "neutral";
}

function subscriptionLabel(user: AdminUser): string {
  return user.subscription?.plan_name || user.subscription?.status || "—";
}

function RegisterState({
  loading,
  error,
  empty,
  retry,
}: {
  loading?: boolean;
  error?: ReactNode;
  empty?: ReactNode;
  retry?: () => void;
}) {
  const { t } = useI18n();
  if (loading) return <div className="workspace-register-state"><span className="workspace-spinner" aria-hidden /> <span>{t("正在加载")}</span></div>;
  if (error) return <EmptyWorkState embedded title={error} action={retry ? <ControlButton size="sm" variant="secondary" onPress={retry}>{t("重试")}</ControlButton> : undefined} />;
  if (empty) return <EmptyWorkState embedded title={empty} />;
  return null;
}

export function WorkbenchPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
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
  const visiblePlatforms = useMemo(() => platformItems.slice(0, PLATFORM_GRAPH_ROWS), [platformItems]);
  const circuitOpen = useMemo(() => facts.filter((fact) => fact.circuitOpen).length, [facts]);
  /*
   * A sampled exit can sit on several nodes, and the notice names exits, not rows:
   * counting rows would turn one unassessed IP into as many entries as the sample
   * happens to carry. The set is what makes the number match the word, and it stays
   * a statement about the sample.
   */
  const unassessed = useMemo(
    () => new Set(facts.filter((fact) => fact.egressIp && !fact.ipType).map((fact) => fact.egressIp)).size,
    [facts],
  );
  const pulses = useQueries({
    queries: visiblePlatforms.map((platform) => ({
      queryKey: ["workbench", "platform-pulse", platform.id],
      queryFn: () => getPlatformPulse(platform.id, getTimeWindow("1h")),
      refetchInterval: 30_000,
      placeholderData: (previous: Awaited<ReturnType<typeof getPlatformPulse>> | undefined) => previous,
    })),
  });
  const platformRows: PlatformRow[] = useMemo(
    () => visiblePlatforms.map((platform, index) => {
      const pulse = pulses[index]?.data;
      return {
        platform,
        routable: pulse ? pulse.pool.routable_node_count : null,
        egress: pulse ? pulse.pool.egress_ip_count : null,
        leases: pulse ? pulse.activeLeases : null,
      };
    }),
    [visiblePlatforms, pulses],
  );
  const attention = useMemo(() => {
    const items: Attention[] = [];
    for (const platform of platformItems) {
      if (platform.routable_node_count === 0) items.push({ id: `platform-${platform.id}`, level: "alert", area: t("平台"), subject: platform.name, reason: t("没有可路由节点，请求会按未命中策略处理"), to: `${opsPaths.platforms}/${platform.id}` });
    }
    for (const subscription of subscriptions.data?.items ?? []) {
      if (subscription.last_error) items.push({ id: `subscription-${subscription.id}`, level: "warn", area: t("订阅源"), subject: subscription.name, reason: subscription.last_error, to: opsPaths.subscriptions });
    }
    for (const job of jobs.data?.items ?? []) {
      if (job.status === "failed" || job.failed > 0) items.push({ id: `job-${job.id}`, level: "warn", area: t("检测任务"), subject: job.id.slice(0, 8), reason: t("{{count}} 项检测失败", { count: job.failed }), to: opsPaths.jobs });
    }
    if (circuitOpen > 0) items.push({ id: "circuit-open", level: "warn", area: t("节点"), subject: t("{{count}} 个采样节点", { count: circuitOpen }), reason: t("被动熔断已暂停这些节点"), to: opsPaths.nodes });
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

  /*
   * The attention list is assembled from several sources, so a retry has to ask all
   * of them again: refetching only the one that failed would still leave the list
   * short of the sources that never answered. Same keys, no new requests beyond them.
   */
  const refetchAttentionSources = () =>
    Promise.all([
      platforms.refetch(),
      subscriptions.refetch(),
      jobs.refetch(),
      alerts.refetch(),
      exits.refetch(),
    ]);

  const jobRows = jobs.data?.items ?? [];
  const userRows = users.data?.items ?? [];
  const regions = [...countries].sort((a, b) => b.exits - a.exits || a.iso.localeCompare(b.iso));
  const regionPeak = regions.reduce((peak, region) => Math.max(peak, region.exits), 0);
  /*
   * This screen combines server-wide aggregates with bounded inventory samples.
   * Exit facts, platforms, subscriptions, jobs, alerts and users are sampled reads.
   * absence and a missing source cannot prove health, so the two questions are
   * separate: `partialSources` decides the header state, `attentionSources` decides
   * whether the attention card may claim there is nothing to do.
   */
  const partialSources = [snapshot, platforms, subscriptions, jobs, alerts, users, usage, events, exits];
  const hasAnyError = partialSources.some((query) => query.isError);
  const allSourcesLoaded = partialSources.every((query) => query.data !== undefined);
  const attentionSources = [platforms, subscriptions, jobs, alerts];
  const attentionAnyError = attentionSources.some((query) => query.isError) || exits.isError;
  const attentionAnyMissing = attentionSources.some((query) => !query.data) || !exits.data;
  const attentionLoading = attentionSources.some((query) => query.isLoading) || exits.isLoading;
  const attentionLoaded = attentionSources.some((query) => query.data) || Boolean(exits.data);
  const workbenchReady = Boolean(snapshot.data || platforms.data || users.data);

  return (
    <WorkspacePage conciseCards
      eyebrow="Control / runtime"
      title={t("控制平面")}
      description={t("围绕交付、资产、客户和治理证据处理实例状态，不把未知数据包装成健康。")}
      meta={<State tone={hasAnyError ? "warn" : allSourcesLoaded ? "live" : "neutral"}>{hasAnyError ? t("部分数据不可用") : allSourcesLoaded ? t("数据已同步") : workbenchReady ? t("部分数据尚未加载") : t("等待数据")}</State>}
      actions={<ControlButton variant="secondary" size="sm" loading={refreshing} onPress={() => void refreshAll()}><RefreshCw size={14} aria-hidden />{t("刷新工作区")}</ControlButton>}
    >
      <div className="workspace-data-grid" aria-label={t("实例状态") }>
        <div className="workspace-data-grid__cell"><span className="workspace-data-grid__label">{t("健康独立出口 IP")}</span><strong className="workspace-data-grid__value">{pool ? formatCount(pool.healthy_egress_ip_count) : PLACEHOLDER}</strong><span className="workspace-data-grid__hint">{snapshot.isSuccess ? t("快照已返回") : t("未加载")}</span></div>
        <div className="workspace-data-grid__cell"><span className="workspace-data-grid__label">{t("可路由节点")}</span><strong className="workspace-data-grid__value">{pool ? formatCount(pool.healthy_nodes) : PLACEHOLDER}</strong><span className="workspace-data-grid__hint">{pool ? t("全池节点 {{count}} 个", { count: pool.total_nodes }) : t("未加载")}</span></div>
        <div className="workspace-data-grid__cell"><span className="workspace-data-grid__label">{t("客户与平台")}</span><strong className="workspace-data-grid__value">{`${usage.data ? formatCount(usage.data.active_users) : PLACEHOLDER} / ${platforms.data ? formatCount(platforms.data.total) : PLACEHOLDER}`}</strong><span className="workspace-data-grid__hint">{t("活跃用户 / 平台总数")}</span></div>
        <div className="workspace-data-grid__cell"><span className="workspace-data-grid__label">{t("需要处理")}</span><strong className={cn("workspace-data-grid__value", attention.length > 0 && "text-alert")}>{attentionLoaded ? formatCount(attention.length) : PLACEHOLDER}</strong><span className="workspace-data-grid__hint">{attention.length > 0 ? t("已有待处理事项") : attentionAnyError ? t("读取失败") : attentionAnyMissing ? t("部分数据尚未加载") : t("暂无待处理事项")}</span></div>
      </div>

      <div className="workspace-split">
        <div className="workspace-stack">
          <WorkspaceCard eyebrow="Signal queue" title={t("需要处理")} meta={attentionLoaded ? t("当前列表 {{count}} 项待处理事项", { count: attention.length }) : t("未加载")} description={t("只列出已有证据支持的异常、风险和待补数据。") } actions={<ControlButton variant="ghost" size="sm" onPress={() => navigate(opsPaths.nodes)}><ArrowUpRight size={14} aria-hidden />{t("打开节点工作台")}</ControlButton>}>
            {attentionAnyError ? <div className="workspace-notice workspace-notice--warn"><span>{t("部分数据读取失败，事项列表可能不完整。")}</span><ControlButton variant="ghost" size="sm" onPress={() => void refetchAttentionSources()}>{t("重试")}</ControlButton></div> : null}
            {attention.length === 0 && attentionLoading ? <RegisterState loading /> : null}
            {attention.length === 0 && !attentionLoading && !attentionAnyError && !attentionAnyMissing ? <RegisterState empty={t("暂无需要处理的事项")} /> : null}
            <ObjectList>
              {attention.slice(0, 8).map((item) => (
                <ObjectRow
                  key={item.id}
                  onClick={() => navigate(item.to)}
                  leading={item.level === "alert" ? <ShieldAlert size={14} /> : item.level === "warn" ? <Waypoints size={14} /> : <ClipboardCheck size={14} />}
                  title={`${item.area} · ${item.subject}`}
                  subtitle={item.reason}
                  status={<State tone={stateTone(item.level)}>{item.level === "alert" ? t("阻断") : item.level === "warn" ? t("需处理") : t("待补")}</State>}
                  action={<ArrowUpRight size={14} aria-hidden />}
                />
              ))}
            </ObjectList>
          </WorkspaceCard>

          <WorkspaceCard className="workspace-card--explainer" eyebrow="Delivery graph" title={t("平台配线")} meta={platforms.data ? t("显示 {{shown}} / 共 {{total}} 个平台", { shown: platformRows.length, total: platforms.data.total }) : t("未加载")} description={t("只渲染前 {{count}} 个平台，并只为这些可见行读取节点池与租约快照。", { count: PLATFORM_GRAPH_ROWS }) } actions={<ControlButton variant="ghost" size="sm" onPress={() => navigate(opsPaths.platforms)}>{t("管理平台")}<ArrowUpRight size={14} aria-hidden /></ControlButton>}>
            {platforms.isError && !platforms.data ? <RegisterState error={t("平台数据不可用")} retry={() => void platforms.refetch()} /> : null}
            {platforms.isLoading && !platforms.data ? <RegisterState loading /> : null}
            {platforms.isError && platforms.data ? <div className="workspace-notice workspace-notice--warn"><span>{t("数据可能已过期")}</span><ControlButton variant="ghost" size="sm" onPress={() => void platforms.refetch()}>{t("重试")}</ControlButton></div> : null}
            {platformRows.length === 0 && platforms.data ? <RegisterState empty={t("还没有平台")} /> : null}
            <ObjectList>
              {platformRows.map((row) => (
                <ObjectRow
                  key={row.platform.id}
                  onClick={() => navigate(`${opsPaths.platforms}/${row.platform.id}`)}
                  leading={<Network size={14} />}
                  title={row.platform.name}
                  subtitle={`${row.platform.sticky_ttl || PLACEHOLDER} · ${row.platform.protocols.length ? row.platform.protocols.join(", ") : t("未限制协议")}`}
                  facts={<><span>{t("可路由")} {row.routable === null ? PLACEHOLDER : formatCount(row.routable)}</span><span>{t("出口")} {row.egress === null ? PLACEHOLDER : formatCount(row.egress)}</span><span>{t("租约")} {row.leases === null ? PLACEHOLDER : formatCount(row.leases)}</span></>}
                  status={<QueryBadge tone={row.platform.routable_node_count > 0 ? "signal" : "alert"} label={row.platform.routable_node_count > 0 ? t("可交付") : t("无节点")} />}
                  action={<ArrowUpRight size={14} aria-hidden />}
                />
              ))}
            </ObjectList>
          </WorkspaceCard>

          <WorkspaceCard eyebrow="Inventory / jobs" title={t("检测任务")} meta={jobs.data ? t("显示 {{shown}} / 共 {{total}} 项", { shown: jobRows.length, total: jobs.data.total }) : t("未加载")} actions={<ControlButton variant="ghost" size="sm" onPress={() => navigate(opsPaths.jobs)}>{t("打开任务")}</ControlButton>}>
            {jobs.isError && !jobs.data ? <RegisterState error={t("检测任务不可用")} retry={() => void jobs.refetch()} /> : null}
            {!jobs.data && !jobs.isError ? <RegisterState loading /> : null}
            {jobs.isError && jobs.data ? <div className="workspace-notice workspace-notice--warn"><span>{t("数据可能已过期")}</span><ControlButton variant="ghost" size="sm" onPress={() => void jobs.refetch()}>{t("重试")}</ControlButton></div> : null}
            {jobs.data && jobRows.length === 0 ? <RegisterState empty={t("还没有检测任务") } /> : null}
            <ObjectList>
              {jobRows.map((job) => <ObjectRow key={job.id} onClick={() => navigate(opsPaths.jobs)} leading={<Database size={14} />} title={`${job.id.slice(0, 8)} · ${job.kind}`} subtitle={`${formatCount(job.done + job.failed + job.skipped)} / ${formatCount(job.total)} · ${job.failed > 0 ? t("{{count}} 项失败", { count: job.failed }) : t("没有失败项")}`} status={<State tone={jobTone(job.status)}>{jobLabel(job.status, t)}</State>} />)}
            </ObjectList>
          </WorkspaceCard>
        </div>

        <div className="workspace-stack">
          <WorkspaceCard eyebrow="Customers" title={t("客户现场")} meta={users.data ? t("显示 {{shown}} / 共 {{total}} 名客户", { shown: userRows.length, total: users.data.total }) : t("未加载")} actions={<ControlButton variant="ghost" size="sm" onPress={() => navigate(opsPaths.users)}><UsersRound size={14} aria-hidden />{t("用户管理")}</ControlButton>}>
            {users.isError && !users.data ? <RegisterState error={t("客户数据不可用")} retry={() => void users.refetch()} /> : null}
            {users.isLoading && !users.data ? <RegisterState loading /> : null}
            {users.isError && users.data ? <div className="workspace-notice workspace-notice--warn"><span>{t("数据可能已过期")}</span><ControlButton variant="ghost" size="sm" onPress={() => void users.refetch()}>{t("重试")}</ControlButton></div> : null}
            {users.data && userRows.length === 0 ? <RegisterState empty={t("暂无用户")} /> : null}
            <ObjectList>
              {userRows.map((user) => <ObjectRow key={user.id} onClick={() => navigate(`${opsPaths.users}/${user.id}`)} leading={<UsersRound size={14} />} title={user.username} subtitle={`${subscriptionLabel(user)} · ${formatCount(user.key_count)} ${t("个密钥")}`} facts={<span>{user.subscription ? new Date(user.subscription.expires_at_ns / 1_000_000).toLocaleDateString() : t("无订阅")}</span>} status={<State tone={user.status === "active" ? "signal" : "warn"}>{user.status === "active" ? t("正常") : t("已停用")}</State>} action={<ArrowUpRight size={14} aria-hidden />} />)}
            </ObjectList>
            <div className="workspace-register-note"><span>{t("今日全站流量")}</span><strong>{usage.data ? formatBytes(usage.data.today_bytes) : PLACEHOLDER}</strong><span>{usage.data ? t("活跃用户 {{count}} 人", { count: usage.data.active_users }) : t("未加载")}</span></div>
            {usage.isError && usage.data ? <div className="workspace-notice workspace-notice--warn"><span>{t("数据可能已过期")}</span><ControlButton variant="ghost" size="sm" onPress={() => void usage.refetch()}>{t("重试")}</ControlButton></div> : null}
          </WorkspaceCard>

          <WorkspaceCard eyebrow="Governance / evidence" title={t("最近变更")} meta={events.data ? t("{{count}} 条", { count: events.data.items.length }) : t("未加载")} actions={<ControlButton variant="ghost" size="sm" onPress={() => navigate(opsPaths.audit)}>{t("审计日志")}</ControlButton>}>
            {events.isError && !events.data ? <RegisterState error={t("审计证据不可用")} retry={() => void events.refetch()} /> : null}
            {!events.data && !events.isError ? <RegisterState loading /> : null}
            {events.isError && events.data ? <div className="workspace-notice workspace-notice--warn"><span>{t("数据可能已过期")}</span><ControlButton variant="ghost" size="sm" onPress={() => void events.refetch()}>{t("重试")}</ControlButton></div> : null}
            {events.data && events.data.items.length === 0 ? <RegisterState empty={t("还没有写操作") } /> : null}
            <div className="workspace-evidence-list">
              {events.data?.items.map((entry) => <AuditRow key={entry.id} entry={entry} />)}
            </div>
          </WorkspaceCard>

          <WorkspaceCard className="workspace-card--explainer" eyebrow="Egress coverage" title={t("出口覆盖")} meta={exits.data ? t("已加载样本中 {{count}} 个地区", { count: regions.length }) : t("未加载")} description={t("只读取最多 {{limit}} 个节点的出口事实样本，不代表全站覆盖；全池数字来自服务端快照。", { limit: NODE_EXIT_FACT_SAMPLE_LIMIT }) }>
            {exits.isError && !exits.data ? <RegisterState error={t("出口事实不可用")} retry={() => void exits.refetch()} /> : null}
            {!exits.data ? <RegisterState loading={exits.isLoading} /> : null}
            {exits.isError && exits.data ? <div className="workspace-notice workspace-notice--warn"><span>{t("数据可能已过期")}</span><ControlButton variant="ghost" size="sm" onPress={() => void exits.refetch()}>{t("重试")}</ControlButton></div> : null}
            {exits.data && facts.length === 0 ? <RegisterState empty={t("还没有出口事实") } /> : null}
            {regions.length > 0 ? <ul className="workspace-coverage-list">{regions.slice(0, 10).map((region) => <li key={region.iso}><span>{countryName(region.iso)}</span><span className="workspace-coverage-list__bar" aria-hidden><span style={{ width: `${regionPeak === 0 ? 0 : Math.max(4, (region.exits / regionPeak) * 100)}%` }} /></span><strong>{formatCount(region.healthy)} / {formatCount(region.exits)}</strong></li>)}</ul> : null}
          </WorkspaceCard>
        </div>
      </div>
    </WorkspacePage>
  );
}

function AuditRow({ entry }: { entry: AuditLogEntry }) {
  const [method, ...rest] = entry.action.split(" ");
  return <div className="workspace-evidence-row"><time>{formatTimestamp(entry.at_ns / 1_000_000)}</time><strong>{method}</strong><code title={rest.join(" ")}>{rest.join(" ") || entry.action}</code><span>{entry.actor || PLACEHOLDER}</span></div>;
}
