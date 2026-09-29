import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, ChevronLeft, ChevronRight, Info, RefreshCw, Search, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useForm, useFormState, useWatch } from "react-hook-form";
import { useLocation, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input, Textarea } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader, PanelToolbar } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout, ReadoutCell, ReadoutStrip } from "../../components/ui/Readout";
import { Switch } from "../../components/ui/Switch";
import { Table, TableWrap, TBody, TD, TDClip, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../../components/ui/Tabs";
import { ToastContainer } from "../../components/ui/Toast";
import { Tooltip, TooltipProvider } from "../../components/ui/Tooltip";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatDateTime, formatGoDuration, formatRelativeTime } from "../../lib/time";
import {
  clearAllPlatformLeases,
  deletePlatform,
  deletePlatformLease,
  getPlatform,
  listPlatformLeases,
  resetPlatform,
  updatePlatform,
} from "./api";
import {
  allocationPolicies,
  allocationPolicyLabel,
  emptyAccountBehaviorLabel,
  emptyAccountBehaviors,
  missActionLabel,
  missActions,
} from "./constants";
import {
  defaultPlatformFormValues,
  platformFormSchema,
  platformNameRuleHint,
  platformToFormValues,
  toPlatformUpdateInput,
  type PlatformFormValues,
} from "./formModel";
import { NodeCriteriaFields } from "./NodeCriteriaFields";
import { PlatformAccessPanel } from "./PlatformAccessPanel";
import { PlatformMonitorPanel } from "./PlatformMonitorPanel";
import type { PlatformLease } from "./types";

type PlatformDetailTab = "monitor" | "access" | "config" | "ops";

const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
const LEASE_MANAGEMENT_ANCHOR = "platform-lease-management";
const LEASE_SEARCH_DEBOUNCE_MS = 300;
const LEASE_PAGE_SIZE_OPTIONS = [10, 25, 50, 100] as const;
const DETAIL_TABS: Array<{ key: PlatformDetailTab; label: string; hint: string }> = [
  { key: "monitor", label: "监控", hint: "平台运行态趋势和快照" },
  { key: "access", label: "接入", hint: "复制正向/反向代理地址" },
  { key: "config", label: "配置", hint: "过滤规则与分配策略" },
  { key: "ops", label: "运维", hint: "重置、清租约、删除操作" },
];

const selectClass =
  "h-8 w-full rounded-control border border-rule bg-paper-raised px-2 text-sm text-ink";

function PageNavigator({
  page,
  totalPages,
  totalItems,
  pageSize,
  pageSizeOptions,
  disabled,
  onPageChange,
  onPageSizeChange,
}: {
  page: number;
  totalPages: number;
  totalItems: number;
  pageSize: number;
  pageSizeOptions: readonly number[];
  disabled: boolean;
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
    <PanelFooter className="justify-between gap-x-4 gap-y-2">
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
          <select
            className={cn(selectClass, "h-7 w-auto px-1.5 text-xs")}
            value={pageSize}
            disabled={disabled}
            onChange={(event) => onPageSizeChange(Number(event.target.value))}
          >
            {pageSizeOptions.map((size) => (
              <option key={size} value={size}>
                {size}
              </option>
            ))}
          </select>
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
            disabled={disabled}
            className="h-7 w-14 px-1.5 text-xs"
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
          disabled={disabled || current === 0}
          onClick={() => onPageChange(current - 1)}
        >
          <ChevronLeft size={16} />
        </Button>
        <Button
          variant="ghost"
          size="icon"
          aria-label={t("下一页")}
          title={t("下一页")}
          disabled={disabled || current >= pages - 1}
          onClick={() => onPageChange(current + 1)}
        >
          <ChevronRight size={16} />
        </Button>
      </div>
    </PanelFooter>
  );
}

