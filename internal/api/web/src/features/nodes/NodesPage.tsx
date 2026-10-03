import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  ChevronLeft,
  ChevronRight,
  Copy,
  Globe2,
  LoaderCircle,
  Network,
  Radar,
  RefreshCw,
  Search,
  ShieldCheck,
  SlidersHorizontal,
  X,
  Zap,
} from "lucide-react";
import { useState, type ReactNode } from "react";
import {
  Link,
  useLocation,
  useNavigate,
  useSearchParams,
} from "react-router-dom";
import { Badge, type BadgeProps } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import {
  Panel,
  PanelBody,
  PanelFooter,
  PanelHeader,
  PanelToolbar,
  SectionTitle,
} from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import { Sheet } from "../../components/ui/Sheet";
import { TBody, TD, TDClip, TDNum, TH, THead, TR, Table, TableWrap } from "../../components/ui/Table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../../components/ui/Tabs";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime } from "../../lib/time";
import { listPlatforms } from "../platforms/api";
import { listSubscriptions } from "../subscriptions/api";
import { createIntelJob } from "../jobs/api";
import type { IntelJobScope } from "../jobs/types";
import { getNode, listNodes, probeEgress, probeLatency } from "./api";
import { buildBulkIntelScope, hasUnsupportedFilters } from "./intelScope";
import { getAllRegions, getRegionName } from "./regions";
import type { NodeListQuery, NodeSummary, NodeSortBy } from "./types";
import { getQualityStatus, inspectNode, qualityPollingInterval } from "./quality/api";
import { NetworkSignals, QualityDetails, VerdictBadge } from "./quality/QualityDetails";
import { ExitRecordsPanel } from "./quality/ExitRecordsPanel";
import { PurityGuide } from "./quality/PurityGuide";
import { purityBands, typeLabels } from "./quality/presentation";
import { NodeIntelCell, NodeIntelPanel, NodeLabels } from "./NodeIntel";

type Tone = NonNullable<BadgeProps["tone"]>;

// The kit's select still carries a pre-Tailwind class name, so each call site
// supplies the field treatment from the design tokens.

function status(node: NodeSummary): { label: string; tone: Tone } {
  if (!node.enabled) return { label: "禁用", tone: "neutral" };
  if (!node.has_outbound) return { label: "错误", tone: "alert" };
  if (node.circuit_open_since)
    return {
      label: node.failure_count === 0 ? "待测" : "熔断",
      tone: "warn",
    };
  return { label: "健康", tone: "signal" };
}
function nameOf(node: NodeSummary) {
  return node.display_tag || node.tags[0]?.tag || node.node_hash.slice(0, 12);
}
const sizes = [20, 50, 100, 200] as const;
const protocolLabels: Record<string, string> = { shadowsocks: "Shadowsocks", vmess: "VMess", vless: "VLESS", trojan: "Trojan", hysteria: "Hysteria", hysteria2: "Hysteria 2", tuic: "TUIC", wireguard: "WireGuard", shadowtls: "ShadowTLS", socks: "SOCKS", http: "HTTP", ssh: "SSH", anytls: "AnyTLS", direct: "Direct" };
// Sort keys offered by the sort select. purity_score, latency and assessed_at
// are the WP10 §4 keys; the backend rejects anything else.
const sorts = ["tag", "created_at", "failure_count", "region", "purity_score", "latency", "assessed_at"] as const;
function integer(value: string | null, fallback: number) {
  const n = Number(value);
  return value !== null && Number.isSafeInteger(n) && n >= 0 ? n : fallback;
}

function formatCount(value: number | null | undefined) {
  return value == null ? "--" : value.toLocaleString();
}

// optionalInteger reads a numeric filter input. An empty or invalid value means
// "no filter" and is left out of the request entirely.
function optionalInteger(value: string): number | undefined {
  if (!value.trim()) return undefined;
  const n = Number(value);
  return Number.isSafeInteger(n) && n >= 0 ? n : undefined;
}

