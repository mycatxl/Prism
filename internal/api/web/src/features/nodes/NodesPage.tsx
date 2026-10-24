import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDownUp, ArrowLeft, ArrowRight, ArrowUpRight, Copy, Download, ExternalLink, Globe2, Network, Radar, RefreshCw, ShieldCheck, Zap } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router-dom";
import { ControlButton, ControlInput, ControlSelect, ControlTabs, DetailFact, EmptyWorkState, FilterChip, InspectorDrawer, ObjectList, ObjectRow, PageFrame, StatusMark, WorkSurface } from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { opsPaths } from "../../lib/paths";
import { formatDateTime } from "../../lib/time";
import { listPlatforms } from "../platforms/api";
import { listSubscriptions } from "../subscriptions/api";
import { createIntelJob } from "../jobs/api";
import type { IntelJobScope } from "../jobs/types";
import { buildBulkIntelScope, hasUnsupportedFilters } from "./intelScope";
import { getAllRegions, getRegionName } from "./regions";
import { exportNodes, getNode, listNodes, NODE_EXPORT_FORMATS, probeEgress, probeLatency, type NodeExportFormat } from "./api";
import type { NodeListQuery, NodeSummary, NodeSortBy } from "./types";
import { getQualityStatus, inspectNode, qualityPollingInterval } from "./quality/api";
import { QualityDetails } from "./quality/QualityDetails";
import { ExitRecordsPanel } from "./quality/ExitRecordsPanel";
import { purityBands } from "./quality/presentation";
import { NodeIntelPanel } from "./NodeIntel";

const sizes = [20, 50, 100, 200] as const;
const protocolLabels: Record<string, string> = { shadowsocks: "Shadowsocks", vmess: "VMess", vless: "VLESS", trojan: "Trojan", hysteria: "Hysteria", hysteria2: "Hysteria 2", tuic: "TUIC", wireguard: "WireGuard", shadowtls: "ShadowTLS", socks: "SOCKS", http: "HTTP", ssh: "SSH", anytls: "AnyTLS", direct: "Direct" };
const exportLabels: Record<NodeExportFormat, string> = { singbox: "sing-box JSON", mihomo: "mihomo / Clash YAML", v2rayn: "v2rayN Base64", uri: "URI 分享链接", proxy: "代理地址", csv: "CSV 分析表", json: "JSON 分析数据" };
const sortLabels: Record<NodeSortBy, string> = { tag: "节点名称", created_at: "创建时间", failure_count: "连续失败", region: "地区", purity_score: "纯净度", latency: "参考延迟", assessed_at: "最近检测" };

type HealthTone = "neutral" | "signal" | "warn" | "alert";

function nodeState(node: NodeSummary): { label: string; tone: HealthTone } {
  if (!node.enabled) return { label: "禁用", tone: "neutral" };
  if (!node.has_outbound) return { label: "危急", tone: "alert" };
  if (node.circuit_open_since) return { label: "降级", tone: "warn" };
  return { label: "健康", tone: "signal" };
}

function nameOf(node: NodeSummary) {
  return node.display_tag || node.tags[0]?.tag || node.node_hash.slice(0, 12);
}

function integer(value: string | null, fallback: number) {
  const parsed = Number(value);
  return value !== null && Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : fallback;
}

function optionalInteger(value: string): number | undefined {
  if (!value.trim()) return undefined;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : undefined;
}

function toneForState(tone: HealthTone): "neutral" | "signal" | "warn" | "alert" {
  return tone;
}

function DetailSection({ title, children }: { title: ReactNode; children: ReactNode }) {
  return <section className="cp-node-detail-section"><h3>{title}</h3>{children}</section>;
}

