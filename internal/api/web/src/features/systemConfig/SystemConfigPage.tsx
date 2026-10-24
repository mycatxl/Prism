import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, ArrowRight, LockKeyhole, PencilLine, RefreshCw, RotateCcw, Save, Search } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useBeforeUnload, useSearchParams } from "react-router-dom";
import {
  CommandSurface,
  ConfirmDrawer,
  ControlButton,
  ControlInput,
  ControlSwitch,
  ControlTextarea,
  EmptyWorkState,
  ObjectList,
  ObjectRow,
  PageFrame,
  QueryBadge,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { Tooltip } from "../../components/ui/Tooltip";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import {
  getDefaultSystemConfig,
  getEnvConfig,
  getSystemConfig,
  patchSystemConfig,
  SYSTEM_CONFIG_DEFAULT_QUERY_KEY,
  SYSTEM_CONFIG_QUERY_KEY,
  SYSTEM_CONFIG_STALE_MS,
  SYSTEM_ENV_CONFIG_QUERY_KEY,
  SYSTEM_ENV_STALE_MS,
} from "./api";
import type { EnvConfig, RuntimeConfig, RuntimeConfigPatch } from "./types";

type RuntimeConfigForm = {
  request_log_enabled: boolean;
  reverse_proxy_log_detail_enabled: boolean;
  reverse_proxy_log_req_headers_max_bytes: string;
  reverse_proxy_log_req_body_max_bytes: string;
  reverse_proxy_log_resp_headers_max_bytes: string;
  reverse_proxy_log_resp_body_max_bytes: string;
  max_consecutive_failures: string;
  max_latency_test_interval: string;
  max_authority_latency_test_interval: string;
  max_egress_test_interval: string;
  latency_test_url: string;
  latency_authorities_raw: string;
  p2c_latency_window: string;
  latency_decay_window: string;
  cache_flush_interval: string;
  cache_flush_dirty_threshold: string;
};

type RuntimeField = keyof RuntimeConfigForm;
type Category = { id: string; title: string; description: string; fields: readonly RuntimeField[]; staticCount: number };
type Translator = (text: string, values?: Record<string, unknown>) => string;
type StaticRow = { label: string; value?: string; status?: { tone: "signal" | "warn" | "neutral"; text: string } };

const EDITABLE_FIELDS: readonly (keyof RuntimeConfig)[] = [
  "request_log_enabled",
  "reverse_proxy_log_detail_enabled",
  "reverse_proxy_log_req_headers_max_bytes",
  "reverse_proxy_log_req_body_max_bytes",
  "reverse_proxy_log_resp_headers_max_bytes",
  "reverse_proxy_log_resp_body_max_bytes",
  "max_consecutive_failures",
  "max_latency_test_interval",
  "max_authority_latency_test_interval",
  "max_egress_test_interval",
  "latency_test_url",
  "latency_authorities",
  "p2c_latency_window",
  "latency_decay_window",
  "cache_flush_interval",
  "cache_flush_dirty_threshold",
];

const FIELD_LABELS: Record<RuntimeField, string> = {
  request_log_enabled: "启用请求日志",
  reverse_proxy_log_detail_enabled: "记录详细反代日志",
  reverse_proxy_log_req_headers_max_bytes: "请求头最大字节数",
  reverse_proxy_log_req_body_max_bytes: "请求体最大字节数",
  reverse_proxy_log_resp_headers_max_bytes: "响应头最大字节数",
  reverse_proxy_log_resp_body_max_bytes: "响应体最大字节数",
  max_consecutive_failures: "最大连续失败次数",
  max_latency_test_interval: "节点延迟最大测试间隔",
  max_authority_latency_test_interval: "权威域名最大测试间隔",
  max_egress_test_interval: "出口 IP 更新检查间隔",
  latency_test_url: "延迟测试目标 URL",
  latency_authorities_raw: "延迟测试权威域名列表",
  p2c_latency_window: "P2C 延迟衰减窗口",
  latency_decay_window: "历史延迟衰减窗口",
  cache_flush_interval: "缓存异步刷盘间隔",
  cache_flush_dirty_threshold: "缓存刷盘脏阈值",
};

const CATEGORIES: readonly Category[] = [
  { id: "health", title: "探测与路由", description: "健康阈值、出口探测和线路选择", fields: ["max_consecutive_failures", "latency_test_url", "max_latency_test_interval", "max_authority_latency_test_interval", "max_egress_test_interval", "p2c_latency_window", "latency_decay_window", "latency_authorities_raw"], staticCount: 0 },
  { id: "logs", title: "请求日志", description: "记录内容、大小上限和日志落库", fields: ["request_log_enabled", "reverse_proxy_log_detail_enabled", "reverse_proxy_log_req_headers_max_bytes", "reverse_proxy_log_req_body_max_bytes", "reverse_proxy_log_resp_headers_max_bytes", "reverse_proxy_log_resp_body_max_bytes"], staticCount: 5 },
  { id: "storage", title: "缓存与持久化", description: "运行状态的刷盘频率和批量阈值", fields: ["cache_flush_interval", "cache_flush_dirty_threshold"], staticCount: 0 },
  { id: "network", title: "网络与性能", description: "DNS、并发、超时和连接池", fields: [], staticCount: 11 },
  { id: "platform", title: "默认平台策略", description: "会话、账号提取和默认筛选规则", fields: [], staticCount: 7 },
  { id: "metrics", title: "指标与统计", description: "采样频率、历史保留和统计精度", fields: [], staticCount: 9 },
  { id: "deployment", title: "服务与部署", description: "数据目录、监听端口和鉴权状态", fields: [], staticCount: 7 },
];

function configToForm(config: RuntimeConfig): RuntimeConfigForm {
  return {
    request_log_enabled: config.request_log_enabled,
    reverse_proxy_log_detail_enabled: config.reverse_proxy_log_detail_enabled,
    reverse_proxy_log_req_headers_max_bytes: String(config.reverse_proxy_log_req_headers_max_bytes),
    reverse_proxy_log_req_body_max_bytes: String(config.reverse_proxy_log_req_body_max_bytes),
    reverse_proxy_log_resp_headers_max_bytes: String(config.reverse_proxy_log_resp_headers_max_bytes),
    reverse_proxy_log_resp_body_max_bytes: String(config.reverse_proxy_log_resp_body_max_bytes),
    max_consecutive_failures: String(config.max_consecutive_failures),
    max_latency_test_interval: config.max_latency_test_interval,
    max_authority_latency_test_interval: config.max_authority_latency_test_interval,
    max_egress_test_interval: config.max_egress_test_interval,
    latency_test_url: config.latency_test_url,
    latency_authorities_raw: config.latency_authorities.join("\n"),
    p2c_latency_window: config.p2c_latency_window,
    latency_decay_window: config.latency_decay_window,
    cache_flush_interval: config.cache_flush_interval,
    cache_flush_dirty_threshold: String(config.cache_flush_dirty_threshold),
  };
}

function parseNonNegativeInt(field: string, raw: string, t: Translator): number {
  const value = raw.trim();
  if (!value) throw new Error(t("{{field}} 不能为空", { field: t(field) }));
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || parsed < 0) throw new Error(t("{{field}} 必须是非负整数", { field: t(field) }));
  return parsed;
}

