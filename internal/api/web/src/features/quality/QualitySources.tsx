import { useQuery } from "@tanstack/react-query";
import { ArrowUpRight, RefreshCw } from "lucide-react";
import { Link } from "react-router-dom";
import { Badge, type BadgeProps } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Panel, PanelHeader, SectionTitle } from "../../components/ui/Panel";
import { ErrorState, LoadingState } from "../../components/ui/QueryState";
import { cn } from "../../lib/cn";
import { useI18n } from "../../i18n";
import { formatDateTime } from "../../lib/time";
import { getQualityStatus, qualityPollingInterval } from "./api";
import { inspectionErrorLabel, useQualityTime } from "./presentation";
import { PurityGuide } from "./PurityGuide";
import type { SourceStatus } from "./types";

type Tone = NonNullable<BadgeProps["tone"]>;

function SourceCard({ source, enabled }: { source: SourceStatus; enabled: boolean }) {
  const { t } = useI18n();
  const now = useQualityTime();
  const waiting = source.used_today >= source.daily_limit || Boolean(source.next_allowed_at && Date.parse(source.next_allowed_at) > now + 10000);
  const available = enabled && source.configured && !source.paused && !waiting;
  const state: { label: string; tone: Tone } = !enabled
    ? { label: "已停用", tone: "neutral" }
    : !source.configured
      ? { label: "待配置", tone: "neutral" }
      : source.paused
        ? { label: "已暂停", tone: "warn" }
        : waiting
          ? { label: "等待额度", tone: "warn" }
          : { label: "可用", tone: "signal" };
  const usedShare = source.daily_limit ? Math.min(100, source.used_today / source.daily_limit * 100) : 0;
  return <Panel>
    <PanelHeader
      title={source.name}
      description={t(source.id === "proxycheck" ? "网络类型与匿名特征" : "近期滥用记录")}
      actions={<Badge tone={state.tone} dot>{t(state.label)}</Badge>}
    />
    <div className="space-y-3 px-4 py-3">
      {source.configured && <>
        <div className="space-y-1.5">
          <div className="flex flex-wrap items-baseline justify-between gap-3">
            <span className="label">{t("今日查询预算")}</span>
            <span className="flex items-baseline gap-1">
              <span className="readout text-sm font-semibold">{source.used_today}</span>
              <span className="readout text-2xs text-ink-faint">/ {source.daily_limit}</span>
            </span>
          </div>
          <div
            className="h-1.5 w-full bg-paper-sunk"
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={source.daily_limit}
            aria-valuenow={source.used_today}
            aria-label={source.name + " " + t("今日查询预算")}
          >
            <div
              className={cn("h-full", available ? "bg-signal" : "bg-warn")}
              style={{ width: usedShare + "%" }}
            />
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-ink-soft">
          <span>{t("排队")} <span className="readout font-medium text-ink">{source.queued}</span></span>
          <span>{t("进行中")} <span className="readout font-medium text-ink">{source.running}</span></span>
          <span>{t("失败")} <span className="readout font-medium text-ink">{source.failed}</span></span>
        </div>
        {source.error_code && <p className="flex items-start gap-2 border border-warn/35 bg-warn-wash px-3 py-2 text-xs leading-relaxed text-warn" role="status">
          {t(inspectionErrorLabel(source.error_code))}
        </p>}
        {source.next_allowed_at && Date.parse(source.next_allowed_at) > now + 10000 && <p className="text-xs text-ink-soft">
          {t("预计恢复")} <span className="readout">{formatDateTime(source.next_allowed_at)}</span>
        </p>}
      </>}

      <div className="space-y-1.5 border-t border-rule pt-3">
        <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t(source.id === "proxycheck"
          ? "免 Key 可用，本实例保留每日 80 次预算；免费账号 Key 默认提高至 900 次。"
          : "免费账号每日 1,000 次，配置 Key 后启用独立复核。")}</p>
        <details>
          <summary className="cursor-pointer text-xs text-ink-soft transition-colors hover:text-ink">{t("配置方式")}</summary>
          <div className="mt-1.5 space-y-1.5">
            <p className="text-xs leading-relaxed text-ink-soft">{t("在部署的 .env 中设置此变量，然后重启服务：")}</p>
            <code className="block border border-rule bg-paper-sunk px-2 py-1 text-xs">
              {source.id === "proxycheck" ? "PRISM_QUALITY_API_KEY" : "PRISM_ABUSEIPDB_API_KEY"}
            </code>
            <p className="text-xs leading-relaxed text-ink-soft">{t(source.has_key ? "已配置 Key，页面不显示密钥内容。" : "当前未配置 Key。")}</p>
          </div>
        </details>
      </div>

      <a className="inline-flex items-center gap-1 text-xs" href={source.website} target="_blank" rel="noreferrer">
        {t("数据源说明")}<ArrowUpRight size={12} aria-hidden />
      </a>
    </div>
  </Panel>;
}

