import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowDownLeft,
  ArrowRight,
  ArrowUpRight,
  CircleCheck,
  CircleHelp,
  Globe2,
  Network,
  Plus,
  RefreshCw,
  Rss,
  ShieldCheck,
  Waypoints,
} from "lucide-react";
import { lazy, Suspense } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { Button } from "../../components/ui/Button";
import { QueryState } from "../../components/ui/QueryState";
import { Badge } from "../../components/ui/Badge";
import { useI18n } from "../../i18n";
import { formatRelativeTime } from "../../lib/time";
import { listPlatforms } from "../platforms/api";
import { allocationPolicyLabel } from "../platforms/constants";
import { listSubscriptions } from "../subscriptions/api";
import { getQualityStatus, qualityPollingInterval } from "../quality/api";
import { QualityOverview } from "../quality/QualityOverview";
import {
  getDashboardGlobalRealtimeData,
  getDashboardGlobalSnapshotData,
} from "./api";

const Trends = lazy(() =>
  import("./DashboardPage").then((module) => ({
    default: module.DashboardPage,
  })),
);
const count = (n: number | undefined) =>
  n === undefined ? "--" : n.toLocaleString();
const rate = (n: number | undefined) =>
  n === undefined
    ? "--"
    : n >= 1e6
      ? `${(n / 1e6).toFixed(1)} Mbps`
      : `${(n / 1e3).toFixed(1)} Kbps`;

