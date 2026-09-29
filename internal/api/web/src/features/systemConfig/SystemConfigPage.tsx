import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  ArrowLeft,
  BarChart3,
  ChevronRight,
  FileText,
  HardDrive,
  Network,
  RefreshCw,
  RotateCcw,
  Save,
  Search,
  Server,
  Waypoints,
} from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";
import { useBeforeUnload, useSearchParams } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelHeader, PanelToolbar, SectionTitle } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Switch } from "../../components/ui/Switch";
import { Table, TableWrap, TBody, TD, TDClip, TR } from "../../components/ui/Table";
import { Textarea } from "../../components/ui/Textarea";
import { ToastContainer } from "../../components/ui/Toast";
import { cn } from "../../lib/cn";
import { useToast } from "../../hooks/useToast";
import i18next, { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { getEnvConfig, patchSystemConfig, getSystemConfig, getDefaultSystemConfig } from "./api";
import type { RuntimeConfig, RuntimeConfigPatch } from "./types";

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

const EDITABLE_FIELDS: Array<keyof RuntimeConfig> = [
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

const FIELD_LABELS: Record<keyof RuntimeConfig, string> = {
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
  latency_authorities: "延迟测试权威域名列表",
  p2c_latency_window: "P2C 延迟衰减窗口",
  latency_decay_window: "历史延迟衰减窗口",
  cache_flush_interval: "缓存异步刷盘间隔",
  cache_flush_dirty_threshold: "缓存刷盘脏阈值",
};


const SETTINGS_CATEGORIES = [
  { id: "health", title: "探测与路由", description: "健康阈值、出口探测和线路选择", icon: Activity, fields: ["max_consecutive_failures", "max_latency_test_interval", "max_authority_latency_test_interval", "max_egress_test_interval", "latency_test_url", "latency_authorities", "p2c_latency_window", "latency_decay_window"], staticCount: 0 },
  { id: "logs", title: "请求日志", description: "记录内容、大小上限和日志留存", icon: FileText, fields: ["request_log_enabled", "reverse_proxy_log_detail_enabled", "reverse_proxy_log_req_headers_max_bytes", "reverse_proxy_log_req_body_max_bytes", "reverse_proxy_log_resp_headers_max_bytes", "reverse_proxy_log_resp_body_max_bytes"], staticCount: 5 },
  { id: "storage", title: "缓存与持久化", description: "运行状态的刷盘频率和批量阈值", icon: HardDrive, fields: ["cache_flush_interval", "cache_flush_dirty_threshold"], staticCount: 0 },
  { id: "network", title: "网络与性能", description: "DNS、并发、超时和连接池", icon: Network, fields: [], staticCount: 11 },
  { id: "platform", title: "默认平台策略", description: "会话、账号提取和默认筛选规则", icon: Waypoints, fields: [], staticCount: 7 },
  { id: "metrics", title: "指标与统计", description: "采样频率、历史保留和统计精度", icon: BarChart3, fields: [], staticCount: 9 },
  { id: "deployment", title: "服务与部署", description: "数据目录、监听端口和鉴权状态", icon: Server, fields: [], staticCount: 7 },
] as const;

const ALLOCATION_POLICY_LABELS: Record<string, string> = {
  BALANCED: "均衡",
  PREFER_LOW_LATENCY: "优先低延迟",
  PREFER_IDLE_IP: "优先空闲出口 IP",
};

const MISS_ACTION_LABELS: Record<string, string> = {
  TREAT_AS_EMPTY: "按空账号处理",
  REJECT: "拒绝代理请求",
};

const EMPTY_ACCOUNT_BEHAVIOR_LABELS: Record<string, string> = {
  RANDOM: "随机路由",
  FIXED_HEADER: "提取指定请求头作为 Account",
  ACCOUNT_HEADER_RULE: "按照全局请求头规则提取 Account",
};

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

function requiredFieldLabel(field: string): string {
  return i18next.t(field);
}

function parseNonNegativeInt(field: string, raw: string): number {
  const value = raw.trim();
  if (!value) {
    throw new Error(i18next.t("{{field}} 不能为空", { field: requiredFieldLabel(field) }));
  }
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || parsed < 0) {
    throw new Error(i18next.t("{{field}} 必须是非负整数", { field: requiredFieldLabel(field) }));
  }
  return parsed;
}

function parseDurationField(field: string, raw: string): string {
  const value = raw.trim();
  if (!value) {
    throw new Error(i18next.t("{{field}} 不能为空", { field: requiredFieldLabel(field) }));
  }
  return value;
}

function parseAuthorities(raw: string): string[] {
  const items = raw
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);

  return Array.from(new Set(items));
}