function parseDurationField(field: string, raw: string, t: Translator): string {
  const value = raw.trim();
  if (!value) throw new Error(t("{{field}} 不能为空", { field: t(field) }));
  return value;
}

function parseAuthorities(raw: string): string[] {
  return Array.from(new Set(raw.split(/[\n,]/).map((item) => item.trim()).filter(Boolean)));
}

function parseForm(form: RuntimeConfigForm, t: Translator): RuntimeConfig {
  const latencyURL = form.latency_test_url.trim();
  if (!latencyURL) throw new Error(t("延迟测试目标 URL 不能为空"));
  if (!latencyURL.startsWith("http://") && !latencyURL.startsWith("https://")) throw new Error(t("延迟测试目标 URL 必须是 http/https 地址"));
  return {
    request_log_enabled: form.request_log_enabled,
    reverse_proxy_log_detail_enabled: form.reverse_proxy_log_detail_enabled,
    reverse_proxy_log_req_headers_max_bytes: parseNonNegativeInt("请求头最大字节数", form.reverse_proxy_log_req_headers_max_bytes, t),
    reverse_proxy_log_req_body_max_bytes: parseNonNegativeInt("请求体最大字节数", form.reverse_proxy_log_req_body_max_bytes, t),
    reverse_proxy_log_resp_headers_max_bytes: parseNonNegativeInt("响应头最大字节数", form.reverse_proxy_log_resp_headers_max_bytes, t),
    reverse_proxy_log_resp_body_max_bytes: parseNonNegativeInt("响应体最大字节数", form.reverse_proxy_log_resp_body_max_bytes, t),
    max_consecutive_failures: parseNonNegativeInt("最大连续失败次数", form.max_consecutive_failures, t),
    max_latency_test_interval: parseDurationField("节点延迟最大测试间隔", form.max_latency_test_interval, t),
    max_authority_latency_test_interval: parseDurationField("权威域名最大测试间隔", form.max_authority_latency_test_interval, t),
    max_egress_test_interval: parseDurationField("出口 IP 更新检查间隔", form.max_egress_test_interval, t),
    latency_test_url: latencyURL,
    latency_authorities: parseAuthorities(form.latency_authorities_raw),
    p2c_latency_window: parseDurationField("P2C 延迟衰减窗口", form.p2c_latency_window, t),
    latency_decay_window: parseDurationField("历史延迟衰减窗口", form.latency_decay_window, t),
    cache_flush_interval: parseDurationField("缓存异步刷盘间隔", form.cache_flush_interval, t),
    cache_flush_dirty_threshold: parseNonNegativeInt("缓存刷盘脏阈值", form.cache_flush_dirty_threshold, t),
  };
}

