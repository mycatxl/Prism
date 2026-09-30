import { Sheet } from "../../components/ui/Sheet";
import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, Eye, Filter, Info, Pencil, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Controller, useForm, useFormState } from "react-hook-form";
import { Link, useSearchParams } from "react-router-dom";
import { z } from "zod";
import { cn } from "../../lib/cn";
import { Tooltip, TooltipProvider } from "../../components/ui/Tooltip";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader, PanelToolbar } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Fieldset, Input, Textarea } from "../../components/ui/Input";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { Select } from "../../components/ui/Select";
import { Table, TableWrap, TBody, TD, TDClip, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { Tabs, TabsList, TabsTrigger } from "../../components/ui/Tabs";
import { Switch } from "../../components/ui/Switch";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime, formatGoDuration, formatRelativeTime } from "../../lib/time";
import {
  cleanupSubscriptionCircuitOpenNodes,
  createSubscription,
  deleteSubscription,
  listSubscriptions,
  getSubscription,
  getSubscriptionParseReport,
  refreshSubscription,
  updateSubscription,
} from "./api";
import type { Subscription, SubscriptionParseReason } from "./types";

type EnabledFilter = "all" | "enabled" | "disabled";
type SubscriptionSourceType = "remote" | "local";
type SubscriptionEnabledMutation = {
  subscription: Subscription;
  enabled: boolean;
};

const SUBSCRIPTION_SOURCE_TABS: Array<{ key: SubscriptionSourceType; label: string; hint: string }> = [
  { key: "remote", label: "远程", hint: "从 HTTP/HTTPS 订阅链接拉取内容" },
  { key: "local", label: "本地", hint: "直接填写订阅文本，不经过网络拉取" },
];

const subscriptionCreateSchema = z.object({
  name: z.string().trim().min(1, "订阅名称不能为空"),
  source_type: z.enum(["remote", "local"]),
  url: z.string(),
  content: z.string(),
  update_interval: z.string().trim().min(1, "更新间隔不能为空"),
  ephemeral_node_evict_delay: z.string().trim().min(1, "临时节点驱逐延迟不能为空"),
  enabled: z.boolean(),
  ephemeral: z.boolean(),
  incremental_alive_nodes: z.boolean(),
  auto_intel: z.boolean(),
}).superRefine((value, ctx) => {
  const url = value.url.trim();
  const content = value.content.trim();
  if (value.source_type === "remote") {
    if (!url) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["url"], message: "URL 不能为空" });
      return;
    }
    if (!(url.startsWith("http://") || url.startsWith("https://"))) {
      ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["url"], message: "URL 必须是 http/https 地址" });
    }
    return;
  }
  if (!content) {
    ctx.addIssue({ code: z.ZodIssueCode.custom, path: ["content"], message: "订阅内容不能为空" });
  }
});

const subscriptionEditSchema = subscriptionCreateSchema;

type SubscriptionCreateForm = z.infer<typeof subscriptionCreateSchema>;
type SubscriptionEditForm = z.infer<typeof subscriptionEditSchema>;
const EMPTY_SUBSCRIPTIONS: Subscription[] = [];
const PAGE_SIZE_OPTIONS = [10, 20, 50, 100] as const;
const LOCAL_SOURCE_UPDATE_INTERVAL = "12h";
const SUBSCRIPTION_DISABLE_HINT = "禁用订阅后，相关节点不会参与平台路由、健康统计或自动探测。";
const SUBSCRIPTION_EPHEMERAL_HINT = "临时订阅的非健康节点会在一段时间后被自动删除。订阅本身不会被删除。";
const SUBSCRIPTION_INCREMENTAL_HINT = "开启后刷新时保留当前仍存活的旧节点，仅清理失效旧节点，并合并新订阅内容；关闭后仅保留刷新后的订阅内容。";
const SUBSCRIPTION_AUTO_INTEL_HINT = "开启后，该订阅刷新新增的节点会自动进入 Intel 批量检测；关闭后该订阅不会自动排队检测。";
const SUBSCRIPTION_PARSE_REPORT_HINT = "解析报告按原因分组列出被丢弃的节点：原因、数量以及一个有限的名称样例。";

function extractHostname(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return url;
  }
}

function subscriptionToEditForm(subscription: Subscription): SubscriptionEditForm {
  return {
    name: subscription.name,
    source_type: subscription.source_type,
    url: subscription.url,
    content: subscription.content ?? "",
    update_interval: subscription.update_interval,
    ephemeral_node_evict_delay: subscription.ephemeral_node_evict_delay,
    enabled: subscription.enabled,
    ephemeral: subscription.ephemeral,
    incremental_alive_nodes: subscription.incremental_alive_nodes,
    auto_intel: subscription.auto_intel,
  };
}

function sourceTypeLabel(sourceType: SubscriptionSourceType): string {
  return sourceType === "local" ? "本地" : "远程";
}

function parseEnabledFilter(value: EnabledFilter): boolean | undefined {
  if (value === "enabled") {
    return true;
  }
  if (value === "disabled") {
    return false;
  }
  return undefined;
}

function normalizeSubmitUpdateInterval(sourceType: SubscriptionSourceType, raw: string): string {
  if (sourceType === "local") {
    return LOCAL_SOURCE_UPDATE_INTERVAL;
  }
  return raw.trim();
}

