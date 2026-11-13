import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Plus, RefreshCw, Search } from "lucide-react";
import { useMemo } from "react";
import { useForm, useWatch } from "react-hook-form";
import { Navigate, useLocation, useNavigate, useSearchParams } from "react-router-dom";
import {
  ControlButton,
  ControlInput,
  ControlSelect,
  ControlSwitch,
  ControlTextarea,
  EmptyWorkState,
  FilterChip,
  InspectorDrawer,
  ObjectList,
  ObjectRow,
  WorkspacePage,
  StatusMark,
  WorkspaceCard,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/control-plane";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { opsPaths } from "../../lib/paths";
import { formatRelativeTime } from "../../lib/time";
import { adminPlansQueryKey, listPlans } from "../admin/api";
import { getPlatformPulse } from "../dashboard/api";
import { getTimeWindow } from "../dashboard/range";
import { listSubscriptions } from "../subscriptions/api";
import { createPlatform, listPlatforms } from "./api";
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
  toPlatformCreateInput,
  type PlatformFormValues,
} from "./formModel";
import { NodeCriteriaFields } from "./NodeCriteriaFields";
import { platformStatus } from "./platformMeta";

const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
const PAGE_SIZE_OPTIONS = [12, 24, 48, 96] as const;
type PlatformStatusFilter = "all" | "routable" | "empty";

const statusOptions: Array<{ value: PlatformStatusFilter; label: string }> = [
  { value: "all", label: "全部状态" },
  { value: "routable", label: "有可路由节点" },
  { value: "empty", label: "无可路由节点" },
];

function integerParam(value: string | null, fallback: number): number {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : fallback;
}

function PlatformPageNavigator({
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
    <div className="workspace-pagination">
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
          selectedKey={String(pageSize)}
          disabled={disabled}
          items={PAGE_SIZE_OPTIONS.map((size) => ({ id: String(size), label: `${size} / ${t("页")}` }))}
          onSelectionChange={(key) => {
            if (key !== null) onPageSizeChange(Number(key));
          }}
        />
        <ControlButton
          variant="ghost"
          size="sm"
          isIconOnly
          aria-label={t("上一页")}
          isDisabled={disabled || current === 0}
          onPress={() => onPageChange(current - 1)}
        >
          <span aria-hidden>←</span>
        </ControlButton>
        <ControlButton
          variant="ghost"
          size="sm"
          isIconOnly
          aria-label={t("下一页")}
          isDisabled={disabled || current >= totalPages - 1}
          onPress={() => onPageChange(current + 1)}
        >
          <span aria-hidden>→</span>
        </ControlButton>
      </div>
    </div>
  );
}

