import { useQuery } from "@tanstack/react-query";
import { ArrowUpRight, RefreshCw, ShieldCheck } from "lucide-react";
import { Link } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { QueryState } from "../../components/ui/QueryState";
import { useI18n } from "../../i18n";
import { formatDateTime } from "../../lib/time";
import { getQualityStatus, qualityPollingInterval } from "./api";
import { inspectionErrorLabel, useQualityTime } from "./presentation";
import { PurityGuide } from "./PurityGuide";
import type { SourceStatus } from "./types";

function SourceCard({ source, enabled }: { source: SourceStatus; enabled: boolean }) {
  const { t } = useI18n();
  const now = useQualityTime();
  const waiting = source.used_today >= source.daily_limit || Boolean(source.next_allowed_at && Date.parse(source.next_allowed_at) > now + 10000);
  const available = enabled && source.configured && !source.paused && !waiting;
  return <section className="inspection-source">
    <div className="inspection-source-heading">
      <span className={"source-symbol source-symbol-" + source.id}><ShieldCheck size={19} /></span>
      <div><strong>{source.name}</strong><p>{t(source.id === "proxycheck" ? "网络类型与匿名特征" : "近期滥用记录")}</p></div>
      <Badge variant={available ? "success" : "neutral"}>{t(!enabled ? "已停用" : !source.configured ? "待配置" : source.paused ? "已暂停" : waiting ? "等待额度" : "可用")}</Badge>
    </div>
    {source.configured && <>
      <div className="source-budget-label"><span>{t("今日查询预算")}</span><strong>{source.used_today}<small> / {source.daily_limit}</small></strong></div>
      <progress max={source.daily_limit} value={source.used_today} aria-label={source.name + " " + t("今日查询预算")} />
      <div className="source-queue-facts"><span>{t("排队")} <b>{source.queued}</b></span><span>{t("进行中")} <b>{source.running}</b></span><span>{t("失败")} <b>{source.failed}</b></span></div>
      {source.error_code && <p className="quality-inline-warning">{t(inspectionErrorLabel(source.error_code))}</p>}
      {source.next_allowed_at && Date.parse(source.next_allowed_at) > now + 10000 && <p className="quality-explanation">{t("预计恢复")} {formatDateTime(source.next_allowed_at)}</p>}
    </>}
    <div className="source-setup">
      <p>{t(source.id === "proxycheck"
        ? "免 Key 可用，本实例保留每日 80 次预算；免费账号 Key 默认提高至 900 次。"
        : "免费账号每日 1,000 次，配置 Key 后启用独立复核。")}</p>
      <details><summary>{t("配置方式")}</summary>
        <p>{t("在部署的 .env 中设置此变量，然后重启服务：")}</p>
        <code>{source.id === "proxycheck" ? "PRISM_QUALITY_API_KEY" : "PRISM_ABUSEIPDB_API_KEY"}</code>
        <p>{t(source.has_key ? "已配置 Key，页面不显示密钥内容。" : "当前未配置 Key。")}</p>
      </details>
    </div>
    <a className="source-documentation" href={source.website} target="_blank" rel="noreferrer">{t("数据源说明")}<ArrowUpRight size={12} /></a>
  </section>;
}

export function QualitySources() {
  const { t } = useI18n();
  const now = useQualityTime();
  const query = useQuery({ queryKey: ["quality", "status"], queryFn: getQualityStatus, refetchInterval: result => qualityPollingInterval(result.state.data) });
  const manual = query.data?.manual_sources?.find(source => source.id === "ippure");
  const registry = query.data?.registry_sources?.find(source => source.id === "torproject");
  const manualWaiting = manual?.next_allowed_at && Date.parse(manual.next_allowed_at) > now;
  return <div className="quality-source-settings">
    <div className="settings-section-heading"><div><h3>{t("质量与数据源")}</h3><p>{t("自动检测来源、免费额度与节点按需复核。")}</p></div>
      <Button variant="secondary" size="sm" onClick={() => void query.refetch()} disabled={query.isFetching}><RefreshCw size={14} />{t("刷新")}</Button></div>
    <QueryState loading={query.isLoading} error={query.error} onRetry={() => void query.refetch()} />
    {query.data?.enabled === false && <p className="quality-notice">{t("质量检测已停用，历史证据仍可查看。")}</p>}
    {query.data?.storage_error && <p className="quality-inline-warning" role="alert">{t(inspectionErrorLabel(query.data.storage_error))}</p>}
    {manual && <section className="inspection-source manual-source-card">
      <div className="inspection-source-heading"><span className="source-symbol"><ShieldCheck size={19} /></span>
        <div><strong>IPPure</strong><p>{t("通过选中节点查询本次出口")}</p></div><Badge variant={manual.busy || manualWaiting ? "warning" : "info"}>{t(manual.busy ? "检测中" : manualWaiting ? "冷却中" : "按需查询")}</Badge></div>
      <p className="quality-explanation">{t("节点纯净度的主来源。无需 Key，由目标节点请求官方接口，核对实际出口后用于评分。")}</p>
      <p className="quality-explanation">{t("按需查询，每分钟最多一次。已核对出口的结果临时复用 24 小时，服务重启后清除。")}</p>
      <p className="quality-explanation">{t("官方接口仍在测试阶段，没有公开批量额度，不自动扫描节点池。")}</p>
      {manualWaiting && <p className="quality-explanation">{t("预计恢复")} {formatDateTime(manual.next_allowed_at!)}</p>}
      <Link className="source-documentation" to="/nodes">{t("到节点详情复核")}<ArrowUpRight size={12} /></Link>
    </section>}
    <div className="inspection-sources">{query.data?.sources.map(source => <SourceCard key={source.id} source={source} enabled={query.data?.enabled ?? false} />)}</div>

    {registry && <section className="inspection-source registry-source-card">
      <div className="inspection-source-heading"><span className="source-symbol"><ShieldCheck size={19} /></span><div><strong>Tor Project</strong><p>{t("公开中继角色资料")}</p></div><Badge variant={registry.ready ? "success" : "neutral"}>{t(registry.ready ? "已加载" : "等待更新")}</Badge></div>
      <p className="quality-explanation">{t("用于区分 Tor 出口、入口守卫与中继，不参与纯净度打分。公开资料每小时检查更新。")}</p>
      {registry.updated_at && <p className="quality-explanation">{t("资料发布时间")} {formatDateTime(registry.updated_at)}</p>}
      <a className="source-documentation" href="https://metrics.torproject.org/onionoo.html" target="_blank" rel="noreferrer">{t("数据源说明")}<ArrowUpRight size={12} /></a>
    </section>}
    <PurityGuide />
  </div>;
}
