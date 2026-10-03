import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, Info, Plus, RefreshCw, Search } from "lucide-react";
import { useState } from "react";
import { useForm, useWatch } from "react-hook-form";
import { useNavigate } from "react-router-dom";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input, Textarea } from "../../components/ui/Input";
import { Page, PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader, PanelToolbar } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import { Sheet } from "../../components/ui/Sheet";
import { Switch } from "../../components/ui/Switch";
import { Table, TableWrap, TBody, TDClip, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { Tooltip, TooltipProvider } from "../../components/ui/Tooltip";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { cn } from "../../lib/cn";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatGoDuration } from "../../lib/time";
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
import type { Platform } from "./types";

const ZERO_UUID = "00000000-0000-0000-0000-000000000000";
const EMPTY_PLATFORMS: Platform[] = [];
const PAGE_SIZE_OPTIONS = [12, 24, 48, 96] as const;

/**
 * Offset pagination for this page's table footer.
 *
 * It lives here rather than in the shared kit because it is a layout of kit
 * primitives (a labelled page-size select, a page jump and two icon buttons) that
 * only pages with a table need.
 */
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
          <Select
            className="w-auto px-1.5 text-xs"
            value={pageSize}
            disabled={disabled}
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
            disabled={disabled}
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

export function PlatformPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(24);
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const { toasts, showToast, dismissToast } = useToast();

  const queryClient = useQueryClient();
  const formatPlatformMutationError = (error: unknown) => {
    const base = formatApiErrorMessage(error, t);
    if (base.includes("name:")) {
      return `${base}；${t(platformNameRuleHint)}`;
    }
    return base;
  };

  const platformsQuery = useQuery({
    queryKey: ["platforms", "page", page, pageSize, search],
    queryFn: () =>
      listPlatforms({
        limit: pageSize,
        offset: page * pageSize,
        keyword: search,
      }),
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  });

  const platforms = platformsQuery.data?.items ?? EMPTY_PLATFORMS;

  const totalPlatforms = platformsQuery.data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(totalPlatforms / pageSize));
  const currentPage = Math.min(page, totalPages - 1);

  const createForm = useForm<PlatformFormValues>({
    resolver: zodResolver(platformFormSchema),
    defaultValues: defaultPlatformFormValues,
  });
  const createEmptyAccountBehavior = useWatch({ control: createForm.control, name: "reverse_proxy_empty_account_behavior" });
  const createPassiveCircuitBreakerDisabled = useWatch({
    control: createForm.control,
    name: "passive_circuit_breaker_disabled",
  });

  const createMutation = useMutation({
    mutationFn: createPlatform,
    onSuccess: async (created) => {
      await queryClient.invalidateQueries({ queryKey: ["platforms"] });
      setCreateModalOpen(false);
      createForm.reset();
      showToast("success", t("平台 {{name}} 创建成功", { name: created.name }));
      navigate(`/platforms/${created.id}`);
    },
    onError: (error) => {
      showToast("error", formatPlatformMutationError(error));
    },
  });

  const onCreateSubmit = createForm.handleSubmit(async (values) => {
    createMutation.mutate(toPlatformCreateInput(values));
  });

  const changePageSize = (next: number) => {
    setPageSize(next);
    setPage(0);
  };

  const passiveCircuitBreakerHint = t(
    "开启后，此平台的代理请求失败不会增加节点熔断计数；主动探测不受影响。",
  );

  return (
    <TooltipProvider>
      <Page bleed>
        <PageHeader
          title={t("平台管理")}
          actions={
            <>
              <Button variant="secondary" size="sm" onClick={() => setCreateModalOpen(true)}>
                <Plus size={14} />
                {t("新建")}
              </Button>
              <Button
                variant="ghost"
                size="icon"
                onClick={() => platformsQuery.refetch()}
                disabled={platformsQuery.isFetching}
                aria-label={t("刷新")}
                title={t("刷新")}
              >
                <RefreshCw size={15} className={cn(platformsQuery.isFetching && "animate-spin")} />
              </Button>
            </>
          }
        />

        <ToastContainer toasts={toasts} onDismiss={dismissToast} />

        <div className="page-content page-content--fill">
          <Panel className="flex min-w-0 flex-col">
            <PanelHeader
              title={t("平台列表")}
            />

            <PanelToolbar>
              <div className="relative w-full sm:w-64">
                <label htmlFor="platform-search" className="sr-only">
                  {t("搜索平台")}
                </label>
                <Search
                  size={14}
                  aria-hidden
                  className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-ink-faint"
                />
                <Input
                  id="platform-search"
                  placeholder={t("搜索平台")}
                  value={search}
                  onChange={(event) => {
                    setSearch(event.target.value);
                    setPage(0);
                  }}
                  className="h-[var(--control-h)] pl-7 text-xs"
                />
              </div>
            </PanelToolbar>

            {platformsQuery.isLoading ? (
              <PanelBody>
                <LoadingState label={t("正在加载")} />
              </PanelBody>
            ) : platformsQuery.isError ? (
              <PanelBody>
                <ErrorState
                  onRetry={() => void platformsQuery.refetch()}
                  message={
                    <>
                      <span className="font-medium">{t("数据暂时不可用")}</span>
                      <p className="mt-0.5">{t("请检查连接后重试。")}</p>
                    </>
                  }
                />
              </PanelBody>
            ) : platforms.length === 0 ? (
              <PanelBody>
                <EmptyState
                  title={t("没有匹配的平台")}
                  hint={t("共 {{count}} 个平台", { count: totalPlatforms })}
                  action={
                    <Button variant="secondary" size="sm" onClick={() => setCreateModalOpen(true)}>
                      <Plus size={14} />
                      {t("新建")}
                    </Button>
                  }
                />
              </PanelBody>
            ) : (
              <TableWrap>
                <Table>
                  <caption className="sr-only">{t("平台列表")}</caption>
                  <THead>
                    <TR className="hover:bg-transparent">
                      <TH>{t("平台")}</TH>
                      <TH>{t("标签规则")}</TH>
                      <TH>{t("地区")}</TH>
                      <TH>{t("策略")}</TH>
                      <TH className="text-right">{t("可用节点")}</TH>
                      <TH className="text-right">{t("租约时长")}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {platforms.map((platform) => {
                      const open = () => navigate(`/platforms/${platform.id}`);
                      const policy = t(
                        platform.id === ZERO_UUID
                          ? "内置平台"
                          : allocationPolicyLabel[platform.allocation_policy],
                      );
                      const tagRules = platform.regex_filters.join("  ");
                      const regions = platform.region_filters.join(" / ").toUpperCase();
                      return (
                        <TR
                          key={platform.id}
                          tabIndex={0}
                          className="cursor-pointer"
                          onClick={open}
                          onKeyDown={(event) => {
                            if (event.target !== event.currentTarget) return;
                            if (event.key === "Enter" || event.key === " ") {
                              event.preventDefault();
                              open();
                            }
                          }}
                        >
                          <TDClip
                            className="font-medium text-ink"
                            title={`${platform.name} · ${policy}`}
                          >
                            {platform.name}
                          </TDClip>
                          <TDClip
                            className="font-mono text-2xs text-ink-soft"
                            title={tagRules || undefined}
                          >
                            {tagRules || t("全部标签")}
                          </TDClip>
                          <TDClip className="readout text-xs text-ink-soft" title={regions || undefined}>
                            {regions || t("不限地区")}
                          </TDClip>
                          <TDClip className="text-xs text-ink-soft" title={policy}>
                            {policy}
                          </TDClip>
                          <TDNum
                            className={platform.routable_node_count ? undefined : "text-ink-faint"}
                          >
                            {platform.routable_node_count.toLocaleString()}
                          </TDNum>
                          <TDNum className="text-xs text-ink-soft">
                            {formatGoDuration(platform.sticky_ttl, t("默认"))}
                          </TDNum>
                        </TR>
                      );
                    })}
                  </TBody>
                </Table>
              </TableWrap>
            )}

            {platformsQuery.data && platforms.length > 0 && (
              <PageNavigator
                page={currentPage}
                totalPages={totalPages}
                totalItems={totalPlatforms}
                pageSize={pageSize}
                pageSizeOptions={PAGE_SIZE_OPTIONS}
                onPageChange={setPage}
                onPageSizeChange={changePageSize}
                disabled={platformsQuery.isFetching}
              />
            )}
          </Panel>
        </div>

        <Sheet
          open={createModalOpen}
          onOpenChange={(open) => setCreateModalOpen(open)}
          title={t("新建平台")}
          width="md"
          footer={
            <div className="flex items-center justify-end gap-2">
              <Button variant="secondary" onClick={() => setCreateModalOpen(false)}>
                {t("取消")}
              </Button>
              <Button type="submit" form="platform-create-form" disabled={createMutation.isPending} loading={createMutation.isPending}>
                {t("确认创建")}
              </Button>
            </div>
          }
        >
          <form id="platform-create-form" className="space-y-4" onSubmit={onCreateSubmit}>
            <Fieldset
              label={t("名称")}
              htmlFor="create-name"
              hint={t(platformNameRuleHint)}
            >
              <Input
                id="create-name"
                aria-invalid={Boolean(createForm.formState.errors.name) || undefined}
                className={cn(createForm.formState.errors.name && "border-alert")}
                {...createForm.register("name")}
              />
              {createForm.formState.errors.name?.message ? (
                <p className="text-xs text-alert">{t(createForm.formState.errors.name.message)}</p>
              ) : null}
            </Fieldset>

            <Fieldset label={t("租约保持时长（可选）")} htmlFor="create-sticky">
              <Input id="create-sticky" placeholder={t("例如 168h")} {...createForm.register("sticky_ttl")} />
            </Fieldset>

            <Fieldset label={t("反向代理账号解析出错策略")} htmlFor="create-miss-action">
              <Select
                id="create-miss-action"
                {...createForm.register("reverse_proxy_miss_action")}
              >
                {missActions.map((item) => (
                  <option key={item} value={item}>
                    {t(missActionLabel[item])}
                  </option>
                ))}
              </Select>
            </Fieldset>

            <Fieldset label={t("节点分配策略")} htmlFor="create-policy">
              <Select
                id="create-policy"
                {...createForm.register("allocation_policy")}
              >
                {allocationPolicies.map((item) => (
                  <option key={item} value={item}>
                    {t(allocationPolicyLabel[item])}
                  </option>
                ))}
              </Select>
            </Fieldset>

            <div className="flex items-start justify-between gap-4 border-t border-rule pt-3">
              <div className="flex min-w-0 items-center gap-1.5">
                <label
                  htmlFor="create-passive-circuit-breaker"
                  className="text-xs font-medium text-ink-soft"
                >
                  {t("禁用请求失败熔断")}
                </label>
                <Tooltip content={passiveCircuitBreakerHint}>
                  <Button
                    type="button"
                    variant="quiet"
                    size="icon"
                    aria-label={passiveCircuitBreakerHint}
                    className="text-ink-faint"
                  >
                    <Info size={13} aria-hidden />
                  </Button>
                </Tooltip>
              </div>
              <Switch
                id="create-passive-circuit-breaker"
                checked={Boolean(createPassiveCircuitBreakerDisabled)}
                onCheckedChange={(checked) =>
                  createForm.setValue("passive_circuit_breaker_disabled", checked, {
                    shouldDirty: true,
                  })
                }
              />
            </div>

            <Fieldset label={t("反向代理账号为空行为")} htmlFor="create-empty-account-behavior">
              <Select
                id="create-empty-account-behavior"
                {...createForm.register("reverse_proxy_empty_account_behavior")}
              >
                {emptyAccountBehaviors.map((item) => (
                  <option key={item} value={item}>
                    {t(emptyAccountBehaviorLabel[item])}
                  </option>
                ))}
              </Select>
            </Fieldset>

            {createEmptyAccountBehavior === "FIXED_HEADER" ? (
              <Fieldset
                label={t("用于提取 Account 的 Headers（每行一个）")}
                htmlFor="create-fixed-account-header"
              >
                <Textarea
                  id="create-fixed-account-header"
                  rows={3}
                  placeholder={t("每行一个，例如 Authorization 或 X-Account-Id")}
                  aria-invalid={
                    Boolean(createForm.formState.errors.reverse_proxy_fixed_account_header) || undefined
                  }
                  className={cn(
                    createForm.formState.errors.reverse_proxy_fixed_account_header && "border-alert",
                  )}
                  {...createForm.register("reverse_proxy_fixed_account_header")}
                />
                {createForm.formState.errors.reverse_proxy_fixed_account_header?.message ? (
                  <p className="text-xs text-alert">
                    {t(createForm.formState.errors.reverse_proxy_fixed_account_header.message)}
                  </p>
                ) : null}
              </Fieldset>
            ) : null}

            {/* The node-selection criteria the platform loads. Every criterion
                is ANDed with the others and the values inside one criterion are
                alternatives; the live preview below the pickers reports the
                number of nodes the current selection would load. */}
            <NodeCriteriaFields form={createForm} idPrefix="create" />
          </form>
        </Sheet>
      </Page>
    </TooltipProvider>
  );
}