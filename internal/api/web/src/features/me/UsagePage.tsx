import { useQuery } from "@tanstack/react-query";
import { Activity, ArrowDown, ArrowUp, Gauge, RefreshCw } from "lucide-react";
import { useState } from "react";
import {
  ControlButton,
  ControlTable,
  ControlTableBody,
  ControlTableCell,
  ControlTableColumn,
  ControlTableHead,
  ControlTableNumericCell,
  ControlTableRow,
  ControlTableWrap,
  ControlTabs,
  EmptyWorkState,
  QueryBadge,
  StatusMark,
  WorkspaceCard,
  WorkspacePage,
} from "../../components/control-plane";
import { ErrorState, LoadingState } from "../../components/ui/QueryState";
import { DailyUsageChart } from "../../components/ui/DailyUsageChart";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import { getOverview, getUsage } from "./api";

const NANOS_PER_MILLI = 1_000_000;

function formatDay(ns: number) {
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, { month: "short", day: "2-digit", timeZone: "UTC" }).format(new Date(Math.round(ns / NANOS_PER_MILLI)));
}

export function UsagePage() {
  const { t } = useI18n();
  const [days, setDays] = useState(30);
  const usageQuery = useQuery({ queryKey: ["me-usage", days], queryFn: () => getUsage(days) });
  const overviewQuery = useQuery({ queryKey: ["me-overview"], queryFn: getOverview });
  const items = [...(usageQuery.data?.items ?? [])].sort((a, b) => a.day_ns - b.day_ns);
  const ingress = items.reduce((sum, item) => sum + item.ingress_bytes, 0);
  const egress = items.reduce((sum, item) => sum + item.egress_bytes, 0);
  const total = ingress + egress;
  const peak = items.reduce((highest, item) => Math.max(highest, item.total_bytes), 0);
  const subscription = overviewQuery.data?.subscription;
  const quota = subscription?.traffic_bytes ?? 0;
  const used = overviewQuery.data?.period_used_bytes ?? 0;
  const quotaPercent = quota > 0 ? Math.min(100, Math.round((used / quota) * 100)) : 0;
  const refreshing = usageQuery.isFetching || overviewQuery.isFetching;

  const refresh = () => {
    void usageQuery.refetch();
    void overviewQuery.refetch();
  };

  return (
    <WorkspacePage
      eyebrow="Self Service / usage"
      title={t("用量分析")}
      description={t("查看流量趋势、周期配额与每日明细。")}
      meta={<StatusMark tone={usageQuery.isError || overviewQuery.isError ? "warn" : usageQuery.data && overviewQuery.data ? "live" : "neutral"} label={usageQuery.isError || overviewQuery.isError ? t("读取失败") : usageQuery.data && overviewQuery.data ? t("数据已同步") : t("等待数据")} />}
      actions={<ControlButton variant="secondary" size="sm" onPress={refresh} loading={refreshing}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>}
    >
      <div className="usage-toolbar">
        <ControlTabs
          label={t("统计周期")}
          selectedKey={String(days)}
          onSelectionChange={(value) => setDays(Number(value))}
          items={[{ id: "7", label: t("7 天") }, { id: "30", label: t("30 天") }, { id: "90", label: t("90 天") }]}
        />
      </div>

      {usageQuery.isError && !usageQuery.data ? (
        <EmptyWorkState title={t("用量读取失败")} detail={formatApiErrorMessage(usageQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void usageQuery.refetch()}>{t("重试")}</ControlButton>} />
      ) : null}

      <div className="usage-bento">
        <WorkspaceCard className="usage-bento__trend" title={t("带宽趋势")} description={t("每日入站与出站流量合计。") } showDescription meta={usageQuery.data ? <QueryBadge label={t("{{count}} 天", { count: items.length })} /> : undefined}>
          {usageQuery.isLoading ? <LoadingState /> : usageQuery.isError && !usageQuery.data ? <ErrorState message={t("用量读取失败")} onRetry={() => void usageQuery.refetch()} /> : items.length === 0 ? <EmptyWorkState embedded icon={<Activity size={22} />} title={t("暂无每日流量数据")} detail={t("所选周期内未记录到流量消耗。")} /> : <>
          <div className="usage-summary">
            <strong>{formatBytes(total)}</strong>
            <span>{t("所选周期总流量")}</span>
          </div>
          <DailyUsageChart items={items} />
          </>}
        </WorkspaceCard>

        <WorkspaceCard className="usage-bento__quota" title={t("周期配额")} actions={<Gauge size={18} aria-hidden />}>
          {overviewQuery.isLoading ? <LoadingState /> : overviewQuery.isError && !overviewQuery.data ? <ErrorState message={t("读取失败")} onRetry={() => void overviewQuery.refetch()} /> : !subscription ? <EmptyWorkState embedded icon={<Gauge size={22} />} title={t("暂无套餐")} /> : <>
          <div className="quota-ring" style={{ "--quota": `${quotaPercent * 3.6}deg` } as React.CSSProperties}>
            <div><strong>{quota > 0 ? `${quotaPercent}%` : "∞"}</strong><span>{t("已使用")}</span></div>
          </div>
          <div className="usage-quota-copy"><span>{formatBytes(used)}</span><span>{quota > 0 ? formatBytes(quota) : t("无限制")}</span></div>
          </>}
        </WorkspaceCard>

        <WorkspaceCard title={t("流量方向")} className="usage-bento__directions">
          <div className="usage-direction"><ArrowDown size={17} aria-hidden /><span>{t("入站流量")}</span><strong>{usageQuery.data ? formatBytes(ingress) : "—"}</strong></div>
          <div className="usage-direction"><ArrowUp size={17} aria-hidden /><span>{t("出站流量")}</span><strong>{usageQuery.data ? formatBytes(egress) : "—"}</strong></div>
          <div className="usage-direction"><Activity size={17} aria-hidden /><span>{t("单日峰值")}</span><strong>{usageQuery.data ? formatBytes(peak) : "—"}</strong></div>
        </WorkspaceCard>

        <WorkspaceCard title={t("请求质量")} className="usage-bento__quality">
          <div className="usage-quality-grid">
            <div><span>{t("请求数")}</span><strong>—</strong><small>{t("暂未采集")}</small></div>
            <div><span>{t("错误率")}</span><strong>—</strong><small>{t("暂未采集")}</small></div>
          </div>
        </WorkspaceCard>
      </div>

      <WorkspaceCard title={t("每日明细")} description={t("按天拆分入站、出站与总流量。") } showDescription>
        {items.length === 0 && usageQuery.isSuccess ? <EmptyWorkState embedded title={t("暂无每日流量数据")} detail={t("所选周期内未记录到流量消耗。") } /> : null}
        {usageQuery.isLoading ? <LoadingState /> : null}
        {usageQuery.isError && !usageQuery.data ? <ErrorState message={t("用量读取失败")} onRetry={() => void usageQuery.refetch()} /> : null}
        {items.length > 0 ? (
          <ControlTableWrap>
            <ControlTable>
              <ControlTableHead><ControlTableColumn>{t("日期")}</ControlTableColumn><ControlTableColumn className="text-right">{t("入站流量")}</ControlTableColumn><ControlTableColumn className="text-right">{t("出站流量")}</ControlTableColumn><ControlTableColumn className="text-right">{t("合计流量")}</ControlTableColumn></ControlTableHead>
              <ControlTableBody>
                {items.map((item) => <ControlTableRow key={item.day_ns}><ControlTableCell>{formatDay(item.day_ns)}</ControlTableCell><ControlTableNumericCell>{formatBytes(item.ingress_bytes)}</ControlTableNumericCell><ControlTableNumericCell>{formatBytes(item.egress_bytes)}</ControlTableNumericCell><ControlTableNumericCell>{formatBytes(item.total_bytes)}</ControlTableNumericCell></ControlTableRow>)}
              </ControlTableBody>
            </ControlTable>
          </ControlTableWrap>
        ) : null}
      </WorkspaceCard>
    </WorkspacePage>
  );
}