function arrayEquals(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

function buildPatch(current: RuntimeConfig, next: RuntimeConfig): RuntimeConfigPatch {
  const patch: RuntimeConfigPatch = {};
  const mutable = patch as Record<string, unknown>;
  for (const field of EDITABLE_FIELDS) {
    const currentValue = current[field];
    const nextValue = next[field];
    if (Array.isArray(currentValue) && Array.isArray(nextValue)) {
      if (!arrayEquals(currentValue, nextValue)) mutable[field] = nextValue;
    } else if (currentValue !== nextValue) {
      mutable[field] = nextValue;
    }
  }
  return patch;
}

function displayList(values: string[] | null | undefined, t: Translator): string {
  return values?.length ? values.join(", ") : t("无");
}

function staticRows(categoryId: string, env: EnvConfig, t: Translator): Array<{ title: string; rows: StaticRow[] }> {
  const status = (value: boolean) => ({ tone: value ? "signal" as const : "neutral" as const, text: value ? t("已配置") : t("未配置") });
  if (categoryId === "deployment") return [
    { title: t("目录与端口"), rows: [
      { label: t("数据缓存目录"), value: env.cache_dir },
      { label: t("状态存储目录"), value: env.state_dir },
      { label: t("日志保留目录"), value: env.log_dir },
      { label: t("代理 / API 监听地址"), value: env.listen_address },
      { label: t("代理 / API 端口"), value: String(env.prism_port) },
      { label: t("认证版本"), value: env.auth_version },
    ] },
    { title: t("服务鉴权状态"), rows: [{ label: t("已配置代理令牌"), status: status(env.proxy_token_set) }, { label: t("代理令牌强度"), status: env.proxy_token_weak ? { tone: "warn", text: t("强度较弱") } : { tone: "signal", text: t("未发现弱强度标记") } }] },
  ];
  if (categoryId === "network") return [{ title: t("全局限额与性能调优"), rows: [
    { label: t("控制面最大请求体"), value: String(env.api_max_body_bytes) },
    { label: t("最大延迟表条目数"), value: String(env.max_latency_table_entries) },
    { label: t("节点拨测并发数"), value: String(env.probe_concurrency) },
    { label: t("拨测超时时间"), value: env.probe_timeout },
    { label: t("资源获取超时时间"), value: env.resource_fetch_timeout },
    { label: t("节点 DNS 上游"), value: displayList(env.node_dns_upstreams, t) },
    { label: t("GeoIP 更新计划"), value: env.geoip_update_schedule },
    { label: t("代理传输最大空闲连接"), value: String(env.proxy_transport_max_idle_conns) },
    { label: t("单主机最大空闲连接"), value: String(env.proxy_transport_max_idle_conns_per_host) },
    { label: t("空闲连接超时时间"), value: env.proxy_transport_idle_conn_timeout },
    { label: t("代理直连目标"), value: displayList(env.proxy_bypass_rules, t) },
  ] }];
  if (categoryId === "platform") return [{ title: t("默认平台回退规则"), rows: [
    { label: t("默认粘性会话 TTL"), value: env.default_platform_sticky_ttl },
    { label: t("默认节点分配策略"), value: env.default_platform_allocation_policy },
    { label: t("默认反代不匹配行为"), value: env.default_platform_reverse_proxy_miss_action },
    { label: t("默认反代空账号行为"), value: env.default_platform_reverse_proxy_empty_account_behavior },
    { label: t("默认反代固定账号 Header 列表"), value: env.default_platform_reverse_proxy_fixed_account_header || t("无") },
    { label: t("默认正则黑名单"), value: displayList(env.default_platform_regex_filters, t) },
    { label: t("默认地区黑名单"), value: displayList(env.default_platform_region_filters, t) },
  ] }];
  if (categoryId === "logs") return [{ title: t("请求日志落库"), rows: [
    { label: t("队列大小"), value: String(env.request_log_queue_size) },
    { label: t("落盘批大小"), value: String(env.request_log_queue_flush_batch_size) },
    { label: t("落盘间隔"), value: env.request_log_queue_flush_interval },
    { label: t("数据库保留阈值"), value: `${env.request_log_db_max_mb} MB` },
    { label: t("数据库旧分片保留数"), value: String(env.request_log_db_retain_count) },
  ] }];
  if (categoryId === "metrics") return [{ title: t("可观测性指标"), rows: [
    { label: t("吞吐量抽样间隔"), value: `${env.metric_throughput_interval_seconds}s` },
    { label: t("吞吐量保留时间"), value: `${env.metric_throughput_retention_seconds}s` },
    { label: t("连接数抽样间隔"), value: `${env.metric_connections_interval_seconds}s` },
    { label: t("连接数保留时间"), value: `${env.metric_connections_retention_seconds}s` },
    { label: t("租期与连接指标分桶数"), value: `${env.metric_bucket_seconds}s` },
    { label: t("租期抽样间隔"), value: `${env.metric_leases_interval_seconds}s` },
    { label: t("租期保留时间"), value: `${env.metric_leases_retention_seconds}s` },
    { label: t("延迟统计桶宽"), value: `${env.metric_latency_bin_width_ms}ms` },
    { label: t("延迟统计截断值"), value: `${env.metric_latency_bin_overflow_ms}ms` },
  ] }];
  return [];
}

function StaticGroup({ title, rows }: { title: string; rows: StaticRow[] }) {
  return <div className="cp-form-section"><span className="cp-eyebrow">{title}</span><ObjectList>{rows.map((row) => <ObjectRow key={row.label} leading={<span className="cp-platform-row__mark" aria-hidden>ENV</span>} title={row.label} facts={row.value ? <code>{row.value}</code> : undefined} status={row.status ? <StatusMark tone={row.status.tone} label={row.status.text} /> : undefined} />)}</ObjectList></div>;
}

function RestoreButton({ label, value, disabled, onPress }: { label: string; value: string; disabled?: boolean; onPress: () => void }) {
  return <Tooltip content={value ? `${label}: ${value}` : label}><ControlButton variant="ghost" size="sm" isIconOnly aria-label={label} isDisabled={disabled} onPress={onPress}><RotateCcw size={14} aria-hidden /></ControlButton></Tooltip>;
}

function RuntimeFieldEditor({ field, form, defaultForm, pending, setField, restore }: { field: RuntimeField; form: RuntimeConfigForm; defaultForm: RuntimeConfigForm | null; pending: boolean; setField: (field: RuntimeField, value: string | boolean) => void; restore: (field: RuntimeField) => void }) {
  const { t } = useI18n();
  const label = t(FIELD_LABELS[field]);
  const defaultValue = defaultForm ? String(field === "latency_authorities_raw" ? defaultForm.latency_authorities_raw : defaultForm[field]) : "";
  const restoreButton = <RestoreButton label={t("恢复为默认值")} value={defaultValue} disabled={pending || !defaultForm} onPress={() => restore(field)} />;
  if (field === "request_log_enabled" || field === "reverse_proxy_log_detail_enabled") {
    return <div className="cp-inset-stack"><ControlSwitch label={label} hint={field === "request_log_enabled" ? t("记录代理请求的结构化日志。"): t("记录更详细的反向代理诊断字段。")} isSelected={Boolean(form[field])} isDisabled={pending} onChange={(value) => setField(field, value)} /><span className="cp-drawer-actions">{restoreButton}</span></div>;
  }
  if (field === "latency_authorities_raw") {
    return <div className="cp-field-with-action"><ControlTextarea label={label} hint={t("每行一个域名，也支持逗号分隔。")} rows={5} value={form[field]} disabled={pending} onChange={(event) => setField(field, event.target.value)} />{restoreButton}</div>;
  }
  const numeric = field.includes("bytes") || field === "max_consecutive_failures" || field === "cache_flush_dirty_threshold";
  return <div className="cp-field-with-action"><ControlInput label={label} type={numeric ? "number" : "text"} min={numeric ? "0" : undefined} value={form[field]} disabled={pending} onChange={(event) => setField(field, event.target.value)} />{restoreButton}</div>;
}

export function SystemConfigPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const [categorySearch, setCategorySearch] = useState("");
  const category = CATEGORIES.find((item) => item.id === params.get("category"));
  const [draftForm, setDraftForm] = useState<RuntimeConfigForm | null>(null);
  const [customPatchText, setCustomPatchText] = useState<string | null>(null);
  const [reloadOpen, setReloadOpen] = useState(false);
  const [reloadPending, setReloadPending] = useState(false);
  const { toasts, showToast, dismissToast } = useToast();
  const queryClient = useQueryClient();
  const configQuery = useQuery({ queryKey: SYSTEM_CONFIG_QUERY_KEY, queryFn: getSystemConfig, staleTime: SYSTEM_CONFIG_STALE_MS });
  const defaultQuery = useQuery({ queryKey: SYSTEM_CONFIG_DEFAULT_QUERY_KEY, queryFn: getDefaultSystemConfig, staleTime: SYSTEM_CONFIG_STALE_MS });
  const envQuery = useQuery({ queryKey: SYSTEM_ENV_CONFIG_QUERY_KEY, queryFn: getEnvConfig, staleTime: SYSTEM_ENV_STALE_MS });
  const baseline = configQuery.data ?? null;
  const defaultBaseline = defaultQuery.data ?? null;
  const envBaseline = envQuery.data ?? null;
  const form = useMemo(() => baseline ? draftForm ?? configToForm(baseline) : null, [baseline, draftForm]);
  const parsedResult = useMemo(() => {
    if (!form) return { config: null as RuntimeConfig | null, error: "" };
    try { return { config: parseForm(form, t), error: "" }; } catch (error) { return { config: null, error: formatApiErrorMessage(error, t) }; }
  }, [form, t]);
  const patchPreview = useMemo<RuntimeConfigPatch>(() => baseline && parsedResult.config ? buildPatch(baseline, parsedResult.config) : {}, [baseline, parsedResult.config]);
  const changedKeys = useMemo(() => {
    if (!baseline || !form) return [];
    const previous = configToForm(baseline);
    return EDITABLE_FIELDS.filter((field) => {
      const formField = field === "latency_authorities" ? "latency_authorities_raw" : field;
      return form[formField] !== previous[formField];
    });
  }, [baseline, form]);
  const hasUnsavedChanges = changedKeys.length > 0 || customPatchText !== null;
  const baselineSignature = useMemo(() => baseline ? JSON.stringify(baseline) : null, [baseline]);
  const draftSeedSignatureRef = useRef<string | null>(null);
  const [staleDraft, setStaleDraft] = useState(false);

  useBeforeUnload((event) => {
    if (hasUnsavedChanges) {
      event.preventDefault();
      event.returnValue = "";
    }
  });

  useEffect(() => {
    if (!baselineSignature) return;
    if (!hasUnsavedChanges) {
      draftSeedSignatureRef.current = baselineSignature;
      setStaleDraft(false);
      return;
    }
    if (draftSeedSignatureRef.current === null) draftSeedSignatureRef.current = baselineSignature;
    else if (draftSeedSignatureRef.current !== baselineSignature) setStaleDraft(true);
  }, [baselineSignature, hasUnsavedChanges]);

  const saveMutation = useMutation({
    mutationFn: async () => {
      if (!baseline || !form) throw new Error(t("配置尚未加载完成"));
      if (customPatchText !== null) throw new Error(t("请先将 JSON 应用到草稿"));
      const patch = buildPatch(baseline, parseForm(form, t));
      if (!Object.keys(patch).length) throw new Error(t("没有可提交的变更"));
      return { updated: await patchSystemConfig(patch), changedCount: Object.keys(patch).length };
    },
    onSuccess: ({ updated, changedCount }) => {
      queryClient.setQueryData(SYSTEM_CONFIG_QUERY_KEY, updated);
      setDraftForm(null);
      setCustomPatchText(null);
      showToast("success", t("配置已更新（{{count}} 项变更）", { count: changedCount }));
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const setFormField = (field: RuntimeField, value: string | boolean) => {
    setDraftForm((previous) => {
      if (!baseline) return previous;
      return { ...(previous ?? configToForm(baseline)), [field]: value } as RuntimeConfigForm;
    });
  };
  const restoreField = (field: RuntimeField) => {
    if (!baseline || !defaultBaseline) return;
    const defaults = configToForm(defaultBaseline);
    setDraftForm((previous) => ({ ...(previous ?? configToForm(baseline)), [field]: defaults[field] }));
  };
  const resetDraft = () => { setDraftForm(null); setCustomPatchText(null); setStaleDraft(false); };
  const reloadFromServer = async (discard = false) => {
    if (hasUnsavedChanges && !discard) { setReloadOpen(true); return; }
    setReloadPending(true);
    setDraftForm(null);
    setCustomPatchText(null);
    const result = await configQuery.refetch();
    setReloadPending(false);
    if (result.data) showToast("success", t("已加载最新运行时配置"));
  };
  const applyCustomPatch = () => {
    if (!form || !baseline || customPatchText === null) return;
    try {
      const patch: unknown = JSON.parse(customPatchText);
      if (!patch || Array.isArray(patch) || typeof patch !== "object") throw new Error(t("JSON 必须是配置对象"));
      const candidate = { ...form } as RuntimeConfigForm;
      const mutable = candidate as unknown as Record<string, string | boolean>;
      for (const [key, value] of Object.entries(patch)) {
        if (!EDITABLE_FIELDS.includes(key as keyof RuntimeConfig)) throw new Error(`${t("JSON 包含不支持的配置项")}: ${key}`);
        const current = baseline[key as keyof RuntimeConfig];
        if (Array.isArray(current)) {
          if (!Array.isArray(value) || value.some((item) => typeof item !== "string")) throw new Error(`${t("JSON 配置项类型不正确")}: ${key}`);
          mutable.latency_authorities_raw = value.join("\n");
        } else {
          if (typeof value !== typeof current) throw new Error(`${t("JSON 配置项类型不正确")}: ${key}`);
          mutable[key] = typeof value === "boolean" ? value : String(value);
        }
      }
      parseForm(candidate, t);
      setDraftForm(candidate);
      setCustomPatchText(null);
      showToast("success", t("JSON 已应用到草稿，保存后生效"));
    } catch (error) {
      showToast("error", formatApiErrorMessage(error, t));
    }
  };

  const visibleCategories = CATEGORIES.filter((item) => [t(item.title), t(item.description), ...item.fields.map((field) => t(FIELD_LABELS[field]))].join(" ").toLowerCase().includes(categorySearch.trim().toLowerCase()));
  const displayedPatchText = customPatchText ?? JSON.stringify(patchPreview, null, 2);
  const isSaveDisabled = saveMutation.isPending || customPatchText !== null || Boolean(parsedResult.error) || changedKeys.length === 0;
  const selectCategory = (id?: string) => {
    setParams((previous) => {
      const next = new URLSearchParams(previous);
      if (id) next.set("category", id); else next.delete("category");
      return next;
    }, { replace: true });
  };

  return (
    <PageFrame
      eyebrow="Governance / runtime configuration"
      title={t("系统配置")}
      description={t("编辑运行时配置，查看部署级只读信息；环境值不会被提交到 runtime config。")}
      meta={<StatusMark tone={configQuery.isError ? "warn" : baseline ? "live" : "neutral"} label={configQuery.isError ? t("数据可能已过期") : baseline ? t("配置已同步") : t("等待配置")} />}
      actions={category ? <ControlButton variant="secondary" size="sm" onPress={() => selectCategory()}><ArrowLeft size={14} aria-hidden />{t("配置目录")}</ControlButton> : undefined}
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      <CommandSurface>
        <ControlInput label={t("搜索配置分类")} hint={t("按领域、说明或参数名称筛选")} value={categorySearch} onChange={(event) => setCategorySearch(event.target.value)} placeholder={t("搜索配置分类或参数")} />
        <span className="cp-command__actions"><Search size={14} aria-hidden /><QueryBadge label={t("{{count}} 个分类", { count: visibleCategories.length })} /></span>
      </CommandSurface>

      {!form ? <WorkSurface title={t("配置状态")}><EmptyWorkState title={configQuery.isError ? t("配置读取失败") : t("正在加载配置") } detail={configQuery.isError ? formatApiErrorMessage(configQuery.error, t) : t("运行时配置和部署值正在读取。")} action={<ControlButton variant="secondary" size="sm" onPress={() => void configQuery.refetch()}>{t("重试")}</ControlButton>} /></WorkSurface> : null}

      {form && !category ? <WorkSurface eyebrow="Governance / directory" title={t("配置目录")} description={t("按领域管理可编辑配置，或查看部署级只读信息。")}>
        {visibleCategories.length ? <ObjectList>{visibleCategories.map((item) => {
          const dirty = changedKeys.filter((field) => (item.fields as readonly string[]).includes(field)).length;
          return <ObjectRow key={item.id} onClick={() => selectCategory(item.id)} leading={<span className="cp-platform-row__mark" aria-hidden>{item.id.slice(0, 3).toUpperCase()}</span>} title={t(item.title)} subtitle={t(item.description)} facts={<span>{t("{{count}} 个配置项", { count: item.fields.length + item.staticCount })}</span>} status={dirty ? <StatusMark tone="warn" label={t("{{count}} 项待保存", { count: dirty })} /> : <QueryBadge label={t("未修改")} />} action={<ArrowRight size={14} aria-hidden />} />;
        })}</ObjectList> : <EmptyWorkState title={t("没有匹配的配置分类")} detail={t("搜索配置分类或参数")} />}
      </WorkSurface> : null}

      {form && category ? <>
        <WorkSurface eyebrow={`Governance / ${category.id}`} title={t(category.title)} description={t(category.description)} meta={t("{{count}} 个配置项", { count: category.fields.length + category.staticCount })} actions={<ControlButton variant="secondary" size="sm" onPress={() => void reloadFromServer()} isDisabled={reloadPending || saveMutation.isPending} loading={reloadPending}><RefreshCw size={14} aria-hidden />{t("重新加载")}</ControlButton>}>
          {category.fields.length ? <div className="cp-form-section"><div className="cp-section-heading"><span><PencilLine size={14} aria-hidden />{t("可编辑配置")}</span><QueryBadge label={t("运行时变更，保存后生效")} /></div><div className="cp-form-grid">{category.fields.map((field) => <RuntimeFieldEditor key={field} field={field} form={form} defaultForm={defaultBaseline ? configToForm(defaultBaseline) : null} pending={saveMutation.isPending} setField={setFormField} restore={restoreField} />)}</div></div> : null}
          {category.staticCount > 0 ? envBaseline ? <div className="cp-form-section"><div className="cp-section-heading"><span><LockKeyhole size={14} aria-hidden />{t("部署只读信息")}</span><QueryBadge label={t("来自部署环境，修改需重启")} /></div>{staticRows(category.id, envBaseline, t).map((group) => <StaticGroup key={group.title} title={group.title} rows={group.rows} />)}</div> : <EmptyWorkState title={t("静态配置加载失败")} detail={formatApiErrorMessage(envQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void envQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
        </WorkSurface>

        <WorkSurface eyebrow="Governance / draft" title={t("配置草稿")} meta={changedKeys.length ? t("{{count}} 项待保存", { count: changedKeys.length }) : t("当前无未保存改动")} description={t(customPatchText !== null ? "请先将 JSON 应用到草稿" : "保存会提交所有分类的草稿更改。") }>
          {staleDraft ? <p className="cp-notice cp-notice--warn">{t("运行配置已在后台更新，草稿基于旧数据；直接保存会用旧值覆盖新值，建议先重新加载。")} <ControlButton variant="secondary" size="sm" onPress={() => void reloadFromServer()}>{t("重新加载")}</ControlButton></p> : null}
          {parsedResult.error ? <p className="cp-form-error" role="alert">{parsedResult.error}</p> : null}
          <div className="cp-form-stack">
            <ControlTextarea label={t("高级：JSON 变更")} hint={t("JSON 先应用到草稿，再统一保存；分类切换会保留未保存更改。")} aria-label={t("JSON 变更内容")} value={displayedPatchText} onChange={(event) => setCustomPatchText(event.target.value)} rows={8} spellCheck={false} disabled={saveMutation.isPending} />
            <div className="cp-drawer-actions"><ControlButton variant="secondary" size="sm" onPress={applyCustomPatch} isDisabled={customPatchText === null || saveMutation.isPending}>{t("应用 JSON 到草稿")}</ControlButton><ControlButton variant="ghost" size="sm" onPress={() => setCustomPatchText(null)} isDisabled={customPatchText === null || saveMutation.isPending}>{t("取消 JSON 编辑")}</ControlButton></div>
          </div>
        </WorkSurface>

        <CommandSurface actions={<><ControlButton variant="ghost" size="sm" onPress={resetDraft} isDisabled={!hasUnsavedChanges || saveMutation.isPending}><RotateCcw size={14} aria-hidden />{t("重置草稿")}</ControlButton><ControlButton variant="primary" size="sm" onPress={() => saveMutation.mutate()} isDisabled={isSaveDisabled} loading={saveMutation.isPending}><Save size={14} aria-hidden />{t("保存全部更改")}</ControlButton></>}>
          <div><strong>{changedKeys.length ? t("{{count}} 项待保存", { count: changedKeys.length }) : t("当前无未保存改动")}</strong><span className="cp-muted">{customPatchText !== null ? t("请先将 JSON 应用到草稿") : t("保存会提交所有分类的草稿更改。")}</span></div>
        </CommandSurface>
      </> : null}

      <ConfirmDrawer open={reloadOpen} onOpenChange={setReloadOpen} title={t("丢弃草稿并重新加载？")} eyebrow="Governance / reload" message={t("当前有未保存变更，重新加载会丢弃草稿并读取服务端运行时配置。") } cancelLabel={t("取消")} confirmLabel={t("丢弃并重新加载")} loading={reloadPending} onConfirm={() => { setReloadOpen(false); void reloadFromServer(true); }} />
    </PageFrame>
  );
}

export default SystemConfigPage;