/** One fact of the drawer: quiet label left, value right, closed by a hairline. */
function Fact({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-b border-rule py-1.5">
      <dt className="label shrink-0">{label}</dt>
      <dd className="flex min-w-0 flex-wrap items-center justify-end gap-1.5 text-sm">
        {children}
      </dd>
    </div>
  );
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
    purity_min: params.get("purity_min") || "",
    purity_max: params.get("purity_max") || "",
    verdict: params.get("verdict") || "",
    confidence_min: params.get("confidence_min") || "",
    native: params.get("native") || "",
    asn: params.get("asn") || "",
    country: params.get("country") || "",
    check: params.get("check") || "",
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
  // The URL keeps the raw text of every filter (inputs bind to it); the API
  // query converts the intel filters to their typed form.
  const nodeQuery: NodeListQuery = {
    ...filter,
    purity_min: optionalInteger(filter.purity_min),
    purity_max: optionalInteger(filter.purity_max),
    asn: optionalInteger(filter.asn),
    native: filter.native === "true" ? true : filter.native === "false" ? false : undefined,
    verdict: filter.verdict || undefined,
    confidence_min: filter.confidence_min || undefined,
    country: filter.country || undefined,
    check: params.getAll("check").map((value) => value.trim()).filter(Boolean),
  };
  const qualityStatus = useQuery({
    queryKey: ["quality", "status"],
    queryFn: getQualityStatus,
    refetchInterval: query => qualityPollingInterval(query.state.data),
  });
  const nodesQuery = useQuery({
    queryKey: ["nodes", filter],
    queryFn: ({ signal }) => listNodes(nodeQuery, signal),
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

  // Bulk intel entry point of the node pool: the
  // current URL filters become the job scope (intelScope.ts) and every run is
  // limited to healthy nodes. `total` counts the filtered list, so the button
  // shows an upper bound of the nodes the job will cover.
  const bulkCount = nodesQuery.data?.total ?? 0;
  const bulkScopeBlocked = mode === "error" || mode === "disabled" || mode === "circuit_open";
  const bulkScopeWider = hasUnsupportedFilters(params);
  const [bulkJobID, setBulkJobID] = useState("");
  const bulkJob = useMutation({
    mutationFn: (request: { scope: IntelJobScope; count: number }) =>
      createIntelJob({ kind: "full", scope: request.scope, force: true }),
    onSuccess: (response, request) => {
      void queryClient.invalidateQueries({ queryKey: ["intel-jobs"] });
      setBulkJobID(response.job.id);
      showToast("success", t("已创建情报任务（预计 {{count}} 个节点）", { count: request.count }));
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
    "purity_min",
    "purity_max",
    "verdict",
    "confidence_min",
    "native",
    "asn",
    "country",
    "check",
  ].filter((key) => params.get(key)).length;
  const sortButton = (key: NodeSortBy, label: string) => (
    <Button
      type="button"
      variant="quiet"
      size="sm"
      onClick={() => changeSort(key)}
      className="px-0 text-xs font-medium text-ink-soft"
    >
      {t(label)}
      {sort !== key ? (
        <ArrowUpDown size={12} aria-hidden className="text-ink-faint" />
      ) : order === "asc" ? (
        <ArrowUp size={12} aria-hidden className="text-signal-deep" />
      ) : (
        <ArrowDown size={12} aria-hidden className="text-signal-deep" />
      )}
    </Button>
  );
  const sortableTH = (key: NodeSortBy, label: string, hint?: string) => (
    <TH
      className="w-auto"
      aria-sort={
        sort === key ? (order === "asc" ? "ascending" : "descending") : undefined
      }
    >
      {sortButton(key, label)}
      {hint && <span className="ml-1 text-2xs font-normal text-ink-faint">{hint}</span>}
    </TH>
  );
  const currentPage = Math.min(page, Math.max(0, Math.ceil((nodesQuery.data?.total ?? 0) / pageSize) - 1));
  const totalPages = Math.max(1, Math.ceil((nodesQuery.data?.total ?? 0) / pageSize));
  const jumpToPage = (raw: string) => {
    const value = Number(raw);
    if (Number.isInteger(value) && value > 0)
      update("page", String(Math.max(0, Math.min(totalPages - 1, value - 1))));
  };
  return (
    <Page bleed>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <Tabs
        className="flex min-h-full min-w-0 flex-1 flex-col"
        value={view}
        onValueChange={(next) => changeView(next === "exits" ? "exits" : "nodes")}
      >
        <PageHeader
          title={t("节点池")}
          description={t("按线路查看健康，按出口查看质量。")}
          meta={
            <>
              <PageMeta
                label={t("节点总数")}
                value={formatCount(nodesQuery.data?.total)}
              />
              <PageMeta
                label={t("独立出口")}
                value={formatCount(nodesQuery.data?.unique_egress_ips)}
              />
            </>
          }
          actions={
            <>
              <Button
                variant="ghost"
                size="icon"
                title={t("刷新")}
                aria-label={t("刷新")}
                disabled={view === "nodes" && nodesQuery.isFetching}
                onClick={refresh}
              >
                <RefreshCw
                  size={16}
                  className={nodesQuery.isFetching ? "animate-spin" : undefined}
                />
              </Button>
              <Button asChild variant="secondary">
                <Link to="/subscriptions?create=1">{t("导入节点")}</Link>
              </Button>
            </>
          }
          tabs={
            <TabsList>
              <TabsTrigger value="nodes">
                <span className="inline-flex items-center gap-1.5">
                  <Network size={14} aria-hidden />
                  {t("节点线路")}
                </span>
              </TabsTrigger>
              <TabsTrigger value="exits">
                <span className="inline-flex items-center gap-1.5">
                  <Globe2 size={14} aria-hidden />
                  {t("出口记录")}
                </span>
              </TabsTrigger>
            </TabsList>
          }
        />

        <TabsContent
          value="exits"
          className="page-content page-content--fill"
        >
          <ExitRecordsPanel />
        </TabsContent>

        <TabsContent
          value="nodes"
          className="page-content page-content--fill"
        >
          <PurityGuide />

          {/* 列表自身的筛选与分页都归这个 Panel：工具条在上，表格与
              空/错误状态在面板体内，分页落在 PanelFooter。 */}
          <Panel className="flex min-w-0 flex-col">
            <PanelHeader title={t("节点线路")} />
            <PanelToolbar className="filter-toolbar">
            <div className="relative w-full min-w-48 sm:w-64">
              <Search
                size={14}
                aria-hidden
                className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-ink-faint"
              />
              <Input
                aria-label={t("搜索节点")}
                placeholder={t("搜索节点名称或标签")}
                className="pl-8 pr-8"
                value={keyword}
                onChange={(event) => update("tag_keyword", event.target.value)}
              />
              {keyword && (
                <Button
                  type="button"
                  variant="quiet"
                  size="icon"
                  aria-label={t("清除搜索")}
                  className="absolute top-1/2 right-1 -translate-y-1/2 text-ink-faint"
                  onClick={() => update("tag_keyword", "")}
                >
                  <X size={14} aria-hidden />
                </Button>
              )}
            </div>
            <Select
              className="w-full sm:w-40"
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
              {numberOfFilters ? <Badge tone="outline">{numberOfFilters}</Badge> : null}
            </Button>
            <Button
              variant="secondary"
              aria-label={t("批量拉取情报")}
              title={t(bulkScopeBlocked
                ? "批量拉取只对健康节点生效，请先切换状态筛选。"
                : "对当前筛选结果中健康的节点批量拉取情报。")}
              disabled={bulkJob.isPending || bulkScopeBlocked || bulkCount === 0}
              onClick={() => bulkJob.mutate({ scope: buildBulkIntelScope(params), count: bulkCount })}
            >
              {bulkJob.isPending ? (
                <LoaderCircle size={15} className="animate-spin" />
              ) : (
                <Radar size={15} />
              )}
              <span>{t("批量拉取情报")}</span>
              <span className="readout text-2xs text-ink-faint">
                {bulkCount.toLocaleString()}
              </span>
            </Button>
            {bulkScopeWider && !bulkScopeBlocked ? (
              <span className="max-w-[40ch] text-xs leading-relaxed text-warn">
                {t("部分筛选条件不适用于批量拉取，实际范围可能更大。")}
              </span>
            ) : null}
            {bulkJobID ? (
              <Button asChild variant="ghost" size="sm">
                <Link to="/jobs">{t("查看检测任务")}</Link>
              </Button>
            ) : null}
            </PanelToolbar>

            <PanelToolbar className="filter-toolbar filter-toolbar--advanced">
            <Select
              className="w-full sm:w-40"
              aria-label={t("IP 类型")}
              value={filter.ip_type}
              onChange={event => update("ip_type", event.target.value)}
            >
              <option value="">{t("全部 IP 类型")}</option>
              {Object.entries(typeLabels).map(([value, label]) => (
                <option value={value} key={value}>{t(label)}</option>
              ))}
            </Select>
            <Select
              className="w-full sm:w-40"
              aria-label={t("连接协议")}
              value={filter.protocol}
              onChange={event => update("protocol", event.target.value)}
            >
              <option value="">{t("全部协议")}</option>
              {Object.entries(protocolLabels).map(([value, label]) => (
                <option value={value} key={value}>{label}</option>
              ))}
            </Select>
            <Select
              className="w-full sm:w-40"
              aria-label={t("质量状态")}
              value={filter.quality_state}
              onChange={event => update("quality_state", event.target.value)}
            >
              {[["", "全部质量状态"], ["valid", "证据齐全"], ["partial", "仅部分证据"], ["pending", "等待检测"], ["unobserved", "未检测"], ["stale", "已过期"], ["conflicting", "来源有分歧"], ["unsupported", "不支持检测"]].map(([value, label]) => (
                <option value={value} key={value}>{t(label)}</option>
              ))}
            </Select>
            <Select
              className="w-full sm:w-40"
              aria-label={t("纯净度分级")}
              value={filter.purity_band}
              onChange={event => update("purity_band", event.target.value)}
            >
              <option value="">{t("全部纯净度")}</option>
              {purityBands.map(band => (
                <option value={band.id} key={band.id}>{band.min}–{band.max} {t(band.label)}</option>
              ))}
              <option value="review">{t("需要复核")}</option>
              <option value="unknown">{t("评级未知")}</option>
            </Select>
            <Select
              className="w-full sm:w-40"
              aria-label={t("排序")}
              value={sort}
              onChange={event => update("sort", event.target.value)}
            >
              {[
                ["tag", "节点名称"],
                ["created_at", "创建时间"],
                ["failure_count", "连续失败"],
                ["region", "地区"],
                ["purity_score", "纯净度评分"],
                ["latency", "参考延迟"],
                ["assessed_at", "评估时间"],
              ].map(([value, label]) => <option value={value} key={value}>{t(label)}</option>)}
            </Select>
            <Select
              className="w-full sm:w-40"
              aria-label={t("排序方向")}
              value={order}
              onChange={event => update("order", event.target.value)}
            >
              <option value="asc">{t("升序")}</option>
              <option value="desc">{t("降序")}</option>
            </Select>
            <span className="flex items-center gap-1 text-xs text-ink-faint">
              <ShieldCheck size={13} aria-hidden />
              {t("质量按出口 IP 共享")}
            </span>
            </PanelToolbar>

            {advanced && (
            <div
              className="grid grid-cols-1 gap-x-4 gap-y-3 border-b border-rule-faint px-4 py-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"
              id="node-filters"
            >
              <Fieldset label={t("平台")}>
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
              </Fieldset>
              <Fieldset label={t("订阅")}>
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
              </Fieldset>
              <Fieldset label={t("地区")}>
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
              </Fieldset>
              <Fieldset label={t("出口 IP")}>
                <Input
                  aria-label={t("出口 IP")}
                  className="readout w-full"
                  value={filter.egress_ip}
                  onChange={(event) => update("egress_ip", event.target.value)}
                  placeholder={t("精确出口 IP")}
                />
              </Fieldset>
              <Fieldset label={t("来源风险等级")}>
                <Select
                  aria-label={t("风险等级")}
                  value={filter.risk_grade}
                  onChange={event => update("risk_grade", event.target.value)}
                >
                  {[["", "全部风险等级"], ["low", "较低风险"], ["moderate", "一般风险"], ["high", "较高风险"], ["severe", "严重风险"], ["review", "有滥用记录"], ["unknown", "评级未知"]].map(([value, label]) => (
                    <option value={value} key={value}>{t(label)}</option>
                  ))}
                </Select>
              </Fieldset>
              <Fieldset label={t("最低纯净度")}>
                <Input
                  aria-label={t("最低纯净度")}
                  className="readout w-full"
                  type="number"
                  min={0}
                  max={100}
                  value={filter.purity_min}
                  onChange={(event) => update("purity_min", event.target.value)}
                  placeholder="0"
                />
              </Fieldset>
              <Fieldset label={t("最高纯净度")}>
                <Input
                  aria-label={t("最高纯净度")}
                  className="readout w-full"
                  type="number"
                  min={0}
                  max={100}
                  value={filter.purity_max}
                  onChange={(event) => update("purity_max", event.target.value)}
                  placeholder="100"
                />
              </Fieldset>
              <Fieldset label={t("判定")}>
                <Select
                  aria-label={t("判定")}
                  value={filter.verdict}
                  onChange={event => update("verdict", event.target.value)}
                >
                  <option value="">{t("全部判定")}</option>
                  {[
                    ["favorable", "未见明显风险"],
                    ["caution", "谨慎使用"],
                    ["incomplete", "特征未齐"],
                    ["review", "需要复核"],
                    ["conflicting", "类型有分歧"],
                    ["high_risk", "风险较高"],
                    ["pending", "等待评估"],
                  ].map(([value, label]) => <option value={value} key={value}>{t(label)}</option>)}
                </Select>
              </Fieldset>
              <Fieldset label={t("最低置信度")}>
                <Select
                  aria-label={t("最低置信度")}
                  value={filter.confidence_min}
                  onChange={event => update("confidence_min", event.target.value)}
                >
                  <option value="">{t("全部置信度")}</option>
                  {[["low", "置信度低"], ["medium", "置信度中"], ["high", "置信度高"]].map(([value, label]) => <option value={value} key={value}>{t(label)}</option>)}
                </Select>
              </Fieldset>
              <Fieldset label={t("原生 IP")}>
                <Select
                  aria-label={t("原生 IP")}
                  value={filter.native}
                  onChange={event => update("native", event.target.value)}
                >
                  <option value="">{t("全部原生类型")}</option>
                  <option value="true">{t("原生 IP")}</option>
                  <option value="false">{t("广播 IP")}</option>
                </Select>
              </Fieldset>
              <Fieldset label="ASN">
                <Input
                  aria-label="ASN"
                  className="readout w-full"
                  value={filter.asn}
                  onChange={(event) => update("asn", event.target.value)}
                  placeholder="13335"
                />
              </Fieldset>
              <Fieldset label={t("国家 / 地区")}>
                <Input
                  aria-label={t("国家 / 地区")}
                  className="readout w-full"
                  value={filter.country}
                  onChange={(event) => update("country", event.target.value)}
                  placeholder="JP"
                />
              </Fieldset>
              <Fieldset label={t("检测结果")}>
                <Input
                  aria-label={t("检测结果")}
                  className="w-full"
                  value={filter.check}
                  onChange={(event) => update("check", event.target.value)}
                  placeholder={t("格式 检测项:结果，如 chatgpt:available")}
                />
              </Fieldset>
              {/* The reset is the panel's own action, not a condition, so it takes the
                  grid's trailing row rather than a field's cell. */}
              <div className="flex items-center justify-end sm:col-span-2 xl:col-span-3 2xl:col-span-4">
                <Button
                  variant="ghost"
                  onClick={() => setParams({}, { replace: true })}
                >
                  <X size={14} />
                  {t("清除筛选")}
                </Button>
              </div>
              {(platforms.isError || subscriptions.isError) && (
                <div className="sm:col-span-2 xl:col-span-3 2xl:col-span-4">
                  <ErrorState
                    message={t("数据暂时不可用")}
                    onRetry={() => {
                      void platforms.refetch();
                      void subscriptions.refetch();
                    }}
                  />
                </div>
              )}
            </div>
          )}

          {!advanced && numberOfFilters > 0 && (
            <div className="flex flex-wrap items-center gap-2 border-b border-rule-faint px-4 py-1.5 text-xs text-ink-soft">
              <span>
                {t("已应用筛选")} <span className="readout">{numberOfFilters}</span>
              </span>
              <Button variant="quiet" size="sm" onClick={() => setAdvanced(true)}>
                {t("查看")}
              </Button>
              <Button
                variant="quiet"
                size="sm"
                onClick={() => setParams({}, { replace: true })}
              >
                {t("清除筛选")}
              </Button>
            </div>
          )}

          {nodesQuery.isLoading && (
            <PanelBody>
              <LoadingState />
            </PanelBody>
          )}
          {nodesQuery.isError && (
            <PanelBody>
              <ErrorState
                message={t("数据暂时不可用")}
                onRetry={() => void nodesQuery.refetch()}
              />
            </PanelBody>
          )}
          {!nodesQuery.isLoading && !nodesQuery.isError && nodes.length === 0 && (
            <PanelBody>
              <EmptyState
                title={t(
                  keyword || mode !== "all" || numberOfFilters
                    ? "没有匹配的节点"
                    : "还没有节点",
                )}
                action={
                  <div className="flex flex-wrap items-center justify-center gap-2">
                    <Button
                      variant="secondary"
                      onClick={() => setParams({}, { replace: true })}
                    >
                      {t("清除筛选")}
                    </Button>
                    <Button asChild>
                      <Link to="/subscriptions?create=1">{t("添加订阅")}</Link>
                    </Button>
                  </div>
                }
              />
            </PanelBody>
          )}

          {nodes.length > 0 && (
            <TableWrap aria-busy={nodesQuery.isFetching}>
              <Table className="min-w-[920px]">
                <THead>
                  <TR>
                    {sortableTH("tag", "节点名称")}
                    <TH>{t("状态")}</TH>
                    <TH>{t("出口 IP")}</TH>
                    {sortableTH("region", "地区 / 网络类型")}
                    {sortableTH("purity_score", "纯净度", "prism-purity-v2")}
                    <TH className="w-auto">
                      {t("网络特征")}
                      <span className="ml-1 text-2xs font-normal text-ink-faint">ProxyCheck</span>
                    </TH>
                    {sortableTH("latency", "参考延迟")}
                    <TH className="w-24 text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {nodes.map((node) => {
                    const state = status(node);
                    const inventory = protocolLabels[node.protocol || ""];
                    // 每个节点只占一行：协议、来源订阅与短哈希退到 title，状态单独成列，
                    // 这样再长的标签也撑不高行高。
                    const subtitle = [
                      inventory,
                      node.tags[0]?.subscription_name ?? node.node_hash.slice(0, 12),
                      node.tags.length > 1 ? `+${node.tags.length - 1}` : "",
                    ]
                      .filter(Boolean)
                      .join(" · ");
                    const native = node.quality?.assessment?.native;
                    const nativeLabel =
                      native === null || native === undefined
                        ? ""
                        : t(native ? "原生 IP" : "广播 IP");
                    return (
                      <TR key={node.node_hash} selected={selected === node.node_hash}>
                        <TDClip title={subtitle}>
                          <Button
                            type="button"
                            variant="quiet"
                            className="h-auto w-full min-w-0 justify-start gap-2 p-0 text-left"
                            onClick={() => open(node.node_hash)}
                          >
                            <Network size={14} aria-hidden className="shrink-0 text-ink-faint" />
                            <span className="truncate font-medium text-ink">{nameOf(node)}</span>
                          </Button>
                        </TDClip>
                        <TD>
                          <Badge tone={state.tone} dot>
                            {t(state.label)}
                          </Badge>
                        </TD>
                        <TDClip title={nativeLabel || undefined}>
                          <span className="readout truncate font-medium text-ink">
                            {node.egress_ip || "—"}
                          </span>
                        </TDClip>
                        <TDClip>
                          {/* 地区与网络类型同格：地点是国家码加城市（无城市时用机房代号），
                              类型用 Badge。整格强制单行，行高才是一个 token 的高度。 */}
                          <NodeLabels
                            intel={node.intel}
                            region={node.region}
                            quality={node.quality}
                          />
                        </TDClip>
                        <TDClip>
                          <NodeIntelCell intel={node.intel} />
                        </TDClip>
                        <TDClip>
                          {/* 两个徽标组件各自 flex-wrap；这里强制单行，行高才是一个 token。 */}
                          <span className="flex min-w-0 items-center gap-1.5 [&>span]:flex-nowrap">
                            <NetworkSignals summary={node.quality} />
                            {node.quality?.assessment?.verdict &&
                              node.quality.assessment.verdict !== "pending" && (
                                <VerdictBadge summary={node.quality} />
                              )}
                          </span>
                        </TDClip>
                        <TDNum>
                          {node.reference_latency_ms !== undefined && state.tone === "signal" ? (
                            <span className="readout">
                              {Math.round(node.reference_latency_ms) + " ms"}
                            </span>
                          ) : (
                            <span className="text-ink-faint">--</span>
                          )}
                        </TDNum>
                        <TD className="text-right">
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => open(node.node_hash)}
                            aria-label={t("查看节点详情")}
                          >
                            {t("详情")}
                          </Button>
                        </TD>
                      </TR>
                    );
                  })}
                </TBody>
              </Table>
            </TableWrap>
          )}

          {nodesQuery.data && (
            <PanelFooter className="justify-between">
              <p className="readout text-xs text-ink-soft">
                {t("第 {{page}} / {{pages}} 页 · 显示 {{start}}-{{end}} / {{total}}", {
                  page: currentPage + 1,
                  pages: totalPages,
                  start: nodesQuery.data.total ? currentPage * pageSize + 1 : 0,
                  end: Math.min((currentPage + 1) * pageSize, nodesQuery.data.total),
                  total: nodesQuery.data.total,
                })}
              </p>
              <div className="flex flex-wrap items-center gap-2">
                <label className="flex items-center gap-1.5 text-xs text-ink-soft">
                  <span>{t("每页")}</span>
                  <Select
                    value={pageSize}
                    disabled={nodesQuery.isFetching}
                    aria-label={t("每页")}
                    onChange={(event) => update("size", event.target.value)}
                  >
                    {sizes.map((size) => (
                      <option key={size} value={size}>
                        {size}
                      </option>
                    ))}
                  </Select>
                </label>
                <label className="flex items-center gap-1.5 text-xs text-ink-soft">
                  <span>{t("跳至")}</span>
                  <Input
                    key={currentPage}
                    className="readout w-16 text-center"
                    type="number"
                    inputMode="numeric"
                    min={1}
                    max={totalPages}
                    defaultValue={currentPage + 1}
                    aria-label={t("选择页码")}
                    disabled={nodesQuery.isFetching}
                    onKeyDown={(event) => {
                      if (event.key === "Enter") jumpToPage(event.currentTarget.value);
                    }}
                    onBlur={(event) => jumpToPage(event.currentTarget.value)}
                  />
                </label>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={t("上一页")}
                  title={t("上一页")}
                  disabled={nodesQuery.isFetching || currentPage === 0}
                  onClick={() => update("page", String(currentPage - 1))}
                >
                  <ChevronLeft size={16} />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={t("下一页")}
                  title={t("下一页")}
                  disabled={nodesQuery.isFetching || currentPage >= totalPages - 1}
                  onClick={() => update("page", String(currentPage + 1))}
                >
                  <ChevronRight size={16} />
                </Button>
              </div>
            </PanelFooter>
          )}
          </Panel>
        </TabsContent>
      </Tabs>

      {selected && (
        <Sheet
          open
          onOpenChange={(open) => {
            if (!open) close();
          }}
          title={t("节点详情")}
          description={
            <span className="readout">
              {detail ? nameOf(detail) : selected.slice(0, 12)}
            </span>
          }
          width="lg"
          footer={
            detail ? (
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-1">
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={t("上一个节点")}
                    title={t("上一个节点")}
                    disabled={selectedIndex <= 0}
                    onClick={() => update("selected", nodes[selectedIndex - 1].node_hash)}
                  >
                    <ChevronLeft size={16} />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={t("下一个节点")}
                    title={t("下一个节点")}
                    disabled={selectedIndex < 0 || selectedIndex >= nodes.length - 1}
                    onClick={() => update("selected", nodes[selectedIndex + 1].node_hash)}
                  >
                    <ChevronRight size={16} />
                  </Button>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    disabled={
                      qualityProbe.isPending ||
                      !detail.has_outbound ||
                      qualityStatus.data?.enabled === false
                    }
                    onClick={() => qualityProbe.mutate(detail.node_hash)}
                  >
                    {qualityProbe.isPending ? (
                      <LoaderCircle size={15} className="animate-spin" />
                    ) : (
                      <ShieldCheck size={15} />
                    )}
                    {t("更新网络特征")}
                  </Button>
                  <Button
                    variant="secondary"
                    disabled={probe.isPending || !detail.has_outbound}
                    onClick={() => runProbe(detail.node_hash, "egress")}
                  >
                    {probe.isPending && probe.variables?.kind === "egress" ? (
                      <LoaderCircle size={15} className="animate-spin" />
                    ) : (
                      <Globe2 size={15} />
                    )}
                    {t("出口探测")}
                  </Button>
                  <Button
                    variant="secondary"
                    disabled={probe.isPending || !detail.has_outbound}
                    onClick={() => runProbe(detail.node_hash, "latency")}
                  >
                    {probe.isPending && probe.variables?.kind === "latency" ? (
                      <LoaderCircle size={15} className="animate-spin" />
                    ) : (
                      <Zap size={15} />
                    )}
                    {t("延迟探测")}
                  </Button>
                </div>
              </div>
            ) : undefined
          }
        >
          <div className="space-y-4">
            {detailQuery.isLoading && !detail && <LoadingState />}
            {detailQuery.isError && (
              <ErrorState
                message={t("数据暂时不可用")}
                onRetry={() => void detailQuery.refetch()}
              />
            )}
            {detail && (
              <>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-rule pb-3">
                  <span
                    aria-hidden
                    className="grid size-8 shrink-0 place-items-center rounded-control border border-rule text-ink-soft"
                  >
                    <Network size={16} />
                  </span>
                  <h2 className="text-lg font-semibold">{nameOf(detail)}</h2>
                  <Badge tone={status(detail).tone} dot>
                    {t(status(detail).label)}
                  </Badge>
                  <code className="readout ml-auto text-2xs text-ink-faint">
                    {detail.node_hash}
                  </code>
                </div>

                <section className="space-y-2">
                  <SectionTitle>{t("出口与健康")}</SectionTitle>
                  <dl className="grid gap-x-8 sm:grid-cols-2">
                    <Fact label={t("连接协议")}>
                      <span>
                        {protocolLabels[detail.protocol || ""] ||
                          detail.protocol ||
                          t("未知")}
                      </span>
                    </Fact>
                    <Fact label={t("出口 IP")}>
                      <span className="readout">{detail.egress_ip || "--"}</span>
                      {detail.egress_ip && (
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label={t("复制出口 IP")}
                          title={t("复制出口 IP")}
                          onClick={() => void copy(detail.egress_ip!)}
                        >
                          <Copy size={13} />
                        </Button>
                      )}
                    </Fact>
                    <Fact label={t("地区")}>
                      <span>
                        {detail.region
                          ? getRegionName(detail.region.toUpperCase())
                          : "--"}
                      </span>
                    </Fact>
                    <Fact label={t("参考延迟")}>
                      <span className="readout">
                        {detail.reference_latency_ms === undefined
                          ? "--"
                          : Math.round(detail.reference_latency_ms) + " ms"}
                      </span>
                    </Fact>
                    <Fact label={t("连续失败")}>
                      <span className="readout">{detail.failure_count}</span>
                    </Fact>
                    <Fact label={t("出口更新")}>
                      <span className="readout">
                        {formatDateTime(detail.last_egress_update || "")}
                      </span>
                    </Fact>
                    <Fact label={t("创建时间")}>
                      <span className="readout">{formatDateTime(detail.created_at)}</span>
                    </Fact>
                  </dl>
                  {detail.last_error && <ErrorState message={detail.last_error} />}
                </section>

                <section className="border-t border-rule pt-4">
                  <QualityDetails
                    summary={detail.quality}
                    nodeHash={detail.node_hash}
                    nodeIP={detail.egress_ip}
                    nodeReady={detail.has_outbound}
                  />
                </section>

                <section className="space-y-3 border-t border-rule pt-4">
                  <SectionTitle>{t("纯净度评估")}</SectionTitle>
                  <NodeIntelPanel
                    intel={detail.intel}
                    nodeHash={detail.node_hash}
                    ready={detail.has_outbound}
                    notify={showToast}
                  />
                </section>

                <section className="space-y-2 border-t border-rule pt-4">
                  <SectionTitle>{t("来源与标签")}</SectionTitle>
                  {detail.tags.length > 0 && (
                    <div className="divide-y divide-rule border-y border-rule">
                      {detail.tags.map((tag) => (
                        <div
                          className="flex flex-wrap items-center justify-between gap-3 py-2"
                          key={tag.subscription_id + tag.tag}
                        >
                          <Link to={`/subscriptions?selected=${tag.subscription_id}`}>
                            {tag.subscription_name}
                          </Link>
                          <span className="text-sm text-ink-soft">{tag.tag}</span>
                        </div>
                      ))}
                    </div>
                  )}
                </section>
              </>
            )}
          </div>
        </Sheet>
      )}
    </Page>
  );
}