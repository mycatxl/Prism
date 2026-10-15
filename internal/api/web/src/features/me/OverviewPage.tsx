import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout } from "../../components/ui/Readout";
import { Sparkline } from "../../components/ui/Sparkline";
import { Table, TableWrap, TBody, TD, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import { getOverview, getSessions, revokeSession } from "./api";

const NANOS_PER_MILLI = 1_000_000;

function formatNsDate(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(date);
}

function formatNsDateTime(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(date);
}

export function OverviewPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  const overviewQuery = useQuery({
    queryKey: ["me-overview"],
    queryFn: getOverview,
  });

  const sessionsQuery = useQuery({
    queryKey: ["me-sessions"],
    queryFn: getSessions,
  });

  const isRefreshing = overviewQuery.isFetching || sessionsQuery.isFetching;

  const handleRefresh = () => {
    void overviewQuery.refetch();
    void sessionsQuery.refetch();
  };

  const revokeMutation = useMutation({
    mutationFn: (sessionID: string) => revokeSession(sessionID),
    onSuccess: () => {
      showToast("success", t("会话已撤销"));
      void queryClient.invalidateQueries({ queryKey: ["me-sessions"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  const overview = overviewQuery.data;
  const sessions = sessionsQuery.data?.items ?? [];
  const dailyItems = overview?.usage_daily ?? [];
  const sparklineValues = dailyItems.map((d) => d.total_bytes);

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("概览")}
        description={t("查看当前套餐配额、实时代理并发、用量趋势与登录会话。")}
        actions={
          <Button
            variant="secondary"
            onClick={handleRefresh}
            disabled={isRefreshing}
            title={t("刷新")}
          >
            <RefreshCw
              size={14}
              className={isRefreshing ? "animate-spin" : undefined}
            />
            <span>{t("刷新")}</span>
          </Button>
        }
      />

      {overviewQuery.isLoading ? (
        <Panel>
          <PanelBody>
            <LoadingState label={t("正在加载概览数据...")} className="py-12" />
          </PanelBody>
        </Panel>
      ) : overviewQuery.isError ? (
        <Panel>
          <PanelBody>
            <ErrorState
              message={formatApiErrorMessage(overviewQuery.error, t)}
              onRetry={() => void overviewQuery.refetch()}
              className="py-12"
            />
          </PanelBody>
        </Panel>
      ) : (
        <>
          {/* Subscription Card */}
          <Panel>
            <PanelHeader
              title={t("订阅与配额")}
              description={t("当前生效的套餐规格、周期流量使用进度与限制指标。")}
              meta={
                overview?.subscription ? (
                  <Badge
                    tone={
                      overview.subscription.status === "active"
                        ? "signal"
                        : "warn"
                    }
                  >
                    {overview.subscription.status === "active"
                      ? t("生效中")
                      : overview.subscription.status}
                  </Badge>
                ) : null
              }
            />
            <PanelBody>
              {!overview?.subscription ? (
                <EmptyState
                  title={t("暂无有效订阅")}
                  hint={t(
                    "当前账号暂未分配套餐，套餐由平台运营人员统一分配与调整。",
                  )}
                  className="py-8"
                />
              ) : (
                <div className="space-y-6">
                  <div className="flex flex-wrap items-center justify-between gap-4 border-b border-glass-edge pb-4">
                    <div className="space-y-1">
                      <span className="text-xs text-ink-faint">
                        {t("当前套餐")}
                      </span>
                      <div className="text-lg font-semibold text-ink">
                        {overview.subscription.plan_name}
                      </div>
                    </div>
                    <div className="text-right">
                      <span className="text-xs text-ink-faint">
                        {t("到期时间")}
                      </span>
                      <div className="font-mono text-sm text-ink-soft">
                        {overview.subscription.expires_at_ns > 0
                          ? formatNsDate(overview.subscription.expires_at_ns)
                          : t("永不过期")}
                      </div>
                    </div>
                  </div>

                  {/* Quota Progress Bar (when traffic_bytes > 0) */}
                  {overview.traffic_bytes > 0 && (
                    <div className="space-y-2">
                      <div className="flex items-center justify-between text-xs">
                        <span className="text-ink-soft">
                          {t("本周期流量使用进度")}
                        </span>
                        <span className="font-mono text-ink">
                          {formatBytes(overview.period_used_bytes)} /{" "}
                          {formatBytes(overview.traffic_bytes)} (
                          {Math.min(
                            100,
                            Math.round(
                              (overview.period_used_bytes /
                                overview.traffic_bytes) *
                                100,
                            ),
                          )}
                          %)
                        </span>
                      </div>
                      <div className="h-2 w-full overflow-hidden rounded bg-paper-sunken">
                        <div
                          className="h-full bg-accent transition-all duration-300"
                          style={{
                            width: `${Math.min(
                              100,
                              (overview.period_used_bytes /
                                overview.traffic_bytes) *
                                100,
                            )}%`,
                          }}
                        />
                      </div>
                    </div>
                  )}

                  {/* Readout Metrics */}
                  <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
                    <div className="rounded-panel border border-glass-edge bg-glass p-3">
                      <Readout
                        label={t("本周期已用流量")}
                        value={formatBytes(overview.period_used_bytes)}
                        tone="ink"
                      />
                    </div>
                    <div className="rounded-panel border border-glass-edge bg-glass p-3">
                      <Readout
                        label={t("周期剩余流量")}
                        value={
                          overview.remaining_bytes !== null
                            ? formatBytes(overview.remaining_bytes)
                            : t("无限制")
                        }
                        tone={
                          overview.remaining_bytes !== null &&
                          overview.remaining_bytes <= 0
                            ? "alert"
                            : "signal"
                        }
                      />
                    </div>
                    <div className="rounded-panel border border-glass-edge bg-glass p-3">
                      <Readout
                        label={t("最大并发限制")}
                        value={
                          overview.max_concurrent > 0
                            ? String(overview.max_concurrent)
                            : t("无限制")
                        }
                        unit={overview.max_concurrent > 0 ? t("路") : undefined}
                        tone="ink"
                      />
                    </div>
                    <div className="rounded-panel border border-glass-edge bg-glass p-3">
                      <Readout
                        label={t("活跃密钥数 / 配额")}
                        value={
                          overview.max_keys > 0
                            ? `${overview.active_keys} / ${overview.max_keys}`
                            : `${overview.active_keys} / ${t("无限制")}`
                        }
                        tone={
                          overview.max_keys > 0 &&
                          overview.active_keys >= overview.max_keys
                            ? "warn"
                            : "ink"
                        }
                      />
                    </div>
                  </div>
                </div>
              )}
            </PanelBody>
          </Panel>

          {/* Live Concurrency Card */}
          <div className="grid grid-cols-1 gap-6 sm:grid-cols-3">
            <Panel className="sm:col-span-1">
              <PanelHeader
                title={t("实时并发连接")}
                description={t("反映此刻正在建立的代理连接总数。")}
              />
              <PanelBody className="space-y-3">
                <Readout
                  label={t("此刻实时连接")}
                  value={String(overview?.live_concurrent ?? 0)}
                  unit={t("路")}
                  tone={
                    (overview?.live_concurrent ?? 0) > 0 ? "live" : "muted"
                  }
                  hint={t(
                    "说明：此数值为此刻网络层正在活动的连接数，反映当前负载，而非配额上限。",
                  )}
                />
              </PanelBody>
            </Panel>

            <Panel className="sm:col-span-2">
              <PanelHeader
                title={t("可用平台范围")}
                description={t("当前套餐及密钥已授权的可访问目标平台。")}
                meta={
                  <Badge tone="neutral">
                    {overview?.platforms?.length ?? 0}
                  </Badge>
                }
              />
              <PanelBody>
                {!overview?.platforms || overview.platforms.length === 0 ? (
                  <div className="py-4 text-xs text-ink-soft">
                    {t("暂无可路由平台或未配置限制。")}
                  </div>
                ) : (
                  <div className="flex flex-wrap gap-2">
                    {overview.platforms.map((plat) => (
                      <div
                        key={plat.platform_id}
                        className="flex items-center gap-1.5 rounded-control border border-glass-edge bg-glass px-2.5 py-1 text-xs"
                      >
                        <span className="font-medium text-ink">
                          {plat.platform_name || plat.platform_id}
                        </span>
                        <span className="font-mono text-ink-faint">
                          ({plat.platform_id})
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </PanelBody>
            </Panel>
          </div>

          {/* 30-Day Usage Trend */}
          <Panel>
            <PanelHeader
              title={t("近 30 天用量趋势")}
              description={t("按天统计的历史入站与出站流量消耗。")}
              actions={
                sparklineValues.length >= 2 ? (
                  <div className="w-36">
                    <Sparkline
                      values={sparklineValues}
                      tone="accent"
                      height={28}
                    />
                  </div>
                ) : undefined
              }
            />
            <PanelBody className="p-0">
              {dailyItems.length === 0 ? (
                <EmptyState
                  title={t("暂无每日流量数据")}
                  hint={t("最近 30 天内未记录到流量消耗。")}
                  className="py-12"
                />
              ) : (
                <TableWrap>
                  <Table>
                    <THead>
                      <TR>
                        <TH>{t("日期")}</TH>
                        <TH className="text-right">{t("入站流量")}</TH>
                        <TH className="text-right">{t("出站流量")}</TH>
                        <TH className="text-right">{t("合计流量")}</TH>
                      </TR>
                    </THead>
                    <TBody>
                      {dailyItems.map((day) => (
                        <TR key={day.day_ns}>
                          <TD className="readout font-mono text-xs">
                            {formatNsDate(day.day_ns)}
                          </TD>
                          <TD className="readout text-right text-xs text-ink-soft">
                            {formatBytes(day.ingress_bytes)}
                          </TD>
                          <TD className="readout text-right text-xs text-ink-soft">
                            {formatBytes(day.egress_bytes)}
                          </TD>
                          <TD className="readout text-right text-xs font-semibold text-ink">
                            {formatBytes(day.total_bytes)}
                          </TD>
                        </TR>
                      ))}
                    </TBody>
                  </Table>
                </TableWrap>
              )}
            </PanelBody>
          </Panel>

          {/* Active Sessions Panel */}
          <Panel>
            <PanelHeader
              title={t("活跃会话")}
              description={t("当前登录此控制台的设备与浏览器会话。")}
              meta={<Badge tone="neutral">{sessions.length}</Badge>}
            />
            <PanelBody className="p-0">
              {sessionsQuery.isLoading ? (
                <LoadingState
                  label={t("正在加载会话列表...")}
                  className="py-8"
                />
              ) : sessionsQuery.isError ? (
                <ErrorState
                  message={formatApiErrorMessage(sessionsQuery.error, t)}
                  onRetry={() => void sessionsQuery.refetch()}
                  className="py-8"
                />
              ) : sessions.length === 0 ? (
                <EmptyState
                  title={t("暂无活跃会话")}
                  hint={t("未查询到任何活动的登录会话。")}
                  className="py-8"
                />
              ) : (
                <TableWrap>
                  <Table>
                    <THead>
                      <TR>
                        <TH>{t("登录 IP")}</TH>
                        <TH>{t("客户端环境")}</TH>
                        <TH>{t("登录时间")}</TH>
                        <TH>{t("会话到期")}</TH>
                        <TH className="text-right">{t("操作")}</TH>
                      </TR>
                    </THead>
                    <TBody>
                      {sessions.map((sess) => (
                        <TR key={sess.id}>
                          <TD className="font-mono text-xs text-ink">
                            <div className="flex items-center gap-2">
                              <span>{sess.ip || "-"}</span>
                              {sess.current && (
                                <Badge tone="signal">{t("当前设备")}</Badge>
                              )}
                            </div>
                          </TD>
                          <TD className="max-w-xs truncate text-xs text-ink-soft" title={sess.user_agent}>
                            {sess.user_agent || "-"}
                          </TD>
                          <TD className="readout font-mono text-xs text-ink-soft">
                            {formatNsDateTime(sess.created_at_ns)}
                          </TD>
                          <TD className="readout font-mono text-xs text-ink-soft">
                            {formatNsDateTime(sess.expires_at_ns)}
                          </TD>
                          <TD className="text-right">
                            {sess.current ? (
                              <span className="text-xs text-ink-faint">
                                {t("当前会话不可撤销")}
                              </span>
                            ) : (
                              <Button
                                variant="secondary"
                                onClick={() => revokeMutation.mutate(sess.id)}
                                disabled={revokeMutation.isPending}
                                title={t("撤销此设备的登录凭证")}
                              >
                                {t("撤销")}
                              </Button>
                            )}
                          </TD>
                        </TR>
                      ))}
                    </TBody>
                  </Table>
                </TableWrap>
              )}
            </PanelBody>
          </Panel>
        </>
      )}

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
  );
}
