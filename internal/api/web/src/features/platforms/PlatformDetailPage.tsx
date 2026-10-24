import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, RefreshCw, ShieldAlert, Trash2 } from "lucide-react";
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { useForm, useFormState, useWatch } from "react-hook-form";
import { useLocation, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  ControlButton,
  ControlInput,
  ControlSelect,
  ControlSwitch,
  ControlTextarea,
  ControlTabs,
  EmptyWorkState,
  ObjectList,
  ObjectRow,
  PageFrame,
  QueryBadge,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { opsPaths } from "../../lib/paths";
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
import { platformStatus } from "./platformMeta";
import type { Platform, PlatformLease } from "./types";

const PlatformMonitorPanel = lazy(() => import("./PlatformMonitorPanel").then((module) => ({ default: module.PlatformMonitorPanel })));

const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
const LEASE_MANAGEMENT_ANCHOR = "platform-lease-management";
const LEASE_SEARCH_DEBOUNCE_MS = 300;
const LEASE_PAGE_SIZE_OPTIONS = [10, 25, 50, 100] as const;
type PlatformDetailTab = "monitor" | "access" | "config" | "ops";

const detailTabs: Array<{ id: PlatformDetailTab; label: string; count?: string }> = [
  { id: "monitor", label: "监控" },
  { id: "access", label: "接入" },
  { id: "config", label: "配置" },
  { id: "ops", label: "运维" },
];

function PlatformLeaseNavigator({
  page,
  totalPages,
  totalItems,
  pageSize,
  disabled,
  onPageChange,
  onPageSizeChange,
}: {
  page: number;
  totalPages: number;
  totalItems: number;
  pageSize: number;
  disabled: boolean;
  onPageChange: (page: number) => void;
  onPageSizeChange: (size: number) => void;
}) {
  const { t } = useI18n();
  const current = Math.min(Math.max(0, page), Math.max(0, totalPages - 1));
  return (
    <div className="cp-pagination">
      <span>
        {t("第 {{page}} / {{pages}} 页 · {{start}}-{{end}} / {{total}}", {
          page: current + 1,
          pages: Math.max(1, totalPages),
          start: totalItems ? current * pageSize + 1 : 0,
          end: Math.min((current + 1) * pageSize, totalItems),
          total: totalItems,
        })}
      </span>
      <div>
        <ControlSelect
          aria-label={t("每页")}
          value={pageSize}
          disabled={disabled}
          onChange={(event) => onPageSizeChange(Number(event.target.value))}
        >
          {LEASE_PAGE_SIZE_OPTIONS.map((size) => <option key={size} value={size}>{size} / {t("页")}</option>)}
        </ControlSelect>
        <ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("上一页")} isDisabled={disabled || current === 0} onPress={() => onPageChange(current - 1)}>
          <span aria-hidden>←</span>
        </ControlButton>
        <ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("下一页")} isDisabled={disabled || current >= totalPages - 1} onPress={() => onPageChange(current + 1)}>
          <span aria-hidden>→</span>
        </ControlButton>
      </div>
    </div>
  );
}

function PlatformIdentity({ platform }: { platform: Platform }) {
  const { t } = useI18n();
  const state = platformStatus(platform);
  return (
    <div className="cp-platform-identity">
      <span className="cp-platform-row__mark" aria-hidden>{platform.name.slice(0, 1).toUpperCase()}</span>
      <div className="cp-platform-identity__copy">
        <div className="cp-platform-identity__title"><strong>{platform.name}</strong><StatusMark tone={state.tone === "alert" ? "alert" : "signal"} label={t(state.label)} /></div>
        <code>{platform.id}</code>
      </div>
      <QueryBadge tone={platform.id === ZERO_UUID ? "neutral" : "signal"} label={platform.id === ZERO_UUID ? t("内置平台") : t("管理员平台")} />
    </div>
  );
}

