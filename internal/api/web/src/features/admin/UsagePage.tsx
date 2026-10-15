import { useQuery } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout } from "../../components/ui/Readout";
import { Select } from "../../components/ui/Select";
import { Sparkline } from "../../components/ui/Sparkline";
import {
  Table,
  TableWrap,
  TBody,
  TD,
  TDClip,
  TH,
  THead,
  TR,
} from "../../components/ui/Table";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import {
  getUsageDaily,
  getUsagePlatforms,
  getUsageSummary,
  getUsageUsers,
} from "./api";

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


export function UsagePage() {
  const { t } = useI18n();
  const [days, setDays] = useState<number>(30);

  const summaryQuery = useQuery({
    queryKey: ["admin-usage-summary"],
    queryFn: getUsageSummary,
  });

  const dailyQuery = useQuery({
    queryKey: ["admin-usage-daily", days],
    queryFn: () => getUsageDaily({ days }),
  });

  const usersQuery = useQuery({
    queryKey: ["admin-usage-users", days],
    queryFn: () => getUsageUsers({ days, limit: 50 }),
  });

  const platformsQuery = useQuery({
    queryKey: ["admin-usage-platforms", days],
    queryFn: () => getUsagePlatforms({ days }),
  });

  const isRefreshing =
    summaryQuery.isFetching ||
    dailyQuery.isFetching ||
    usersQuery.isFetching ||
    platformsQuery.isFetching;

  const handleRefresh = () => {
    void summaryQuery.refetch();
    void dailyQuery.refetch();
    void usersQuery.refetch();
    void platformsQuery.refetch();
  };

  const summary = summaryQuery.data;
  const dailyItems = dailyQuery.data?.items ?? [];
  const sparklineValues = dailyItems.map((d) => d.total_bytes);
  const userItems = usersQuery.data?.items ?? [];
  const platformItems = platformsQuery.data?.items ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("用量与分析")}
        description={t("全站资源用量、用户流量排名与每日用量趋势统计。")}
        actions={
          <div className="flex items-center gap-3">
            <div className="flex items-center gap-2">
              <span className="text-xs text-ink-soft">{t("统计周期")}</span>
              <Select
                value={String(days)}
                onChange={(e) => setDays(Number(e.target.value))}
                className="w-28"
              >
                <option value="7">{t("最近 7 天")}</option>
                <option value="30">{t("最近 30 天")}</option>
                <option value="90">{t("最近 90 天")}</option>
              </Select>
            </div>
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
          </div>
        }
      />

      {/* Metric Readout Cards */}
      {summaryQuery.isError ? (
        <Panel>
          <PanelBody>
            <ErrorState
              message={formatApiErrorMessage(summaryQuery.error, t)}
              onRetry={() => void summaryQuery.refetch()}
            />
          </PanelBody>
        </Panel>
      ) : summaryQuery.isLoading ? (
        <Panel>
          <PanelBody>
            <LoadingState label={t("正在加载用量概览...")} />
          </PanelBody>
        </Panel>
      ) : (
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-5">
          <Panel className="p-4">
            <Readout
              label={t("活跃用户")}
              value={String(summary?.active_users ?? 0)}
              unit={t("人")}
              tone="signal"
            />
          </Panel>
          <Panel className="p-4">
            <Readout
              label={t("总用户数")}
              value={String(summary?.total_users ?? 0)}
              unit={t("人")}
              tone="ink"
            />
          </Panel>
          <Panel className="p-4">
            <Readout
              label={t("今日全站流量")}
              value={formatBytes(summary?.today_bytes ?? 0)}
              tone="ink"
            />
          </Panel>
          <Panel className="p-4">
            <Readout
              label={t("本周期总流量")}
              value={formatBytes(summary?.period_bytes ?? 0)}
              tone="ink"
            />
          </Panel>
          <Panel className="p-4">
            <Readout
              label={t("即将到期用户")}
              value={String(summary?.expiring_soon_users ?? 0)}
              unit={t("人")}
              hint={
                summary?.expiring_within_days
                  ? t("未来 {{days}} 天内", { days: summary.expiring_within_days })
                  : undefined
              }
              tone={
                (summary?.expiring_soon_users ?? 0) > 0 ? "warn" : "muted"
              }
            />
          </Panel>
        </div>
      )}

      {/* Daily Traffic Trend */}
      <Panel>
        <PanelHeader
          title={t("每日流量趋势")}
          description={t("所选时间范围内的全站每日流量消耗趋势。")}
          actions={
            sparklineValues.length >= 2 ? (
              <div className="w-36">
                <Sparkline values={sparklineValues} tone="accent" height={28} />
              </div>
            ) : undefined
          }
        />
        <PanelBody className="p-0">
          {dailyQuery.isLoading ? (
            <LoadingState label={t("正在加载每日流量...")} className="py-12" />
          ) : dailyQuery.isError ? (
            <ErrorState
              message={formatApiErrorMessage(dailyQuery.error, t)}
              onRetry={() => void dailyQuery.refetch()}
              className="py-12"
            />
          ) : dailyItems.length === 0 ? (
            <EmptyState
              title={t("暂无每日流量数据")}
              hint={t("所选时间窗口内未记录到流量数据。")}
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

      {/* Two Columns: User Usage & Platform Usage */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        {/* User Usage Ranking */}
        <Panel>
          <PanelHeader
            title={t("用户用量排行")}
            description={t("统计所选时间窗口内流量消耗最高的用户（前 50 名）。")}
            meta={<Badge tone="neutral">{userItems.length}</Badge>}
          />
          <PanelBody className="p-0">
            {usersQuery.isLoading ? (
              <LoadingState label={t("正在加载用户用量排行...")} className="py-12" />
            ) : usersQuery.isError ? (
              <ErrorState
                message={formatApiErrorMessage(usersQuery.error, t)}
                onRetry={() => void usersQuery.refetch()}
                className="py-12"
              />
            ) : userItems.length === 0 ? (
              <EmptyState
                title={t("暂无用户用量数据")}
                hint={t("所选时间窗口内未记录到用户流量。")}
                className="py-12"
              />
            ) : (
              <TableWrap>
                <Table>
                  <THead>
                    <TR>
                      <TH>{t("用户名")}</TH>
                      <TH>{t("套餐")}</TH>
                      <TH className="text-right">{t("合计流量")}</TH>
                      <TH className="text-right">{t("周期用量")}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {userItems.map((u) => (
                      <TR key={u.user_id}>
                        <TD>
                          <TDClip className="font-semibold text-ink">
                            {u.username}
                          </TDClip>
                        </TD>
                        <TD>
                          <TDClip className="text-xs text-ink-soft">
                            {u.plan_name || "-"}
                          </TDClip>
                        </TD>
                        <TD className="readout text-right text-xs font-semibold text-ink">
                          {formatBytes(u.total_bytes)}
                        </TD>
                        <TD className="readout text-right text-xs text-ink-soft">
                          {formatBytes(u.period_used_bytes)} /{" "}
                          {u.traffic_bytes > 0
                            ? formatBytes(u.traffic_bytes)
                            : t("不限")}
                        </TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              </TableWrap>
            )}
          </PanelBody>
        </Panel>

        {/* Platform Usage Distribution */}
        <Panel>
          <PanelHeader
            title={t("平台用量分布")}
            description={t("各平台在所选时间窗口内的流量分发情况。")}
            meta={<Badge tone="neutral">{platformItems.length}</Badge>}
          />
          <PanelBody className="p-0">
            {platformsQuery.isLoading ? (
              <LoadingState label={t("正在加载平台用量...")} className="py-12" />
            ) : platformsQuery.isError ? (
              <ErrorState
                message={formatApiErrorMessage(platformsQuery.error, t)}
                onRetry={() => void platformsQuery.refetch()}
                className="py-12"
              />
            ) : platformItems.length === 0 ? (
              <EmptyState
                title={t("暂无平台用量数据")}
                hint={t("所选时间窗口内未记录到平台流量。")}
                className="py-12"
              />
            ) : (
              <TableWrap>
                <Table>
                  <THead>
                    <TR>
                      <TH>{t("平台标识")}</TH>
                      <TH>{t("平台名称")}</TH>
                      <TH className="text-right">{t("合计流量")}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {platformItems.map((p) => (
                      <TR key={p.platform_id}>
                        <TD className="font-mono text-xs text-ink-soft">
                          {p.platform_id}
                        </TD>
                        <TD>
                          <TDClip className="font-semibold text-ink">
                            {p.platform_name || p.platform_id}
                          </TDClip>
                        </TD>
                        <TD className="readout text-right text-xs font-semibold text-ink">
                          {formatBytes(p.total_bytes)}
                        </TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              </TableWrap>
            )}
          </PanelBody>
        </Panel>
      </div>
    </div>
  );
}
