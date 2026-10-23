import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowUpDown,
  ChevronLeft,
  ChevronRight,
  Copy,
  Download,
  ExternalLink,
  Globe2,
  LoaderCircle,
  Network,
  Radar,
  RefreshCw,
  ShieldCheck,
  X,
  Zap,
} from "lucide-react";
import { Fragment, useMemo, useRef, useState, type ReactNode } from "react";
import {
  Link,
  useLocation,
  useNavigate,
  useSearchParams,
} from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Input } from "../../components/ui/Input";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuTrigger } from "../../components/ui/Menu";
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
import { Table, TableWrap, TBody, TD, TDClip, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../../components/ui/Tabs";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { opsPaths } from "../../lib/paths";
import { formatDateTime, formatRelativeTime } from "../../lib/time";
import { listPlatforms } from "../platforms/api";
import { createIntelJob } from "../jobs/api";
import type { IntelJobScope } from "../jobs/types";
import {
  exportNodes,
  getNode,
  listNodes,
  NODE_EXPORT_FORMATS,
  probeEgress,
  probeLatency,
  type NodeExportFormat,
} from "./api";
import { buildBulkIntelScope, hasUnsupportedFilters } from "./intelScope";
import { getAllRegions, getRegionName } from "./regions";
import type { NodeListQuery, NodeSummary, NodeSortBy } from "./types";
import { getQualityStatus, inspectNode, qualityPollingInterval } from "./quality/api";
import { QualityDetails } from "./quality/QualityDetails";
import { ExitRecordsPanel } from "./quality/ExitRecordsPanel";
import { purityBands } from "./quality/presentation";
import { NodeIntelPanel } from "./NodeIntel";

/*
 * The node pool list, rebuilt on the Signal Console rhythm: page header, one
 * panel, a toolbar of the four filters an operator actually drives (health,
 * platform, country, refresh), a single data grid whose headers own the sort,
 * and a footer that carries the counts and the pager. The drawer keeps the
 * per-node work (probes, quality, intel) it always had.
 */

type HealthTone = "neutral" | "signal" | "warn" | "alert";

/**
 * Health is one verdict in four words: the dot carries the hue, the word
 * carries the meaning. `circuit_open_since` covers both a pending probe and an
 * open breaker — the node still routes, just not cleanly, so it is degraded
 * rather than critical; only a node without a usable outbound is critical.
 */
function status(node: NodeSummary): { label: string; tone: HealthTone } {
  if (!node.enabled) return { label: "禁用", tone: "neutral" };
  if (!node.has_outbound) return { label: "危急", tone: "alert" };
  if (node.circuit_open_since) return { label: "降级", tone: "warn" };
  return { label: "健康", tone: "signal" };
}

function healthDotClass(tone: HealthTone): string {
  return tone === "neutral" ? "state-dot" : `state-dot state-dot--${tone}`;
}

function nameOf(node: NodeSummary) {
  return node.display_tag || node.tags[0]?.tag || node.node_hash.slice(0, 12);
}
const sizes = [20, 50, 100, 200] as const;
const protocolLabels: Record<string, string> = { shadowsocks: "Shadowsocks", vmess: "VMess", vless: "VLESS", trojan: "Trojan", hysteria: "Hysteria", hysteria2: "Hysteria 2", tuic: "TUIC", wireguard: "WireGuard", shadowtls: "ShadowTLS", socks: "SOCKS", http: "HTTP", ssh: "SSH", anytls: "AnyTLS", direct: "Direct" };
// The sort keys the backend accepts; every one of them is a table column, so
// the header is the only sort control on the page.
const sorts = ["tag", "created_at", "failure_count", "region", "purity_score", "latency", "assessed_at"] as const;
function integer(value: string | null, fallback: number) {
  const n = Number(value);
  return value !== null && Number.isSafeInteger(n) && n >= 0 ? n : fallback;
}

// Export formats in menu order: client configs first, then analysis files.
const EXPORT_FORMAT_LABELS: Record<NodeExportFormat, { label: string; hint: string }> = {
  singbox: { label: "sing-box", hint: "JSON" },
  mihomo: { label: "mihomo / Clash Meta", hint: "YAML" },
  v2rayn: { label: "v2rayN", hint: "Base64" },
  uri: { label: "分享链接", hint: "URI" },
  proxy: { label: "代理地址", hint: "HTTP / SOCKS5" },
  csv: { label: "表格", hint: "CSV" },
  json: { label: "分析数据", hint: "JSON" },
};

function downloadBlob(blob: Blob, fileName: string) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = fileName;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
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

/**
 * The most countries the open menu ever renders. The picker is searchable, so
 * the toolbar never mounts the full country list: an empty query shows a hint,
 * a typed query shows the first matches only.
 */
const COUNTRY_MATCH_LIMIT = 60;

/**
 * The country filter: a searchable dropdown composed from the kit's menu and
 * input. Typing stays in the input because the printable keystrokes are kept
 * away from the menu's own typeahead, which would otherwise yank focus to a
 * matching item mid-word; Enter picks the first match, items are clickable.
 */
function CountryPicker({ value, onChange }: { value: string; onChange: (code: string) => void }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  // The region list is static; option names localize at render time.
  const regions = useMemo(() => getAllRegions(), []);
  const matches = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return [];
    return regions
      .filter((region) => region.code.toLowerCase().includes(needle) || region.name.toLowerCase().includes(needle))
      .slice(0, COUNTRY_MATCH_LIMIT)
      .map((region) => ({ code: region.code, name: getRegionName(region.code) ?? region.name }));
  }, [query, regions]);
  const currentName = value ? getRegionName(value.toUpperCase()) : undefined;

  return (
    <Menu
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          setQuery("");
          // Radix hands focus to the first menu item on entry; the search
          // field is the one thing this menu is for, so it wins focus back
          // one macrotask later.
          window.setTimeout(() => inputRef.current?.focus(), 0);
        }
      }}
    >
      <MenuTrigger asChild>
        <Button
          variant="secondary"
          className="max-w-56"
          aria-label={t("按国家或地区筛选")}
          title={value ? currentName || value.toUpperCase() : t("按国家或地区筛选")}
        >
          <Globe2 size={15} aria-hidden className="shrink-0" />
          <span className="min-w-0 truncate">
            {value ? `${value.toUpperCase()} · ${currentName ?? ""}` : t("国家 / 地区")}
          </span>
        </Button>
      </MenuTrigger>
      <MenuContent className="w-72">
        <div className="border-b border-rule-faint p-1.5">
          <Input
            ref={inputRef}
            aria-label={t("搜索国家或地区")}
            placeholder={t("搜索国家或地区")}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              // Printable keys must not reach the menu's typeahead, and Enter
              // picks the first match instead of the focused item.
              if (event.key.length === 1) event.stopPropagation();
              if (event.key === "Enter" && matches[0]) {
                event.preventDefault();
                onChange(matches[0].code);
                setOpen(false);
              }
            }}
          />
        </div>
        <div className="max-h-72 overflow-y-auto p-1">
          {value ? (
            <MenuItem onSelect={() => { onChange(""); setOpen(false); }}>
              <span>{t("全部国家 / 地区")}</span>
            </MenuItem>
          ) : null}
          {matches.map((region) => (
            <MenuItem
              key={region.code}
              onSelect={() => { onChange(region.code); setOpen(false); }}
            >
              <span className="readout shrink-0 text-xs">{region.code}</span>
              <span className="min-w-0 truncate text-ink-soft">
                {region.name}
              </span>
            </MenuItem>
          ))}
          {!query.trim() ? (
            <MenuLabel className="px-2.5 py-2 text-2xs text-ink-faint">
              {t("输入国家或地区名称或代码")}
            </MenuLabel>
          ) : null}
          {query.trim() && matches.length === 0 ? (
            <MenuLabel className="px-2.5 py-2 text-2xs text-ink-faint">
              {t("没有匹配的国家 / 地区")}
            </MenuLabel>
          ) : null}
        </div>
      </MenuContent>
    </Menu>
  );
}