export function QualitySources() {
  const { t } = useI18n();
  const now = useQualityTime();
  const query = useQuery({ queryKey: ["quality", "status"], queryFn: getQualityStatus, refetchInterval: result => qualityPollingInterval(result.state.data) });
  const manual = query.data?.manual_sources?.find(source => source.id === "ippure");
  const registry = query.data?.registry_sources?.find(source => source.id === "torproject");
  const manualWaiting = manual?.next_allowed_at && Date.parse(manual.next_allowed_at) > now;
  return <div className="space-y-3">
    <SectionTitle
      trailing={
        <Button variant="secondary" size="sm" onClick={() => void query.refetch()} disabled={query.isFetching}>
          <RefreshCw size={14} className={query.isFetching ? "animate-spin" : undefined} />
          {t("刷新")}
        </Button>
      }
    >
      {t("质量与数据源")}
    </SectionTitle>
    <p className="max-w-[80ch] text-sm text-ink-soft">{t("自动检测来源、免费额度与节点按需复核。")}</p>

    {query.isLoading && <LoadingState />}
    {query.isError && <ErrorState message={t("数据暂时不可用")} onRetry={() => void query.refetch()} />}
    {query.data?.enabled === false && <p className="border border-warn/35 bg-warn-wash px-3 py-2 text-sm text-warn" role="status">{t("质量检测已停用，历史证据仍可查看。")}</p>}
    {query.data?.storage_error && <ErrorState message={t(inspectionErrorLabel(query.data.storage_error))} />}

    {manual && <Panel>
      <PanelHeader
        title="IPPure"
        description={t("通过选中节点查询本次出口")}
        actions={<Badge tone={manual.busy || manualWaiting ? "warn" : "outline"} dot={manual.busy}>
          {t(manual.busy ? "检测中" : manualWaiting ? "冷却中" : "按需查询")}
        </Badge>}
      />
      <div className="space-y-2 px-4 py-3">
        <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t("节点纯净度的主来源。无需 Key，由目标节点请求官方接口，核对实际出口后用于评分。")}</p>
        <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t("按需查询，每分钟最多一次。已核对出口的结果临时复用 24 小时，服务重启后清除。")}</p>
        <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t("官方接口仍在测试阶段，没有公开批量额度，不自动扫描节点池。")}</p>
        {manualWaiting && <p className="text-xs text-ink-soft">
          {t("预计恢复")} <span className="readout">{formatDateTime(manual.next_allowed_at!)}</span>
        </p>}
        <Link className="inline-flex items-center gap-1 text-xs" to="/nodes">{t("到节点详情复核")}</Link>
      </div>
    </Panel>}

    <div className="grid gap-3 lg:grid-cols-2">
      {query.data?.sources.map(source => <SourceCard key={source.id} source={source} enabled={query.data?.enabled ?? false} />)}
    </div>

    {registry && <Panel>
      <PanelHeader
        title="Tor Project"
        description={t("公开中继角色资料")}
        actions={<Badge tone={registry.ready ? "signal" : "neutral"} dot>{t(registry.ready ? "已加载" : "等待更新")}</Badge>}
      />
      <div className="space-y-2 px-4 py-3">
        <p className="max-w-[80ch] text-xs leading-relaxed text-ink-soft">{t("用于区分 Tor 出口、入口守卫与中继，不参与纯净度打分。公开资料每小时检查更新。")}</p>
        {registry.updated_at && <p className="text-xs text-ink-soft">
          {t("资料发布时间")} <span className="readout">{formatDateTime(registry.updated_at)}</span>
        </p>}
        <a className="inline-flex items-center gap-1 text-xs" href="https://metrics.torproject.org/onionoo.html" target="_blank" rel="noreferrer">
          {t("数据源说明")}<ArrowUpRight size={12} aria-hidden />
        </a>
      </div>
    </Panel>}

    <PurityGuide />
  </div>;
}