// SubscriptionParseReportSection renders the parse report of one subscription:
// the grouped skip summary that every subscription response carries, plus the
// exact (bounded) list from GET /api/v1/subscriptions/{id}/parse-report.
function SubscriptionParseReportSection({ subscription }: { subscription: Subscription }) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const detailQuery = useQuery({
    queryKey: ["subscriptions", "parse-report", subscription.id],
    queryFn: () => getSubscriptionParseReport(subscription.id),
    enabled: expanded,
  });

  const summary = subscription.parse_report ?? null;
  const detail = detailQuery.data;

  return (
    <section>
      <div className="flex items-center gap-1.5">
        <h3 className="text-xs font-medium text-ink-soft">{t("解析报告")}</h3>
        <InfoHint text={t(SUBSCRIPTION_PARSE_REPORT_HINT)} />
      </div>

      {summary === null ? (
        <p className="mt-1 max-w-[68ch] text-xs text-ink-faint">
          {t("该订阅尚未解析；刷新一次后会在这里列出被丢弃的节点及原因。")}
        </p>
      ) : (
        <>
          <p className="mt-1 max-w-[68ch] text-xs text-ink-soft">
            {t("共 {{total}} 个节点：导入 {{imported}} 个，丢弃 {{skipped}} 个。", {
              total: summary.total,
              imported: summary.imported,
              skipped: summary.skipped,
            })}
          </p>

          {summary.reasons.length === 0 ? (
            <p className="mt-1 max-w-[68ch] text-xs text-ink-faint">{t("没有节点被丢弃。")}</p>
          ) : (
            <ul className="mt-2 divide-y divide-rule border-y border-rule">
              {summary.reasons.map((bucket: SubscriptionParseReason) => (
                <li key={bucket.reason} className="py-2">
                  <h4 className="text-xs font-medium">{`${bucket.reason} × ${bucket.count}`}</h4>
                  <p className="mt-0.5 max-w-[68ch] text-xs text-ink-faint">
                    {bucket.sample_names.length > 0
                      ? t("名称样例：{{names}}", { names: bucket.sample_names.join("、") })
                      : t("没有可显示的名称")}
                    {bucket.samples_truncated ? t("（样例已截断）") : ""}
                  </p>
                  {bucket.detail ? (
                    <p className="mt-0.5 max-w-[68ch] text-xs text-ink-faint">{bucket.detail}</p>
                  ) : null}
                </li>
              ))}
            </ul>
          )}

          {summary.skipped_overflow ? (
            <p className="mt-1 max-w-[68ch] text-xs text-ink-faint">
              {t("另有 {{count}} 个被丢弃的节点只计入数量，未记录名称。", {
                count: summary.skipped_overflow,
              })}
            </p>
          ) : null}
          {summary.reasons_overflow ? (
            <p className="mt-1 max-w-[68ch] text-xs text-ink-faint">
              {t("另有 {{count}} 种原因只计入数量。", { count: summary.reasons_overflow })}
            </p>
          ) : null}

          <Button
            variant="ghost"
            size="sm"
            className="mt-1"
            onClick={() => setExpanded((previous) => !previous)}
          >
            {expanded ? t("收起完整列表") : t("查看完整列表")}
          </Button>

          {expanded ? (
            detailQuery.isPending ? (
              <LoadingState />
            ) : detailQuery.error ? (
              <ErrorState
                className="mt-2"
                message={formatApiErrorMessage(detailQuery.error, t)}
                onRetry={() => void detailQuery.refetch()}
              />
            ) : !detail || !detail.parsed ? (
              <p className="mt-1 max-w-[68ch] text-xs text-ink-faint">{t("该订阅尚未解析。")}</p>
            ) : detail.truncated ? (
              <p className="mt-1 max-w-[68ch] text-xs text-ink-faint">
                {t("解析报告超过 64 KiB，只保留了截断标记。")}
              </p>
            ) : (
              <ul className="mt-2 divide-y divide-rule border-y border-rule">
                {detail.skipped.map((entry) => (
                  <li key={`${entry.type}|${entry.name}|${entry.reason}`} className="py-2">
                    <h4 className="text-xs font-medium">
                      {`${entry.name || t("未命名")} · ${entry.type || t("未知类型")}`}
                    </h4>
                    <p className="mt-0.5 max-w-[68ch] text-xs text-ink-faint">{`${entry.reason}: ${entry.detail}`}</p>
                  </li>
                ))}
              </ul>
            )
          ) : null}
        </>
      )}
    </section>
  );
}

/**
 * A hint attached to a label.
 *
 * The icon is a real button inside a Radix tooltip, so the explanation is reachable
 * with the keyboard rather than only by hovering a bare span.
 */
function InfoHint({ text }: { text: string }) {
  return (
    <Tooltip content={text}>
      <Button
        type="button"
        variant="quiet"
        size="icon"
        aria-label={text}
        className="text-ink-faint"
      >
        <Info size={13} aria-hidden />
      </Button>
    </Tooltip>
  );
}

