import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  ChevronLeft,
  ChevronRight,
  Copy,
  Globe2,
  Network,
  RefreshCw,
  Search,
  ShieldCheck,
  SlidersHorizontal,
  X,
  Zap,
} from "lucide-react";
import { useState } from "react";
import {
  Link,
  useLocation,
  useNavigate,
  useSearchParams,
} from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { DialogSurface } from "../../components/ui/DialogSurface";
import { Input } from "../../components/ui/Input";
import { OffsetPagination } from "../../components/ui/OffsetPagination";
import { QueryState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime, formatRelativeTime } from "../../lib/time";
import { listPlatforms } from "../platforms/api";
import { listSubscriptions } from "../subscriptions/api";
import { getNode, listNodes, probeEgress, probeLatency } from "./api";
import { getAllRegions, getRegionName } from "./regions";
import type { NodeSummary, NodeSortBy } from "./types";
import { getQualityStatus, inspectNode, qualityPollingInterval } from "../quality/api";
import { IPTypeBadge, NetworkSignals, QualityBadge, QualityDetails, VerdictBadge } from "../quality/QualityDetails";
import { ExitRecordsPanel } from "../quality/QualityPage";
import { PurityGuide } from "../quality/PurityGuide";
import { evidenceFor, purityBands, typeLabels } from "../quality/presentation";

function status(node: NodeSummary): {
  label: string;
  variant: "neutral" | "danger" | "warning" | "success";
} {
  if (!node.enabled) return { label: "禁用", variant: "neutral" };
  if (!node.has_outbound) return { label: "错误", variant: "danger" };
  if (node.circuit_open_since)
    return {
      label: node.failure_count === 0 ? "待测" : "熔断",
      variant: "warning",
    };
  return { label: "健康", variant: "success" };
}
function nameOf(node: NodeSummary) {
  return node.display_tag || node.tags[0]?.tag || node.node_hash.slice(0, 12);
}
const sizes = [20, 50, 100, 200] as const;
const protocolLabels: Record<string, string> = { shadowsocks: "Shadowsocks", vmess: "VMess", vless: "VLESS", trojan: "Trojan", hysteria: "Hysteria", hysteria2: "Hysteria 2", tuic: "TUIC", wireguard: "WireGuard", shadowtls: "ShadowTLS", socks: "SOCKS", http: "HTTP", ssh: "SSH", anytls: "AnyTLS", direct: "Direct" };
const sorts = ["tag", "created_at", "failure_count", "region"] as const;
function integer(value: string | null, fallback: number) {
  const n = Number(value);
  return value !== null && Number.isSafeInteger(n) && n >= 0 ? n : fallback;
}

