import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, ArrowUpRight, Clock3, Globe2, LoaderCircle, RefreshCw, Search, ShieldCheck, X } from "lucide-react";
import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Button } from "../../components/ui/Button";
import { DialogSurface } from "../../components/ui/DialogSurface";
import { Input } from "../../components/ui/Input";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
import { QueryState } from "../../components/ui/QueryState";
import { ToastContainer } from "../../components/ui/Toast";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatRelativeTime } from "../../lib/time";
import { getIPQuality, getQualityStatus, inspectIP, listQuality, qualityPollingInterval } from "./api";
import { IPTypeBadge, QualityBadge, QualityDetails, VerdictBadge } from "./QualityDetails";
import { evidenceFor, inspectionErrorLabel } from "./presentation";

export function ExitRecordsPanel() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const selected = params.get("quality_ip") || "";
  const keyword = params.get("quality_q") || "";
  const rawPage = Number(params.get("quality_page"));
  const page = Number.isSafeInteger(rawPage) && rawPage >= 0 ? rawPage : 0;
  const deferredKeyword = useDebouncedValue(keyword);
  const [ipInput, setIPInput] = useState("");
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const status = useQuery({ queryKey: ["quality", "status"], queryFn: getQualityStatus,
    refetchInterval: query => qualityPollingInterval(query.state.data) });
  const records = useQuery({ queryKey: ["quality", "records", deferredKeyword, page],
    queryFn: () => listQuality(deferredKeyword, page), refetchInterval: qualityPollingInterval(status.data) });
  const detail = useQuery({ queryKey: ["quality", "ip", selected], queryFn: () => getIPQuality(selected),
    enabled: Boolean(selected),
    refetchInterval: query => {
      const current = query.state.data;
      if (current && (current.state === "pending" || current.state === "partial")) return 3000;
      return qualityPollingInterval(status.data);
    } });
  const inspect = useMutation({
    mutationFn: inspectIP,
    onSuccess: (result) => {
      queryClient.setQueryData(["quality", "ip", result.quality.ip], result.quality);
      void queryClient.invalidateQueries({ queryKey: ["quality"] });
      void queryClient.invalidateQueries({ queryKey: ["nodes"] });
      showToast("success", t(result.queued ? "检测已排队" : "已复用最近的检测结果"));
      setParams(previous => { const next = new URLSearchParams(previous); next.set("quality_ip", result.quality.ip || ""); return next; });
    },
    onError: error => showToast("error", formatApiErrorMessage(error, t)),
  });
  const update = (key: string, value: string) => setParams(previous => {
    const next = new URLSearchParams(previous);
    if (value) next.set(key, value); else next.delete(key);
    if (key === "quality_q") next.delete("quality_page");
    return next;
  }, { replace: true });
  const summary = detail.data ?? records.data?.items.find(item => item.ip === selected);
  const count = (value?: number) => value === undefined ? "—" : value.toLocaleString();
  return <section className="quality-workspace">
    <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    <div className="quality-metrics">
      {[
        { label: "已查询 IP", value: status.data?.known_ips, icon: Globe2 },
        { label: "网络证据", value: status.data?.checked_ips, icon: ShieldCheck },
        { label: "IPPure 有效评分", value: status.data?.manual_sources?.find(source => source.id === "ippure")?.current_ips, icon: Activity },
        { label: "证据已过期", value: status.data?.stale_ips, icon: Clock3 },
      ].map(item => <div className="quality-metric" key={item.label}><span><item.icon size={16} />{t(item.label)}</span><strong>{count(item.value)}</strong></div>)}
    </div>
    <QueryState error={status.error} onRetry={() => void status.refetch()} />
    {status.data?.enabled === false && <div className="quality-notice">{t("质量检测已停用，历史证据仍可查看。")}</div>}
    {status.data?.storage_error && <div className="quality-inline-warning" role="alert">{t(inspectionErrorLabel(status.data.storage_error))}</div>}
    <section className="quality-inventory">
      <div className="quality-inventory-heading">
        <div><h2>{t("IP 检测记录")}</h2><p>{t("相同出口共享结果，节点连通状态独立记录。")}</p></div>
        <div className="page-actions"><Link to="/system-config?category=quality">{t("数据源与额度")}<ArrowUpRight size={13} /></Link><Button variant="ghost" size="sm" onClick={() => void records.refetch()} disabled={records.isFetching}><RefreshCw size={14} />{t("刷新")}</Button></div>
      </div>
      <form className="inspection-form" onSubmit={event => { event.preventDefault(); inspect.mutate(ipInput.trim()); }}>
        <Globe2 size={17} /><Input value={ipInput} maxLength={80} onChange={event => setIPInput(event.target.value)} placeholder={t("输入公网 IPv4 或 IPv6")} aria-label={t("检测 IP 地址")} />
        <Button type="submit" disabled={!ipInput.trim() || inspect.isPending || status.data?.enabled === false}>
          {inspect.isPending ? <LoaderCircle size={15} className="spin" /> : <ShieldCheck size={15} />}{t("查询网络特征")}
        </Button>
      </form>
      <label className="search-field quality-search"><Search size={15} /><Input value={keyword} onChange={event => update("quality_q", event.target.value)} aria-label={t("搜索质量记录")} placeholder={t("搜索 IP、ASN 或网络组织")} /></label>
      <QueryState loading={records.isLoading} error={records.error} onRetry={() => void records.refetch()} />
      {!records.isLoading && !records.isError && !records.data?.items.length && <div className="quality-empty">
        <span><ShieldCheck size={30} /></span><h3>{t("为出口建立第一份质量记录")}</h3>
        <p>{t("在这里输入 IP，或到节点池选择“检测质量”。新发现的出口也会自动排队。")}</p>
      </div>}
      {Boolean(records.data?.items.length) && <div className="quality-table-scroll"><table className="workbench-table quality-table">
        <thead><tr><th>{t("出口 IP")}</th><th>{t("IP 类型")}</th><th>{t("IPPure 纯净度参考")}</th><th>{t("综合判定")}</th><th>{t("IPPure 复核时间")}</th></tr></thead>
        <tbody>{records.data?.items.map(item => <tr key={item.ip} data-selected={selected === item.ip}>
          <td><button className="quality-ip" onClick={() => update("quality_ip", item.ip || "")}><Globe2 size={17} /><span><strong>{item.ip}</strong><small>{item.evidence?.organization || t("等待来源数据")}</small></span></button></td>
          <td><IPTypeBadge summary={item} /></td><td><QualityBadge summary={item} /></td>
          <td><VerdictBadge summary={item} /></td>
          <td className="muted">{formatRelativeTime(evidenceFor(item,"ippure")?.observed_at)}</td>
        </tr>)}</tbody>
      </table></div>}
      {records.data && <OffsetPagination page={page} totalPages={Math.max(1, Math.ceil(records.data.total / 25))} totalItems={records.data.total}
        pageSize={25} pageSizeOptions={[25]} onPageChange={next => update("quality_page", String(next))} onPageSizeChange={() => {}} disabled={records.isFetching} />}
    </section>
    {selected && <DialogSurface title={t("IP 质量详情")} variant="drawer" onClose={() => update("quality_ip", "")}>
      <div className="node-inspector quality-inspector">
        <header className="inspector-toolbar"><span>{t("IP 质量详情")}</span><Button variant="ghost" className="icon-button" aria-label={t("关闭")} onClick={() => update("quality_ip", "")}><X size={17} /></Button></header>
        <div className="quality-selected-ip"><span className="object-icon"><Globe2 size={24} /></span><h2>{selected}</h2><p>{t("独立出口的质量证据")}</p></div>
        <QueryState loading={detail.isLoading && !summary} error={detail.error} onRetry={() => void detail.refetch()} />
        <QualityDetails summary={summary} onInspect={() => inspect.mutate(selected)} pending={inspect.isPending} disabled={status.data?.enabled === false} />
      </div>
    </DialogSurface>}
  </section>;
}