function parseForm(form: RuntimeConfigForm): RuntimeConfig {
  const latencyURL = form.latency_test_url.trim();
  if (!latencyURL) {
    throw new Error("延迟测试目标 URL 不能为空");
  }
  if (!latencyURL.startsWith("http://") && !latencyURL.startsWith("https://")) {
    throw new Error("延迟测试目标 URL 必须是 http/https 地址");
  }

  return {
    request_log_enabled: form.request_log_enabled,
    reverse_proxy_log_detail_enabled: form.reverse_proxy_log_detail_enabled,
    reverse_proxy_log_req_headers_max_bytes: parseNonNegativeInt(
      "请求头最大字节数",
      form.reverse_proxy_log_req_headers_max_bytes,
    ),
    reverse_proxy_log_req_body_max_bytes: parseNonNegativeInt("请求体最大字节数", form.reverse_proxy_log_req_body_max_bytes),
    reverse_proxy_log_resp_headers_max_bytes: parseNonNegativeInt(
      "响应头最大字节数",
      form.reverse_proxy_log_resp_headers_max_bytes,
    ),
    reverse_proxy_log_resp_body_max_bytes: parseNonNegativeInt(
      "响应体最大字节数",
      form.reverse_proxy_log_resp_body_max_bytes,
    ),
    max_consecutive_failures: parseNonNegativeInt("最大连续失败次数", form.max_consecutive_failures),
    max_latency_test_interval: parseDurationField("节点延迟最大测试间隔", form.max_latency_test_interval),
    max_authority_latency_test_interval: parseDurationField(
      "权威域名最大测试间隔",
      form.max_authority_latency_test_interval,
    ),
    max_egress_test_interval: parseDurationField("出口 IP 更新检查间隔", form.max_egress_test_interval),
    latency_test_url: latencyURL,
    latency_authorities: parseAuthorities(form.latency_authorities_raw),
    p2c_latency_window: parseDurationField("P2C 延迟衰减窗口", form.p2c_latency_window),
    latency_decay_window: parseDurationField("历史延迟衰减窗口", form.latency_decay_window),
    cache_flush_interval: parseDurationField("缓存异步刷盘间隔", form.cache_flush_interval),
    cache_flush_dirty_threshold: parseNonNegativeInt("缓存刷盘脏阈值", form.cache_flush_dirty_threshold),
  };
}

function displayAllocationPolicy(value: string): string {
  return ALLOCATION_POLICY_LABELS[value] ?? value;
}

function displayMissAction(value: string): string {
  return MISS_ACTION_LABELS[value] ?? value;
}

function displayEmptyAccountBehavior(value: string): string {
  return EMPTY_ACCOUNT_BEHAVIOR_LABELS[value] ?? value;
}

function arrayEquals(a: string[], b: string[]): boolean {
  if (a.length !== b.length) {
    return false;
  }
  for (let i = 0; i < a.length; i += 1) {
    if (a[i] !== b[i]) {
      return false;
    }
  }
  return true;
}

function buildPatch(current: RuntimeConfig, next: RuntimeConfig): RuntimeConfigPatch {
  const patch: RuntimeConfigPatch = {};
  const patchMutable = patch as Record<string, unknown>;

  for (const field of EDITABLE_FIELDS) {
    const currentValue = current[field];
    const nextValue = next[field];

    if (Array.isArray(currentValue) && Array.isArray(nextValue)) {
      if (!arrayEquals(currentValue, nextValue)) {
        patchMutable[field] = nextValue;
      }
      continue;
    }

    if (currentValue !== nextValue) {
      patchMutable[field] = nextValue;
    }
  }

  return patch;
}

/** One block of related controls: its own panel, so the group is a region and not a heading. */
function ConfigSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Panel className="min-w-0">
      <PanelHeader as="h3" title={title} />
      <PanelBody>
        <div className="grid gap-4 sm:grid-cols-2">{children}</div>
      </PanelBody>
    </Panel>
  );
}

/** A labelled control with the reset action sitting beside its input. */
function ConfigField({
  label,
  htmlFor,
  span,
  restore,
  children,
}: {
  label: string;
  htmlFor: string;
  span?: boolean;
  restore?: ReactNode;
  children: ReactNode;
}) {
  return (
    <Fieldset className={cn(span && "sm:col-span-2")} label={label} htmlFor={htmlFor}>
      <div className="flex items-start gap-1.5">
        <div className="min-w-0 flex-1">{children}</div>
        {restore}
      </div>
    </Fieldset>
  );
}

/** A boolean setting: the label is the switch's accessible name. */
function ConfigSwitch({
  label,
  checked,
  disabled,
  restore,
  onChange,
}: {
  label: string;
  checked: boolean;
  disabled?: boolean;
  restore?: ReactNode;
  onChange: (checked: boolean) => void;
}) {
  return (
    <div className="flex items-center justify-between gap-3 py-2.5">
      <span className="min-w-0 text-sm text-ink">{label}</span>
      <span className="flex shrink-0 items-center gap-1.5">
        {restore}
        <Switch checked={checked} disabled={disabled} aria-label={label} onCheckedChange={onChange} />
      </span>
    </div>
  );
}

/**
 * A read-only value that comes from the deployment environment: the label is the
 * row's name and the value is clipped to one line, because a data directory path
 * is read as a value, not as a paragraph.
 */
function StaticRow({ label, value }: { label: string; value: string }) {
  return (
    <TR>
      <TD className="w-1/3 text-ink-soft">{label}</TD>
      <TDClip className="readout text-xs">{value}</TDClip>
    </TR>
  );
}

/** One read-only block of the deployment configuration. */
function StaticGroup({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="space-y-1.5">
      <SectionTitle>{title}</SectionTitle>
      <TableWrap>
        <Table>
          <TBody>{children}</TBody>
        </Table>
      </TableWrap>
    </div>
  );
}