export function PlatformPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const location = useLocation();
  const [params, setParams] = useSearchParams();
  const { toasts, showToast, dismissToast } = useToast();
  const queryClient = useQueryClient();
  const keyword = params.get("keyword") ?? "";
  const settledKeyword = useDebouncedValue(keyword);
  const statusFilter = (params.get("status") as PlatformStatusFilter | null) ?? "all";
  const status = statusOptions.some((item) => item.value === statusFilter) ? statusFilter : "all";
  const selected = params.get("selected") ?? "";
  const createOpen = params.get("create") === "1";
  const page = integerParam(params.get("page"), 0);
  const rawPageSize = integerParam(params.get("size"), 24);
  const pageSize = PAGE_SIZE_OPTIONS.includes(rawPageSize as (typeof PAGE_SIZE_OPTIONS)[number]) ? rawPageSize : 24;

  const updateParam = (key: string, value: string, resetPage = true) => {
    setParams((previous) => {
      const next = new URLSearchParams(previous);
      if (value) next.set(key, value); else next.delete(key);
      if (resetPage && key !== "page" && key !== "selected" && key !== "create") next.delete("page");
      return next;
    }, { replace: true });
  };
  const openCreate = () => updateParam("create", "1", false);
  const closeCreate = () => updateParam("create", "", false);
  const clearFilters = () => {
    setParams((previous) => {
      const next = new URLSearchParams();
      for (const key of ["selected", "create", "size"]) {
        const value = previous.get(key);
        if (value) next.set(key, value);
      }
      return next;
    }, { replace: true });
  };

  const platformsQuery = useQuery({
    queryKey: ["platforms", "page", page, pageSize, settledKeyword],
    queryFn: () => listPlatforms({ limit: pageSize, offset: page * pageSize, keyword: settledKeyword }),
    placeholderData: (previous) => previous,
    refetchInterval: 30_000,
  });
  const subscriptionsQuery = useQuery({
    queryKey: ["platforms", "upstream"],
    queryFn: () => listSubscriptions({ limit: 200 }),
    staleTime: 60_000,
  });
  const plansQuery = useQuery({ queryKey: adminPlansQueryKey, queryFn: listPlans, staleTime: 60_000 });
  const platformItems = platformsQuery.data?.items ?? [];
  const pulses = useQueries({
    queries: platformItems.map((platform) => ({
      queryKey: ["platforms", "pulse", platform.id],
      queryFn: () => getPlatformPulse(platform.id, getTimeWindow("1h")),
      refetchInterval: 30_000,
      placeholderData: (previous: Awaited<ReturnType<typeof getPlatformPulse>> | undefined) => previous,
    })),
  });
  const activeLeasesById = new Map<string, number | null>();
  platformItems.forEach((platform, index) => activeLeasesById.set(platform.id, pulses[index]?.data?.activeLeases ?? null));
  const subscriptionNameById = useMemo(
    () => new Map((subscriptionsQuery.data?.items ?? []).map((item) => [item.id, item.name])),
    [subscriptionsQuery.data],
  );
  const visiblePlatforms = status === "all"
    ? platformItems
    : platformItems.filter((platform) => (platform.routable_node_count > 0) === (status === "routable"));
  const totalPlatforms = platformsQuery.data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(totalPlatforms / pageSize));
  const currentPage = Math.min(page, totalPages - 1);
  const hasActiveFilters = Boolean(settledKeyword) || status !== "all";

  const createForm = useForm<PlatformFormValues>({
    resolver: zodResolver(platformFormSchema),
    defaultValues: defaultPlatformFormValues,
  });
  const createEmptyAccountBehavior = useWatch({ control: createForm.control, name: "reverse_proxy_empty_account_behavior" });
  const createPassiveCircuitBreakerDisabled = useWatch({ control: createForm.control, name: "passive_circuit_breaker_disabled" });
  const createMissAction = useWatch({ control: createForm.control, name: "reverse_proxy_miss_action" });
  const createAllocationPolicy = useWatch({ control: createForm.control, name: "allocation_policy" });
  const createMutation = useMutation({
    mutationFn: createPlatform,
    onSuccess: async (created) => {
      await queryClient.invalidateQueries({ queryKey: ["platforms"] });
      closeCreate();
      createForm.reset(defaultPlatformFormValues);
      showToast("success", t("平台 {{name}} 创建成功", { name: created.name }));
      navigate(`${opsPaths.platforms}/${created.id}`);
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const onCreateSubmit = createForm.handleSubmit((values) => createMutation.mutate(toPlatformCreateInput(values)));
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["platforms"] });
    void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });
  };
  const statusTone = platformsQuery.isError ? "warn" : platformsQuery.isSuccess ? "live" : "neutral";

  if (selected) return <Navigate replace to={`${opsPaths.platforms}/${encodeURIComponent(selected)}`} />;

  return (
    <WorkspacePage
      eyebrow="Delivery / platform registry"
      title={t("平台")}
      description={t("管理管理员定义的交付平台、节点准入条件、接入关系和租约入口。租户只能使用已授权的平台。")}
      meta={<StatusMark tone={statusTone} label={platformsQuery.isError ? t("读取失败") : platformsQuery.isSuccess ? t("目录已同步") : t("等待目录")} />}
      actions={<>
        <ControlButton variant="secondary" size="sm" onPress={refresh} loading={platformsQuery.isFetching}>
          <RefreshCw size={14} aria-hidden />{t("刷新")}
        </ControlButton>
        <ControlButton variant="primary" size="sm" onPress={openCreate}>
          <Plus size={14} aria-hidden />{t("新建平台")}
        </ControlButton>
      </>}
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <section className="workspace-command workspace-platform-command">
        <div className="workspace-command__body">
          <ControlInput
            label={t("搜索平台")}
            value={keyword}
            onChange={(event) => updateParam("keyword", event.target.value)}
            placeholder={t("按平台名称或标识搜索")}
            className="workspace-platform-command__search"
          />
          <ControlSelect
            label={t("路由状态")}
            selectedKey={status}
            items={statusOptions.map((item) => ({ id: item.value, label: t(item.label) }))}
            onSelectionChange={(key) => updateParam("status", key ?? "")}
          />
        </div>
        <div className="workspace-command__actions">
          <Search size={14} aria-hidden className="workspace-platform-command__icon" />
          {hasActiveFilters ? <ControlButton variant="ghost" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : null}
        </div>
      </section>

      {hasActiveFilters ? (
        <div className="workspace-active-filters" aria-label={t("当前筛选")}>
          {settledKeyword ? <FilterChip label={`${t("关键词")}: ${settledKeyword}`} onRemove={() => updateParam("keyword", "")} /> : null}
          {status !== "all" ? <FilterChip label={`${t("路由状态")}: ${t(statusOptions.find((item) => item.value === status)?.label ?? status)}`} onRemove={() => updateParam("status", "")} /> : null}
        </div>
      ) : null}

      <WorkspaceCard headerless
        eyebrow="Platform objects"
        title={t("平台目录")}
        meta={platformsQuery.data ? t("{{count}} 个平台", { count: totalPlatforms }) : t("未加载")}
        description={t("每个平台都链接到真实节点准入、套餐引用、活动租约和最近变更；选择一行打开 inspector。")}
      >
        {platformsQuery.isError && !platformsQuery.data ? (
          <EmptyWorkState embedded
            title={t("平台目录读取失败")}
            detail={formatApiErrorMessage(platformsQuery.error, t)}
            action={<ControlButton variant="secondary" size="sm" onPress={() => void platformsQuery.refetch()}>{t("重试")}</ControlButton>}
          />
        ) : null}
        {platformsQuery.isLoading && !platformsQuery.data ? (
          <div className="workspace-register-state"><span className="workspace-spinner" aria-hidden />{t("正在读取平台目录")}</div>
        ) : null}
        {!platformsQuery.isLoading && !platformsQuery.isError && visiblePlatforms.length === 0 ? (
          <EmptyWorkState embedded
            title={t(hasActiveFilters ? "没有匹配的平台" : "还没有平台")}
            detail={hasActiveFilters ? t("移除筛选后重新查看管理员定义的平台。") : t("创建第一个平台后，节点准入和租约关系会显示在这里。")}
            action={hasActiveFilters ? <ControlButton variant="secondary" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : <ControlButton variant="primary" size="sm" onPress={openCreate}><Plus size={14} aria-hidden />{t("新建平台")}</ControlButton>}
          />
        ) : null}
        {visiblePlatforms.length > 0 ? (
          <ObjectList className="workspace-platform-list">
            {visiblePlatforms.map((platform) => {
              const state = platformStatus(platform);
              const upstream = platform.subscription_filters.map((id) => subscriptionNameById.get(id) ?? id);
              const planCount = plansQuery.data
                ? plansQuery.data.items.filter((plan) => (plan.platforms ?? []).length === 0 || (plan.platforms ?? []).includes(platform.name)).length
                : null;
              const activeLeases = activeLeasesById.get(platform.id) ?? null;
              return (
                <ObjectRow
                  key={platform.id}
                  to={`${opsPaths.platforms}/${platform.id}`}
                  state={{ returnTo: location.pathname + location.search, fromList: true }}
                  leading={<span className="workspace-platform-row__mark" aria-hidden>{platform.name.slice(0, 1).toUpperCase()}</span>}
                  title={platform.name}
                  subtitle={`${platform.id === ZERO_UUID ? t("内置平台") : t("管理员平台")} · ${upstream[0] ?? t("不限订阅")}`}
                  facts={<><span>{t("可路由")} {platform.routable_node_count.toLocaleString()}</span><span>{t("租约")} {activeLeases === null ? "—" : activeLeases.toLocaleString()}</span><span>{t("套餐")} {planCount === null ? "—" : planCount.toLocaleString()}</span><span>{formatRelativeTime(platform.updated_at)}</span></>}
                  status={<StatusMark tone={state.tone === "alert" ? "alert" : "signal"} label={t(state.label)} />}
                  action={<ArrowRight size={14} aria-hidden />}
                />
              );
            })}
          </ObjectList>
        ) : null}
        {platformsQuery.data && visiblePlatforms.length > 0 ? (
          <PlatformPageNavigator
            page={currentPage}
            totalPages={totalPages}
            totalItems={totalPlatforms}
            pageSize={pageSize}
            disabled={platformsQuery.isFetching}
            onPageChange={(next) => updateParam("page", String(next), false)}
            onPageSizeChange={(next) => { updateParam("size", String(next), false); updateParam("page", "0", false); }}
          />
        ) : null}
      </WorkspaceCard>

      <InspectorDrawer
        open={createOpen}
        onOpenChange={(open) => { if (!open) closeCreate(); }}
        title={t("新建平台")}
        eyebrow={t("Administrator action")}
        footer={<div className="workspace-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={closeCreate}>{t("取消")}</ControlButton><ControlButton type="submit" form="platform-create-form" variant="primary" isDisabled={createMutation.isPending} loading={createMutation.isPending}>{t("确认创建")}</ControlButton></div>}
      >
        <form id="platform-create-form" className="workspace-form-stack" onSubmit={onCreateSubmit}>
          <ControlInput label={t("名称")} hint={t(platformNameRuleHint)} aria-invalid={Boolean(createForm.formState.errors.name) || undefined} {...createForm.register("name")} />
          {createForm.formState.errors.name?.message ? <p className="workspace-form-error">{t(createForm.formState.errors.name.message)}</p> : null}
          <ControlInput label={t("租约保持时长（可选）")} placeholder={t("例如 168h")} {...createForm.register("sticky_ttl")} />
          <ControlSelect
            label={t("反向代理账号解析出错策略")}
            selectedKey={createMissAction}
            items={missActions.map((item) => ({ id: item, label: t(missActionLabel[item]) }))}
            onSelectionChange={(key) => {
              const next = missActions.find((action) => action === key);
              if (next) createForm.setValue("reverse_proxy_miss_action", next, { shouldDirty: true });
            }}
          />
          <ControlSelect
            label={t("节点分配策略")}
            selectedKey={createAllocationPolicy}
            items={allocationPolicies.map((item) => ({ id: item, label: t(allocationPolicyLabel[item]) }))}
            onSelectionChange={(key) => {
              const next = allocationPolicies.find((policy) => policy === key);
              if (next) createForm.setValue("allocation_policy", next, { shouldDirty: true });
            }}
          />
          <ControlSwitch
            isSelected={Boolean(createPassiveCircuitBreakerDisabled)}
            onChange={(value) => createForm.setValue("passive_circuit_breaker_disabled", value, { shouldDirty: true })}
            label={t("禁用请求失败熔断")}
            hint={t("开启后，代理请求失败不会增加节点熔断计数；主动探测不受影响。")}
          />
          <ControlSelect
            label={t("反向代理账号为空行为")}
            selectedKey={createEmptyAccountBehavior}
            items={emptyAccountBehaviors.map((item) => ({ id: item, label: t(emptyAccountBehaviorLabel[item]) }))}
            onSelectionChange={(key) => {
              const next = emptyAccountBehaviors.find((behavior) => behavior === key);
              if (next) createForm.setValue("reverse_proxy_empty_account_behavior", next, { shouldDirty: true });
            }}
          />
          {createEmptyAccountBehavior === "FIXED_HEADER" ? <ControlTextarea label={t("用于提取 Account 的 Headers（每行一个）")} rows={3} placeholder={t("每行一个，例如 Authorization 或 X-Account-Id")} {...createForm.register("reverse_proxy_fixed_account_header")} /> : null}
          <NodeCriteriaFields form={createForm} idPrefix="create" />
        </form>
      </InspectorDrawer>
    </WorkspacePage>
  );
}