export function NodesPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const location = useLocation();
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
  // The URL keeps the raw text of every filter it ever carried; filters the
  // toolbar no longer edits still shape the query, so a shared deep link keeps
  // working until someone clears it.
  //
  // `check` is repeatable and therefore not part of `filter`: a single-value
  // object cannot hold it, and reading only `params.get("check")` would drop the
  // second rule. It lives in the query key instead (see `checksKey`), because a
  // key that ignores it would leave the table on the previous result set.
  const selectedChecks = params.getAll("check").map((value) => value.trim()).filter(Boolean);
  // Sorted, so the same set picked in a different order is not a new query.
  const checksKey = [...selectedChecks].sort().join("|");
  const nodeQuery: NodeListQuery = {
    ...filter,
    purity_min: optionalInteger(filter.purity_min),
    purity_max: optionalInteger(filter.purity_max),
    asn: optionalInteger(filter.asn),
    native: filter.native === "true" ? true : filter.native === "false" ? false : undefined,
    verdict: filter.verdict || undefined,
    confidence_min: filter.confidence_min || undefined,
    country: filter.country || undefined,
    check: selectedChecks,
  };
  const qualityStatus = useQuery({
    queryKey: ["quality", "status"],
    queryFn: getQualityStatus,
    refetchInterval: query => qualityPollingInterval(query.state.data),
  });
  const nodesQuery = useQuery({
    queryKey: ["nodes", filter, checksKey],
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
  // The drawer route is the list route plus `selected`; the name cell links
  // there directly so the drawer opens from history like any other navigation.
  const nodeDetailHref = (hash: string) => {
    const next = new URLSearchParams(params);
    next.set("selected", hash);
    return `?${next.toString()}`;
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
  // Clearing filters clears filters only: the view tab, the drawer selection,
  // the page size and the sort survive, so the "clear" button can never close
  // the drawer it sits next to or reset a preference it does not own.
  const clearFilters = () =>
    setParams(
      (previous) => {
        const next = new URLSearchParams();
        for (const key of ["view", "selected", "size", "sort", "order"]) {
          const value = previous.get(key);
          if (value) next.set(key, value);
        }
        return next;
      },
      { replace: true, state: location.state },
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
  const exportMutation = useMutation({
    mutationFn: (format: NodeExportFormat) => exportNodes(format, nodeQuery),
    onSuccess: (result) => {
      downloadBlob(result.blob, result.fileName);
      const parts = [t("已导出 {{count}} 个节点", { count: result.exported })];
      if (result.skipped > 0) parts.push(t("跳过 {{count}} 个", { count: result.skipped }));
      if (result.truncated > 0) parts.push(t("超出上限 {{count}} 个未导出", { count: result.truncated }));
      showToast("success", parts.join(" · "));
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  // One action, every query the page reads: the list, the drawer's node, the
  // filter vocabularies and the quality status line.
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["nodes"] });
    void queryClient.invalidateQueries({ queryKey: ["node"] });
    void queryClient.invalidateQueries({ queryKey: ["platforms", "node-filter"] });
    void queryClient.invalidateQueries({ queryKey: ["quality"] });
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
  const hasActiveFilters = numberOfFilters > 0 || mode !== "all" || Boolean(keyword);

  // Sort lives in the headers: the th carries data-sortable and aria-sort (the
  // stylesheet draws the arrow), the label is a real button for the keyboard.
  const sortTH = (key: NodeSortBy, label: string) => {
    const active = sort === key;
    return (
      <TH
        data-sortable=""
        aria-sort={active ? (order === "asc" ? "ascending" : "descending") : "none"}
        onClick={() => changeSort(key)}
      >
        <Button
          type="button"
          variant="quiet"
          size="sm"
          className={`h-auto gap-1 px-0 py-0 text-2xs font-semibold uppercase tracking-[0.08em] ${active ? "text-ink" : "text-ink-faint"}`}
          onClick={(event) => {
            event.stopPropagation();
            changeSort(key);
          }}
        >
          {t(label)}
          {!active && <ArrowUpDown size={11} aria-hidden />}
        </Button>
      </TH>
    );
  };
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
          title={t("节点")}
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
              <Menu>
                <MenuTrigger asChild>
                  <Button variant="secondary" disabled={exportMutation.isPending}>
                    {exportMutation.isPending ? (
                      <LoaderCircle size={16} className="animate-spin" aria-hidden />
                    ) : (
                      <Download size={16} aria-hidden />
                    )}
                    {t("导出")}
                  </Button>
                </MenuTrigger>
                <MenuContent>
                  <MenuLabel>{t("按当前筛选导出")}</MenuLabel>
                  {NODE_EXPORT_FORMATS.map((format) => (
                    <Fragment key={format}>
                      {/* Client configs first, then the analysis files. Keyed
                          on the format, not the position, so inserting a
                          format cannot move the separator by accident. */}
                      {format === "csv" ? <MenuSeparator /> : null}
                      <MenuItem onSelect={() => exportMutation.mutate(format)}>
                        <span>{t(EXPORT_FORMAT_LABELS[format].label)}</span>
                        <span className="text-xs text-ink-faint">{EXPORT_FORMAT_LABELS[format].hint}</span>
                      </MenuItem>
                    </Fragment>
                  ))}
                </MenuContent>
              </Menu>
              <Button asChild variant="secondary">
                <Link to={`${opsPaths.subscriptions}?create=1`}>{t("导入节点")}</Link>
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
          <Panel className="flex min-w-0 flex-col">
            <PanelHeader
              title={t("节点线路")}
              meta={nodesQuery.data ? t("{{count}} 个节点", { count: nodesQuery.data.total }) : undefined}
              actions={
                <>
                  {bulkJobID ? (
                    <Button asChild variant="ghost" size="sm">
                      <Link to={opsPaths.jobs}>{t("查看检测任务")}</Link>
                    </Button>
                  ) : null}
                  <Button
                    variant="secondary"
                    size="sm"
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
                    {t("批量拉取情报")}
                    <span className="readout text-2xs text-ink-faint">
                      {bulkCount.toLocaleString()}
                    </span>
                  </Button>
                </>
              }
            />

            <PanelToolbar className="filter-toolbar">
              <Select
                className="w-32"
                value={mode}
                aria-label={t("健康状态")}
                onChange={(event) => update("status", event.target.value)}
              >
                {[
                  ["all", "全部"],
                  ["healthy", "健康"],
                  ["circuit_open", "降级"],
                  ["error", "危急"],
                  ["disabled", "禁用"],
                ].map(([key, label]) => (
                  <option key={key} value={key}>
                    {t(label)}
                  </option>
                ))}
              </Select>
              <Select
                className="w-44"
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
              <CountryPicker
                value={filter.country}
                onChange={(code) => update("country", code)}
              />
              {bulkScopeWider && !bulkScopeBlocked ? (
                <span className="max-w-[40ch] text-xs leading-relaxed text-warn">
                  {t("部分筛选条件不适用于批量拉取，实际范围可能更大。")}
                </span>
              ) : null}
              <div className="ml-auto flex items-center gap-2">
                {hasActiveFilters ? (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={clearFilters}
                  >
                    <X size={14} aria-hidden />
                    {t("清除筛选")}
                  </Button>
                ) : null}
                <Button
                  variant="secondary"
                  disabled={view === "nodes" && nodesQuery.isFetching}
                  onClick={refresh}
                >
                  <RefreshCw
                    size={15}
                    className={nodesQuery.isFetching ? "animate-spin" : undefined}
                  />
                  {t("刷新")}
                </Button>
              </div>
            </PanelToolbar>

            {nodesQuery.isLoading && (
              <PanelBody>
                <LoadingState />
              </PanelBody>
            )}
            {nodesQuery.isError && (
              <PanelBody>
                <ErrorState
                  message={formatApiErrorMessage(nodesQuery.error, t)}
                  onRetry={() => void nodesQuery.refetch()}
                />
              </PanelBody>
            )}
            {!nodesQuery.isLoading && !nodesQuery.isError && nodes.length === 0 && (
              <PanelBody>
                <EmptyState
                  title={t(
                    hasActiveFilters
                      ? "没有匹配的节点"
                      : "还没有节点",
                  )}
                  action={
                    <div className="flex flex-wrap items-center justify-center gap-2">
                      {hasActiveFilters ? (
                        <Button
                          variant="secondary"
                          onClick={clearFilters}
                        >
                          {t("清除筛选")}
                        </Button>
                      ) : null}
                      <Button asChild>
                        <Link to={`${opsPaths.subscriptions}?create=1`}>{t("添加订阅")}</Link>
                      </Button>
                    </div>
                  }
                />
              </PanelBody>
            )}
            {nodes.length > 0 && (
              <TableWrap aria-busy={nodesQuery.isFetching}>
                <Table className="nodes-table min-w-[1080px]">
                  <colgroup>
                    {/* c-name is the stylesheet's own width for the name column. */}
                    <col className="c-name" />
                    <col style={{ width: "13%" }} />
                    <col style={{ width: "9%" }} />
                    <col style={{ width: "9%" }} />
                    <col style={{ width: "9%" }} />
                    <col style={{ width: "14%" }} />
                    <col style={{ width: "14%" }} />
                    <col style={{ width: "12%" }} />
                  </colgroup>
                  <THead>
                    <TR>
                      {sortTH("tag", "节点名称")}
                      <TH>{t("来源订阅")}</TH>
                      <TH>{t("健康")}</TH>
                      {sortTH("latency", "延迟")}
                      {sortTH("purity_score", "纯净度")}
                      <TH>{t("出口 IP")}</TH>
                      {sortTH("region", "国家 / 地区")}
                      {sortTH("assessed_at", "最近检测")}
                    </TR>
                  </THead>
                  <TBody>
                    {nodes.map((node) => {
                      const state = status(node);
                      const sourceNames = [...new Set(node.tags.map((tag) => tag.subscription_name))];
                      const intel = node.intel;
                      const purityScore =
                        intel && typeof intel.purity_score === "number" ? intel.purity_score : null;
                      const purityBand = intel
                        ? purityBands.find((item) => item.id === intel.purity_band)
                        : undefined;
                      const countryCode = (intel?.country || node.region || "").toUpperCase();
                      const place = intel?.city || intel?.colo || "";
                      return (
                        <TR
                          key={node.node_hash}
                          selected={selected === node.node_hash}
                          className="cursor-pointer"
                          onClick={(event) => {
                            // The name is its own link; a click that starts on
                            // a control belongs to that control alone.
                            if ((event.target as HTMLElement).closest("a, button, input, select")) return;
                            open(node.node_hash);
                          }}
                        >
                          <TDClip title={nameOf(node)}>
                            <Link
                              to={nodeDetailHref(node.node_hash)}
                              state={{ nodePanel: true }}
                              className="mono block max-w-full truncate text-sm font-medium text-accent"
                            >
                              {nameOf(node)}
                            </Link>
                          </TDClip>
                          <TDClip
                            title={sourceNames.length > 1 ? sourceNames.join(" · ") : undefined}
                          >
                            <span className="flex min-w-0 items-center gap-1.5">
                              <span className="min-w-0 truncate">
                                {sourceNames[0] || "--"}
                              </span>
                              {sourceNames.length > 1 ? (
                                <span className="readout shrink-0 text-2xs text-ink-faint">
                                  +{sourceNames.length - 1}
                                </span>
                              ) : null}
                            </span>
                          </TDClip>
                          <TD className="whitespace-nowrap">
                            <span className="inline-flex items-center gap-2">
                              <span aria-hidden className={healthDotClass(state.tone)} />
                              <span className="state-text">{t(state.label)}</span>
                            </span>
                          </TD>
                          <TDNum>
                            {node.reference_latency_ms !== undefined ? (
                              `${Math.round(node.reference_latency_ms)} ms`
                            ) : (
                              <span className="text-ink-faint">--</span>
                            )}
                          </TDNum>
                          <TDNum
                            title={purityBand ? t(purityBand.label) : purityScore === null ? t("评级未知") : undefined}
                          >
                            {purityScore !== null ? (
                              purityScore
                            ) : (
                              <span className="text-ink-faint">--</span>
                            )}
                          </TDNum>
                          <TDClip title={node.egress_ip || undefined}>
                            <span className="mono truncate">
                              {node.egress_ip || <span className="text-ink-faint">--</span>}
                            </span>
                          </TDClip>
                          <TDClip
                            title={
                              [
                                countryCode ? getRegionName(countryCode) : undefined,
                                place,
                              ]
                                .filter(Boolean)
                                .join(" · ") || undefined
                            }
                          >
                            <span className="flex min-w-0 items-center gap-1.5">
                              <span className="readout shrink-0 text-ink">{countryCode || "--"}</span>
                              {countryCode && place ? (
                                <>
                                  <span aria-hidden className="shrink-0 text-ink-faint">
                                    ·
                                  </span>
                                  <span className="min-w-0 truncate text-xs text-ink-soft">{place}</span>
                                </>
                              ) : null}
                            </span>
                          </TDClip>
                          <TDNum
                            title={intel?.assessed_at ? formatDateTime(intel.assessed_at) : undefined}
                          >
                            {intel?.assessed_at ? (
                              formatRelativeTime(intel.assessed_at)
                            ) : (
                              <span className="text-ink-faint">--</span>
                            )}
                          </TDNum>
                        </TR>
                      );
                    })}
                  </TBody>
                </Table>
              </TableWrap>
            )}

            <PanelFooter className="justify-between gap-3">
              <p className="text-xs text-ink-soft">
                {nodesQuery.data
                  ? t("第 {{page}} / {{pages}} 页 · 显示 {{start}}-{{end}} / {{total}}", {
                      page: currentPage + 1,
                      pages: totalPages,
                      start: nodesQuery.data.total ? currentPage * pageSize + 1 : 0,
                      end: Math.min((currentPage + 1) * pageSize, nodesQuery.data.total),
                      total: nodesQuery.data.total,
                    })
                  : null}
              </p>
              <div className="flex items-center gap-2">
                <label className="flex items-center gap-1.5 text-xs text-ink-soft">
                  <span>{t("每页")}</span>
                  <Select
                    className="w-auto"
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
                    variant="ghost"
                    size="icon"
                    aria-label={t("打开完整页面")}
                    title={t("打开完整页面")}
                    asChild
                  >
                    <Link to={`${opsPaths.nodes}/${detail.node_hash}`}>
                      <ExternalLink size={15} />
                    </Link>
                  </Button>
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
                          <Link to={`${opsPaths.subscriptions}?selected=${tag.subscription_id}`}>
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
