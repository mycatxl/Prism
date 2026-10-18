import { useQueries, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { Link } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { DataTable, type Column } from "../../components/ui/DataTable";
import { PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { Stat, StatStrip } from "../../components/ui/StatStrip";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { listAuditLogs } from "../audit/api";
import type { AuditLogEntry } from "../audit/types";
import { listIntelJobs } from "../jobs/api";
import type { IntelJob } from "../jobs/types";
import { listPlatforms } from "../platforms/api";
import type { Platform } from "../platforms/types";
import { listSubscriptions } from "../subscriptions/api";
import { getDashboardGlobalSnapshotData, getPlatformPulse, getTrafficAndProbes, listNodeExitFacts } from "./api";
import EgressMap from "./EgressMap";
import { aggregateExitCountries, useCountryName } from "./exitCountries";
import { PLACEHOLDER, formatBytes, formatCount, formatTimestamp } from "./format";
import { NODE_EXITS_REFRESH_MS, SNAPSHOT_REFRESH_MS, getTimeWindow } from "./range";

const OFFLINE = "无法连接服务，请检查连接后重试。";

/** One thing an operator has to look at, and the page that fixes it. */
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

/**
 * The operator's first screen: the condition of the whole deployment, the things
 * that need a decision, and the registers the decisions are made from.
 *
 * Composition rather than decoration: a condition band, one queue, one matrix,
 * two registers and a log, separated by rules instead of boxed into tiles. Every
 * figure comes from the API — a source that is loading, failed or empty says so
 * instead of printing a plausible zero.
 */
export function WorkbenchPage() {
  const { t } = useI18n();
  const countryName = useCountryName();
  const offline = t(OFFLINE);

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
  const day = useQuery({
    queryKey: ["workbench", "traffic-probes-24h"],
    queryFn: () => getTrafficAndProbes(getTimeWindow("24h")),
    refetchInterval: 60_000,
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

  const facts = useMemo(() => exits.data ?? [], [exits.data]);
  const countries = useMemo(() => aggregateExitCountries(facts), [facts]);
  const pool = snapshot.data?.snapshot_node_pool;
  const egress24h = useMemo(
    () => (day.data?.traffic.items ?? []).reduce((sum, item) => sum + item.egress_bytes, 0),
    [day.data],
  );
  const requests24h = useMemo(
    () => (day.data?.probes.items ?? []).reduce((sum, item) => sum + item.total_count, 0),
    [day.data],
  );
  const circuitOpen = useMemo(() => facts.filter((fact) => fact.circuitOpen).length, [facts]);
  const unassessed = useMemo(() => facts.filter((fact) => fact.egressIp && !fact.ipType).length, [facts]);

  const platformItems = useMemo(() => platforms.data?.items ?? [], [platforms.data]);
  const pulses = useQueries({
    queries: platformItems.map((platform) => ({
      queryKey: ["workbench", "platform-pulse", platform.id],
      queryFn: () => getPlatformPulse(platform.id, getTimeWindow("1h")),
      refetchInterval: 30_000,
      placeholderData: (previous: Awaited<ReturnType<typeof getPlatformPulse>> | undefined) => previous,
    })),
  });

  const platformRows: PlatformRow[] = useMemo(
    () =>
      platformItems.map((platform, index) => {
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

  /* ---- the queue: only real faults ---- */
  const attention = useMemo(() => {
    const items: Attention[] = [];
    for (const platform of platformItems) {
      if (platform.routable_node_count === 0) {
        items.push({
          id: `platform-${platform.id}`,
          level: "alert",
          area: t("平台"),
          subject: platform.name,
          reason: t("没有可路由节点，请求会按未命中策略处理"),
          to: `/platforms/${platform.id}`,
        });
      }
    }
    for (const subscription of subscriptions.data?.items ?? []) {
      if (subscription.last_error) {
        items.push({
          id: `subscription-${subscription.id}`,
          level: "warn",
          area: t("订阅源"),
          subject: subscription.name,
          reason: subscription.last_error,
          to: "/subscriptions",
        });
      }
    }
    if (circuitOpen > 0) {
      items.push({
        id: "circuit-open",
        level: "warn",
        area: t("节点"),
        subject: t("{{count}} 个节点", { count: circuitOpen }),
        reason: t("被动熔断已暂停这些节点"),
        to: "/nodes",
      });
    }
    for (const job of jobs.data?.items ?? []) {
      if (job.status === "failed" || job.failed > 0) {
        items.push({
          id: `job-${job.id}`,
          level: "warn",
          area: t("检测任务"),
          subject: job.id.slice(0, 8),
          reason: t("{{count}} 项检测失败", { count: job.failed }),
          to: "/jobs",
        });
      }
    }
    if (unassessed > 0) {
      items.push({
        id: "unassessed",
        level: "notice",
        area: t("出口"),
        subject: t("{{count}} 个出口未评估", { count: unassessed }),
        reason: t("没有 IP 类型与纯净度，按条件的平台不会选中它"),
        to: "/nodes",
      });
    }
    return items.sort((a, b) => LEVEL_ORDER[a.level] - LEVEL_ORDER[b.level]);
  }, [circuitOpen, jobs.data, platformItems, subscriptions.data, t, unassessed]);

  const attentionColumns: Column<Attention>[] = [
    {
      id: "level",
      header: t("级别"),
      width: "5.5rem",
      cell: (row) => (
        <Badge tone={row.level === "alert" ? "alert" : row.level === "warn" ? "warn" : "neutral"} dot>
          {row.level === "alert" ? t("阻断") : row.level === "warn" ? t("需处理") : t("待补")}
        </Badge>
      ),
    },
    { id: "area", header: t("类别"), width: "6rem", cell: (row) => <span className="text-ink-soft">{row.area}</span> },
    {
      id: "subject",
      header: t("对象"),
      width: "minmax(0,1.1fr)",
      cell: (row) => (
        <Link to={row.to} className="link-quiet font-medium text-ink">
          {row.subject}
        </Link>
      ),
    },
    {
      id: "reason",
      header: t("原因"),
      width: "minmax(0,1.6fr)",
      cell: (row) => <span className="text-ink-soft">{row.reason}</span>,
    },
    {
      id: "action",
      header: t("处理"),
      width: "5rem",
      align: "end",
      cell: (row) => (
        <Link to={row.to} className="link-quiet text-accent">
          {t("查看")}
        </Link>
      ),
    },
  ];

  const platformColumns: Column<PlatformRow>[] = [
    {
      id: "name",
      header: t("平台"),
      width: "minmax(0,1.4fr)",
      cell: (row) => (
        <Link to={`/platforms/${row.platform.id}`} className="link-quiet font-medium text-ink">
          {row.platform.name}
        </Link>
      ),
    },
    {
      id: "routable",
      header: t("可路由节点"),
      align: "end",
      mono: true,
      cell: (row) => (row.routable === null ? PLACEHOLDER : formatCount(row.routable)),
    },
    {
      id: "egress",
      header: t("出口 IP"),
      align: "end",
      mono: true,
      cell: (row) => (row.egress === null ? PLACEHOLDER : formatCount(row.egress)),
    },
    {
      id: "leases",
      header: t("活跃租约"),
      align: "end",
      mono: true,
      cell: (row) => (row.leases === null ? PLACEHOLDER : formatCount(row.leases)),
    },
    {
      id: "ttl",
      header: t("粘性 TTL"),
      align: "end",
      mono: true,
      cell: (row) => <span className="text-ink-soft">{row.platform.sticky_ttl || PLACEHOLDER}</span>,
    },
    {
      id: "updated",
      header: t("最近更新"),
      align: "end",
      mono: true,
      cell: (row) => {
        const updated = snapshotTime(row.platform.updated_at);
        return <span className="text-ink-faint">{updated === null ? PLACEHOLDER : formatTimestamp(updated)}</span>;
      },
    },
  ];

  const regions = useMemo(
    () => [...countries].sort((a, b) => b.exits - a.exits || a.iso.localeCompare(b.iso)),
    [countries],
  );
  const regionPeak = regions.reduce((peak, region) => Math.max(peak, region.exits), 0);

  const jobRows = jobs.data?.items ?? [];
  const running = jobRows.filter((job) => job.status === "running").length;
  const queued = jobRows.filter((job) => job.status === "queued").length;
  /*
   * A fresh install ships one placeholder platform and no nodes. That is a first
   * run, not a dashboard of zeros: four cells reading 0 / 0 teach nothing, and a
   * red "Default has no routable nodes" only restates the emptiness.
   */
  const firstRun =
    Boolean(exits.data) &&
    facts.length === 0 &&
    Boolean(subscriptions.data) &&
    (subscriptions.data?.items.length ?? 0) === 0;

  return (
    <>
      <PageHeader
        title={t("总览")}
        description={t("全站的出口、平台与最近操作。这里只显示服务端返回的事实。")}
        actions={
          <Button
            size="sm"
            variant="secondary"
            onClick={() => {
              void snapshot.refetch();
              void exits.refetch();
              void platforms.refetch();
              void subscriptions.refetch();
              void jobs.refetch();
            }}
          >
            {t("刷新")}
          </Button>
        }
      />

      <div className="flex flex-col gap-5 px-[var(--page-gutter)] py-4 lg:py-5">
        {firstRun ? (
          <div className="mx-auto w-full max-w-[46rem] pt-6 lg:pt-14">
          <section className="panel panel--ruled" aria-labelledby="first-run">
            <div className="panel-header">
              <h2 className="panel-header__title" id="first-run">
                {t("从三步开始")}
              </h2>
              <p className="panel-header__description">{t("现在还没有节点，也没有平台。按顺序做完这三步，这页就有内容了。")}</p>
            </div>
            <div className="panel-body">
              <ol className="flex flex-col">
                {[
                  { step: "01", title: t("导入订阅"), hint: t("订阅提供节点，节点提供出口。"), to: "/subscriptions" },
                  { step: "02", title: t("建一个平台"), hint: t("平台是接入用户名里的一段，客户端按它路由。"), to: "/platforms" },
                  { step: "03", title: t("跑一次检测"), hint: t("检测给出出口地区、IP 类型与纯净度。"), to: "/jobs" },
                ].map((item) => (
                  <li
                    key={item.step}
                    className="grid grid-cols-[2.5rem_minmax(0,1fr)_auto] items-baseline gap-3 border-b border-row-rule py-3 last:border-0"
                  >
                    <span className="readout text-xs text-ink-faint">{item.step}</span>
                    <span className="flex min-w-0 flex-col gap-0.5">
                      <Link to={item.to} className="link-quiet text-sm font-medium text-ink">
                        {item.title}
                      </Link>
                      <span className="text-xs text-ink-faint">{item.hint}</span>
                    </span>
                    <Link to={item.to} className="link-quiet text-xs text-accent">
                      {t("打开")}
                    </Link>
                  </li>
                ))}
              </ol>
            </div>
          </section>
          </div>
        ) : (
          <>
            <StatStrip>
              <Stat
                label={t("可用出口 IP")}
                value={
                  pool
                    ? `${formatCount(pool.healthy_egress_ip_count)} / ${formatCount(pool.egress_ip_count)}`
                    : PLACEHOLDER
                }
                hint={pool ? t("{{count}} 个节点在池中", { count: pool.total_nodes }) : snapshot.isError ? offline : undefined}
                tone={pool && pool.healthy_egress_ip_count === 0 && pool.egress_ip_count > 0 ? "alert" : "default"}
              />
              <Stat
                label={t("覆盖地区")}
                value={exits.data ? formatCount(regions.length) : PLACEHOLDER}
                hint={exits.data ? t("按出口 IP 的国家与地区") : exits.isError ? offline : undefined}
              />
              <Stat
                label={t("出站 24h")}
                value={day.data ? formatBytes(egress24h) : PLACEHOLDER}
                hint={day.data ? t("{{count}} 次请求", { count: requests24h }) : day.isError ? offline : undefined}
              />
              <Stat
                label={t("熔断节点")}
                value={exits.data ? formatCount(circuitOpen) : PLACEHOLDER}
                hint={exits.data ? t("被动熔断暂停的节点数") : exits.isError ? offline : undefined}
                tone={circuitOpen > 0 ? "warn" : "default"}
              />
            </StatStrip>

            <div className="grid gap-5 lg:grid-cols-12">
              <Panel className="lg:col-span-7">
                <PanelHeader
                  title={t("需要处理")}
                  meta={
                    platforms.data && subscriptions.data ? t("{{count}} 项", { count: attention.length }) : undefined
                  }
                />
                <DataTable
                  dense
                  caption={t("需要处理")}
                  columns={attentionColumns}
                  rows={attention}
                  rowKey={(row) => row.id}
                  loading={!platforms.data && !subscriptions.data && platforms.isLoading}
                  error={
                    platforms.isError && !platforms.data
                      ? { message: offline, retry: () => void platforms.refetch() }
                      : null
                  }
                  empty={{
                    title: t("没有需要处理的事项"),
                    hint: t("每个平台都有可路由节点，订阅与检测任务都没有报错。"),
                  }}
                />
              </Panel>

              <Panel className="lg:col-span-5">
                <PanelHeader
                  title={t("检测任务")}
                  meta={jobs.data ? t("运行 {{running}} · 排队 {{queued}}", { running, queued }) : undefined}
                  actions={
                    <Link to="/jobs" className="link-quiet text-xs text-ink-soft">
                      {t("全部任务")}
                    </Link>
                  }
                />
                <PanelBody className="p-0">
                  {jobs.isError && !jobs.data ? (
                    <p className="px-4 py-3 text-xs text-ink-faint">{offline}</p>
                  ) : !jobs.data ? (
                    <p className="px-4 py-3 text-xs text-ink-faint">{t("正在加载")}</p>
                  ) : jobRows.length === 0 ? (
                    <p className="px-4 py-3 text-xs text-ink-faint">{t("还没有检测任务。")}</p>
                  ) : (
                    <ul>
                      {jobRows.map((job) => (
                        <JobRow key={job.id} job={job} label={jobStatusLabel(job.status, t)} />
                      ))}
                    </ul>
                  )}
                </PanelBody>
              </Panel>
            </div>

            <Panel>
              <PanelHeader
                title={t("平台配线")}
                description={t("一个平台一行：它能路由多少节点、对外用几个出口、此刻挂着多少租约。")}
                meta={platforms.data ? t("{{count}} 个平台", { count: platformItems.length }) : undefined}
              />
              <DataTable
                dense
                caption={t("平台配线")}
                columns={platformColumns}
                rows={platformRows}
                rowKey={(row) => row.platform.id}
                loading={platforms.isLoading && !platforms.data}
                error={
                  platforms.isError && !platforms.data
                    ? { message: offline, retry: () => void platforms.refetch() }
                    : null
                }
                empty={{
                  title: t("还没有平台"),
                  hint: t("平台是接入用户名里的一段，先建一个平台，客户端才能连上来。"),
                  action: (
                    <Button asChild size="sm" variant="primary">
                      <Link to="/platforms">{t("新建")}</Link>
                    </Button>
                  ),
                }}
              />
            </Panel>

            <div className="grid gap-5 lg:grid-cols-12">
              <Panel className="lg:col-span-7">
                <PanelHeader
                  title={t("覆盖地区")}
                  meta={exits.data ? t("{{count}} 个地区", { count: regions.length }) : undefined}
                />
                <PanelBody className="flex flex-col gap-3">
                  {exits.data && facts.length > 0 ? (
                    <div className="overflow-hidden border-y border-rule">
                      <EgressMap countries={countries} origin="" />
                    </div>
                  ) : null}
                  {exits.isError && !exits.data ? (
                    <p className="text-xs text-ink-faint">{offline}</p>
                  ) : !exits.data ? (
                    <p className="text-xs text-ink-faint">{t("正在加载")}</p>
                  ) : regions.length === 0 ? (
                    <p className="text-xs text-ink-faint">{t("还没有出口，导入订阅后这里会按国家列出。")}</p>
                  ) : (
                    <ul className="flex flex-col">
                      {regions.slice(0, 8).map((region) => (
                        <li
                          key={region.iso}
                          className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)_3.5rem] items-center gap-2 border-b border-row-rule py-1.5 last:border-0"
                        >
                          <span className="truncate text-xs text-ink">{countryName(region.iso)}</span>
                          <span aria-hidden className="h-1.5 overflow-hidden rounded-[2px] bg-sunk">
                            <span
                              className={cn("block h-full", region.healthy === region.exits ? "bg-signal" : "bg-warn")}
                              style={{
                                width: `${regionPeak === 0 ? 0 : Math.max(4, (region.exits / regionPeak) * 100)}%`,
                              }}
                            />
                          </span>
                          <span className="readout text-right text-2xs text-ink-soft">
                            {formatCount(region.healthy)} / {formatCount(region.exits)}
                          </span>
                        </li>
                      ))}
                    </ul>
                  )}
                </PanelBody>
              </Panel>

              <Panel className="lg:col-span-5">
                <PanelHeader
                  title={t("最近变更")}
                  meta={events.data ? t("{{count}} 条", { count: events.data.items.length }) : undefined}
                  actions={
                    <Link to="/audit" className="link-quiet text-xs text-ink-soft">
                      {t("审计日志")}
                    </Link>
                  }
                />
                <PanelBody className="p-0">
                  {events.isError && !events.data ? (
                    <p className="px-4 py-3 text-xs text-ink-faint">{offline}</p>
                  ) : !events.data ? (
                    <p className="px-4 py-3 text-xs text-ink-faint">{t("正在加载")}</p>
                  ) : events.data.items.length === 0 ? (
                    <p className="px-4 py-3 text-xs text-ink-faint">{t("还没有写操作。")}</p>
                  ) : (
                    <ul>
                      {events.data.items.map((entry) => (
                        <AuditRow key={entry.id} entry={entry} />
                      ))}
                    </ul>
                  )}
                </PanelBody>
              </Panel>
            </div>
          </>
        )}
      </div>
    </>
  );
}

function snapshotTime(iso: string): number | null {
  const ms = Date.parse(iso);
  return Number.isFinite(ms) ? ms : null;
}

function jobStatusLabel(status: string, t: (key: string) => string): string {
  switch (status) {
    case "running":
      return t("运行中");
    case "queued":
      return t("排队中");
    case "done":
      return t("已完成");
    case "failed":
      return t("失败");
    case "cancelled":
      return t("已取消");
    default:
      return status;
  }
}

function jobTone(status: string): "signal" | "live" | "warn" | "alert" | "neutral" {
  switch (status) {
    case "running":
      return "live";
    case "done":
      return "signal";
    case "queued":
      return "neutral";
    case "failed":
      return "alert";
    default:
      return "neutral";
  }
}

function JobRow({ job, label }: { job: IntelJob; label: string }) {
  const settled = job.done + job.failed + job.skipped;
  const share = job.total > 0 ? Math.min(100, (settled / job.total) * 100) : 0;
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_5.5rem] items-center gap-3 border-b border-row-rule px-4 py-2 last:border-0">
      <div className="flex min-w-0 flex-col gap-1">
        <span className="flex items-baseline gap-2">
          <span className="readout truncate text-xs text-ink">{job.id.slice(0, 8)}</span>
          <span className="text-2xs text-ink-faint">{job.kind}</span>
        </span>
        <span className="flex items-center gap-2">
          <span aria-hidden className="h-1.5 flex-1 overflow-hidden rounded-[2px] bg-sunk">
            <span
              className={cn(
                "block h-full",
                job.failed > 0 ? "bg-alert" : job.status === "done" ? "bg-signal" : "bg-live",
              )}
              style={{ width: `${share}%` }}
            />
          </span>
          <span className="readout shrink-0 text-2xs text-ink-soft">
            {formatCount(settled)} / {formatCount(job.total)}
          </span>
        </span>
      </div>
      <Badge tone={jobTone(job.status)} className="justify-self-end">
        {label}
      </Badge>
    </li>
  );
}

function AuditRow({ entry }: { entry: AuditLogEntry }) {
  const [method, ...rest] = entry.action.split(" ");
  const path = rest.join(" ");
  return (
    <li className="grid grid-cols-[4.5rem_4.5rem_minmax(0,1fr)] items-center gap-3 border-b border-row-rule px-4 py-2 last:border-0">
      <span className="readout text-2xs text-ink-faint">{formatTimestamp(entry.at_ns / 1_000_000)}</span>
      <Badge tone="neutral" className="justify-self-start">
        {method}
      </Badge>
      <span className="readout truncate text-xs text-ink-soft" title={path || entry.action}>
        {path || entry.action}
      </span>
    </li>
  );
}