/**
 * System configuration.
 *
 * A directory of categories that opens into one ruled form: runtime settings
 * live in the top panel, read-only deployment values below it, and the draft
 * (pending changes, JSON escape hatch and save bar) sits at the end so the
 * reader always meets the same three blocks in the same order.
 */
export function SystemConfigPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const [categorySearch, setCategorySearch] = useState("");
  const category = SETTINGS_CATEGORIES.find(item => item.id === params.get("category"));
  const activeCategory = category?.id;
  const selectCategory = (id?: string) => {
    setParams(previous => { const next = new URLSearchParams(previous); if (id) next.set("category", id); else next.delete("category"); return next; }, { replace: true });
    requestAnimationFrame(() => document.getElementById("settings-title")?.focus());
  };
  const [draftForm, setDraftForm] = useState<RuntimeConfigForm | null>(null);
  const [customPatchText, setCustomPatchText] = useState<string | null>(null);
  const { toasts, showToast, dismissToast } = useToast();
  const queryClient = useQueryClient();

  const configQuery = useQuery({
    queryKey: ["system-config"],
    queryFn: getSystemConfig,
    staleTime: 30_000,
  });

  const defaultConfigQuery = useQuery({
    queryKey: ["system-config-default"],
    queryFn: getDefaultSystemConfig,
    staleTime: 30_000,
  });

  const envConfigQuery = useQuery({
    queryKey: ["system-config-env"],
    queryFn: getEnvConfig,
    staleTime: Infinity, // Env config does not change at runtime
  });

  const baseline = configQuery.data ?? null;
  const defaultBaseline = defaultConfigQuery.data ?? null;
  const envBaseline = envConfigQuery.data ?? null;

  const form = useMemo(() => {
    if (!baseline) {
      return null;
    }
    return draftForm ?? configToForm(baseline);
  }, [baseline, draftForm]);

  const parsedResult = useMemo(() => {
    if (!form) {
      return { config: null as RuntimeConfig | null, error: "" };
    }

    try {
      return { config: parseForm(form), error: "" };
    } catch (error) {
      return { config: null, error: formatApiErrorMessage(error, t) };
    }
  }, [form, t]);

  const patchPreview = useMemo<RuntimeConfigPatch>(() => {
    if (!baseline || !parsedResult.config) {
      return {};
    }
    return buildPatch(baseline, parsedResult.config);
  }, [baseline, parsedResult.config]);

  // Dirty state must survive invalid input and category switches.
  const changedKeys = useMemo(() => {
    if (!baseline || !form) return [];
    const previous = configToForm(baseline);
    return EDITABLE_FIELDS.filter(key => {
      const formKey = key === "latency_authorities" ? "latency_authorities_raw" : key;
      return form[formKey] !== previous[formKey];
    });
  }, [baseline, form]);
  const hasUnsavedChanges = changedKeys.length > 0 || customPatchText !== null;
  useBeforeUnload(event => {
    if (hasUnsavedChanges) { event.preventDefault(); event.returnValue = ""; }
  });

  const saveMutation = useMutation({
    mutationFn: async () => {
      if (!baseline || !form) {
        throw new Error("配置尚未加载完成");
      }
      if (customPatchText !== null) throw new Error(t("请先将 JSON 应用到草稿"));
      const patchToSend = buildPatch(baseline, parseForm(form));

      const changedCount = Object.keys(patchToSend).length;
      if (!changedCount) {
        throw new Error("没有可提交的变更");
      }
      const updated = await patchSystemConfig(patchToSend);
      return { updated, changedCount };
    },
    onSuccess: ({ updated, changedCount }) => {
      queryClient.setQueryData(["system-config"], updated);
      setDraftForm(null);
      setCustomPatchText(null);
      showToast("success", t("配置已更新（{{count}} 项变更）", { count: changedCount }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const setFormField = <K extends keyof RuntimeConfigForm>(key: K, value: RuntimeConfigForm[K]) => {
    setDraftForm((prev) => {
      if (!baseline) {
        return prev;
      }
      const source = prev ?? configToForm(baseline);
      return { ...source, [key]: value };
    });
  };

  const handleRestoreDefault = (key: keyof RuntimeConfigForm) => {
    if (!defaultBaseline || !baseline) {
      showToast("error", "默认配置尚未加载");
      return;
    }

    const defaultForm = configToForm(defaultBaseline);
    const value = defaultForm[key];

    setDraftForm((prev) => {
      const source = prev ?? configToForm(baseline);
      return { ...source, [key]: value };
    });
  };

  const renderRestoreButton = (fieldKey: keyof RuntimeConfigForm) => {
    const displayVal = defaultBaseline ? (() => {
      const val = configToForm(defaultBaseline)[fieldKey];
      if (typeof val === "boolean") return val ? t("开启") : t("关闭");
      if (val === "") return t("空");
      return String(val);
    })() : "";

    return (
      <Button
        type="button"
        size="icon"
        variant="quiet"
        className="shrink-0 text-ink-faint hover:text-ink"
        title={displayVal ? t("恢复为默认值: {{value}}", { value: displayVal }) : t("恢复为默认值")}
        aria-label={t("恢复为默认值")}
        onClick={() => handleRestoreDefault(fieldKey)}
      >
        <RotateCcw size={14} aria-hidden />
      </Button>
    );
  };

  const resetDraft = () => {
    setDraftForm(null);
    setCustomPatchText(null);
  };

  const reloadFromServer = async () => {
    if (hasUnsavedChanges) {
      const confirmed = window.confirm(t("当前有未保存变更，确认丢弃并重新加载运行时配置？"));
      if (!confirmed) {
        return;
      }
    }

    setDraftForm(null);
    setCustomPatchText(null);
    const result = await configQuery.refetch();
    if (result.data) {
      showToast("success", t("已加载最新运行时配置"));
    }
  };

  const handlePatchEdit = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    setCustomPatchText(e.target.value);
  };

  const defaultPatchText = useMemo(() => {
    return JSON.stringify(patchPreview, null, 2);
  }, [patchPreview]);

  const displayedPatchText = customPatchText ?? defaultPatchText;

  const applyCustomPatch = () => {
    if (!form || !baseline || customPatchText === null) return;
    try {
      const patch: unknown = JSON.parse(customPatchText);
      if (!patch || Array.isArray(patch) || typeof patch !== "object") throw new Error(t("JSON 必须是配置对象"));
      const candidate = { ...form };
      const mutable = candidate as Record<keyof RuntimeConfigForm, string | boolean>;
      for (const [key, value] of Object.entries(patch)) {
        if (!EDITABLE_FIELDS.includes(key as keyof RuntimeConfig)) throw new Error(t("JSON 包含不支持的配置项") + ": " + key);
        const field = key as keyof RuntimeConfig;
        const current = baseline[field];
        if (Array.isArray(current)) {
          if (!Array.isArray(value) || value.some(item => typeof item !== "string")) throw new Error(t("JSON 配置项类型不正确") + ": " + key);
          mutable.latency_authorities_raw = value.join("\n");
        } else {
          if (typeof value !== typeof current) throw new Error(t("JSON 配置项类型不正确") + ": " + key);
          mutable[field as keyof RuntimeConfigForm] = typeof value === "boolean" ? value : String(value);
        }
      }
      parseForm(candidate);
      setDraftForm(candidate);
      setCustomPatchText(null);
      showToast("success", t("JSON 已应用到草稿，保存后生效"));
    } catch (error) { showToast("error", formatApiErrorMessage(error, t)); }
  };
  const isSaveDisabled = saveMutation.isPending || customPatchText !== null || Boolean(parsedResult.error) || changedKeys.length === 0;
  const visibleCategories = SETTINGS_CATEGORIES.filter(item => {
    const haystack = [t(item.title), t(item.description), ...item.fields.map(field => t(FIELD_LABELS[field]))].join(" ").toLowerCase();
    return haystack.includes(categorySearch.trim().toLowerCase());
  });

  return (
    <Page bleed>
      <PageHeader
        title={
          <span id="settings-title" tabIndex={-1} className="outline-none">
            {t("系统配置")}
          </span>
        }
        description={category ? t(category.description) : t("选择一类设置，集中查看和调整。")}
        meta={
          <>
            <PageMeta label={t("所有配置")} value={t("{{count}} 个配置项", { count: SETTINGS_CATEGORIES.length })} />
            <PageMeta
              label={t("配置草稿")}
              value={changedKeys.length ? t("{{count}} 项待保存", { count: changedKeys.length }) : t("当前无未保存改动")}
            />
          </>
        }
        actions={category ? (
          <Button variant="secondary" onClick={() => selectCategory()}>
            <ArrowLeft size={15} aria-hidden />
            {t("所有配置")}
          </Button>
        ) : undefined}
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="px-4 py-3 lg:px-5 lg:py-4 2xl:px-6 2xl:py-5">
        {!form ? (
          <Panel className="max-w-3xl">
            <PanelBody>
              {configQuery.isLoading || envConfigQuery.isLoading ? (
                <LoadingState label={t("正在加载配置...")} />
              ) : null}
              {configQuery.isError ? (
                <ErrorState
                  message={formatApiErrorMessage(configQuery.error, t)}
                  onRetry={() => void configQuery.refetch()}
                />
              ) : null}
            </PanelBody>
          </Panel>
        ) : (
          <div className="space-y-3">
            {!category && (
              <Panel className="flex min-w-0 flex-col">
                <PanelHeader title={t("所有配置")} description={t("选择一类设置，集中查看和调整。")} />
                <PanelToolbar>
                  <label className="relative block w-full sm:w-72">
                    <Search
                      size={14}
                      aria-hidden
                      className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-ink-faint"
                    />
                    <Input
                      className="h-7 pl-7 text-xs"
                      aria-label={t("搜索配置分类")}
                      placeholder={t("搜索配置分类或参数")}
                      value={categorySearch}
                      onChange={event => setCategorySearch(event.target.value)}
                    />
                  </label>
                  <span className="ml-auto text-xs text-ink-faint">
                    {t("{{count}} 个配置项", { count: visibleCategories.length })}
                  </span>
                </PanelToolbar>
                {visibleCategories.length ? (
                  <ul className="divide-y divide-rule-faint">
                    {visibleCategories.map(item => {
                      const dirty = changedKeys.filter(field => (item.fields as readonly string[]).includes(field)).length;
                      return (
                        <li key={item.id}>
                          <button
                            type="button"
                            className="flex w-full min-w-0 items-center gap-3 px-4 py-2 text-left transition-colors hover:bg-paper-sunk/60"
                            onClick={() => selectCategory(item.id)}
                            aria-label={t(item.title)}
                          >
                            <item.icon size={16} aria-hidden className="shrink-0 text-ink-faint" />
                            <span className="w-40 shrink-0 truncate text-sm font-medium text-ink">{t(item.title)}</span>
                            <span className="min-w-0 flex-1 truncate text-xs text-ink-soft">{t(item.description)}</span>
                            {dirty ? (
                              <Badge tone="warn">{t("{{count}} 项待保存", { count: dirty })}</Badge>
                            ) : (
                              <span className="shrink-0 text-xs text-ink-faint">
                                {t("{{count}} 个配置项", { count: item.fields.length + item.staticCount })}
                              </span>
                            )}
                            <ChevronRight size={15} aria-hidden className="shrink-0 text-ink-faint" />
                          </button>
                        </li>
                      );
                    })}
                  </ul>
                ) : (
                  <PanelBody>
                    <EmptyState
                      className="py-8"
                      title={t("没有匹配的配置分类")}
                      hint={t("搜索配置分类或参数")}
                    />
                  </PanelBody>
                )}
              </Panel>
            )}

            {category && (
              <div className="space-y-3" data-category={category.id}>
                {category.fields.length > 0 && (
                  <div className="space-y-3">
                    <SectionTitle
                      trailing={
                        <Button
                          variant="secondary"
                          size="sm"
                          onClick={() => void reloadFromServer()}
                          disabled={configQuery.isFetching || saveMutation.isPending}
                        >
                          <RefreshCw
                            size={14}
                            aria-hidden
                            className={cn(configQuery.isFetching && "animate-spin")}
                          />
                          {t("重新加载")}
                        </Button>
                      }
                    >
                      <span className="flex items-baseline gap-2">
                        {t(category.title)}
                        <span className="text-xs font-normal text-ink-faint">{t("运行时配置，保存后生效。")}</span>
                      </span>
                    </SectionTitle>

                    <fieldset className="min-w-0 space-y-3" disabled={saveMutation.isPending}>
                      {activeCategory === "health" && (
                        <ConfigSection title={t("基础与健康检查")}>
                          <ConfigField
                            label={t("最大连续失败次数")}
                            htmlFor="sys-max-fail"
                            restore={renderRestoreButton("max_consecutive_failures")}
                          >
                            <Input
                              id="sys-max-fail"
                              className="font-mono"
                              type="number"
                              min={0}
                              value={form.max_consecutive_failures}
                              onChange={(event) => setFormField("max_consecutive_failures", event.target.value)}
                            />
                          </ConfigField>
                        </ConfigSection>
                      )}

                      {activeCategory === "health" && (
                        <ConfigSection title={t("探测与路由")}>
                          <ConfigField
                            span
                            label={t("延迟测试目标 URL")}
                            htmlFor="sys-latency-url"
                            restore={renderRestoreButton("latency_test_url")}
                          >
                            <Input
                              id="sys-latency-url"
                              className="font-mono"
                              value={form.latency_test_url}
                              onChange={(event) => setFormField("latency_test_url", event.target.value)}
                            />
                          </ConfigField>

                          <ConfigField
                            label={t("节点延迟最大测试间隔")}
                            htmlFor="sys-max-latency-int"
                            restore={renderRestoreButton("max_latency_test_interval")}
                          >
                            <Input
                              id="sys-max-latency-int"
                              className="font-mono"
                              value={form.max_latency_test_interval}
                              onChange={(event) => setFormField("max_latency_test_interval", event.target.value)}
                            />
                          </ConfigField>

                          <ConfigField
                            label={t("权威域名最大测试间隔")}
                            htmlFor="sys-max-auth-latency-int"
                            restore={renderRestoreButton("max_authority_latency_test_interval")}
                          >
                            <Input
                              id="sys-max-auth-latency-int"
                              className="font-mono"
                              value={form.max_authority_latency_test_interval}
                              onChange={(event) => setFormField("max_authority_latency_test_interval", event.target.value)}
                            />
                          </ConfigField>

                          <ConfigField
                            label={t("出口 IP 更新检查间隔")}
                            htmlFor="sys-max-egress-int"
                            restore={renderRestoreButton("max_egress_test_interval")}
                          >
                            <Input
                              id="sys-max-egress-int"
                              className="font-mono"
                              value={form.max_egress_test_interval}
                              onChange={(event) => setFormField("max_egress_test_interval", event.target.value)}
                            />
                          </ConfigField>

                          <ConfigField
                            label={t("P2C 延迟衰减窗口")}
                            htmlFor="sys-p2c-window"
                            restore={renderRestoreButton("p2c_latency_window")}
                          >
                            <Input
                              id="sys-p2c-window"
                              className="font-mono"
                              value={form.p2c_latency_window}
                              onChange={(event) => setFormField("p2c_latency_window", event.target.value)}
                            />
                          </ConfigField>

                          <ConfigField
                            label={t("历史延迟衰减窗口")}
                            htmlFor="sys-decay-window"
                            restore={renderRestoreButton("latency_decay_window")}
                          >
                            <Input
                              id="sys-decay-window"
                              className="font-mono"
                              value={form.latency_decay_window}
                              onChange={(event) => setFormField("latency_decay_window", event.target.value)}
                            />
                          </ConfigField>

                          <ConfigField
                            span
                            label={t("延迟测试权威域名列表")}
                            htmlFor="sys-latency-authorities"
                            restore={renderRestoreButton("latency_authorities_raw")}
                          >
                            <Textarea
                              id="sys-latency-authorities"
                              className="font-mono"
                              rows={4}
                              placeholder={"gstatic.com\ngoogle.com\ncloudflare.com"}
                              value={form.latency_authorities_raw}
                              onChange={(event) => setFormField("latency_authorities_raw", event.target.value)}
                            />
                          </ConfigField>
                        </ConfigSection>
                      )}

                      {activeCategory === "logs" && (
                        <ConfigSection title={t("请求日志")}>
                          <div className="divide-y divide-rule-faint border-y border-rule-faint sm:col-span-2">
                            <ConfigSwitch
                              label={t("启用请求日志")}
                              checked={form.request_log_enabled}
                              restore={renderRestoreButton("request_log_enabled")}
                              onChange={(checked) => setFormField("request_log_enabled", checked)}
                            />
                            <ConfigSwitch
                              label={t("记录详细反代日志")}
                              checked={form.reverse_proxy_log_detail_enabled}
                              restore={renderRestoreButton("reverse_proxy_log_detail_enabled")}
                              onChange={(checked) => setFormField("reverse_proxy_log_detail_enabled", checked)}
                            />
                          </div>

                          <ConfigField
                            label={t("请求头最大字节数")}
                            htmlFor="sys-req-h-max"
                            restore={renderRestoreButton("reverse_proxy_log_req_headers_max_bytes")}
                          >
                            <Input
                              id="sys-req-h-max"
                              className="font-mono"
                              type="number"
                              min={0}
                              value={form.reverse_proxy_log_req_headers_max_bytes}
                              onChange={(event) => setFormField("reverse_proxy_log_req_headers_max_bytes", event.target.value)}
                            />
                          </ConfigField>

                          <ConfigField
                            label={t("请求体最大字节数")}
                            htmlFor="sys-req-b-max"
                            restore={renderRestoreButton("reverse_proxy_log_req_body_max_bytes")}
                          >
                            <Input
                              id="sys-req-b-max"
                              className="font-mono"
                              type="number"
                              min={0}
                              value={form.reverse_proxy_log_req_body_max_bytes}
                              onChange={(event) => setFormField("reverse_proxy_log_req_body_max_bytes", event.target.value)}
                            />
                          </ConfigField>

                          <ConfigField
                            label={t("响应头最大字节数")}
                            htmlFor="sys-resp-h-max"
                            restore={renderRestoreButton("reverse_proxy_log_resp_headers_max_bytes")}
                          >
                            <Input
                              id="sys-resp-h-max"
                              className="font-mono"
                              type="number"
                              min={0}
                              value={form.reverse_proxy_log_resp_headers_max_bytes}
                              onChange={(event) => setFormField("reverse_proxy_log_resp_headers_max_bytes", event.target.value)}
                            />
                          </ConfigField>

                          <ConfigField
                            label={t("响应体最大字节数")}
                            htmlFor="sys-resp-b-max"
                            restore={renderRestoreButton("reverse_proxy_log_resp_body_max_bytes")}
                          >
                            <Input
                              id="sys-resp-b-max"
                              className="font-mono"
                              type="number"
                              min={0}
                              value={form.reverse_proxy_log_resp_body_max_bytes}
                              onChange={(event) => setFormField("reverse_proxy_log_resp_body_max_bytes", event.target.value)}
                            />
                          </ConfigField>
                        </ConfigSection>
                      )}

                      {activeCategory === "storage" && (
                        <ConfigSection title={t("持久化策略")}>
                          <ConfigField
                            label={t("缓存异步刷盘间隔")}
                            htmlFor="sys-cache-flush-int"
                            restore={renderRestoreButton("cache_flush_interval")}
                          >
                            <Input
                              id="sys-cache-flush-int"
                              className="font-mono"
                              value={form.cache_flush_interval}
                              onChange={(event) => setFormField("cache_flush_interval", event.target.value)}
                            />
                          </ConfigField>

                          <ConfigField
                            label={t("缓存刷盘脏阈值")}
                            htmlFor="sys-cache-threshold"
                            restore={renderRestoreButton("cache_flush_dirty_threshold")}
                          >
                            <Input
                              id="sys-cache-threshold"
                              className="font-mono"
                              type="number"
                              min={0}
                              value={form.cache_flush_dirty_threshold}
                              onChange={(event) => setFormField("cache_flush_dirty_threshold", event.target.value)}
                            />
                          </ConfigField>
                        </ConfigSection>
                      )}
                    </fieldset>
                  </div>
                )}

                {category.staticCount > 0 && (
                  <>
                    {envConfigQuery.isError && (
                      <ErrorState
                        message={`${t("静态配置加载失败")}: ${formatApiErrorMessage(envConfigQuery.error, t)}`}
                        onRetry={() => void envConfigQuery.refetch()}
                      />
                    )}
                    {envBaseline && (
                      <Panel className="min-w-0">
                        <PanelHeader
                          title={t("启动配置")}
                          description={t("只读参数，修改部署配置并重启服务后生效。")}
                          actions={
                            <Button
                              variant="secondary"
                              size="sm"
                              onClick={() => void envConfigQuery.refetch()}
                              disabled={envConfigQuery.isFetching}
                            >
                              <RefreshCw
                                size={14}
                                aria-hidden
                                className={cn(envConfigQuery.isFetching && "animate-spin")}
                              />
                              {t("刷新")}
                            </Button>
                          }
                        />

                        <PanelBody className="space-y-3">
                          {activeCategory === "deployment" && (
                            <StaticGroup title={t("目录与端口")}>
                              <StaticRow label={t("数据缓存目录")} value={envBaseline.cache_dir} />
                              <StaticRow label={t("状态存储目录")} value={envBaseline.state_dir} />
                              <StaticRow label={t("日志保留目录")} value={envBaseline.log_dir} />
                              <StaticRow label={t("代理 / API 监听地址")} value={envBaseline.listen_address} />
                              <StaticRow label={t("代理 / API 端口")} value={String(envBaseline.prism_port)} />
                            </StaticGroup>
                          )}

                          {activeCategory === "network" && (
                            <StaticGroup title={t("全局限额与性能调优")}>
                              <StaticRow label={t("控制面最大请求体")} value={String(envBaseline.api_max_body_bytes)} />
                              <StaticRow label={t("最大延迟表条目数")} value={String(envBaseline.max_latency_table_entries)} />
                              <StaticRow label={t("节点拨测并发数")} value={String(envBaseline.probe_concurrency)} />
                              <StaticRow label={t("拨测超时时间")} value={envBaseline.probe_timeout} />
                              <StaticRow label={t("资源获取超时时间")} value={envBaseline.resource_fetch_timeout} />
                              <StaticRow label={t("节点 DNS 上游")} value={envBaseline.node_dns_upstreams?.join(", ") || t("无")} />
                              <StaticRow label={t("GeoIP 更新计划")} value={envBaseline.geoip_update_schedule} />
                              <StaticRow label={t("代理传输最大空闲连接")} value={String(envBaseline.proxy_transport_max_idle_conns)} />
                              <StaticRow label={t("单主机最大空闲连接")} value={String(envBaseline.proxy_transport_max_idle_conns_per_host)} />
                              <StaticRow label={t("空闲连接超时时间")} value={envBaseline.proxy_transport_idle_conn_timeout} />
                              <StaticRow label={t("代理直连目标")} value={envBaseline.proxy_bypass_rules?.join(", ") || t("无")} />
                            </StaticGroup>
                          )}

                          {activeCategory === "platform" && (
                            <StaticGroup title={t("默认平台回退规则")}>
                              <StaticRow label={t("默认粘性会话 TTL")} value={envBaseline.default_platform_sticky_ttl} />
                              <StaticRow
                                label={t("默认节点分配策略")}
                                value={t(displayAllocationPolicy(envBaseline.default_platform_allocation_policy))}
                              />
                              <StaticRow
                                label={t("默认反代不匹配行为")}
                                value={t(displayMissAction(envBaseline.default_platform_reverse_proxy_miss_action))}
                              />
                              <StaticRow
                                label={t("默认反代空账号行为")}
                                value={t(displayEmptyAccountBehavior(envBaseline.default_platform_reverse_proxy_empty_account_behavior))}
                              />
                              <StaticRow
                                label={t("默认反代固定账号 Header 列表")}
                                value={envBaseline.default_platform_reverse_proxy_fixed_account_header || t("无")}
                              />
                              <StaticRow
                                label={t("默认正则黑名单")}
                                value={envBaseline.default_platform_regex_filters?.join(", ") || t("无")}
                              />
                              <StaticRow
                                label={t("默认地区黑名单")}
                                value={envBaseline.default_platform_region_filters?.join(",") || t("无")}
                              />
                            </StaticGroup>
                          )}

                          {activeCategory === "logs" && (
                            <StaticGroup title={t("请求日志落库")}>
                              <StaticRow label={t("队列大小")} value={String(envBaseline.request_log_queue_size)} />
                              <StaticRow label={t("落盘批大小")} value={String(envBaseline.request_log_queue_flush_batch_size)} />
                              <StaticRow label={t("落盘间隔")} value={envBaseline.request_log_queue_flush_interval} />
                              <StaticRow label={t("数据库保留阈值")} value={envBaseline.request_log_db_max_mb + " MB"} />
                              <StaticRow label={t("数据库旧分片保留数")} value={String(envBaseline.request_log_db_retain_count)} />
                            </StaticGroup>
                          )}

                          {activeCategory === "metrics" && (
                            <StaticGroup title={t("可观测性指标")}>
                              <StaticRow label={t("吞吐量抽样间隔")} value={envBaseline.metric_throughput_interval_seconds + "s"} />
                              <StaticRow label={t("吞吐量保留时间")} value={envBaseline.metric_throughput_retention_seconds + "s"} />
                              <StaticRow label={t("连接数抽样间隔")} value={envBaseline.metric_connections_interval_seconds + "s"} />
                              <StaticRow label={t("连接数保留时间")} value={envBaseline.metric_connections_retention_seconds + "s"} />
                              <StaticRow label={t("租期与连接指标分桶数")} value={envBaseline.metric_bucket_seconds + "s"} />
                              <StaticRow label={t("租期抽样间隔")} value={envBaseline.metric_leases_interval_seconds + "s"} />
                              <StaticRow label={t("租期保留时间")} value={envBaseline.metric_leases_retention_seconds + "s"} />
                              <StaticRow label={t("延迟统计桶宽")} value={envBaseline.metric_latency_bin_width_ms + "ms"} />
                              <StaticRow label={t("延迟统计截断值")} value={envBaseline.metric_latency_bin_overflow_ms + "ms"} />
                            </StaticGroup>
                          )}

                          {activeCategory === "deployment" && (
                            <div className="space-y-1.5">
                              <SectionTitle>{t("服务鉴权状态")}</SectionTitle>
                              <div className="divide-y divide-rule-faint border-y border-rule-faint">
                                <ConfigSwitch
                                  label={t("已配置管理端令牌")}
                                  checked={envBaseline.admin_token_set}
                                  disabled
                                  onChange={() => undefined}
                                />
                                <ConfigSwitch
                                  label={t("已配置代理令牌")}
                                  checked={envBaseline.proxy_token_set}
                                  disabled
                                  onChange={() => undefined}
                                />
                              </div>
                            </div>
                          )}
                        </PanelBody>
                      </Panel>
                    )}
                  </>
                )}
              </div>
            )}

            <Panel className="min-w-0">
              <PanelHeader
                title={t("配置草稿")}
                description={t(customPatchText !== null ? "请先将 JSON 应用到草稿" : "保存会提交所有分类的草稿更改。")}
                actions={
                  changedKeys.length ? (
                    <Badge tone="warn">{t("{{count}} 项待保存", { count: changedKeys.length })}</Badge>
                  ) : (
                    <span className="text-xs text-ink-faint">{t("当前无未保存改动")}</span>
                  )
                }
              />

              <PanelBody className="space-y-3">
                {changedKeys.length > 0 && (
                  <div className="flex flex-wrap gap-1.5">
                    {changedKeys.map(field => (
                      <Button
                        key={field}
                        type="button"
                        size="sm"
                        variant="secondary"
                        onClick={() => selectCategory(SETTINGS_CATEGORIES.find(item => (item.fields as readonly string[]).includes(field))?.id)}
                      >
                        {t(FIELD_LABELS[field])}
                      </Button>
                    ))}
                  </div>
                )}

                {parsedResult.error && <ErrorState message={parsedResult.error} />}

                <details>
                  <summary className="cursor-pointer text-sm font-medium text-ink-soft select-none hover:text-ink">
                    {t("高级：JSON 变更")}
                    {customPatchText !== null && (
                      <Badge tone="warn" className="ml-2">
                        {t("待应用")}
                      </Badge>
                    )}
                  </summary>
                  <p className="mt-2 max-w-[68ch] text-xs leading-relaxed text-ink-soft">
                    {t("JSON 先应用到草稿，再统一保存。分类切换会保留所有未保存更改。")}
                  </p>
                  <Textarea
                    className="mt-2 font-mono"
                    aria-label={t("JSON 变更内容")}
                    value={displayedPatchText}
                    onChange={handlePatchEdit}
                    rows={7}
                    spellCheck={false}
                    disabled={saveMutation.isPending}
                  />
                  <div className="mt-2 flex flex-wrap gap-1.5">
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={applyCustomPatch}
                      disabled={customPatchText === null || saveMutation.isPending}
                    >
                      {t("应用 JSON 到草稿")}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setCustomPatchText(null)}
                      disabled={customPatchText === null || saveMutation.isPending}
                    >
                      {t("取消 JSON 编辑")}
                    </Button>
                  </div>
                </details>
              </PanelBody>
            </Panel>

            <Panel className="sticky bottom-0 z-10">
              <PanelBody className="flex flex-wrap items-center justify-between gap-3">
                <div className="min-w-0">
                  <p className="text-sm font-medium text-ink">
                    {changedKeys.length
                      ? t("{{count}} 项待保存", { count: changedKeys.length })
                      : t("当前无未保存改动")}
                  </p>
                  <p className="mt-0.5 max-w-[68ch] text-xs leading-relaxed text-ink-soft">
                    {t(customPatchText !== null ? "请先将 JSON 应用到草稿" : "保存会提交所有分类的草稿更改。")}
                  </p>
                </div>
                <div className="flex shrink-0 flex-wrap items-center gap-1.5">
                  <Button variant="ghost" onClick={resetDraft} disabled={!hasUnsavedChanges || saveMutation.isPending}>
                    <RotateCcw size={14} aria-hidden />
                    {t("重置草稿")}
                  </Button>
                  <Button variant="primary" onClick={() => saveMutation.mutate()} disabled={isSaveDisabled}>
                    <Save size={14} aria-hidden />
                    {t(saveMutation.isPending ? "保存中..." : "保存全部更改")}
                  </Button>
                </div>
              </PanelBody>
            </Panel>
          </div>
        )}
      </div>
    </Page>
  );
}