export function PlatformDetailPage() {
  const { t } = useI18n();
  const { platformId = "" } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const { toasts, showToast, dismissToast } = useToast();
  const queryClient = useQueryClient();
  const rawTab = params.get("tab");
  const activeTab: PlatformDetailTab = location.hash === `#${LEASE_MANAGEMENT_ANCHOR}`
    ? "ops"
    : detailTabs.some((tab) => tab.id === rawTab)
      ? rawTab as PlatformDetailTab
      : "monitor";
  const setActiveTab = (tab: PlatformDetailTab) => {
    setParams((previous) => {
      const next = new URLSearchParams(previous);
      next.set("tab", tab);
      return next;
    }, { replace: true });
  };
  const [leasePage, setLeasePage] = useState(0);
  const [leasePageSize, setLeasePageSize] = useState<number>(LEASE_PAGE_SIZE_OPTIONS[0]);
  const [leaseSearch, setLeaseSearch] = useState("");
  const [debouncedLeaseSearch, setDebouncedLeaseSearch] = useState("");
  const [confirmingAction, setConfirmingAction] = useState<string | null>(null);

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
      if (!platform) throw new Error("平台不存在或已被删除");
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
  const leasesPage = leaseQuery.data ?? { items: [], total: 0, limit: leasePageSize, offset: leasePage * leasePageSize };
  const leases = leasesPage.items;
  const isLeasePageTransitioning = leaseQuery.isFetching && leaseQuery.isPlaceholderData;
  const leaseTotalPages = Math.max(1, Math.ceil(leasesPage.total / leasePageSize));

  const editForm = useForm<PlatformFormValues>({ resolver: zodResolver(platformFormSchema), defaultValues: defaultPlatformFormValues });
  const detailEmptyAccountBehavior = useWatch({ control: editForm.control, name: "reverse_proxy_empty_account_behavior" });
  const detailPassiveCircuitBreakerDisabled = useWatch({ control: editForm.control, name: "passive_circuit_breaker_disabled" });
  const { dirtyFields } = useFormState({ control: editForm.control });
  const hasUnsavedChanges = Object.keys(dirtyFields).length > 0;
  const lastEditedId = useRef("");

  useEffect(() => {
    if (!platform) return;
    editForm.reset(platformToFormValues(platform), { keepDirtyValues: lastEditedId.current === platform.id });
    lastEditedId.current = platform.id;
  }, [editForm, platform]);

  useEffect(() => {
    setLeasePage(0);
    setLeaseSearch("");
    setDebouncedLeaseSearch("");
  }, [platformId]);

  useEffect(() => {
    const timeoutId = window.setTimeout(() => {
      setDebouncedLeaseSearch(leaseSearch.trim());
      setLeasePage(0);
    }, LEASE_SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timeoutId);
  }, [leaseSearch]);

  useEffect(() => {
    const maxPage = Math.max(0, Math.ceil(leasesPage.total / leasePageSize) - 1);
    if (leasePage > maxPage) setLeasePage(maxPage);
  }, [leasePage, leasePageSize, leasesPage.total]);

  useEffect(() => {
    if (activeTab !== "ops" || location.hash !== `#${LEASE_MANAGEMENT_ANCHOR}`) return;
    window.requestAnimationFrame(() => document.getElementById(LEASE_MANAGEMENT_ANCHOR)?.scrollIntoView({ block: "start" }));
  }, [activeTab, location.hash]);

  const invalidatePlatform = async (id: string) => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["platforms"] }),
      queryClient.invalidateQueries({ queryKey: ["platform", id] }),
    ]);
  };
  const mutationError = (error: unknown) => formatApiErrorMessage(error, t);

  const updateMutation = useMutation({
    mutationFn: async (values: PlatformFormValues) => {
      if (!platform) throw new Error("平台不存在或已被删除");
      return updatePlatform(platform.id, toPlatformUpdateInput(values, platform.quality_policy));
    },
    onSuccess: async (updated) => {
      await invalidatePlatform(updated.id);
      editForm.reset(platformToFormValues(updated));
      showToast("success", t("平台 {{name}} 已更新", { name: updated.name }));
    },
    onError: (error) => showToast("error", mutationError(error)),
  });
  const resetMutation = useMutation({
    mutationFn: async () => {
      if (!platform) throw new Error("平台不存在或已被删除");
      return resetPlatform(platform.id);
    },
    onSuccess: async (updated) => {
      await invalidatePlatform(updated.id);
      editForm.reset(platformToFormValues(updated));
      showToast("success", t("平台 {{name}} 已重置为默认配置", { name: updated.name }));
    },
    onError: (error) => showToast("error", mutationError(error)),
  });
  const clearLeasesMutation = useMutation({
    mutationFn: async () => {
      if (!platform) throw new Error("平台不存在或已被删除");
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
    onError: (error) => showToast("error", mutationError(error)),
  });
  const releaseLeaseMutation = useMutation({
    mutationFn: async (lease: PlatformLease) => {
      if (!platform) throw new Error("平台不存在或已被删除");
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
    onError: (error) => showToast("error", mutationError(error)),
  });
  const deleteMutation = useMutation({
    mutationFn: async () => {
      if (!platform) throw new Error("平台不存在或已被删除");
      await deletePlatform(platform.id);
      return platform;
    },
    onSuccess: async (deleted) => {
      await queryClient.invalidateQueries({ queryKey: ["platforms"] });
      showToast("success", t("平台 {{name}} 已删除", { name: deleted.name }));
      navigate(opsPaths.platforms, { replace: true });
    },
    onError: (error) => showToast("error", mutationError(error)),
  });

  const confirmAndRun = async (key: string, action: () => Promise<void>) => {
    if (confirmingAction) return;
    setConfirmingAction(key);
    try {
      await action();
    } finally {
      setConfirmingAction(null);
    }
  };
  const handleDelete = () => {
    if (!platform || platform.id === ZERO_UUID) return;
    void confirmAndRun("delete", async () => {
      if (!window.confirm(t("确认删除平台 {{name}}？该操作不可撤销。", { name: platform.name }))) return;
      await deleteMutation.mutateAsync();
    });
  };
  const handleClearAllLeases = () => {
    if (!platform) return;
    void confirmAndRun("clear-leases", async () => {
      if (!window.confirm(t("确认清除平台 {{name}} 的所有租约？", { name: platform.name }))) return;
      await clearLeasesMutation.mutateAsync();
    });
  };
  const handleReleaseLease = (lease: PlatformLease) => {
    void confirmAndRun(`release-${lease.account}`, async () => {
      if (!window.confirm(t("确认释放账号 {{account}} 的租约？", { account: lease.account }))) return;
      await releaseLeaseMutation.mutateAsync(lease);
    });
  };
  const handleReset = () => {
    void confirmAndRun("reset", async () => {
      if (!window.confirm(t("确认将平台 {{name}} 重置为默认配置？", { name: platform?.name ?? "" }))) return;
      await resetMutation.mutateAsync();
    });
  };
  const changeLeasePageSize = (next: number) => {
    setLeasePageSize(next);
    setLeasePage(0);
  };
  const onEditSubmit = editForm.handleSubmit((values) => updateMutation.mutate(values));
  const status = platform ? platformStatus(platform) : null;
  const pageTone = platformQuery.isError ? "warn" : platform ? (status?.tone === "alert" ? "alert" : "live") : "neutral";
  const deleteDisabled = !platform || platform.id === ZERO_UUID || deleteMutation.isPending || Boolean(confirmingAction);
  const fixedAccountHeaderInvalid = Boolean(editForm.formState.errors.reverse_proxy_fixed_account_header);

  return (
    <PageFrame
      eyebrow="Delivery / platform object"
      title={platform?.name ?? t("平台详情")}
      description={t("查看平台到节点池、接入地址和租约状态的关系；配置与危险操作保持在独立工作面。")}
      meta={<StatusMark tone={pageTone} label={platformQuery.isError ? t("读取失败") : platform ? t(status?.label ?? "未知") : t("等待对象")} />}
      actions={<>
        <ControlButton variant="secondary" size="sm" onPress={() => navigate(opsPaths.platforms)}><ArrowLeft size={14} aria-hidden />{t("返回平台目录")}</ControlButton>
        <ControlButton variant="secondary" size="sm" onPress={() => void platformQuery.refetch()} isDisabled={!platformId || platformQuery.isFetching} loading={platformQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>
      </>}
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      {!platformId ? <EmptyWorkState title={t("平台 ID 缺失")} detail={t("无法加载平台详情。")} /> : null}
      {platformQuery.isError && !platform ? <EmptyWorkState title={t("平台详情读取失败")} detail={formatApiErrorMessage(platformQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void platformQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
      {platformQuery.isLoading && !platform ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在读取平台详情")}</div> : null}

      {platform ? (
        <div className="cp-platform-detail">
          <WorkSurface eyebrow="Platform identity" title={<PlatformIdentity platform={platform} />} description={t("对象身份、路由状态和最近同步时间来自平台 API；未返回的关系保持未知。")}>
            <div className="cp-data-grid">
              <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("可路由节点")}</span><strong className="cp-data-grid__value">{platform.routable_node_count.toLocaleString()}</strong><span className="cp-data-grid__hint">{t("当前节点池视图")}</span></div>
              <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("租约保持")}</span><strong className="cp-data-grid__value">{formatGoDuration(platform.sticky_ttl, t("默认"))}</strong><span className="cp-data-grid__hint">{t("平台策略")}</span></div>
              <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("分配策略")}</span><strong className="cp-data-grid__value cp-data-grid__value--text">{t(allocationPolicyLabel[platform.allocation_policy])}</strong><span className="cp-data-grid__hint">{t("新租约选择")}</span></div>
              <div className="cp-data-grid__cell"><span className="cp-data-grid__label">{t("最近更新")}</span><strong className="cp-data-grid__value cp-data-grid__value--text">{formatRelativeTime(platform.updated_at)}</strong><span className="cp-data-grid__hint">{formatDateTime(platform.updated_at)}</span></div>
            </div>
          </WorkSurface>

          <ControlTabs label={t("平台详情板块")} selectedKey={activeTab} onSelectionChange={(key) => setActiveTab(key as PlatformDetailTab)} items={detailTabs} />

          {activeTab === "monitor" ? (
            <Suspense fallback={<div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载监控")}</div>}>
              <PlatformMonitorPanel platform={platform} />
            </Suspense>
          ) : null}

          {activeTab === "access" ? <PlatformAccessPanel platformName={platform.name} /> : null}

          {activeTab === "config" ? (
            <WorkSurface
              eyebrow="Policy / node admission"
              title={t("平台配置")}
              meta={hasUnsavedChanges ? <QueryBadge tone="warn" label={t("有未保存修改")} /> : <QueryBadge label={t("配置已同步")} />}
              description={t("条件同时生效，scope preview 使用与保存请求相同的字段；历史节点名规则不会被新请求写入。")}
              actions={<ControlButton variant="primary" size="sm" type="submit" form="platform-edit-form" isDisabled={updateMutation.isPending || !hasUnsavedChanges} loading={updateMutation.isPending}>{t("保存配置")}</ControlButton>}
            >
              <form id="platform-edit-form" className="cp-form-stack" onSubmit={onEditSubmit}>
                <div className="cp-form-grid">
                  <ControlInput label={t("名称")} hint={t(platformNameRuleHint)} aria-invalid={Boolean(editForm.formState.errors.name) || undefined} {...editForm.register("name")} />
                  <ControlInput label={t("租约保持时长")} placeholder={t("例如 168h")} aria-invalid={Boolean(editForm.formState.errors.sticky_ttl) || undefined} {...editForm.register("sticky_ttl")} />
                  <ControlSelect label={t("反向代理账号解析出错策略")} {...editForm.register("reverse_proxy_miss_action")}>
                    {missActions.map((item) => <option key={item} value={item}>{t(missActionLabel[item])}</option>)}
                  </ControlSelect>
                  <ControlSelect label={t("节点分配策略")} {...editForm.register("allocation_policy")}>
                    {allocationPolicies.map((item) => <option key={item} value={item}>{t(allocationPolicyLabel[item])}</option>)}
                  </ControlSelect>
                  <ControlSwitch isSelected={Boolean(detailPassiveCircuitBreakerDisabled)} onChange={(value) => editForm.setValue("passive_circuit_breaker_disabled", value, { shouldDirty: true })} label={t("禁用请求失败熔断")} hint={t("开启后，代理请求失败不会增加节点熔断计数；主动探测不受影响。")} />
                  <ControlSelect label={t("反向代理账号为空行为")} {...editForm.register("reverse_proxy_empty_account_behavior")}>
                    {emptyAccountBehaviors.map((item) => <option key={item} value={item}>{t(emptyAccountBehaviorLabel[item])}</option>)}
                  </ControlSelect>
                </div>
                {editForm.formState.errors.name?.message ? <p className="cp-form-error">{t(editForm.formState.errors.name.message)}</p> : null}
                {detailEmptyAccountBehavior === "FIXED_HEADER" ? <ControlTextarea label={t("用于提取 Account 的 Headers（每行一个）")} rows={4} placeholder={t("每行一个，例如 Authorization 或 X-Account-Id")} aria-invalid={fixedAccountHeaderInvalid || undefined} {...editForm.register("reverse_proxy_fixed_account_header")} /> : null}
                {editForm.formState.errors.reverse_proxy_fixed_account_header?.message ? <p className="cp-form-error">{t(editForm.formState.errors.reverse_proxy_fixed_account_header.message)}</p> : null}
                <NodeCriteriaFields form={editForm} idPrefix="detail-edit" />
                {platform.regex_filters.length > 0 ? <p className="cp-legacy-note">{t("当前对象包含历史节点名规则。它仍由后端兼容读取，但本次保存不会创建、编辑或清除该规则。")}</p> : null}
              </form>
            </WorkSurface>
          ) : null}

          {activeTab === "ops" ? (
            <div className="cp-stack">
              <WorkSurface eyebrow="Operations / guarded" title={t("危险操作")} description={t("这些动作会直接改变平台路由或租约状态，均需要二次确认并显示操作结果。")}>
                <div className="cp-danger-list">
                  <div className="cp-danger-row"><div><strong>{t("重置为默认配置")}</strong><p>{t("恢复环境默认策略并覆盖当前可编辑配置。")}</p></div><ControlButton variant="secondary" size="sm" onPress={handleReset} isDisabled={Boolean(confirmingAction) || resetMutation.isPending} loading={resetMutation.isPending}>{t("重置")}</ControlButton></div>
                  <div className="cp-danger-row cp-danger-row--warn"><div><strong>{t("清除所有租约")}</strong><p>{t("清除当前平台全部租约，下次请求将重新分配出口。")}</p></div><ControlButton variant="danger" size="sm" onPress={handleClearAllLeases} isDisabled={Boolean(confirmingAction) || clearLeasesMutation.isPending} loading={clearLeasesMutation.isPending}><ShieldAlert size={14} aria-hidden />{t("清除全部")}</ControlButton></div>
                  <div className="cp-danger-row cp-danger-row--alert"><div><strong>{t("删除平台")}</strong><p>{platform.id === ZERO_UUID ? t("内置平台不可删除。") : t("永久删除当前平台及其配置，操作不可撤销。")}</p></div><ControlButton variant="danger" size="sm" onPress={handleDelete} isDisabled={deleteDisabled} loading={deleteMutation.isPending}><Trash2 size={14} aria-hidden />{t("删除平台")}</ControlButton></div>
                </div>
              </WorkSurface>

              <WorkSurface eyebrow="Evidence / leases" title={t("租约管理")} description={t("按账号查看当前平台绑定的节点和出口，并释放单个租约。") }>
                <div id={LEASE_MANAGEMENT_ANCHOR} className="cp-command cp-lease-command">
                  <div className="cp-command__body"><ControlInput label={t("搜索账号")} value={leaseSearch} onChange={(event) => setLeaseSearch(event.target.value)} placeholder={t("按账号精确或模糊查找")} /></div>
                  <div className="cp-command__actions"><ControlButton variant="secondary" size="sm" onPress={() => void leaseQuery.refetch()} loading={leaseQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton></div>
                </div>
                {leaseQuery.isLoading || isLeasePageTransitioning ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载租约")}</div> : null}
                {leaseQuery.isError ? <EmptyWorkState title={t("租约读取失败")} detail={formatApiErrorMessage(leaseQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void leaseQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
                {!leaseQuery.isLoading && !leaseQuery.isError && leases.length === 0 ? <EmptyWorkState title={debouncedLeaseSearch ? t("没有匹配的租约") : t("当前平台暂无租约")} detail={debouncedLeaseSearch ? t("尝试清除账号筛选。") : t("租约会在租户请求平台后出现。")} /> : null}
                {leases.length > 0 ? (
                  <ObjectList className="cp-lease-list">
                    {leases.map((lease) => {
                      const releasing = releaseLeaseMutation.isPending && releaseLeaseMutation.variables?.account === lease.account;
                      const rowBusy = releasing || clearLeasesMutation.isPending || confirmingAction === `release-${lease.account}`;
                      return (
                        <ObjectRow
                          key={lease.account}
                          leading={<span className="cp-lease-row__mark" aria-hidden>↗</span>}
                          title={lease.account || t("未命名账号")}
                          subtitle={`${lease.node_tag || t("节点标识未知")} · ${lease.node_hash || t("节点哈希未知")}`}
                          facts={<><span>{t("出口")} {lease.egress_ip || "—"}</span><span>{t("过期")} {formatDateTime(lease.expiry)}</span><span>{t("最后访问")} {formatDateTime(lease.last_accessed)}</span></>}
                          action={<ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("释放账号 {{account}} 的租约", { account: lease.account })} onPress={() => handleReleaseLease(lease)} isDisabled={rowBusy}><Trash2 size={14} aria-hidden /></ControlButton>}
                        />
                      );
                    })}
                  </ObjectList>
                ) : null}
                <PlatformLeaseNavigator page={leasePage} totalPages={leaseTotalPages} totalItems={leasesPage.total} pageSize={leasePageSize} disabled={isLeasePageTransitioning} onPageChange={setLeasePage} onPageSizeChange={changeLeasePageSize} />
              </WorkSurface>
            </div>
          ) : null}
        </div>
      ) : null}
    </PageFrame>
  );
}