export function WorkbenchPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const tab = params.get("view") === "trends" ? "trends" : "overview";
  const queryClient = useQueryClient();
  const snapshot = useQuery({
    queryKey: ["dashboard-global-snapshot"],
    queryFn: getDashboardGlobalSnapshotData,
    refetchInterval: 15_000,
  });
  const realtime = useQuery({
    queryKey: ["workbench", "realtime"],
    queryFn: () =>
      getDashboardGlobalRealtimeData({
        from: new Date(Date.now() - 30 * 60_000).toISOString(),
        to: new Date().toISOString(),
      }),
    refetchInterval: 15_000,
  });
  const platforms = useQuery({
    queryKey: ["platforms", "workbench"],
    queryFn: () => listPlatforms({ limit: 5 }),
    refetchInterval: 30_000,
  });
  const subscriptions = useQuery({
    queryKey: ["subscriptions", "workbench"],
    queryFn: () => listSubscriptions({ limit: 4 }),
    refetchInterval: 30_000,
  });
  const pool = snapshot.data?.snapshot_node_pool;
  const quality = useQuery({
    queryKey: ["quality", "status"],
    queryFn: getQualityStatus,
    refetchInterval: query => qualityPollingInterval(query.state.data),
  });
  const samples = realtime.data?.realtime_throughput.items ?? [];
  const latest = samples.at(-1);
  const connections = realtime.data?.realtime_connections.items.at(-1);
  const leases = realtime.data?.realtime_leases.items.at(-1);
  const healthPercent = pool?.total_nodes
    ? Math.round((pool.healthy_nodes / pool.total_nodes) * 100)
    : 0;
  const loading =
    snapshot.isFetching ||
    realtime.isFetching ||
    platforms.isFetching ||
    subscriptions.isFetching;
  const otherNodes = pool ? Math.max(0, pool.total_nodes - pool.healthy_nodes) : 0;
  return (
    <section className="workbench-page">
      <header className="module-header">
        <div>
          <h1>{t("总览看板")}</h1>
          <p className="workspace-description">
            {t("查看线路健康、出口质量和实时连接。")}
          </p>
        </div>
        <div className="page-actions">
          <Button
            variant="ghost"
            className="icon-button"
            title={t("刷新")}
            aria-label={t("刷新")}
            disabled={loading}
            onClick={() => void queryClient.invalidateQueries()}
          >
            <RefreshCw size={17} className={loading ? "spin" : ""} />
          </Button>
          <Link className="btn btn-primary" to="/subscriptions?create=1">
            <Plus size={16} />
            {t("添加订阅")}
          </Link>
        </div>
      </header>
      <div className="view-tabs" role="tablist" aria-label={t("总览视图")}>
        {[
          { key: "overview", label: "运行概况" },
          { key: "trends", label: "历史趋势" },
        ].map((item, index) => (
          <button
            key={item.key}
            role="tab"
            aria-selected={tab === item.key}
            aria-controls="workbench-view"
            tabIndex={tab === item.key ? 0 : -1}
            className={tab === item.key ? "is-active" : ""}
            onClick={() =>
              setParams(item.key === "overview" ? {} : { view: item.key })
            }
            onKeyDown={(event) => {
              if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
                event.preventDefault();
                setParams(index === 0 ? { view: "trends" } : {});
                (
                  event.currentTarget.parentElement?.children[
                    index === 0 ? 1 : 0
                  ] as HTMLElement
                )?.focus();
              }
            }}
          >
            {t(item.label)}
          </button>
        ))}
      </div>
      <div id="workbench-view" role="tabpanel">
        {tab === "trends" ? (
          <Suspense fallback={<QueryState loading />}>
            <Trends />
          </Suspense>
        ) : (
          <>
            <QueryState
              error={snapshot.error}
              onRetry={() => void snapshot.refetch()}
            />
            {pool?.total_nodes === 0 && !snapshot.isError && (
              <div className="onboarding-row">
                <span className="onboarding-icon"><Rss size={23} /></span>
                <div>
                  <h2>{t("建立你的第一个节点池")}</h2>
                  <p>{t("添加订阅链接或导入本地节点，开始查看线路状态。")}</p>
                </div>
                <Link className="btn btn-primary" to="/subscriptions?create=1">
                  {t("开始导入")}<ArrowRight size={15} />
                </Link>
              </div>
            )}
            <div className="resource-overview" aria-busy={snapshot.isLoading}>
              <div className="resource-total">
                <span>
                  <Network size={16} />
                  {t("库存节点")}
                </span>
                <strong>{count(pool?.total_nodes)}</strong>
                <Link to="/nodes">
                  {t("查看节点池")}
                  <ArrowUpRight size={15} />
                </Link>
              </div>
              <div className="resource-health">
                <div className="section-heading">
                  <h2>{t("节点健康")}</h2>
                  <span>{pool?.total_nodes ? `${healthPercent}%` : "--"}</span>
                </div>
                <div
                  className="health-strip"
                  role="img"
                  aria-label={
                    pool
                      ? `${t("健康")} ${pool.healthy_nodes} / ${pool.total_nodes}`
                      : t("正在加载")
                  }
                >
                  {Array.from({ length: 24 }, (_, i) => (
                    <span
                      className={
                        pool && i < healthPercent * 0.24 ? "healthy" : ""
                      }
                      key={i}
                    />
                  ))}
                </div>
                <div className="health-legend">
                  <Link to="/nodes?status=healthy">
                    <i className="dot-success" />
                    {t("健康")}
                    <b>{count(pool?.healthy_nodes)}</b>
                  </Link>
                  <Link to="/nodes">
                    <i className="dot-muted" />
                    {t("其他节点")}
                    <b>
                      {pool
                        ? count(pool.total_nodes - pool.healthy_nodes)
                        : "--"}
                    </b>
                  </Link>
                </div>
              </div>
              <div className="resource-exits">
                <span>
                  <Globe2 size={16} />
                  {t("健康出口 IP")}
                </span>
                <strong>{count(pool?.healthy_egress_ip_count)}</strong>
                <small>
                  {t("已发现出口")} {count(pool?.egress_ip_count)}
                </small>
              </div>
              <div className="resource-quality">
                <span><ShieldCheck size={16} />{t("网络证据")}</span>
                <strong>{count(quality.data?.checked_ips)}</strong>
                <Link to="/nodes?view=exits">{t("出口记录")}<ArrowUpRight size={15} /></Link>
              </div>
            </div>
            {otherNodes > 0 && !snapshot.isError && (
              <div className="pool-attention">
                <CircleHelp size={16} aria-hidden="true" />
                <span>{t("还有 {{count}} 个节点待检测或暂不可用", { count: otherNodes })}</span>
                <Link to="/nodes">{t("查看节点状态")}<ArrowRight size={14} /></Link>
              </div>
            )}
            <QualityOverview status={quality.data} />
            <div className="workbench-live">
              <section className="traffic-section">
                <div className="section-heading">
                  <h2>{t("实时流量")}</h2>
                  <span>{t("最近 30 分钟")}</span>
                </div>
                <div className="traffic-values">
                  <span>
                    <ArrowDownLeft size={15} />
                    {t("下载")} <strong>{rate(latest?.ingress_bps)}</strong>
                  </span>
                  <span>
                    <ArrowUpRight size={15} />
                    {t("上传")} <strong>{rate(latest?.egress_bps)}</strong>
                  </span>
                </div>
                <QueryState
                  loading={realtime.isLoading}
                  error={realtime.error}
                  empty={
                    !realtime.isLoading && !realtime.isError && !samples.length
                  }
                  emptyText={t("暂无流量采样")}
                  onRetry={() => void realtime.refetch()}
                />
                {samples.length > 0 && (
                  <div className="live-chart" aria-label={t("实时流量")}>
                    <ResponsiveContainer width="100%" height="100%">
                      <LineChart
                        data={samples}
                        margin={{ top: 12, right: 12, bottom: 0, left: 0 }}
                      >
                        <CartesianGrid
                          stroke="var(--border)"
                          vertical={false}
                          strokeDasharray="2 4"
                        />
                        <XAxis
                          dataKey="ts"
                          tickFormatter={(v: string) =>
                            new Date(v).toLocaleTimeString([], {
                              hour: "2-digit",
                              minute: "2-digit",
                              ...(samples.length > 1 && Date.parse(samples.at(-1)!.ts) - Date.parse(samples[0].ts) < 120000 ? { second: "2-digit" as const } : {}),
                            })
                          }
                          minTickGap={55}
                          tick={{ fill: "var(--text-muted)", fontSize: 11 }}
                          tickLine={false}
                          axisLine={false}
                        />
                        <YAxis
                          width={72}
                          domain={[0, (maximum: number) => Math.max(1000, maximum)]}
                          tickFormatter={(v: number) => rate(v)}
                          tick={{ fill: "var(--text-muted)", fontSize: 10 }}
                          tickLine={false}
                          axisLine={false}
                        />
                        <Tooltip
                          labelFormatter={(v) =>
                            new Date(String(v)).toLocaleTimeString()
                          }
                          formatter={(v) => rate(Number(v))}
                          contentStyle={{
                            background: "var(--surface)",
                            border: "1px solid var(--border)",
                            borderRadius: 6,
                            color: "var(--text)",
                          }}
                        />
                        <Line
                          dataKey="ingress_bps"
                          name={t("下载")}
                          stroke="var(--primary)"
                          strokeWidth={2}
                          dot={false}
                          isAnimationActive={false}
                        />
                        <Line
                          dataKey="egress_bps"
                          name={t("上传")}
                          stroke="var(--chart-secondary)"
                          strokeWidth={1.5}
                          dot={false}
                          isAnimationActive={false}
                        />
                      </LineChart>
                    </ResponsiveContainer>
                  </div>
                )}
              </section>
              <aside className="connection-section">
                <div className="section-heading">
                  <h2>{t("连接与会话")}</h2>
                  <Link
                    to="/endpoints"
                    title={t("接入点")}
                    aria-label={t("接入点")}
                  >
                    <ArrowUpRight size={17} />
                  </Link>
                </div>
                <dl className="connection-facts">
                  <div>
                    <dt>{t("入站连接")}</dt>
                    <dd>{count(connections?.inbound_connections)}</dd>
                  </div>
                  <div>
                    <dt>{t("出站连接")}</dt>
                    <dd>{count(connections?.outbound_connections)}</dd>
                  </div>
                  <div>
                    <dt>{t("活跃租约数")}</dt>
                    <dd>{count(leases?.active_leases)}</dd>
                  </div>
                </dl>
                <Link className="text-link" to="/request-logs">
                  {t("查看请求日志")}
                  <ArrowRight size={15} />
                </Link>
              </aside>
            </div>
            <div className="workbench-objects">
              <section>
                <div className="section-heading">
                  <h2>
                    <Waypoints size={17} />
                    {t("平台管理")}
                  </h2>
                  <Link to="/platforms">
                    {t("全部平台")}
                    <ArrowUpRight size={14} />
                  </Link>
                </div>
                <QueryState
                  loading={platforms.isLoading}
                  error={platforms.error}
                  empty={
                    !platforms.isLoading &&
                    !platforms.isError &&
                    !platforms.data?.items.length
                  }
                  emptyText={t("暂无平台")}
                  onRetry={() => void platforms.refetch()}
                />
                {platforms.data?.items.map((platform) => (
                  <Link
                    className="workbench-object"
                    to={`/platforms/${platform.id}`}
                    key={platform.id}
                  >
                    <span className="object-icon">
                      <Waypoints size={17} />
                    </span>
                    <div>
                      <strong>{platform.name}</strong>
                      <small>
                        {t(allocationPolicyLabel[platform.allocation_policy])} /{" "}
                        {platform.regex_filters.length
                          ? platform.regex_filters.join("  ")
                          : t("全部标签")}
                      </small>
                    </div>
                    <Badge
                      variant={
                        platform.routable_node_count ? "success" : "neutral"
                      }
                    >
                      {count(platform.routable_node_count)} {t("节点")}
                    </Badge>
                    <ArrowRight size={14} />
                  </Link>
                ))}
              </section>
              <section>
                <div className="section-heading">
                  <h2>
                    <Rss size={17} />
                    {t("最近订阅")}
                  </h2>
                  <Link to="/subscriptions">
                    {t("全部订阅")}
                    <ArrowUpRight size={14} />
                  </Link>
                </div>
                <QueryState
                  loading={subscriptions.isLoading}
                  error={subscriptions.error}
                  empty={
                    !subscriptions.isLoading &&
                    !subscriptions.isError &&
                    !subscriptions.data?.items.length
                  }
                  emptyText={t("尚未添加订阅")}
                  onRetry={() => void subscriptions.refetch()}
                />
                {subscriptions.data?.items.map((sub) => (
                  <Link
                    className="workbench-object"
                    to={`/subscriptions?selected=${sub.id}`}
                    key={sub.id}
                  >
                    <span className="object-icon">
                      <Rss size={17} />
                    </span>
                    <div>
                      <strong>{sub.name}</strong>
                      <small>
                        {sub.last_error || formatRelativeTime(sub.last_checked)}
                      </small>
                    </div>
                    <Badge
                      variant={
                        sub.last_error
                          ? "danger"
                          : sub.enabled
                            ? "success"
                            : "neutral"
                      }
                    >
                      {t(
                        sub.last_error
                          ? "异常"
                          : sub.enabled
                            ? "已启用"
                            : "已停用",
                      )}
                    </Badge>
                  </Link>
                ))}
              </section>
            </div>
            <footer className="workbench-footer">
              <CircleCheck size={13} />
              <span>
                {pool?.generated_at
                  ? `${t("快照更新")} ${formatRelativeTime(pool.generated_at)}`
                  : t("等待服务数据")}
              </span>
            </footer>
          </>
        )}
      </div>
    </section>
  );
}