export function NodesPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const location = useLocation();
  const [advanced, setAdvanced] = useState(false);
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const view = params.get("view") === "exits" ? "exits" : "nodes";
  const selected = view === "nodes" ? params.get("selected") || "" : "";
  const page = integer(params.get("page"), 0);
  const rawSize = integer(params.get("size"), 50);
  const pageSize = sizes.includes(rawSize as (typeof sizes)[number])
    ? rawSize
    : 50;
  const sort = sorts.includes(params.get("sort") as NodeSortBy)
    ? (params.get("sort") as NodeSortBy)
    : "tag";
  const order = params.get("order") === "desc" ? "desc" : "asc";
  const mode =
    params.get("status") ||
    (params.get("enabled") === "false"
      ? "disabled"
      : params.get("has_outbound") === "false"
        ? "error"
        : params.get("circuit_open") === "true"
          ? "circuit_open"
          : "all");
  const keyword = params.get("tag_keyword") || params.get("tag") || "";
  const deferredKeyword = useDebouncedValue(keyword);
  const filter = {
    ip_type: params.get("ip_type") || "",
    quality_state: params.get("quality_state") || "",
    risk_grade: params.get("risk_grade") || "",
    purity_band: params.get("purity_band") || "",
    protocol: params.get("protocol") || "",
    tag_keyword: deferredKeyword,
    platform_id: params.get("platform_id") || "",
    subscription_id: params.get("subscription_id") || "",
    region: params.get("region") || "",
    egress_ip: params.get("egress_ip") || "",
    enabled: mode === "disabled" ? false : mode !== "all" ? true : undefined,
    has_outbound:
      mode === "error"
        ? false
        : mode === "healthy" || mode === "circuit_open"
          ? true
          : undefined,
    circuit_open:
      mode === "healthy" ? false : mode === "circuit_open" ? true : undefined,
    limit: pageSize,
    offset: page * pageSize,
    sort_by: sort,
    sort_order: order,
  } as const;
  const qualityStatus = useQuery({
    queryKey: ["quality", "status"],
    queryFn: getQualityStatus,
    refetchInterval: query => qualityPollingInterval(query.state.data),
  });
  const nodesQuery = useQuery({
    queryKey: ["nodes", filter],
    queryFn: ({ signal }) => listNodes(filter, signal),
    enabled: view === "nodes",
    placeholderData: (previous) => previous,
    refetchInterval: qualityPollingInterval(qualityStatus.data),
  });
  const nodes = nodesQuery.data?.items ?? [];
  const detailQuery = useQuery({
    queryKey: ["node", selected],
    queryFn: () => getNode(selected),
    enabled: Boolean(selected),
    refetchInterval: qualityPollingInterval(qualityStatus.data),
  });
  const detail =
    detailQuery.data ?? nodes.find((node) => node.node_hash === selected);
  const platforms = useQuery({
    queryKey: ["platforms", "node-filter"],
    queryFn: () => listPlatforms({ limit: 1000 }),
    enabled: view === "nodes",
    staleTime: 60_000,
  });
  const subscriptions = useQuery({
    queryKey: ["subscriptions", "node-filter"],
    queryFn: () => listSubscriptions({ limit: 1000 }),
    enabled: view === "nodes",
    staleTime: 60_000,
  });
  const update = (key: string, value: string) =>
    setParams(
      (previous) => {
        const next = new URLSearchParams(previous);
        if (value) next.set(key, value);
        else next.delete(key);
        if (key === "tag_keyword") next.delete("tag");
        if (key !== "page" && key !== "selected") next.delete("page");
        return next;
      },
      { replace: true, state: location.state },
    );
  const changeView = (nextView: "nodes" | "exits") => setParams(previous => {
    const next = new URLSearchParams(previous);
    if (nextView === "exits") next.set("view", "exits"); else next.delete("view");
    next.delete("selected");
    next.delete("quality_ip");
    return next;
  }, { replace: true, state: null });
  const open = (hash: string) => {
    const next = new URLSearchParams(params);
    next.set("selected", hash);
    setParams(next, { state: { nodePanel: true } });
  };
  const close = () => {
    if (location.state?.nodePanel) navigate(-1);
    else update("selected", "");
  };
  const changeSort = (key: NodeSortBy) =>
    setParams(
      (previous) => {
        const next = new URLSearchParams(previous);
        next.set("sort", key);
        next.set("order", sort === key && order === "asc" ? "desc" : "asc");
        next.delete("page");
        return next;
      },
      { replace: true },
    );
  const probe = useMutation({
    mutationFn: ({
      hash,
      kind,
    }: {
      hash: string;
      kind: "egress" | "latency";
    }) => (kind === "egress" ? probeEgress(hash) : probeLatency(hash)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["nodes"] });
      void queryClient.invalidateQueries({ queryKey: ["node"] });
      showToast("success", t("探测已完成"));
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const runProbe = (hash: string, kind: "egress" | "latency") =>
    probe.mutate({ hash, kind });
  const qualityProbe = useMutation({
    mutationFn: inspectNode,
    onSuccess: result => {
      void queryClient.invalidateQueries({ queryKey: ["nodes"] });
      void queryClient.invalidateQueries({ queryKey: ["node"] });
      void queryClient.invalidateQueries({ queryKey: ["quality"] });
      showToast("success", t(result.queued ? "检测已排队" : "已复用最近的检测结果"));
    },
    onError: error => showToast("error", formatApiErrorMessage(error, t)),
  });
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["quality"] });
    if (view === "nodes") void nodesQuery.refetch();
    if (selected) void detailQuery.refetch();
  };
  const copy = async (value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      showToast("success", t("已复制"));
    } catch {
      showToast("error", t("无法复制，请手动选择文本"));
    }
  };
  const selectedIndex = nodes.findIndex((node) => node.node_hash === selected);
  const numberOfFilters = [
    "platform_id",
    "subscription_id",
    "region",
    "egress_ip",
    "ip_type",
    "quality_state",
    "risk_grade",
    "purity_band",
    "protocol",
  ].filter((key) => params.get(key)).length;
  const sortButton = (key: NodeSortBy, label: string) => (
    <button className="table-sort-btn" onClick={() => changeSort(key)}>
      {t(label)}
      {sort !== key ? (
        <ArrowUpDown size={12} />
      ) : order === "asc" ? (
        <ArrowUp size={12} />
      ) : (
        <ArrowDown size={12} />
      )}
    </button>
  );
  return (
    <section className="nodes-workspace">
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <header className="module-header">
        <div>
          <h1>
            {t("节点池")}{" "}
            {view === "nodes" && <span className="heading-count">
              {nodesQuery.data ? nodesQuery.data.total.toLocaleString() : "--"}
            </span>}
          </h1>
          <p className="workspace-description">{t("按线路查看健康，按出口查看质量。")}</p>
        </div>
        <div className="page-actions">
          <Link className="btn btn-secondary" to="/system-config?category=quality"><ShieldCheck size={15} />{t("数据源与额度")}</Link>
          <Button
            variant="ghost"
            className="icon-button"
            title={t("刷新")}
            aria-label={t("刷新")}
            disabled={view === "nodes" && nodesQuery.isFetching}
            onClick={refresh}
          >
            <RefreshCw
              size={17}
              className={nodesQuery.isFetching ? "spin" : ""}
            />
          </Button>
          <Link className="btn btn-secondary" to="/subscriptions?create=1">
            <RssImport />
            {t("导入节点")}
          </Link>
        </div>
      </header>
      <nav className="node-view-switch" aria-label={t("节点池视图")}>
        <button type="button" aria-pressed={view === "nodes"} aria-controls="node-view-content" onClick={() => changeView("nodes")}><Network size={15} />{t("节点线路")}</button>
        <button type="button" aria-pressed={view === "exits"} aria-controls="node-view-content" onClick={() => changeView("exits")}><Globe2 size={15} />{t("出口记录")}</button>
      </nav>
      <div id="node-view-content">
      {view === "exits" ? <ExitRecordsPanel /> : <>
      <div className="node-toolbar">
        <label className="search-field">
          <Search size={16} />
          <Input
            aria-label={t("搜索节点")}
            placeholder={t("搜索节点名称或标签")}
            value={keyword}
            onChange={(event) => update("tag_keyword", event.target.value)}
          />
          {keyword && (
            <button
              aria-label={t("清除搜索")}
              onClick={() => update("tag_keyword", "")}
            >
              <X size={14} />
            </button>
          )}
        </label>
        <Select
          value={mode}
          aria-label={t("状态")}
          onChange={(event) => update("status", event.target.value)}
        >
          {[
            ["all", "全部状态"],
            ["healthy", "健康"],
            ["circuit_open", "熔断 / 待测"],
            ["error", "错误"],
            ["disabled", "禁用"],
          ].map(([key, label]) => (
            <option key={key} value={key}>
              {t(label)}
            </option>
          ))}
        </Select>
        <Button
          variant="secondary"
          onClick={() => setAdvanced((value) => !value)}
          aria-expanded={advanced}
          aria-controls="node-filters"
        >
          <SlidersHorizontal size={15} />
          {t("筛选")}
          {numberOfFilters ? (
            <span className="filter-count">{numberOfFilters}</span>
          ) : null}
        </Button>
        <span className="node-ip-count">
          <Globe2 size={15} />
          {nodesQuery.data
            ? nodesQuery.data.unique_egress_ips.toLocaleString()
            : "--"}{" "}
          {t("独立出口")}
        </span>
      </div>
      <div className="quality-filter-row">
        <Select aria-label={t("IP 类型")} value={filter.ip_type} onChange={event => update("ip_type", event.target.value)}>
          <option value="">{t("全部 IP 类型")}</option>
          {Object.entries(typeLabels).map(([value,label]) => <option value={value} key={value}>{t(label)}</option>)}
        </Select>
        <Select aria-label={t("连接协议")} value={filter.protocol} onChange={event => update("protocol", event.target.value)}>
          <option value="">{t("全部协议")}</option>
          {Object.entries(protocolLabels).map(([value,label]) => <option value={value} key={value}>{label}</option>)}
        </Select>
        <Select aria-label={t("质量状态")} value={filter.quality_state} onChange={event => update("quality_state", event.target.value)}>
          {[["", "全部质量状态"], ["valid", "证据齐全"], ["partial", "仅部分证据"], ["pending", "等待检测"], ["unobserved", "未检测"], ["stale", "已过期"], ["conflicting", "来源有分歧"], ["unsupported", "不支持检测"]].map(([value,label]) => <option value={value} key={value}>{t(label)}</option>)}
        </Select>
        <Select aria-label={t("纯净度分级")} value={filter.purity_band} onChange={event => update("purity_band", event.target.value)}>
          <option value="">{t("全部纯净度")}</option>
          {purityBands.map(band => <option value={band.id} key={band.id}>{band.min}–{band.max} · {t(band.label)}</option>)}
          <option value="review">{t("需要复核")}</option><option value="unknown">{t("评级未知")}</option>
        </Select>
        <span><ShieldCheck size={13} />{t("质量按出口 IP 共享")}</span>
      </div>
      <PurityGuide />
      {advanced && (
        <div className="node-filter-grid" id="node-filters">
          <label>
            {t("平台")}
            <Select
              aria-label={t("平台")}
              value={filter.platform_id}
              onChange={(event) => update("platform_id", event.target.value)}
            >
              <option value="">{t("全部平台")}</option>
              {platforms.data?.items.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </Select>
          </label>
          <label>
            {t("订阅")}
            <Select
              aria-label={t("订阅")}
              value={filter.subscription_id}
              onChange={(event) =>
                update("subscription_id", event.target.value)
              }
            >
              <option value="">{t("全部订阅")}</option>
              {subscriptions.data?.items.map((sub) => (
                <option key={sub.id} value={sub.id}>
                  {sub.name}
                </option>
              ))}
            </Select>
          </label>
          <label>
            {t("地区")}
            <Select
              aria-label={t("地区")}
              value={filter.region}
              onChange={(event) => update("region", event.target.value)}
            >
              <option value="">{t("全部地区")}</option>
              {getAllRegions().map((region) => (
                <option key={region.code} value={region.code}>
                  {region.name}
                </option>
              ))}
            </Select>
          </label>
          <label>
            {t("出口 IP")}
            <Input
              aria-label={t("出口 IP")}
              value={filter.egress_ip}
              onChange={(event) => update("egress_ip", event.target.value)}
              placeholder={t("精确出口 IP")}
            />
          </label>
          <label>{t("来源风险等级")}<Select aria-label={t("风险等级")} value={filter.risk_grade} onChange={event => update("risk_grade", event.target.value)}>
            {[["", "全部风险等级"], ["low", "较低风险"], ["moderate", "一般风险"], ["high", "较高风险"], ["severe", "严重风险"], ["review", "有滥用记录"], ["unknown", "评级未知"]].map(([value,label]) => <option value={value} key={value}>{t(label)}</option>)}
          </Select></label>
          <Button
            variant="ghost"
            onClick={() => setParams({}, { replace: true })}
          >
            <X size={14} />
            {t("清除筛选")}
          </Button>
          {(platforms.isError || subscriptions.isError) && (
            <QueryState
              error={platforms.error || subscriptions.error}
              onRetry={() => {
                void platforms.refetch();
                void subscriptions.refetch();
              }}
            />
          )}
        </div>
      )}
      {!advanced && numberOfFilters > 0 && (
        <div className="active-filter-summary">
          <span>
            {t("已应用筛选")} {numberOfFilters}
          </span>
          <Button variant="ghost" size="sm" onClick={() => setAdvanced(true)}>
            {t("查看")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setParams({}, { replace: true })}
          >
            {t("清除筛选")}
          </Button>
        </div>
      )}
      <QueryState
        loading={nodesQuery.isLoading}
        error={nodesQuery.error}
        onRetry={() => void nodesQuery.refetch()}
      />
      {!nodesQuery.isLoading && !nodesQuery.isError && nodes.length === 0 && (
        <div className="workspace-empty">
          <Network size={30} />
          <h2>
            {t(
              keyword || mode !== "all" || numberOfFilters
                ? "没有匹配的节点"
                : "还没有节点",
            )}
          </h2>
          <div className="page-actions">
            <Button
              variant="secondary"
              onClick={() => setParams({}, { replace: true })}
            >
              {t("清除筛选")}
            </Button>
            <Link className="btn btn-primary" to="/subscriptions?create=1">
              {t("添加订阅")}
            </Link>
          </div>
        </div>
      )}
      {nodes.length > 0 && (
        <div className="node-table-scroll" aria-busy={nodesQuery.isFetching}>
          <table className="workbench-table node-inventory-table">
            <colgroup><col className="node-col-identity" /><col className="node-col-exit" /><col className="node-col-purity" /><col className="node-col-signals" /><col className="node-col-latency" /><col className="node-col-actions" /></colgroup>
            <thead>
              <tr>
                <th>{sortButton("tag", "节点名称")}</th>
                <th>{sortButton("region", "出口 / 类型")}</th>
                <th>{t("纯净度")}<small>IPPure</small></th>
                <th>{t("网络特征")}<small>ProxyCheck</small></th>
                <th>{t("参考延迟")}</th>
                <th>{t("操作")}</th>
              </tr>
            </thead>
            <tbody>
              {nodes.map((node) => {
                const state = status(node);
                const pure = evidenceFor(node.quality,"ippure");
                return (
                  <tr
                    key={node.node_hash}
                    data-selected={selected === node.node_hash}
                  >
                    <td>
                      <button
                        className="node-name"
                        onClick={() => open(node.node_hash)}
                      >
                        <span className="node-glyph">
                          <Network size={16} />
                        </span>
                        <span>
                          <strong>{nameOf(node)}</strong>
                          <small>
                            <span className={"node-state node-state-" + state.variant}>{t(state.label)}</span>
                            {node.protocol && <span className="node-protocol">{protocolLabels[node.protocol] || node.protocol}</span>}
                            {node.tags[0]?.subscription_name ||
                              node.node_hash.slice(0, 12)}
                            {node.tags.length > 1 &&
                              ` +${node.tags.length - 1}`}
                          </small>
                        </span>
                      </button>
                    </td>
                    <td><div className="node-exit-cell"><strong>{node.egress_ip || "—"}</strong>
                      <span className="node-exit-meta"><IPTypeBadge summary={node.quality} />{node.region && <span className="node-region" title={getRegionName(node.region.toUpperCase())}>{node.region.toUpperCase()}</span>}</span>
                      {node.quality?.assessment?.native !== null && node.quality?.assessment?.native !== undefined && <small>{t(node.quality.assessment.native ? "原生 IP" : "广播 IP")}</small>}
                    </div></td>
                    <td><div className="node-quality-cell"><QualityBadge summary={node.quality} />
                      <small>{pure ? t("复核于") + " " + formatRelativeTime(pure.observed_at) : t("通过节点获取评分")}</small>
                    </div></td>
                    <td><div className="node-network-cell"><NetworkSignals summary={node.quality} />{node.quality?.assessment?.verdict && node.quality.assessment.verdict !== "pending" && <VerdictBadge summary={node.quality} />}</div></td>
                    <td>
                      <span
                        className={
                          state.variant === "success" ? "latency-value" : ""
                        }
                      >
                        {node.reference_latency_ms !== undefined &&
                        state.variant === "success"
                          ? Math.round(node.reference_latency_ms) + " ms"
                          : "--"}
                      </span>
                    </td>
                    <td>
                      <div className="row-actions">
                        <Button variant="ghost" size="sm" onClick={() => open(node.node_hash)} aria-label={t("查看节点详情")}>{t("详情")}<ChevronRight size={14} /></Button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {nodesQuery.data && (
        <OffsetPagination
          page={page}
          totalPages={Math.max(1, Math.ceil(nodesQuery.data.total / pageSize))}
          totalItems={nodesQuery.data.total}
          pageSize={pageSize}
          pageSizeOptions={sizes}
          onPageChange={(n) => update("page", String(n))}
          onPageSizeChange={(n) => update("size", String(n))}
          disabled={nodesQuery.isFetching}
        />
      )}
      {selected && (
        <DialogSurface title={t("节点详情")} onClose={close} variant="drawer">
          <div className="node-inspector">
            <header className="inspector-toolbar">
              <span>{t("节点详情")}</span>
              <div className="row-actions">
                <Button
                  variant="ghost"
                  className="icon-button"
                  aria-label={t("上一个节点")}
                  title={t("上一个节点")}
                  disabled={selectedIndex <= 0}
                  onClick={() =>
                    update("selected", nodes[selectedIndex - 1].node_hash)
                  }
                >
                  <ChevronLeft size={16} />
                </Button>
                <Button
                  variant="ghost"
                  className="icon-button"
                  aria-label={t("下一个节点")}
                  title={t("下一个节点")}
                  disabled={
                    selectedIndex < 0 || selectedIndex >= nodes.length - 1
                  }
                  onClick={() =>
                    update("selected", nodes[selectedIndex + 1].node_hash)
                  }
                >
                  <ChevronRight size={16} />
                </Button>
                <Button
                  variant="ghost"
                  className="icon-button"
                  aria-label={t("关闭")}
                  onClick={close}
                >
                  <X size={17} />
                </Button>
              </div>
            </header>
            <QueryState
              loading={detailQuery.isLoading && !detail}
              error={detailQuery.error}
              onRetry={() => void detailQuery.refetch()}
            />
            {detail && (
              <>
                <div className="inspector-identity">
                  <span className="object-icon">
                    <Network size={22} />
                  </span>
                  <h2>{nameOf(detail)}</h2>
                  <Badge variant={status(detail).variant}>
                    {t(status(detail).label)}
                  </Badge>
                  <code>{detail.node_hash}</code>
                </div>
                <section className="inspector-section">
                  <h3>{t("出口与健康")}</h3>
                  <dl className="detail-facts">
                    <div><dt>{t("连接协议")}</dt><dd>{protocolLabels[detail.protocol || ""] || detail.protocol || t("未知")}</dd></div>
                    <div>
                      <dt>{t("出口 IP")}</dt>
                      <dd>
                        {detail.egress_ip || "--"}
                        {detail.egress_ip && (
                          <Button
                            variant="ghost"
                            size="sm"
                            className="icon-button"
                            aria-label={t("复制出口 IP")}
                            onClick={() => void copy(detail.egress_ip!)}
                          >
                            <Copy size={13} />
                          </Button>
                        )}
                      </dd>
                    </div>
                    <div>
                      <dt>{t("地区")}</dt>
                      <dd>
                        {detail.region
                          ? getRegionName(detail.region.toUpperCase())
                          : "--"}
                      </dd>
                    </div>
                    <div>
                      <dt>{t("参考延迟")}</dt>
                      <dd>
                        {detail.reference_latency_ms === undefined
                          ? "--"
                          : Math.round(detail.reference_latency_ms) + " ms"}
                      </dd>
                    </div>
                    <div>
                      <dt>{t("连续失败")}</dt>
                      <dd>{detail.failure_count}</dd>
                    </div>
                    <div>
                      <dt>{t("出口更新")}</dt>
                      <dd>{formatDateTime(detail.last_egress_update || "")}</dd>
                    </div>
                    <div>
                      <dt>{t("创建时间")}</dt>
                      <dd>{formatDateTime(detail.created_at)}</dd>
                    </div>
                  </dl>
                  {detail.last_error && (
                    <div className="callout callout-error">
                      {detail.last_error}
                    </div>
                  )}
                </section>
                <section className="inspector-section">
                  <QualityDetails summary={detail.quality} nodeHash={detail.node_hash} nodeIP={detail.egress_ip} nodeReady={detail.has_outbound} />
                </section>
                <section className="inspector-section">
                  <h3>{t("来源与标签")}</h3>
                  {detail.tags.map((tag) => (
                    <div
                      className="source-tag"
                      key={tag.subscription_id + tag.tag}
                    >
                      <Link
                        to={`/subscriptions?selected=${tag.subscription_id}`}
                      >
                        {tag.subscription_name}
                      </Link>
                      <span>{tag.tag}</span>
                    </div>
                  ))}
                </section>
                <footer className="inspector-actions">
                  <Button disabled={qualityProbe.isPending || !detail.has_outbound || qualityStatus.data?.enabled === false}
                    onClick={() => qualityProbe.mutate(detail.node_hash)}><ShieldCheck size={15} />{t("更新网络特征")}</Button>
                  <Button
                    variant="secondary"
                    disabled={probe.isPending || !detail.has_outbound}
                    onClick={() => runProbe(detail.node_hash, "egress")}
                  >
                    <Globe2 size={15} />
                    {t("出口探测")}
                  </Button>
                  <Button
                    variant="secondary"
                    disabled={probe.isPending || !detail.has_outbound}
                    onClick={() => runProbe(detail.node_hash, "latency")}
                  >
                    <Zap size={15} />
                    {t("延迟探测")}
                  </Button>
                </footer>
              </>
            )}
          </div>
        </DialogSurface>
      )}
      </>}
      </div>
    </section>
  );
}
function RssImport() {
  return <ArrowDown size={15} />;
}