export function PlatformDetailPage() {
  const { t } = useI18n();
  const { platformId = "" } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const rawTab = params.get("tab");
  const activeTab: PlatformDetailTab = location.hash === `#${LEASE_MANAGEMENT_ANCHOR}` ? "ops" : DETAIL_TABS.some((item) => item.key === rawTab) ? rawTab as PlatformDetailTab : "monitor";
  const setActiveTab = (tab: PlatformDetailTab) => setParams({ tab }, { replace: true });
  const [leasePage, setLeasePage] = useState(0);
  const [leasePageSize, setLeasePageSize] = useState<number>(LEASE_PAGE_SIZE_OPTIONS[0]);
  const [leaseSearch, setLeaseSearch] = useState("");
  const [debouncedLeaseSearch, setDebouncedLeaseSearch] = useState("");
  const { toasts, showToast, dismissToast } = useToast();
  const queryClient = useQueryClient();
  const formatPlatformMutationError = (error: unknown) => {
    const base = formatApiErrorMessage(error, t);
    if (base.includes("name:")) {
      return `${base}；${t(platformNameRuleHint)}`;
    }
    return base;
  };

  const platformQuery = useQuery({
    queryKey: ["platform", platformId],
    queryFn: () => getPlatform(platformId),
    enabled: Boolean(platformId),
    refetchInterval: 30_000,
  });

  const platform = platformQuery.data ?? null;

  const leaseQuery = useQuery({
    queryKey: ["platform-leases", platform?.id, leasePage, leasePageSize, debouncedLeaseSearch],
    queryFn: () => {
      if (!platform) {
        throw new Error("平台不存在或已被删除");
      }
      return listPlatformLeases(platform.id, {
        limit: leasePageSize,
        offset: leasePage * leasePageSize,
        account: debouncedLeaseSearch,
        fuzzy: debouncedLeaseSearch ? true : undefined,
        sort_by: "expiry",
        sort_order: "asc",
      });
    },
    enabled: Boolean(platform?.id) && activeTab === "ops",
    refetchInterval: 30_000,
    placeholderData: (previous) => previous,
  });

  const leasesPage = leaseQuery.data ?? {
    items: [],
    total: 0,
    limit: leasePageSize,
    offset: leasePage * leasePageSize,
  };
  const leases = leasesPage.items;
  const isLeasePageTransitioning = leaseQuery.isFetching && leaseQuery.isPlaceholderData;
  const visibleLeases = isLeasePageTransitioning ? [] : leases;
  const leaseTotalPages = Math.max(1, Math.ceil(leasesPage.total / leasePageSize));

  const editForm = useForm<PlatformFormValues>({
    resolver: zodResolver(platformFormSchema),
    defaultValues: defaultPlatformFormValues,
  });
  const detailEmptyAccountBehavior = useWatch({ control: editForm.control, name: "reverse_proxy_empty_account_behavior" });
  const detailPassiveCircuitBreakerDisabled = useWatch({
    control: editForm.control,
    name: "passive_circuit_breaker_disabled",
  });
  const { dirtyFields } = useFormState({ control: editForm.control });
  const hasUnsavedChanges = Object.keys(dirtyFields).length > 0;
  const lastEditedId = useRef("");

  useEffect(() => {
    if (!platform) {
      return;
    }
    editForm.reset(platformToFormValues(platform), { keepDirtyValues: lastEditedId.current === platform.id });
    lastEditedId.current = platform.id;
  }, [platform, editForm]);

  useEffect(() => {
    // Reset pagination when navigating between platform records.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLeasePage(0);
    setLeaseSearch("");
    setDebouncedLeaseSearch("");
  }, [platformId]);

  useEffect(() => {
    const timeoutID = window.setTimeout(() => {
      setDebouncedLeaseSearch(leaseSearch.trim());
      setLeasePage(0);
    }, LEASE_SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timeoutID);
  }, [leaseSearch]);

  useEffect(() => {
    const maxPage = Math.max(0, Math.ceil(leasesPage.total / leasePageSize) - 1);
    if (leasePage > maxPage) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setLeasePage(maxPage);
    }
  }, [leasePage, leasePageSize, leasesPage.total]);

  useEffect(() => {
    if (activeTab !== "ops" || location.hash !== `#${LEASE_MANAGEMENT_ANCHOR}`) {
      return;
    }

    window.requestAnimationFrame(() => {
      document.getElementById(LEASE_MANAGEMENT_ANCHOR)?.scrollIntoView({ block: "start" });
    });
  }, [activeTab, location.hash]);

  const invalidatePlatform = async (id: string) => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["platforms"] }),
      queryClient.invalidateQueries({ queryKey: ["platform", id] }),
    ]);
  };

  const updateMutation = useMutation({
    mutationFn: async (formData: PlatformFormValues) => {
      if (!platform) {
        throw new Error("平台不存在或已被删除");
      }

      return updatePlatform(platform.id, toPlatformUpdateInput(formData));
    },
    onSuccess: async (updated) => {
      await invalidatePlatform(updated.id);
      editForm.reset(platformToFormValues(updated));
      showToast("success", t("平台 {{name}} 已更新", { name: updated.name }));
    },
    onError: (error) => {
      showToast("error", formatPlatformMutationError(error));
    },
  });

  const resetMutation = useMutation({
    mutationFn: async () => {
      if (!platform) {
        throw new Error("平台不存在或已被删除");
      }
      return resetPlatform(platform.id);
    },
    onSuccess: async (updated) => {
      await invalidatePlatform(updated.id);
      editForm.reset(platformToFormValues(updated));
      showToast("success", t("平台 {{name}} 已重置为默认配置", { name: updated.name }));
    },
    onError: (error) => {
      showToast("error", formatPlatformMutationError(error));
    },
  });

  const clearLeasesMutation = useMutation({
    mutationFn: async () => {
      if (!platform) {
        throw new Error("平台不存在或已被删除");
      }
      await clearAllPlatformLeases(platform.id);
      return platform;
    },
    onSuccess: async (updated) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["platform-monitor"] }),
        queryClient.invalidateQueries({ queryKey: ["platform-leases", updated.id] }),
      ]);
      showToast("success", t("平台 {{name}} 的所有租约已清除", { name: updated.name }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const releaseLeaseMutation = useMutation({
    mutationFn: async (lease: PlatformLease) => {
      if (!platform) {
        throw new Error("平台不存在或已被删除");
      }
      await deletePlatformLease(platform.id, lease.account);
      return lease;
    },
    onSuccess: async (lease) => {
      if (platform) {
        await Promise.all([
          queryClient.invalidateQueries({ queryKey: ["platform-monitor"] }),
          queryClient.invalidateQueries({ queryKey: ["platform-leases", platform.id] }),
        ]);
      }
      showToast("success", t("账号 {{account}} 的租约已释放", { account: lease.account }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      if (!platform) {
        throw new Error("平台不存在或已被删除");
      }
      await deletePlatform(platform.id);
      return platform;
    },
    onSuccess: async (deleted) => {
      await queryClient.invalidateQueries({ queryKey: ["platforms"] });
      showToast("success", t("平台 {{name}} 已删除", { name: deleted.name }));
      navigate("/platforms", { replace: true });
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const onEditSubmit = editForm.handleSubmit(async (values) => {
    updateMutation.mutate(values);
  });

  const handleDelete = async () => {
    if (!platform) {
      return;
    }
    if (platform.id === ZERO_UUID) {
      return;
    }
    const confirmed = window.confirm(t("确认删除平台 {{name}}？该操作不可撤销。", { name: platform.name }));
    if (!confirmed) {
      return;
    }
    await deleteMutation.mutateAsync();
  };

  const handleClearAllLeases = async () => {
    if (!platform) {
      return;
    }
    const confirmed = window.confirm(t("确认清除平台 {{name}} 的所有租约？", { name: platform.name }));
    if (!confirmed) {
      return;
    }
    await clearLeasesMutation.mutateAsync();
  };

  const handleReleaseLease = async (lease: PlatformLease) => {
    const confirmed = window.confirm(t("确认释放账号 {{account}} 的租约？", { account: lease.account }));
    if (!confirmed) {
      return;
    }
    await releaseLeaseMutation.mutateAsync(lease);
  };

  const changeLeasePageSize = (next: number) => {
    setLeasePageSize(next);
    setLeasePage(0);
  };

  const stickyTTL = platform ? formatGoDuration(platform.sticky_ttl, t("默认")) : t("默认");
  const regionCount = platform?.region_filters.length ?? 0;
  const regexCount = platform?.regex_filters.length ?? 0;
  const deleteDisabled = !platform || platform.id === ZERO_UUID || deleteMutation.isPending;
  const passiveCircuitBreakerHint = t(
    "开启后，此平台的代理请求失败不会增加节点熔断计数；主动探测不受影响。",
  );
  const fixedAccountHeaderInvalid = Boolean(
    editForm.formState.errors.reverse_proxy_fixed_account_header,
  );

  return (
    <TooltipProvider>
      <Page bleed>
        <Tabs
          value={activeTab}
          onValueChange={(value) => setActiveTab(value as PlatformDetailTab)}
          className="contents"
        >
          <PageHeader
            title={t("平台详情")}
            description={t("调整当前平台策略，并执行维护操作。")}
            meta={
              platform ? (
                <>
                  <Badge tone={platform.id === ZERO_UUID ? "neutral" : "signal"}>
                    {platform.id === ZERO_UUID ? t("内置平台") : t("自定义平台")}
                  </Badge>
                  <PageMeta label={t("同步")} value={formatRelativeTime(platform.updated_at)} />
                </>
              ) : undefined
            }
            actions={
              <>
                <Button variant="secondary" size="sm" onClick={() => navigate("/platforms")}>
                  <ArrowLeft size={15} />
                  {t("返回列表")}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => platformQuery.refetch()}
                  disabled={!platformId || platformQuery.isFetching}
                >
                  <RefreshCw size={15} className={cn(platformQuery.isFetching && "animate-spin")} />
                  {t("刷新")}
                </Button>
              </>
            }
            tabs={
              <TabsList aria-label={t("平台详情板块")}>
                {DETAIL_TABS.map((tab) => (
                  <TabsTrigger key={tab.key} value={tab.key} title={t(tab.hint)}>
                    {t(tab.label)}
                  </TabsTrigger>
                ))}
              </TabsList>
            }
          />

          <ToastContainer toasts={toasts} onDismiss={dismissToast} />

          <div className="flex flex-col gap-3 px-4 py-3 lg:px-5 lg:py-4 2xl:gap-4 2xl:px-6 2xl:py-5">
            {!platformId ? (
              <Panel>
                <PanelBody>
                  <ErrorState message={t("平台 ID 缺失，无法加载详情。")} />
                </PanelBody>
              </Panel>
            ) : null}

            {platformQuery.isError && !platform ? (
              <Panel>
                <PanelBody>
                  <ErrorState
                    message={formatApiErrorMessage(platformQuery.error, t)}
                    onRetry={() => void platformQuery.refetch()}
                  />
                </PanelBody>
              </Panel>
            ) : null}

            {platformQuery.isLoading && !platform ? (
              <Panel>
                <PanelBody>
                  <LoadingState label={t("正在加载平台详情...")} />
                </PanelBody>
              </Panel>
            ) : null}

            {platform ? (
              <>
                <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
                  <h2 className="truncate text-base font-semibold">{platform.name}</h2>
                  <span className="readout truncate text-xs text-ink-faint" title={platform.id}>
                    {platform.id}
                  </span>
                </div>

                <ReadoutStrip>
                  <ReadoutCell>
                    <Readout label={t("区域")} value={regionCount} />
                  </ReadoutCell>
                  <ReadoutCell>
                    <Readout label={t("正则")} value={regexCount} />
                  </ReadoutCell>
                  <ReadoutCell>
                    <Readout label={t("租约时长")} value={stickyTTL} size="sm" />
                  </ReadoutCell>
                  <ReadoutCell>
                    <Readout
                      label={t("策略")}
                      value={t(allocationPolicyLabel[platform.allocation_policy])}
                      size="sm"
                    />
                  </ReadoutCell>
                  <ReadoutCell>
                    <Readout
                      label={t("未命中策略")}
                      value={t(missActionLabel[platform.reverse_proxy_miss_action])}
                      size="sm"
                    />
                  </ReadoutCell>
                  <ReadoutCell>
                    <Readout
                      label={t("空账号行为")}
                      value={t(emptyAccountBehaviorLabel[platform.reverse_proxy_empty_account_behavior])}
                      size="sm"
                    />
                  </ReadoutCell>
                  <ReadoutCell>
                    <Readout
                      label={t("请求失败熔断")}
                      value={platform.passive_circuit_breaker_disabled ? t("已关闭") : t("已开启")}
                      tone={platform.passive_circuit_breaker_disabled ? "warn" : "signal"}
                      size="sm"
                    />
                  </ReadoutCell>
                </ReadoutStrip>

                <TabsContent value="monitor" className="flex flex-col">
                  <PlatformMonitorPanel platform={platform} />
                </TabsContent>

                <TabsContent value="access" className="flex flex-col">
                  <PlatformAccessPanel platformName={platform.name} />
                </TabsContent>

                <TabsContent value="config" className="panel flex flex-col">
                  <PanelHeader
                    title={t("平台配置")}
                    description={t("修改过滤策略与路由策略后点击保存。")}
                    meta={
                      hasUnsavedChanges ? (
                        <Badge tone="warn" dot>
                          {t("保留未保存的修改")}
                        </Badge>
                      ) : undefined
                    }
                  />
                  <form className="space-y-4 px-4 py-3" onSubmit={onEditSubmit}>
                    <div className="grid gap-4 sm:grid-cols-2">
                      <Fieldset
                        label={t("名称")}
                        htmlFor="detail-edit-name"
                        hint={t(platformNameRuleHint)}
                      >
                        <Input
                          id="detail-edit-name"
                          aria-invalid={Boolean(editForm.formState.errors.name) || undefined}
                          className={cn(editForm.formState.errors.name && "border-alert")}
                          {...editForm.register("name")}
                        />
                        {editForm.formState.errors.name?.message ? (
                          <p className="text-xs text-alert">{t(editForm.formState.errors.name.message)}</p>
                        ) : null}
                      </Fieldset>

                      <Fieldset label={t("租约保持时长")} htmlFor="detail-edit-sticky">
                        <Input
                          id="detail-edit-sticky"
                          className={cn(editForm.formState.errors.sticky_ttl && "border-alert")}
                          aria-invalid={Boolean(editForm.formState.errors.sticky_ttl) || undefined}
                          placeholder={t("例如 168h")}
                          {...editForm.register("sticky_ttl")}
                        />
                      </Fieldset>

                      <Fieldset
                        label={t("反向代理账号解析出错策略")}
                        htmlFor="detail-edit-miss-action"
                      >
                        <select
                          id="detail-edit-miss-action"
                          className={selectClass}
                          {...editForm.register("reverse_proxy_miss_action")}
                        >
                          {missActions.map((item) => (
                            <option key={item} value={item}>
                              {t(missActionLabel[item])}
                            </option>
                          ))}
                        </select>
                      </Fieldset>

                      <Fieldset label={t("节点分配策略")} htmlFor="detail-edit-policy">
                        <select
                          id="detail-edit-policy"
                          className={selectClass}
                          {...editForm.register("allocation_policy")}
                        >
                          {allocationPolicies.map((item) => (
                            <option key={item} value={item}>
                              {t(allocationPolicyLabel[item])}
                            </option>
                          ))}
                        </select>
                      </Fieldset>

                      <div className="flex items-start justify-between gap-4 sm:col-span-2">
                        <div className="flex min-w-0 items-center gap-1.5">
                          <label
                            htmlFor="detail-edit-passive-circuit-breaker"
                            className="text-xs font-medium text-ink-soft"
                          >
                            {t("禁用请求失败熔断")}
                          </label>
                          <Tooltip content={passiveCircuitBreakerHint}>
                            <button
                              type="button"
                              aria-label={passiveCircuitBreakerHint}
                              className="grid size-5 place-items-center rounded-control text-ink-faint transition-colors hover:text-ink"
                            >
                              <Info size={13} />
                            </button>
                          </Tooltip>
                        </div>
                        <Switch
                          id="detail-edit-passive-circuit-breaker"
                          checked={Boolean(detailPassiveCircuitBreakerDisabled)}
                          onCheckedChange={(checked) =>
                            editForm.setValue("passive_circuit_breaker_disabled", checked, {
                              shouldDirty: true,
                            })
                          }
                        />
                      </div>

                      <Fieldset
                        label={t("反向代理账号为空行为")}
                        htmlFor="detail-edit-empty-account-behavior"
                      >
                        <select
                          id="detail-edit-empty-account-behavior"
                          className={selectClass}
                          {...editForm.register("reverse_proxy_empty_account_behavior")}
                        >
                          {emptyAccountBehaviors.map((item) => (
                            <option key={item} value={item}>
                              {t(emptyAccountBehaviorLabel[item])}
                            </option>
                          ))}
                        </select>
                      </Fieldset>

                      {detailEmptyAccountBehavior === "FIXED_HEADER" ? (
                        <Fieldset
                          label={t("用于提取 Account 的 Headers（每行一个）")}
                          htmlFor="detail-edit-fixed-account-header"
                          className="sm:col-span-2"
                        >
                          <Textarea
                            id="detail-edit-fixed-account-header"
                            rows={4}
                            placeholder={t("每行一个，例如 Authorization 或 X-Account-Id")}
                            aria-invalid={fixedAccountHeaderInvalid || undefined}
                            className={cn(fixedAccountHeaderInvalid && "border-alert")}
                            {...editForm.register("reverse_proxy_fixed_account_header")}
                          />
                          {editForm.formState.errors.reverse_proxy_fixed_account_header?.message ? (
                            <p className="text-xs text-alert">
                              {t(editForm.formState.errors.reverse_proxy_fixed_account_header.message)}
                            </p>
                          ) : null}
                        </Fieldset>
                      ) : null}

                      {/* Same criteria editor as the create form: AND across
                          criteria, OR inside one criterion's value list, with the
                          live "匹配 N 个节点" preview. */}
                      <div className="sm:col-span-2">
                        <NodeCriteriaFields form={editForm} idPrefix="detail-edit" />
                      </div>
                    </div>

                    <div className="flex items-center gap-2 border-t border-rule pt-3">
                      <Button type="submit" disabled={updateMutation.isPending}>
                        {updateMutation.isPending ? t("保存中...") : t("保存配置")}
                      </Button>
                    </div>
                  </form>
                </TabsContent>

                <TabsContent value="ops" className="flex flex-col gap-3">
                  <Panel className="flex min-w-0 flex-col">
                    <PanelHeader
                      title={t("运维操作")}
                      description={t("以下操作会直接作用于当前平台，请谨慎执行。")}
                    />

                    <div className="divide-y divide-rule">
                      <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
                        <div className="min-w-0">
                          <h3 className="text-sm font-medium">{t("重置为默认配置")}</h3>
                          <p className="mt-0.5 max-w-[68ch] text-xs text-ink-soft">
                            {t("恢复默认设置，并覆盖当前修改。")}
                          </p>
                        </div>
                        <Button
                          variant="secondary"
                          onClick={() => void resetMutation.mutateAsync()}
                          disabled={resetMutation.isPending}
                        >
                          {resetMutation.isPending ? t("重置中...") : t("重置为默认配置")}
                        </Button>
                      </div>

                      <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
                        <div className="min-w-0">
                          <h3 className="text-sm font-medium">{t("清除所有租约")}</h3>
                          <p className="mt-0.5 max-w-[68ch] text-xs text-ink-soft">
                            {t("立即清除当前平台的全部租约，下次请求将重新分配出口。")}
                          </p>
                        </div>
                        <Button
                          variant="danger"
                          onClick={() => void handleClearAllLeases()}
                          disabled={clearLeasesMutation.isPending}
                        >
                          {clearLeasesMutation.isPending ? t("清除中...") : t("清除所有租约")}
                        </Button>
                      </div>

                      <div className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
                        <div className="min-w-0">
                          <h3 className="text-sm font-medium">{t("删除平台")}</h3>
                          <p className="mt-0.5 max-w-[68ch] text-xs text-ink-soft">
                            {t("永久删除当前平台及其配置，操作不可撤销。")}
                          </p>
                        </div>
                        <Button
                          variant="danger"
                          onClick={() => void handleDelete()}
                          disabled={deleteDisabled}
                        >
                          {deleteMutation.isPending ? t("删除中...") : t("删除平台")}
                        </Button>
                      </div>
                    </div>
                  </Panel>

                  <Panel id={LEASE_MANAGEMENT_ANCHOR} className="flex min-w-0 flex-col">
                    <PanelHeader
                      title={t("租约管理")}
                      description={t("查看当前平台的租约绑定，并按账号释放单个租约。")}
                    />

                    <PanelToolbar>
                      <div className="relative w-full sm:w-64">
                        <label htmlFor="platform-lease-search" className="sr-only">
                          {t("搜索账号")}
                        </label>
                        <Search
                          size={14}
                          aria-hidden
                          className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-ink-faint"
                        />
                        <Input
                          id="platform-lease-search"
                          type="search"
                          placeholder={t("搜索账号")}
                          aria-label={t("搜索账号")}
                          value={leaseSearch}
                          onChange={(event) => setLeaseSearch(event.target.value)}
                          className="h-7 pl-7 text-xs"
                        />
                      </div>
                      <Button
                        variant="secondary"
                        size="sm"
                        className="ml-auto"
                        onClick={() => void leaseQuery.refetch()}
                        disabled={leaseQuery.isFetching}
                      >
                        <RefreshCw size={14} className={cn(leaseQuery.isFetching && "animate-spin")} />
                        {t("刷新")}
                      </Button>
                    </PanelToolbar>

                    {leaseQuery.isLoading || isLeasePageTransitioning ? (
                      <PanelBody>
                        <LoadingState label={t("正在加载租约数据...")} />
                      </PanelBody>
                    ) : leaseQuery.isError ? (
                      <PanelBody>
                        <ErrorState
                          message={formatApiErrorMessage(leaseQuery.error, t)}
                          onRetry={() => void leaseQuery.refetch()}
                        />
                      </PanelBody>
                    ) : !visibleLeases.length ? (
                      <PanelBody>
                        <EmptyState
                          title={debouncedLeaseSearch ? t("没有匹配的租约") : t("当前平台暂无租约")}
                        />
                      </PanelBody>
                    ) : (
                      <TableWrap>
                        <Table className="min-w-[760px]">
                          <caption className="sr-only">{t("租约管理")}</caption>
                          <THead>
                            <TR className="hover:bg-transparent">
                              <TH>{t("账号")}</TH>
                              <TH>{t("节点")}</TH>
                              <TH>{t("出口 IP")}</TH>
                              <TH className="text-right">{t("过期时间")}</TH>
                              <TH className="text-right">{t("最后访问")}</TH>
                              <TH className="text-right">{t("操作")}</TH>
                            </TR>
                          </THead>
                          <TBody>
                            {visibleLeases.map((lease) => {
                              const releasing =
                                releaseLeaseMutation.isPending &&
                                releaseLeaseMutation.variables?.account === lease.account;
                              return (
                                <TR key={lease.account}>
                                  <TDClip className="readout text-xs" title={lease.account}>
                                    {lease.account || "-"}
                                  </TDClip>
                                  <TDClip
                                    className="text-xs"
                                    title={lease.node_tag || lease.node_hash}
                                  >
                                    {lease.node_tag || "-"}
                                    <span className="readout text-ink-faint">
                                      {" · "}
                                      {lease.node_hash || "-"}
                                    </span>
                                  </TDClip>
                                  <TD className="readout text-xs">{lease.egress_ip || "-"}</TD>
                                  <TDNum className="text-xs text-ink-soft">
                                    {formatDateTime(lease.expiry)}
                                  </TDNum>
                                  <TDNum className="text-xs text-ink-soft">
                                    {formatDateTime(lease.last_accessed)}
                                  </TDNum>
                                  <TD className="text-right">
                                    <Button
                                      variant="ghost"
                                      size="icon"
                                      onClick={() => void handleReleaseLease(lease)}
                                      disabled={releasing || clearLeasesMutation.isPending}
                                      title={t("释放租约")}
                                      aria-label={t("释放账号 {{account}} 的租约", {
                                        account: lease.account,
                                      })}
                                      className="text-ink-faint hover:bg-alert-wash hover:text-alert"
                                    >
                                      <Trash2 size={14} />
                                    </Button>
                                  </TD>
                                </TR>
                              );
                            })}
                          </TBody>
                        </Table>
                      </TableWrap>
                    )}

                    <PageNavigator
                      page={leasePage}
                      totalPages={leaseTotalPages}
                      totalItems={leasesPage.total}
                      pageSize={leasePageSize}
                      pageSizeOptions={LEASE_PAGE_SIZE_OPTIONS}
                      disabled={isLeasePageTransitioning}
                      onPageChange={setLeasePage}
                      onPageSizeChange={changeLeasePageSize}
                    />
                  </Panel>
                </TabsContent>
              </>
            ) : null}
          </div>
        </Tabs>
      </Page>
    </TooltipProvider>
  );
}