export function NodesPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const location = useLocation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [exportFormat, setExportFormat] = useState<NodeExportFormat>("csv");
  const view = params.get("view") === "exits" ? "exits" : "nodes";
  const selected = view === "nodes" ? params.get("selected") || "" : "";
  const page = integer(params.get("page"), 0);
  const rawSize = integer(params.get("size"), 50);
  const pageSize = sizes.includes(rawSize as (typeof sizes)[number]) ? rawSize : 50;
  const sort = (Object.keys(sortLabels) as NodeSortBy[]).includes(params.get("sort") as NodeSortBy) ? params.get("sort") as NodeSortBy : "tag";
  const order = params.get("order") === "desc" ? "desc" : "asc";
  const mode = params.get("status") || (params.get("enabled") === "false" ? "disabled" : params.get("has_outbound") === "false" ? "error" : params.get("circuit_open") === "true" ? "circuit_open" : "all");
  const keyword = params.get("tag_keyword") || params.get("tag") || "";
  const filter = {
    ip_type: params.get("ip_type") || "",
    quality_state: params.get("quality_state") || "",
    risk_grade: params.get("risk_grade") || "",
    purity_band: params.get("purity_band") || "",
    protocol: params.get("protocol") || "",
    tag_keyword: keyword,
    platform_id: params.get("platform_id") || "",
    subscription_id: params.get("subscription_id") || "",
    region: params.get("region") || "",
    egress_ip: params.get("egress_ip") || "",
    purity_min: params.get("purity_min") || "",
    purity_max: params.get("purity_max") || "",
    verdict: params.get("verdict") || "",
    confidence_min: params.get("confidence_min") || "",
    native: params.get("native") || "",
    asn: params.get("asn") || "",
    country: params.get("country") || "",
    probed_since: params.get("probed_since") || "",
  };
  const selectedChecks = params.getAll("check").map((value) => value.trim()).filter(Boolean);
  const modeFilter = {
    enabled: mode === "disabled" ? false : mode !== "all" ? true : undefined,
    has_outbound: mode === "error" ? false : mode === "healthy" || mode === "circuit_open" ? true : undefined,
    circuit_open: mode === "healthy" ? false : mode === "circuit_open" ? true : undefined,
  };
  const nodeQuery: NodeListQuery = {
    ...filter,
    ...modeFilter,
    limit: pageSize,
    offset: page * pageSize,
    sort_by: sort,
    sort_order: order,
    purity_min: optionalInteger(filter.purity_min),
    purity_max: optionalInteger(filter.purity_max),
    asn: optionalInteger(filter.asn),
    native: filter.native === "true" ? true : filter.native === "false" ? false : undefined,
    verdict: filter.verdict || undefined,
    confidence_min: filter.confidence_min || undefined,
    country: filter.country || undefined,
    check: selectedChecks,
  };

  const qualityStatus = useQuery({ queryKey: ["quality", "status"], queryFn: getQualityStatus, refetchInterval: (query) => qualityPollingInterval(query.state.data) });
  const nodesQuery = useQuery({ queryKey: ["nodes", nodeQuery], queryFn: ({ signal }) => listNodes(nodeQuery, signal), enabled: view === "nodes", placeholderData: (previous) => previous, refetchInterval: qualityPollingInterval(qualityStatus.data) });
  const nodes = nodesQuery.data?.items ?? [];
  const detailQuery = useQuery({ queryKey: ["node", selected], queryFn: () => getNode(selected), enabled: Boolean(selected), refetchInterval: qualityPollingInterval(qualityStatus.data) });
  const detail = detailQuery.data ?? nodes.find((node) => node.node_hash === selected);
  const platforms = useQuery({ queryKey: ["platforms", "node-filter"], queryFn: () => listPlatforms({ limit: 1000 }), enabled: view === "nodes", staleTime: 60_000 });
  const subscriptions = useQuery({ queryKey: ["subscriptions", "node-filter"], queryFn: () => listSubscriptions({ limit: 1000 }), enabled: view === "nodes", staleTime: 60_000 });

  const update = (key: string, value: string) => setParams((previous) => {
    const next = new URLSearchParams(previous);
    if (value) next.set(key, value); else next.delete(key);
    if (key === "tag_keyword") next.delete("tag");
    if (key !== "page" && key !== "selected") next.delete("page");
    return next;
  }, { replace: true, state: location.state });
  const changeView = (nextView: string) => setParams((previous) => {
    const next = new URLSearchParams(previous);
    if (nextView === "exits") next.set("view", "exits"); else next.delete("view");
    next.delete("selected");
    return next;
  }, { replace: true });
  const open = (hash: string) => setParams((previous) => { const next = new URLSearchParams(previous); next.set("selected", hash); return next; }, { state: { nodePanel: true } });
  const close = () => { if (location.state?.nodePanel) navigate(-1); else update("selected", ""); };
  const clearFilters = () => setParams((previous) => {
    const next = new URLSearchParams();
    for (const key of ["view", "selected", "size", "sort", "order"]) { const value = previous.get(key); if (value) next.set(key, value); }
    return next;
  }, { replace: true, state: location.state });
  const updateChecks = (value: string) => setParams((previous) => {
    const next = new URLSearchParams(previous);
    next.delete("check");
    value.split(",").map((item) => item.trim()).filter(Boolean).forEach((item) => next.append("check", item));
    next.delete("page");
    return next;
  }, { replace: true });
  const changeSort = (value: string) => { update("sort", value); if (value === sort) update("order", order === "asc" ? "desc" : "asc"); else update("order", "asc"); };

  const probe = useMutation({ mutationFn: ({ hash, kind }: { hash: string; kind: "egress" | "latency" }) => kind === "egress" ? probeEgress(hash) : probeLatency(hash), onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["nodes"] }); void queryClient.invalidateQueries({ queryKey: ["node"] }); showToast("success", t("探测已完成")); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const qualityProbe = useMutation({ mutationFn: inspectNode, onSuccess: (result) => { void queryClient.invalidateQueries({ queryKey: ["nodes"] }); void queryClient.invalidateQueries({ queryKey: ["node"] }); void queryClient.invalidateQueries({ queryKey: ["quality"] }); showToast("success", t(result.queued ? "检测已排队" : "已复用最近的检测结果")); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const bulkCount = nodesQuery.data?.total ?? 0;
  const bulkScopeBlocked = mode === "error" || mode === "disabled" || mode === "circuit_open";
  const bulkScopeWider = hasUnsupportedFilters(params);
  const [bulkJobID, setBulkJobID] = useState("");
  const bulkJob = useMutation({ mutationFn: (request: { scope: IntelJobScope; count: number }) => createIntelJob({ kind: "full", scope: request.scope, force: true }), onSuccess: (response, request) => { void queryClient.invalidateQueries({ queryKey: ["intel-jobs"] }); setBulkJobID(response.job.id); showToast("success", t("已创建情报任务（预计 {{count}} 个节点）", { count: request.count })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const exportMutation = useMutation({ mutationFn: (format: NodeExportFormat) => exportNodes(format, nodeQuery), onSuccess: (result) => { const url = URL.createObjectURL(result.blob); const anchor = document.createElement("a"); anchor.href = url; anchor.download = result.fileName; anchor.click(); URL.revokeObjectURL(url); showToast("success", t("已导出 {{count}} 个节点", { count: result.exported })); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const refresh = () => { void queryClient.invalidateQueries({ queryKey: ["nodes"] }); void queryClient.invalidateQueries({ queryKey: ["node"] }); void queryClient.invalidateQueries({ queryKey: ["platforms", "node-filter"] }); void queryClient.invalidateQueries({ queryKey: ["subscriptions", "node-filter"] }); void queryClient.invalidateQueries({ queryKey: ["quality"] }); };
  const copy = async (value: string) => { try { await navigator.clipboard.writeText(value); showToast("success", t("已复制")); } catch { showToast("error", t("无法复制，请手动选择文本")); } };

  const activeFilters = [
    ["status", mode !== "all" ? mode : "", t("状态")], ["tag_keyword", keyword, t("关键词")], ["platform_id", filter.platform_id ? platforms.data?.items.find((item) => item.id === filter.platform_id)?.name || filter.platform_id : "", t("平台")], ["subscription_id", filter.subscription_id ? subscriptions.data?.items.find((item) => item.id === filter.subscription_id)?.name || filter.subscription_id : "", t("订阅")], ["country", filter.country, t("国家")], ["region", filter.region, t("地区")], ["ip_type", filter.ip_type, t("IP 类型")], ["quality_state", filter.quality_state, t("质量")], ["risk_grade", filter.risk_grade, t("风险")], ["purity_band", filter.purity_band, t("纯净度")], ["protocol", filter.protocol, t("协议")], ["egress_ip", filter.egress_ip, t("出口 IP")], ["purity_min", filter.purity_min, t("纯净度下限")], ["purity_max", filter.purity_max, t("纯净度上限")], ["verdict", filter.verdict, t("判定")], ["confidence_min", filter.confidence_min, t("置信度")], ["native", filter.native, t("原生 IP")], ["asn", filter.asn, "ASN"], ["probed_since", filter.probed_since, t("探测时间")], ["check", selectedChecks.join(", "), t("检测项")],
  ].filter((item) => Boolean(item[1])) as Array<[string, string, ReactNode]>;
  const hasActiveFilters = activeFilters.length > 0;
  const totalPages = Math.max(1, Math.ceil((nodesQuery.data?.total ?? 0) / pageSize));
  const currentPage = Math.min(page, totalPages - 1);
  const selectedIndex = nodes.findIndex((node) => node.node_hash === selected);
  const regions = useMemo(() => getAllRegions(), []);

  return (
    <PageFrame eyebrow="Inventory / node pool" title={t("节点") } description={t("按健康、来源、网络质量和情报证据查找可交付节点。所有条件都可深链、移除和重新执行。") } meta={<StatusMark tone={nodesQuery.isError ? "warn" : nodesQuery.isSuccess ? "live" : "neutral"} label={nodesQuery.isError ? t("读取失败") : nodesQuery.isSuccess ? t("结果已同步") : t("等待结果")} />} actions={<><ControlButton variant="secondary" size="sm" onPress={refresh} loading={nodesQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton><ControlButton variant="secondary" size="sm" onPress={() => navigate(`${opsPaths.subscriptions}?create=1`)}><Download size={14} aria-hidden />{t("导入订阅")}</ControlButton></>}>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <ControlTabs label={t("节点视图")} selectedKey={view} onSelectionChange={changeView} items={[{ id: "nodes", label: t("节点线路"), count: nodesQuery.data?.total }, { id: "exits", label: t("出口记录") }]} />

      {view === "exits" ? <WorkSurface eyebrow="Evidence / egress" title={t("出口记录")} description={t("查看出口质量和检测证据；详情动作由现有质量工作面提供。") }><ExitRecordsPanel /></WorkSurface> : (
        <>
          <section className="cp-command cp-node-command">
            <div className="cp-command__body">
              <ControlInput label={t("搜索节点") } value={keyword} onChange={(event) => update("tag_keyword", event.target.value)} placeholder={t("名称、标签或节点标识")} className="cp-node-command__keyword" />
              <ControlSelect label={t("健康状态")} value={mode} onChange={(event) => update("status", event.target.value)}><option value="all">{t("全部")}</option><option value="healthy">{t("健康")}</option><option value="circuit_open">{t("降级")}</option><option value="error">{t("危急")}</option><option value="disabled">{t("禁用")}</option></ControlSelect>
              <ControlSelect label={t("平台")} value={filter.platform_id} onChange={(event) => update("platform_id", event.target.value)}><option value="">{t("全部平台")}</option>{platforms.data?.items.map((platform) => <option key={platform.id} value={platform.id}>{platform.name}</option>)}</ControlSelect>
              <ControlSelect label={t("订阅来源")} value={filter.subscription_id} onChange={(event) => update("subscription_id", event.target.value)}><option value="">{t("全部订阅")}</option>{subscriptions.data?.items.map((subscription) => <option key={subscription.id} value={subscription.id}>{subscription.name}</option>)}</ControlSelect>
              <ControlInput label={t("国家 / 地区") } list="node-country-options" value={filter.country} onChange={(event) => update("country", event.target.value)} placeholder="CN / US" />
              <datalist id="node-country-options">{regions.map((region) => <option key={region.code} value={region.code}>{getRegionName(region.code)}</option>)}</datalist>
            </div>
            <div className="cp-command__actions"><ControlButton variant="primary" size="sm" onPress={() => document.getElementById("node-advanced-filters")?.scrollIntoView({ behavior: "smooth", block: "center" })}><ArrowDownUp size={14} aria-hidden />{t("高级筛选")}</ControlButton></div>
          </section>

          {hasActiveFilters ? <div className="cp-active-filters" aria-label={t("当前筛选")}>{activeFilters.map(([key, value, label]) => <FilterChip key={`${key}-${value}`} label={<>{label}: {value}</>} onRemove={() => key === "check" ? updateChecks("") : update(key, "")} />)}<ControlButton variant="ghost" size="sm" onPress={clearFilters}>{t("清除全部")}</ControlButton></div> : null}

          <details id="node-advanced-filters" className="cp-advanced-filters">
            <summary>{t("网络质量、情报与探测条件")}</summary>
            <div className="cp-advanced-filters__grid">
              <ControlInput label={t("地区") } value={filter.region} onChange={(event) => update("region", event.target.value)} placeholder="region" />
              <ControlInput label={t("出口 IP") } value={filter.egress_ip} onChange={(event) => update("egress_ip", event.target.value)} placeholder="IP address" />
              <ControlSelect label={t("IP 类型")} value={filter.ip_type} onChange={(event) => update("ip_type", event.target.value)}><option value="">{t("全部")}</option>{["residential", "mobile", "business", "wireless", "datacenter", "non_residential", "conflicting"].map((value) => <option key={value} value={value}>{value}</option>)}</ControlSelect>
              <ControlSelect label={t("质量状态")} value={filter.quality_state} onChange={(event) => update("quality_state", event.target.value)}><option value="">{t("全部")}</option>{["fresh", "stale", "pending", "unsupported", "error"].map((value) => <option key={value} value={value}>{value}</option>)}</ControlSelect>
              <ControlSelect label={t("风险等级")} value={filter.risk_grade} onChange={(event) => update("risk_grade", event.target.value)}><option value="">{t("全部")}</option>{["low", "medium", "high", "critical"].map((value) => <option key={value} value={value}>{value}</option>)}</ControlSelect>
              <ControlSelect label={t("纯净度等级")} value={filter.purity_band} onChange={(event) => update("purity_band", event.target.value)}><option value="">{t("全部")}</option>{purityBands.map((band) => <option key={band.id} value={band.id}>{t(band.label)}</option>)}</ControlSelect>
              <ControlSelect label={t("协议")} value={filter.protocol} onChange={(event) => update("protocol", event.target.value)}><option value="">{t("全部")}</option>{Object.entries(protocolLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</ControlSelect>
              <ControlInput label={t("纯净度下限") } type="number" min="0" max="100" value={filter.purity_min} onChange={(event) => update("purity_min", event.target.value)} />
              <ControlInput label={t("纯净度上限") } type="number" min="0" max="100" value={filter.purity_max} onChange={(event) => update("purity_max", event.target.value)} />
              <ControlSelect label={t("判定")} value={filter.verdict} onChange={(event) => update("verdict", event.target.value)}><option value="">{t("全部")}</option>{["favorable", "review", "high_risk", "conflicting", "incomplete", "pending"].map((value) => <option key={value} value={value}>{value}</option>)}</ControlSelect>
              <ControlSelect label={t("最低置信度")} value={filter.confidence_min} onChange={(event) => update("confidence_min", event.target.value)}><option value="">{t("全部")}</option>{["low", "medium", "high"].map((value) => <option key={value} value={value}>{value}</option>)}</ControlSelect>
              <ControlSelect label={t("原生 IP")} value={filter.native} onChange={(event) => update("native", event.target.value)}><option value="">{t("全部")}</option><option value="true">{t("是")}</option><option value="false">{t("否")}</option></ControlSelect>
              <ControlInput label="ASN" type="number" min="0" value={filter.asn} onChange={(event) => update("asn", event.target.value)} />
              <ControlInput label={t("探测时间") } type="datetime-local" value={filter.probed_since} onChange={(event) => update("probed_since", event.target.value)} />
              <ControlInput label={t("检测项") } value={selectedChecks.join(", ")} onChange={(event) => updateChecks(event.target.value)} placeholder="proxycheck:clean" hint={t("多个检测项用逗号分隔")} />
            </div>
          </details>

          <WorkSurface eyebrow="Query result" title={t("节点对象") } meta={nodesQuery.data ? t("{{count}} 个结果", { count: nodesQuery.data.total }) : t("未加载")} description={t("列表按身份、来源、出口、质量和最近证据排列；选择一行打开完整 inspector。") } actions={<div className="cp-list-actions"><ControlSelect aria-label={t("排序字段")} value={sort} onChange={(event) => changeSort(event.target.value)}>{Object.entries(sortLabels).map(([value, label]) => <option key={value} value={value}>{t(label)}</option>)}</ControlSelect><ControlButton variant="ghost" size="sm" onPress={() => update("order", order === "asc" ? "desc" : "asc")}><ArrowDownUp size={14} aria-hidden />{order === "asc" ? t("升序") : t("降序")}</ControlButton><ControlSelect aria-label={t("导出格式")} value={exportFormat} onChange={(event) => setExportFormat(event.target.value as NodeExportFormat)}>{NODE_EXPORT_FORMATS.map((format) => <option key={format} value={format}>{t(exportLabels[format])}</option>)}</ControlSelect><ControlButton variant="secondary" size="sm" onPress={() => exportMutation.mutate(exportFormat)} loading={exportMutation.isPending}><Download size={14} aria-hidden />{t("导出")}</ControlButton></div>}>
            <div className="cp-node-bulkbar"><span>{bulkScopeWider && !bulkScopeBlocked ? t("当前筛选包含批量情报暂不支持的条件，任务范围会按服务端规则收敛。") : t("批量情报只对当前筛选结果中的健康节点执行。")}</span>{bulkJobID ? <ControlButton variant="ghost" size="sm" onPress={() => navigate(opsPaths.jobs)}>{t("查看检测任务")}<ArrowUpRight size={14} aria-hidden /></ControlButton> : null}<ControlButton variant="secondary" size="sm" isDisabled={bulkScopeBlocked || bulkCount === 0} onPress={() => bulkJob.mutate({ scope: buildBulkIntelScope(params), count: bulkCount })} loading={bulkJob.isPending}><Radar size={14} aria-hidden />{t("批量拉取情报")} <span className="cp-mono">{bulkCount}</span></ControlButton></div>
            {nodesQuery.isError && !nodesQuery.data ? <EmptyWorkState title={t("节点数据读取失败")} detail={formatApiErrorMessage(nodesQuery.error, t)} action={<ControlButton size="sm" variant="secondary" onPress={() => void nodesQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
            {nodesQuery.isLoading && !nodesQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在读取节点")}</div> : null}
            {!nodesQuery.isLoading && !nodesQuery.isError && nodes.length === 0 ? <EmptyWorkState title={hasActiveFilters ? t("没有匹配的节点") : t("还没有节点")} detail={hasActiveFilters ? t("移除部分筛选或检查订阅来源后重试。") : t("添加订阅后，节点会出现在这里。")} action={hasActiveFilters ? <ControlButton variant="secondary" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : <ControlButton variant="primary" size="sm" onPress={() => navigate(`${opsPaths.subscriptions}?create=1`)}>{t("添加订阅")}</ControlButton>} /> : null}
            {nodes.length > 0 ? <ObjectList className="cp-node-list">
              {nodes.map((node) => {
                const state = nodeState(node);
                const sourceNames = [...new Set(node.tags.map((tag) => tag.subscription_name))];
                const intel = node.intel;
                const locationLabel = [intel?.country || node.region, intel?.city || intel?.colo].filter(Boolean).join(" · ") || t("位置未知");
                const score = typeof intel?.purity_score === "number" ? String(intel.purity_score) : t("未评估");
                return <ObjectRow key={node.node_hash} active={selected === node.node_hash} onClick={() => open(node.node_hash)} leading={<Network size={14} />} title={nameOf(node)} subtitle={`${sourceNames[0] || t("无订阅来源")} · ${protocolLabels[node.protocol || ""] || node.protocol || t("协议未知")}`} facts={<><span>{locationLabel}</span><span>{node.egress_ip || t("无出口")}</span><span>{node.reference_latency_ms === undefined ? "—" : `${Math.round(node.reference_latency_ms)} ms`}</span><span>{t("纯净度")} {score}</span></>} status={<StatusMark tone={toneForState(state.tone)} label={t(state.label)} />} action={<ArrowRight size={14} aria-hidden />} />;
              })}
            </ObjectList> : null}
            <div className="cp-pagination"><span>{nodesQuery.data ? t("第 {{page}} / {{pages}} 页 · {{start}}-{{end}} / {{total}}", { page: currentPage + 1, pages: totalPages, start: nodesQuery.data.total ? currentPage * pageSize + 1 : 0, end: Math.min((currentPage + 1) * pageSize, nodesQuery.data.total), total: nodesQuery.data.total }) : ""}</span><div><ControlSelect aria-label={t("每页") } value={pageSize} onChange={(event) => update("size", event.target.value)}>{sizes.map((size) => <option key={size} value={size}>{size} / {t("页")}</option>)}</ControlSelect><ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("上一页")} isDisabled={nodesQuery.isFetching || currentPage === 0} onPress={() => update("page", String(currentPage - 1))}><ArrowLeft size={15} /></ControlButton><ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("下一页")} isDisabled={nodesQuery.isFetching || currentPage >= totalPages - 1} onPress={() => update("page", String(currentPage + 1))}><ArrowRight size={15} /></ControlButton></div></div>
          </WorkSurface>
        </>
      )}

      <InspectorDrawer open={Boolean(selected)} onOpenChange={(openState) => { if (!openState) close(); }} title={detail ? nameOf(detail) : selected.slice(0, 12)} eyebrow={t("Node inspector")} footer={detail ? <div className="cp-drawer-actions"><div><ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("上一个节点")} isDisabled={selectedIndex <= 0} onPress={() => update("selected", nodes[selectedIndex - 1]?.node_hash || "")}><ArrowLeft size={15} /></ControlButton><ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("下一个节点")} isDisabled={selectedIndex < 0 || selectedIndex >= nodes.length - 1} onPress={() => update("selected", nodes[selectedIndex + 1]?.node_hash || "")}><ArrowRight size={15} /></ControlButton></div><div><ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("打开完整页面")} onPress={() => navigate(`${opsPaths.nodes}/${detail.node_hash}`)}><ExternalLink size={15} /></ControlButton><ControlButton variant="secondary" size="sm" isDisabled={qualityProbe.isPending || !detail.has_outbound || qualityStatus.data?.enabled === false} onPress={() => qualityProbe.mutate(detail.node_hash)} loading={qualityProbe.isPending}><ShieldCheck size={14} aria-hidden />{t("更新网络特征")}</ControlButton><ControlButton variant="secondary" size="sm" isDisabled={probe.isPending || !detail.has_outbound} onPress={() => probe.mutate({ hash: detail.node_hash, kind: "egress" })}><Globe2 size={14} aria-hidden />{t("出口探测")}</ControlButton><ControlButton variant="secondary" size="sm" isDisabled={probe.isPending || !detail.has_outbound} onPress={() => probe.mutate({ hash: detail.node_hash, kind: "latency" })}><Zap size={14} aria-hidden />{t("延迟探测")}</ControlButton></div></div> : undefined}>
        {detailQuery.isLoading && !detail ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在读取节点详情")}</div> : null}
        {detailQuery.isError ? <EmptyWorkState title={t("节点详情读取失败")} action={<ControlButton size="sm" variant="secondary" onPress={() => void detailQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
        {detail ? <div className="cp-node-detail">
          <div className="cp-node-detail__identity"><div className="cp-node-detail__mark"><Network size={17} /></div><div><strong>{nameOf(detail)}</strong><span>{detail.node_hash}</span></div><StatusMark tone={toneForState(nodeState(detail).tone)} label={t(nodeState(detail).label)} /></div>
          <DetailSection title={t("出口与运行状态")}><dl className="cp-detail-facts"><DetailFact label={t("连接协议")} value={protocolLabels[detail.protocol || ""] || detail.protocol || t("未知")} /><DetailFact label={t("出口 IP")} value={<span className="cp-inline-value"><code>{detail.egress_ip || "—"}</code>{detail.egress_ip ? <ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("复制出口 IP")} onPress={() => void copy(detail.egress_ip || "")}><Copy size={13} /></ControlButton> : null}</span>} mono /><DetailFact label={t("地区")} value={detail.region ? getRegionName(detail.region.toUpperCase()) : "—"} /><DetailFact label={t("参考延迟")} value={detail.reference_latency_ms === undefined ? "—" : `${Math.round(detail.reference_latency_ms)} ms`} mono /><DetailFact label={t("连续失败")} value={detail.failure_count} mono /><DetailFact label={t("最近出口更新")} value={formatDateTime(detail.last_egress_update || "")} mono /><DetailFact label={t("创建时间")} value={formatDateTime(detail.created_at)} mono /></dl>{detail.last_error ? <p className="cp-node-error">{detail.last_error}</p> : null}</DetailSection>
          <DetailSection title={t("质量与风险")}><QualityDetails summary={detail.quality} nodeHash={detail.node_hash} nodeIP={detail.egress_ip} nodeReady={detail.has_outbound} /></DetailSection>
          <DetailSection title={t("纯净度与情报")}><NodeIntelPanel intel={detail.intel} nodeHash={detail.node_hash} ready={detail.has_outbound} notify={showToast} /></DetailSection>
          <DetailSection title={t("来源与标签")}>{detail.tags.length ? <div className="cp-node-tags">{detail.tags.map((tag) => <Link key={`${tag.subscription_id}-${tag.tag}`} to={`${opsPaths.subscriptions}?selected=${tag.subscription_id}`}><span>{tag.subscription_name}</span><code>{tag.tag}</code><ArrowUpRight size={13} /></Link>)}</div> : <p className="cp-muted">{t("没有来源标签")}</p>}</DetailSection>
        </div> : null}
      </InspectorDrawer>
    </PageFrame>
  );
}