/** One boolean setting: label and hint on the left, the switch on the right. */
function SwitchRow({
  id,
  label,
  hint,
  checked,
  disabled,
  onChange,
}: {
  id: string;
  label: string;
  hint?: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (next: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4 border-t border-rule pt-3 sm:col-span-2">
      <div className="flex min-w-0 items-center gap-1.5">
        <label htmlFor={id} className="text-xs font-medium text-ink-soft">
          {label}
        </label>
        {hint ? <InfoHint text={hint} /> : null}
      </div>
      <Switch id={id} checked={checked} disabled={disabled} onCheckedChange={onChange} />
    </div>
  );
}

/** Offset pagination for this page's table footer. */
function PageNavigator({
  page,
  totalPages,
  totalItems,
  pageSize,
  pageSizeOptions,
  onPageChange,
  onPageSizeChange,
}: {
  page: number;
  totalPages: number;
  totalItems: number;
  pageSize: number;
  pageSizeOptions: readonly number[];
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
}) {
  const { t } = useI18n();
  const pages = Math.max(1, totalPages);
  const current = Math.min(Math.max(0, page), pages - 1);
  const jump = (raw: string) => {
    const value = Number(raw);
    if (Number.isInteger(value) && value > 0) {
      onPageChange(Math.max(0, Math.min(pages - 1, value - 1)));
    }
  };

  return (
    <PanelFooter className="justify-between gap-x-4">
      <p className="text-xs text-ink-soft">
        {t("第 {{page}} / {{pages}} 页 · 显示 {{start}}-{{end}} / {{total}}", {
          page: current + 1,
          pages,
          start: totalItems ? current * pageSize + 1 : 0,
          end: Math.min((current + 1) * pageSize, totalItems),
          total: totalItems,
        })}
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <label className="flex items-center gap-1.5 text-xs text-ink-soft">
          <span>{t("每页")}</span>
          <Select
            className="w-auto px-1.5 text-xs"
            value={pageSize}
            onChange={(event) => onPageSizeChange(Number(event.target.value))}
          >
            {pageSizeOptions.map((size) => (
              <option key={size} value={size}>
                {size}
              </option>
            ))}
          </Select>
        </label>
        <label className="flex items-center gap-1.5 text-xs text-ink-soft">
          <span>{t("跳至")}</span>
          <Input
            key={current}
            type="number"
            inputMode="numeric"
            min={1}
            max={pages}
            defaultValue={current + 1}
            aria-label={t("选择页码")}
            className="h-[var(--control-h)] w-14 px-1.5 text-xs"
            onKeyDown={(event) => {
              if (event.key === "Enter") jump(event.currentTarget.value);
            }}
            onBlur={(event) => {
              jump(event.currentTarget.value);
            }}
          />
        </label>
        <Button
          variant="ghost"
          size="icon"
          aria-label={t("上一页")}
          title={t("上一页")}
          disabled={current === 0}
          onClick={() => onPageChange(current - 1)}
        >
          <ChevronLeft size={16} />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          aria-label={t("下一页")}
          title={t("下一页")}
          disabled={current >= pages - 1}
          onClick={() => onPageChange(current + 1)}
        >
          <ChevronRight size={16} />
        </Button>
      </div>
    </PanelFooter>
  );
}

export function SubscriptionPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const [enabledFilter, setEnabledFilter] = useState<EnabledFilter>("all");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(20);
  const selectedSubscriptionId = params.get("selected") || "";
  const setSelectedSubscriptionId = useCallback((id: string) => setParams((previous) => { const next = new URLSearchParams(previous); if (id) next.set("selected",id); else next.delete("selected"); return next; }, { replace: true }), [setParams]);
  const drawerOpen = Boolean(selectedSubscriptionId);
  const setDrawerOpen = useCallback((open: boolean) => { if (!open) setSelectedSubscriptionId(""); }, [setSelectedSubscriptionId]);
  const createModalOpen = params.get("create") === "1";
  const setCreateModalOpen = (open: boolean) => setParams((previous) => { const next = new URLSearchParams(previous); if (open) next.set("create","1"); else next.delete("create"); return next; }, { replace: true });
  const [pendingRefreshIds, setPendingRefreshIds] = useState<Set<string>>(() => new Set());
  const [pendingEnabledStates, setPendingEnabledStates] = useState<ReadonlyMap<string, boolean>>(() => new Map());
  const { toasts, showToast, dismissToast } = useToast();
  const pendingRefreshIdsRef = useRef<Set<string>>(new Set());
  const pendingEnabledStatesRef = useRef<Map<string, boolean>>(new Map());

  const queryClient = useQueryClient();
  const enabledValue = parseEnabledFilter(enabledFilter);
  const subscriptionContentPlaceholder = [
    t("支持格式："),
    t("sing-box / Clash|Mihomo / URI（vmess:// vless:// trojan:// ss:// ...）或他们的 base64 格式"),
    "",
    t("HTTP/HTTPS/SOCKS 示例："),
    t("1.2.3.4:8080:user:pass （HTTP 认证代理）"),
    t("http://user:pass@1.2.3.4:8080（HTTP 认证代理）"),
    t("https://user:pass@example.com:8443?sni=example.com（HTTPS + SNI）"),
    t("socks5://user:pass@1.2.3.4:1080"),
    t("socks5h://user:pass@example.com:1080"),
  ].join("\n");

  const subscriptionsQuery = useQuery({
    queryKey: ["subscriptions", enabledFilter, page, pageSize, search],
    queryFn: () =>
      listSubscriptions({
        enabled: enabledValue,
        limit: pageSize,
        offset: page * pageSize,
        keyword: search,
      }),
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  });

  const subscriptions = subscriptionsQuery.data?.items ?? EMPTY_SUBSCRIPTIONS;
  const totalSubscriptions = subscriptionsQuery.data?.total ?? 0;

  const totalPages = Math.max(1, Math.ceil(totalSubscriptions / pageSize));
  const currentPage = Math.min(page, totalPages - 1);

  const selectedQuery = useQuery({ queryKey: ["subscriptions", "detail", selectedSubscriptionId], queryFn: () => getSubscription(selectedSubscriptionId), enabled: Boolean(selectedSubscriptionId), refetchInterval: 30_000 });
  const selectedSubscription = useMemo(() => {
    if (!selectedSubscriptionId) {
      return null;
    }
    return selectedQuery.data ?? subscriptions.find((item) => item.id === selectedSubscriptionId) ?? null;
  }, [selectedSubscriptionId, subscriptions, selectedQuery.data]);

  const drawerVisible = drawerOpen && Boolean(selectedSubscription);

  const createForm = useForm<SubscriptionCreateForm>({
    resolver: zodResolver(subscriptionCreateSchema),
    defaultValues: {
      name: "",
      source_type: "remote",
      url: "",
      content: "",
      update_interval: "12h",
      ephemeral_node_evict_delay: "72h",
      enabled: true,
      ephemeral: false,
      incremental_alive_nodes: false,
      auto_intel: true,
    },
  });

  const createEphemeral = createForm.watch("ephemeral");
  const createSourceType = createForm.watch("source_type");

  const editForm = useForm<SubscriptionEditForm>({
    resolver: zodResolver(subscriptionEditSchema),
    defaultValues: {
      name: "",
      source_type: "remote",
      url: "",
      content: "",
      update_interval: "12h",
      ephemeral_node_evict_delay: "72h",
      enabled: true,
      ephemeral: false,
      incremental_alive_nodes: false,
      auto_intel: true,
    },
  });

  const editEphemeral = editForm.watch("ephemeral");
  const editSourceType = editForm.watch("source_type");
  const { dirtyFields } = useFormState({ control: editForm.control });
  const lastEditedId = useRef("");

  useEffect(() => {
    if (!selectedSubscription) {
      return;
    }
    editForm.reset(subscriptionToEditForm(selectedSubscription), { keepDirtyValues: lastEditedId.current === selectedSubscription.id });
    lastEditedId.current = selectedSubscription.id;
  }, [selectedSubscription, editForm]);

  useEffect(() => {
    if (!drawerVisible) {
      return;
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") {
        return;
      }
      setDrawerOpen(false);
    };

    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [drawerVisible, setDrawerOpen]);

  const invalidateSubscriptions = async () => {
    await queryClient.invalidateQueries({ queryKey: ["subscriptions"] });
  };

  const invalidateSubscriptionsAndNodes = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["subscriptions"] }),
      queryClient.invalidateQueries({ queryKey: ["nodes"] }),
    ]);
  };

  const createMutation = useMutation({
    mutationFn: createSubscription,
    onSuccess: async (created) => {
      await invalidateSubscriptions();
      setCreateModalOpen(false);
      createForm.reset({
        name: "",
        source_type: "remote",
        url: "",
        content: "",
        update_interval: LOCAL_SOURCE_UPDATE_INTERVAL,
        ephemeral_node_evict_delay: "72h",
        enabled: true,
        ephemeral: false,
        incremental_alive_nodes: false,
        auto_intel: true,
      });
      showToast("success", t("订阅 {{name}} 创建成功", { name: created.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const updateMutation = useMutation({
    mutationFn: async (formData: SubscriptionEditForm) => {
      if (!selectedSubscription) {
        throw new Error("请选择要编辑的订阅");
      }

      const payload = {
        name: formData.name.trim(),
        update_interval: normalizeSubmitUpdateInterval(formData.source_type, formData.update_interval),
        ephemeral_node_evict_delay: formData.ephemeral_node_evict_delay.trim(),
        enabled: formData.enabled,
        ephemeral: formData.ephemeral,
        incremental_alive_nodes: formData.incremental_alive_nodes,
        auto_intel: formData.auto_intel,
        ...(formData.source_type === "remote"
          ? { url: formData.url.trim() }
          : { content: formData.content }),
      };
      return updateSubscription(selectedSubscription.id, payload);
    },
    onSuccess: async (updated) => {
      await invalidateSubscriptions();
      setSelectedSubscriptionId(updated.id);
      editForm.reset(subscriptionToEditForm(updated));
      showToast("success", t("订阅 {{name}} 已更新", { name: updated.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (subscription: Subscription) => {
      await deleteSubscription(subscription.id);
      return subscription;
    },
    onSuccess: async (deleted) => {
      await invalidateSubscriptions();
      if (selectedSubscriptionId === deleted.id) {
        setSelectedSubscriptionId("");
        setDrawerOpen(false);
      }
      showToast("success", t("订阅 {{name}} 已删除", { name: deleted.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });
  const deleteSubscriptionMutateAsync = deleteMutation.mutateAsync;
  const isDeletePending = deleteMutation.isPending;

  const refreshMutation = useMutation({
    mutationFn: async (subscription: Subscription) => {
      await refreshSubscription(subscription.id);
      return subscription;
    },
    onSuccess: async (subscription) => {
      await invalidateSubscriptions();
      showToast("success", t("订阅 {{name}} 已手动刷新", { name: subscription.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });
  const refreshSubscriptionMutateAsync = refreshMutation.mutateAsync;

  const toggleEnabledMutation = useMutation({
    mutationFn: ({ subscription, enabled }: SubscriptionEnabledMutation) =>
      updateSubscription(subscription.id, { enabled }),
    onSuccess: async (updated, { enabled }) => {
      await invalidateSubscriptionsAndNodes();
      showToast(
        "success",
        enabled
          ? t("订阅 {{name}} 已启用", { name: updated.name })
          : t("订阅 {{name}} 已禁用", { name: updated.name }),
      );
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });
  const toggleSubscriptionEnabledMutateAsync = toggleEnabledMutation.mutateAsync;

  const markRefreshPending = useCallback((subscriptionId: string): boolean => {
    if (pendingRefreshIdsRef.current.has(subscriptionId)) {
      return false;
    }
    const next = new Set(pendingRefreshIdsRef.current);
    next.add(subscriptionId);
    pendingRefreshIdsRef.current = next;
    setPendingRefreshIds(next);
    return true;
  }, []);

  const clearRefreshPending = useCallback((subscriptionId: string) => {
    if (!pendingRefreshIdsRef.current.has(subscriptionId)) {
      return;
    }
    const next = new Set(pendingRefreshIdsRef.current);
    next.delete(subscriptionId);
    pendingRefreshIdsRef.current = next;
    setPendingRefreshIds(next);
  }, []);

  const isRefreshPending = useCallback((subscriptionId: string): boolean => pendingRefreshIds.has(subscriptionId), [pendingRefreshIds]);

  const markEnabledTogglePending = useCallback((subscriptionId: string, enabled: boolean): boolean => {
    if (pendingEnabledStatesRef.current.has(subscriptionId)) {
      return false;
    }
    const next = new Map(pendingEnabledStatesRef.current);
    next.set(subscriptionId, enabled);
    pendingEnabledStatesRef.current = next;
    setPendingEnabledStates(next);
    return true;
  }, []);

  const clearEnabledTogglePending = useCallback((subscriptionId: string) => {
    if (!pendingEnabledStatesRef.current.has(subscriptionId)) {
      return;
    }
    const next = new Map(pendingEnabledStatesRef.current);
    next.delete(subscriptionId);
    pendingEnabledStatesRef.current = next;
    setPendingEnabledStates(next);
  }, []);

  const isEnabledTogglePending = useCallback(
    (subscriptionId: string): boolean => pendingEnabledStates.has(subscriptionId),
    [pendingEnabledStates],
  );

  const displayedEnabledState = useCallback(
    (subscription: Subscription): boolean => pendingEnabledStates.get(subscription.id) ?? subscription.enabled,
    [pendingEnabledStates],
  );

  const cleanupCircuitOpenNodesMutation = useMutation({
    mutationFn: async (subscription: Subscription) => {
      const cleanedCount = await cleanupSubscriptionCircuitOpenNodes(subscription.id);
      return { subscription, cleanedCount };
    },
    onSuccess: async ({ subscription, cleanedCount }) => {
      await invalidateSubscriptionsAndNodes();
      if (cleanedCount > 0) {
        showToast("success", t("订阅 {{name}} 已清理 {{count}} 个节点", { name: subscription.name, count: cleanedCount }));
        return;
      }
      showToast("success", t("订阅 {{name}} 没有可清理的熔断或异常节点", { name: subscription.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const onCreateSubmit = createForm.handleSubmit(async (values) => {
    const payload = {
      name: values.name.trim(),
      source_type: values.source_type,
      update_interval: normalizeSubmitUpdateInterval(values.source_type, values.update_interval),
      ephemeral_node_evict_delay: values.ephemeral_node_evict_delay.trim(),
      enabled: values.enabled,
      ephemeral: values.ephemeral,
      incremental_alive_nodes: values.incremental_alive_nodes,
      auto_intel: values.auto_intel,
      ...(values.source_type === "remote"
        ? { url: values.url.trim() }
        : { content: values.content }),
    };
    createMutation.mutate(payload);
  });

  const onEditSubmit = editForm.handleSubmit(async (values) => {
    updateMutation.mutate(values);
  });

  const handleDelete = useCallback(async (subscription: Subscription) => {
    const confirmed = window.confirm(t("确认删除订阅 {{name}}？关联节点会被清理。", { name: subscription.name }));
    if (!confirmed) {
      return;
    }
    await deleteSubscriptionMutateAsync(subscription);
  }, [deleteSubscriptionMutateAsync, t]);

  const handleCleanupCircuitOpenNodes = async (subscription: Subscription) => {
    const confirmed = window.confirm(t("确认立即清理订阅 {{name}} 中的熔断或异常节点？", { name: subscription.name }));
    if (!confirmed) {
      return;
    }
    await cleanupCircuitOpenNodesMutation.mutateAsync(subscription);
  };

  const openDrawer = useCallback((subscription: Subscription) => {
    setSelectedSubscriptionId(subscription.id);
  }, [setSelectedSubscriptionId]);

  const handleRefresh = useCallback(async (subscription: Subscription) => {
    if (!markRefreshPending(subscription.id)) {
      return;
    }
    try {
      await refreshSubscriptionMutateAsync(subscription);
    } catch {
      // Mutation callbacks already surface the failure to the user.
    } finally {
      clearRefreshPending(subscription.id);
    }
  }, [clearRefreshPending, markRefreshPending, refreshSubscriptionMutateAsync]);

  const handleEnabledChange = useCallback(async (subscription: Subscription, enabled: boolean) => {
    if (!markEnabledTogglePending(subscription.id, enabled)) {
      return;
    }
    try {
      await toggleSubscriptionEnabledMutateAsync({ subscription, enabled });
    } catch {
      // Mutation callbacks already surface the failure to the user.
    } finally {
      clearEnabledTogglePending(subscription.id);
    }
  }, [clearEnabledTogglePending, markEnabledTogglePending, toggleSubscriptionEnabledMutateAsync]);

  const changePageSize = (next: number) => {
    setPageSize(next);
    setPage(0);
  };


  return (
    <Page bleed>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <PageHeader
        title={t("订阅管理")}
        description={t("保障订阅按计划更新，异常时可一键刷新。")}
        meta={<PageMeta label={t("订阅")} value={totalSubscriptions.toLocaleString()} />}
        actions={
          <>
            <Button variant="secondary" size="sm" onClick={() => setCreateModalOpen(true)}>
              <Plus size={14} />
              {t("新建")}
            </Button>
            <Button
              variant="ghost"
              size="icon"
              onClick={() => subscriptionsQuery.refetch()}
              disabled={subscriptionsQuery.isFetching}
              aria-label={t("刷新")}
              title={t("刷新")}
            >
              <RefreshCw size={15} className={cn(subscriptionsQuery.isFetching && "animate-spin")} />
            </Button>
          </>
        }
      />

      <div className="flex min-w-0 flex-1 flex-col gap-3 px-4 py-3 lg:px-5 lg:py-4 2xl:px-6 2xl:py-5">
        <Panel className="flex min-w-0 flex-col">
          <PanelHeader title={t("订阅列表")} />

          <PanelToolbar>
            <div className="flex items-center gap-1.5">
              <Filter size={14} aria-hidden className="text-ink-faint" />
              <Select
                id="sub-status-filter"
                className="w-auto"
                aria-label={t("刷新状态")}
                value={enabledFilter}
                onChange={(event) => {
                  setEnabledFilter(event.target.value as EnabledFilter);
                  setPage(0);
                }}
              >
                <option value="all">{t("全部")}</option>
                <option value="enabled">{t("仅启用")}</option>
                <option value="disabled">{t("仅禁用")}</option>
              </Select>
            </div>
            <div className="relative w-full sm:w-64">
              <label htmlFor="subscription-search" className="sr-only">
                {t("搜索订阅")}
              </label>
              <Search
                size={14}
                aria-hidden
                className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-ink-faint"
              />
              <Input
                id="subscription-search"
                placeholder={t("搜索订阅")}
                value={search}
                onChange={(event) => {
                  setSearch(event.target.value);
                  setPage(0);
                }}
                className="pl-7"
              />
            </div>
          </PanelToolbar>

          {subscriptionsQuery.isLoading ? (
            <PanelBody>
              <LoadingState label={t("正在加载订阅数据...")} />
            </PanelBody>
          ) : subscriptionsQuery.isError ? (
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(subscriptionsQuery.error, t)}
                onRetry={() => void subscriptionsQuery.refetch()}
              />
            </PanelBody>
          ) : !subscriptions.length ? (
            <PanelBody>
              <EmptyState
                title={t("没有匹配的订阅")}
                action={
                  <Button variant="secondary" size="sm" onClick={() => setCreateModalOpen(true)}>
                    <Plus size={14} />
                    {t("新建")}
                  </Button>
                }
              />
            </PanelBody>
          ) : null}

          {subscriptions.length ? (
            <TableWrap>
              <Table className="min-w-[920px]">
                <caption className="sr-only">{t("订阅列表")}</caption>
                <THead>
                  <TR>
                    <TH>{t("名称")}</TH>
                    <TH>{t("订阅源")}</TH>
                    <TH>{t("更新间隔")}</TH>
                    <TH className="text-right">{t("节点数")}</TH>
                    <TH>{t("刷新状态")}</TH>
                    <TH>{t("上次检查")}</TH>
                    <TH>{t("上次更新")}</TH>
                    <TH>{t("启用")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {subscriptions.map((subscription) => {
                    const enabled = displayedEnabledState(subscription);
                    const toggleLabel = enabled
                      ? t("停用订阅 {{name}}", { name: subscription.name })
                      : t("启用订阅 {{name}}", { name: subscription.name });
                    return (
                      <TR
                        key={subscription.id}
                        tabIndex={0}
                        className="cursor-pointer"
                        onClick={() => openDrawer(subscription)}
                        onKeyDown={(event) => {
                          if (event.target !== event.currentTarget) return;
                          if (event.key === "Enter" || event.key === " ") {
                            event.preventDefault();
                            openDrawer(subscription);
                          }
                        }}
                      >
                        <TDClip className="font-medium text-ink">{subscription.name}</TDClip>
                        <TDClip
                          className="font-mono text-xs text-ink-soft"
                          title={
                            subscription.source_type === "local" ? t("本地订阅") : subscription.url
                          }
                        >
                          {subscription.source_type === "local"
                            ? t("本地订阅")
                            : extractHostname(subscription.url)}
                        </TDClip>
                        <TD className="font-mono text-xs text-ink-soft">
                          {formatGoDuration(subscription.update_interval)}
                        </TD>
                        <TDNum className="text-xs">
                          {`${subscription.healthy_node_count} / ${subscription.node_count}`}
                        </TDNum>
                        <TD>
                          {subscription.last_error ? (
                            <Badge tone="alert" dot>
                              {t("错误")}
                            </Badge>
                          ) : subscription.last_checked ? (
                            <Badge tone="signal" dot>
                              {t("正常")}
                            </Badge>
                          ) : (
                            <Badge tone="neutral" dot>
                              {t("未检查")}
                            </Badge>
                          )}
                        </TD>
                        <TDClip className="font-mono text-xs text-ink-soft">
                          {formatRelativeTime(subscription.last_checked || "")}
                        </TDClip>
                        <TDClip className="font-mono text-xs text-ink-soft">
                          {formatRelativeTime(subscription.last_updated || "")}
                        </TDClip>
                        <TD>
                          <div title={toggleLabel} onClick={(event) => event.stopPropagation()}>
                            <Switch
                              checked={enabled}
                              disabled={isEnabledTogglePending(subscription.id)}
                              onCheckedChange={(next) => void handleEnabledChange(subscription, next)}
                              aria-label={toggleLabel}
                            />
                          </div>
                        </TD>
                        <TD className="text-right">
                          <div
                            className="flex items-center justify-end gap-1"
                            onClick={(event) => event.stopPropagation()}
                          >
                            <Button asChild variant="ghost" size="icon" title={t("预览节点池")}>
                              <Link
                                to={`/nodes?subscription_id=${encodeURIComponent(subscription.id)}`}
                                aria-label={t("预览订阅 {{name}} 的节点池", {
                                  name: subscription.name,
                                })}
                              >
                                <Eye size={14} />
                              </Link>
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              onClick={() => openDrawer(subscription)}
                              title={t("编辑")}
                              aria-label={t("编辑")}
                            >
                              <Pencil size={14} />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              onClick={() => void handleRefresh(subscription)}
                              disabled={isRefreshPending(subscription.id)}
                              title={t("刷新")}
                              aria-label={t("刷新")}
                            >
                              <RefreshCw size={14} />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="text-ink-faint hover:bg-alert-wash hover:text-alert"
                              onClick={() => void handleDelete(subscription)}
                              disabled={isDeletePending}
                              title={t("删除")}
                              aria-label={t("删除")}
                            >
                              <Trash2 size={14} />
                            </Button>
                          </div>
                        </TD>
                      </TR>
                    );
                  })}
                </TBody>
              </Table>
            </TableWrap>
          ) : null}

        <PageNavigator
          page={currentPage}
          totalPages={totalPages}
          totalItems={totalSubscriptions}
          pageSize={pageSize}
          pageSizeOptions={PAGE_SIZE_OPTIONS}
          onPageChange={setPage}
          onPageSizeChange={changePageSize}
        />
        </Panel>
      </div>

      {drawerOpen && !selectedSubscription ? (
        <Sheet
          open
          onOpenChange={(open) => {
            if (!open) setDrawerOpen(false);
          }}
          title={t("订阅详情")}
          width="sm"
        >
          {selectedQuery.isLoading ? (
            <LoadingState />
          ) : selectedQuery.error ? (
            <ErrorState
              message={formatApiErrorMessage(selectedQuery.error, t)}
              onRetry={() => void selectedQuery.refetch()}
            />
          ) : null}
        </Sheet>
      ) : null}
      {drawerVisible && selectedSubscription ? (
        <Sheet
          open
          onOpenChange={(open) => {
            if (!open) setDrawerOpen(false);
          }}
          title={t("编辑订阅 {{name}}", { name: selectedSubscription.name })}
          description={<span className="font-mono">{selectedSubscription.id}</span>}
          width="lg"
        >
          <TooltipProvider>
            <div className="space-y-6">
              {Object.keys(dirtyFields).length > 0 ? (
                <Badge tone="warn" dot>
                  {t("保留未保存的修改")}
                </Badge>
              ) : null}

              <section>
                <div>
                  <h2 className="text-sm font-semibold">{t("订阅配置")}</h2>
                  <p className="mt-0.5 max-w-[68ch] text-xs leading-relaxed text-ink-soft">
                    {editSourceType === "local"
                      ? t("更新本地订阅配置、刷新周期与状态开关后点击保存。")
                      : t("更新 URL、刷新周期与状态开关后点击保存。")}
                  </p>
                </div>

                <ReadoutStrip className="mt-3">
                  <ReadoutCell>
                    <Readout
                      label={t("创建时间")}
                      value={formatDateTime(selectedSubscription.created_at)}
                      size="sm"
                    />
                  </ReadoutCell>
                  <ReadoutCell>
                    <Readout
                      label={t("上次检查")}
                      value={formatDateTime(selectedSubscription.last_checked || "")}
                      size="sm"
                    />
                  </ReadoutCell>
                  <ReadoutCell>
                    <Readout
                      label={t("上次更新")}
                      value={formatDateTime(selectedSubscription.last_updated || "")}
                      size="sm"
                    />
                  </ReadoutCell>
                </ReadoutStrip>

                {selectedSubscription.last_error ? (
                  <p className="mt-3 max-w-[68ch] border border-alert/30 bg-alert-wash px-3 py-2 text-xs text-alert">
                    {t("最近错误：{{message}}", { message: selectedSubscription.last_error })}
                  </p>
                ) : (
                  <p className="mt-3 max-w-[68ch] border border-rule bg-signal-wash px-3 py-2 text-xs text-signal-deep">
                    {t("最近一次刷新无错误")}
                  </p>
                )}

                <div className="mt-4 border-t border-rule pt-4">
                  <SubscriptionParseReportSection subscription={selectedSubscription} />
                </div>

                <form className="mt-4 space-y-4" onSubmit={onEditSubmit}>
                  <input type="hidden" {...editForm.register("source_type")} />

                  <Controller
                    control={editForm.control}
                    name="enabled"
                    render={({ field }) => (
                      <SwitchRow
                        id="edit-sub-enabled"
                        label={t("启用")}
                        hint={t(SUBSCRIPTION_DISABLE_HINT)}
                        checked={Boolean(field.value)}
                        onChange={field.onChange}
                      />
                    )}
                  />

                  <Fieldset label={t("订阅名称")} htmlFor="edit-sub-name">
                    <Input
                      id="edit-sub-name"
                      aria-invalid={Boolean(editForm.formState.errors.name) || undefined}
                      className={cn(editForm.formState.errors.name && "border-alert")}
                      {...editForm.register("name")}
                    />
                    {editForm.formState.errors.name?.message ? (
                      <p className="text-xs text-alert">{t(editForm.formState.errors.name.message)}</p>
                    ) : null}
                  </Fieldset>

                  <Fieldset label={t("订阅类型")} htmlFor="edit-sub-source-type">
                    <Input
                      id="edit-sub-source-type"
                      value={t(sourceTypeLabel(editSourceType))}
                      readOnly
                      disabled
                    />
                  </Fieldset>

                  {editSourceType === "remote" ? (
                    <>
                      <Fieldset label={t("更新间隔")} htmlFor="edit-sub-interval">
                        <Input
                          id="edit-sub-interval"
                          placeholder={t("例如 12h")}
                          aria-invalid={
                            Boolean(editForm.formState.errors.update_interval) || undefined
                          }
                          className={cn(
                            editForm.formState.errors.update_interval && "border-alert",
                          )}
                          {...editForm.register("update_interval")}
                        />
                        {editForm.formState.errors.update_interval?.message ? (
                          <p className="text-xs text-alert">
                            {t(editForm.formState.errors.update_interval.message)}
                          </p>
                        ) : null}
                      </Fieldset>

                      <Fieldset label={t("订阅链接")} htmlFor="edit-sub-url">
                        <Input
                          id="edit-sub-url"
                          className={cn(
                            "font-mono",
                            editForm.formState.errors.url && "border-alert",
                          )}
                          aria-invalid={Boolean(editForm.formState.errors.url) || undefined}
                          {...editForm.register("url")}
                        />
                        {editForm.formState.errors.url?.message ? (
                          <p className="text-xs text-alert">
                            {t(editForm.formState.errors.url.message)}
                          </p>
                        ) : null}
                      </Fieldset>
                    </>
                  ) : (
                    <Fieldset label={t("订阅内容")} htmlFor="edit-sub-content">
                      <Textarea
                        id="edit-sub-content"
                        rows={8}
                        className={cn(
                          "font-mono",
                          editForm.formState.errors.content && "border-alert",
                        )}
                        aria-invalid={Boolean(editForm.formState.errors.content) || undefined}
                        placeholder={subscriptionContentPlaceholder}
                        {...editForm.register("content")}
                      />
                      {editForm.formState.errors.content?.message ? (
                        <p className="text-xs text-alert">
                          {t(editForm.formState.errors.content.message)}
                        </p>
                      ) : null}
                    </Fieldset>
                  )}

                  <Controller
                    control={editForm.control}
                    name="ephemeral"
                    render={({ field }) => (
                      <SwitchRow
                        id="edit-sub-ephemeral"
                        label={t("临时订阅")}
                        hint={t(SUBSCRIPTION_EPHEMERAL_HINT)}
                        checked={Boolean(field.value)}
                        onChange={field.onChange}
                      />
                    )}
                  />

                  <Controller
                    control={editForm.control}
                    name="incremental_alive_nodes"
                    render={({ field }) => (
                      <SwitchRow
                        id="edit-sub-incremental-alive-nodes"
                        label={t("存活节点增量模式")}
                        hint={t(SUBSCRIPTION_INCREMENTAL_HINT)}
                        checked={Boolean(field.value)}
                        onChange={field.onChange}
                      />
                    )}
                  />

                  <Controller
                    control={editForm.control}
                    name="auto_intel"
                    render={({ field }) => (
                      <SwitchRow
                        id="edit-sub-auto-intel"
                        label={t("自动 Intel 检测")}
                        hint={t(SUBSCRIPTION_AUTO_INTEL_HINT)}
                        checked={Boolean(field.value)}
                        onChange={field.onChange}
                      />
                    )}
                  />

                  <Fieldset
                    label={t("临时节点驱逐延迟")}
                    htmlFor="edit-sub-ephemeral-evict-delay"
                  >
                    <Input
                      id="edit-sub-ephemeral-evict-delay"
                      placeholder={t("例如 72h")}
                      disabled={!editEphemeral}
                      aria-invalid={
                        Boolean(editForm.formState.errors.ephemeral_node_evict_delay) || undefined
                      }
                      className={cn(
                        editForm.formState.errors.ephemeral_node_evict_delay && "border-alert",
                      )}
                      {...editForm.register("ephemeral_node_evict_delay")}
                    />
                    {editForm.formState.errors.ephemeral_node_evict_delay?.message ? (
                      <p className="text-xs text-alert">
                        {t(editForm.formState.errors.ephemeral_node_evict_delay.message)}
                      </p>
                    ) : null}
                  </Fieldset>

                  <div className="flex items-center gap-2 border-t border-rule pt-3">
                    <Button type="submit" disabled={updateMutation.isPending} loading={updateMutation.isPending}>
                      {t("保存配置")}
                    </Button>
                  </div>
                </form>
              </section>

              <section className="border-t border-rule pt-4">
                <h2 className="text-sm font-semibold">{t("运维操作")}</h2>

                <div className="mt-3 divide-y divide-rule border-y border-rule">
                  <div className="flex flex-wrap items-center justify-between gap-3 py-3">
                    <div className="min-w-0">
                      <h3 className="text-sm font-medium">{t("手动刷新")}</h3>
                      <p className="mt-0.5 max-w-[68ch] text-xs text-ink-soft">
                        {t("立即刷新订阅并同步节点。")}
                      </p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() => void handleRefresh(selectedSubscription)}
                      disabled={isRefreshPending(selectedSubscription.id)}
                      loading={isRefreshPending(selectedSubscription.id)}
                    >
                      {t("立即刷新")}
                    </Button>
                  </div>

                  <div className="flex flex-wrap items-center justify-between gap-3 py-3">
                    <div className="min-w-0">
                      <h3 className="text-sm font-medium">{t("清理失效节点")}</h3>
                      <p className="mt-0.5 max-w-[68ch] text-xs text-ink-soft">
                        {t("立即清理当前熔断，或出错的节点。")}
                      </p>
                    </div>
                    <Button
                      variant="secondary"
                      onClick={() => void handleCleanupCircuitOpenNodes(selectedSubscription)}
                      disabled={cleanupCircuitOpenNodesMutation.isPending}
                    >
                      {cleanupCircuitOpenNodesMutation.isPending
                        ? t("清理中...")
                        : t("立即清理")}
                    </Button>
                  </div>

                  <div className="flex flex-wrap items-center justify-between gap-3 py-3">
                    <div className="min-w-0">
                      <h3 className="text-sm font-medium">{t("删除订阅")}</h3>
                      <p className="mt-0.5 max-w-[68ch] text-xs text-ink-soft">
                        {t("删除订阅并清理关联节点，操作不可撤销。")}
                      </p>
                    </div>
                    <Button
                      variant="danger"
                      onClick={() => void handleDelete(selectedSubscription)}
                      disabled={deleteMutation.isPending}
                      loading={deleteMutation.isPending}
                    >
                      {t("删除订阅")}
                    </Button>
                  </div>
                </div>
              </section>
            </div>
          </TooltipProvider>
        </Sheet>
      ) : null}

      {createModalOpen ? (
        <Sheet
          open
          onOpenChange={(open) => {
            if (!open) setCreateModalOpen(false);
          }}
          title={t("新建订阅")}
          width="md"
          footer={
            <div className="flex items-center justify-end gap-2">
              <Button variant="secondary" onClick={() => setCreateModalOpen(false)}>
                {t("取消")}
              </Button>
              <Button
                type="submit"
                form="subscription-create-form"
                disabled={createMutation.isPending}
                loading={createMutation.isPending}
              >
                {t("确认创建")}
              </Button>
            </div>
          }
        >
          <TooltipProvider>
            <form id="subscription-create-form" className="space-y-4" onSubmit={onCreateSubmit}>
              <input type="hidden" {...createForm.register("source_type")} />

              <Controller
                control={createForm.control}
                name="enabled"
                render={({ field }) => (
                  <SwitchRow
                    id="create-sub-enabled"
                    label={t("启用")}
                    hint={t(SUBSCRIPTION_DISABLE_HINT)}
                    checked={Boolean(field.value)}
                    onChange={field.onChange}
                  />
                )}
              />

              <Fieldset label={t("订阅名称")} htmlFor="create-sub-name">
                <Input
                  id="create-sub-name"
                  aria-invalid={Boolean(createForm.formState.errors.name) || undefined}
                  className={cn(createForm.formState.errors.name && "border-alert")}
                  {...createForm.register("name")}
                />
                {createForm.formState.errors.name?.message ? (
                  <p className="text-xs text-alert">
                    {t(createForm.formState.errors.name.message)}
                  </p>
                ) : null}
              </Fieldset>

              <div className="space-y-1">
                <span className="block text-xs font-medium text-ink-soft">{t("订阅来源")}</span>
                <Tabs
                  value={createSourceType}
                  onValueChange={(value) =>
                    createForm.setValue("source_type", value as SubscriptionSourceType, {
                      shouldDirty: true,
                      shouldValidate: true,
                    })
                  }
                >
                  <TabsList aria-label={t("订阅来源类型")}>
                    {SUBSCRIPTION_SOURCE_TABS.map((tab) => (
                      <TabsTrigger key={tab.key} value={tab.key} title={t(tab.hint)}>
                        {t(tab.label)}
                      </TabsTrigger>
                    ))}
                  </TabsList>
                </Tabs>
              </div>

              {createSourceType === "remote" ? (
                <>
                  <Fieldset label={t("更新间隔")} htmlFor="create-sub-interval">
                    <Input
                      id="create-sub-interval"
                      placeholder={t("例如 12h")}
                      aria-invalid={
                        Boolean(createForm.formState.errors.update_interval) || undefined
                      }
                      className={cn(
                        createForm.formState.errors.update_interval && "border-alert",
                      )}
                      {...createForm.register("update_interval")}
                    />
                    {createForm.formState.errors.update_interval?.message ? (
                      <p className="text-xs text-alert">
                        {t(createForm.formState.errors.update_interval.message)}
                      </p>
                    ) : null}
                  </Fieldset>

                  <Fieldset label={t("订阅链接")} htmlFor="create-sub-url">
                    <Input
                      id="create-sub-url"
                      className={cn("font-mono", createForm.formState.errors.url && "border-alert")}
                      aria-invalid={Boolean(createForm.formState.errors.url) || undefined}
                      {...createForm.register("url")}
                    />
                    {createForm.formState.errors.url?.message ? (
                      <p className="text-xs text-alert">
                        {t(createForm.formState.errors.url.message)}
                      </p>
                    ) : null}
                  </Fieldset>
                </>
              ) : (
                <Fieldset label={t("订阅内容")} htmlFor="create-sub-content">
                  <Textarea
                    id="create-sub-content"
                    rows={8}
                    className={cn("font-mono", createForm.formState.errors.content && "border-alert")}
                    aria-invalid={Boolean(createForm.formState.errors.content) || undefined}
                    placeholder={subscriptionContentPlaceholder}
                    {...createForm.register("content")}
                  />
                  {createForm.formState.errors.content?.message ? (
                    <p className="text-xs text-alert">
                      {t(createForm.formState.errors.content.message)}
                    </p>
                  ) : null}
                </Fieldset>
              )}

              <Controller
                control={createForm.control}
                name="ephemeral"
                render={({ field }) => (
                  <SwitchRow
                    id="create-sub-ephemeral"
                    label={t("临时订阅")}
                    hint={t(SUBSCRIPTION_EPHEMERAL_HINT)}
                    checked={Boolean(field.value)}
                    onChange={field.onChange}
                  />
                )}
              />

              <Controller
                control={createForm.control}
                name="incremental_alive_nodes"
                render={({ field }) => (
                  <SwitchRow
                    id="create-sub-incremental-alive-nodes"
                    label={t("存活节点增量模式")}
                    hint={t(SUBSCRIPTION_INCREMENTAL_HINT)}
                    checked={Boolean(field.value)}
                    onChange={field.onChange}
                  />
                )}
              />

              <Controller
                control={createForm.control}
                name="auto_intel"
                render={({ field }) => (
                  <SwitchRow
                    id="create-sub-auto-intel"
                    label={t("自动 Intel 检测")}
                    hint={t(SUBSCRIPTION_AUTO_INTEL_HINT)}
                    checked={Boolean(field.value)}
                    onChange={field.onChange}
                  />
                )}
              />

              <Fieldset
                label={t("临时节点驱逐延迟")}
                htmlFor="create-sub-ephemeral-evict-delay"
              >
                <Input
                  id="create-sub-ephemeral-evict-delay"
                  placeholder={t("例如 72h")}
                  disabled={!createEphemeral}
                  aria-invalid={
                    Boolean(createForm.formState.errors.ephemeral_node_evict_delay) || undefined
                  }
                  className={cn(
                    createForm.formState.errors.ephemeral_node_evict_delay && "border-alert",
                  )}
                  {...createForm.register("ephemeral_node_evict_delay")}
                />
                {createForm.formState.errors.ephemeral_node_evict_delay?.message ? (
                  <p className="text-xs text-alert">
                    {t(createForm.formState.errors.ephemeral_node_evict_delay.message)}
                  </p>
                ) : null}
              </Fieldset>
            </form>
          </TooltipProvider>
        </Sheet>
      ) : null}
    </Page>
  );
}
