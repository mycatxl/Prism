import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Info, LockKeyhole, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { type FormEvent, useState } from "react";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader, SectionTitle } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import { Sheet } from "../../components/ui/Sheet";
import { Switch } from "../../components/ui/Switch";
import { Table, TableWrap, TBody, TD, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { Tooltip, TooltipProvider } from "../../components/ui/Tooltip";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { formatApiErrorMessage } from "../../lib/error-message";
import { createEndpoint, deleteEndpoint, listEndpoints, updateEndpoint } from "./api";
import type { Endpoint, EndpointInput } from "./types";

type EndpointFormState = {
  port: string;
  enabled: boolean;
  allow_management: boolean;
  require_proxy_auth_info: boolean;
  allow_http_forward: boolean;
  allow_http_reverse: boolean;
  allow_socks5: boolean;
};

type TranslateFn = (text: string, options?: Record<string, unknown>) => string;

const EMPTY_ENDPOINTS: Endpoint[] = [];
const PAGE_SIZE_OPTIONS = [10, 20, 50, 100] as const;
const REQUIRE_PROXY_AUTH_LABEL = "强制客户端认证";
const REQUIRE_PROXY_AUTH_HINT = `一些应用（例如浏览器）只有在代理服务器强制要求认证的时候，才会发送认证信息。
因此，当 Prism 没有设置代理令牌时，这些应用不会向 Prism 发送认证字段，导致平台与账号信息缺失。
如果你的 Prism 部署没有设置代理令牌，同时又需要兼容这些应用，可以开启此选项。
注意：开启后，如果客户端没有发送认证信息，平台不再被视为 Default，而是拒绝请求。`;

const DEFAULT_FORM: EndpointFormState = {
  port: "",
  enabled: true,
  allow_management: false,
  require_proxy_auth_info: false,
  allow_http_forward: true,
  allow_http_reverse: true,
  allow_socks5: true,
};

function endpointToForm(endpoint: Endpoint | null): EndpointFormState {
  if (!endpoint) {
    return DEFAULT_FORM;
  }

  return {
    port: String(endpoint.port),
    enabled: endpoint.enabled,
    allow_management: endpoint.allow_management,
    require_proxy_auth_info:
      endpoint.require_proxy_auth_info && (endpoint.allow_http_forward || endpoint.allow_socks5),
    allow_http_forward: endpoint.allow_http_forward,
    allow_http_reverse: endpoint.allow_http_reverse,
    allow_socks5: endpoint.allow_socks5,
  };
}

function parseEndpointForm(
  form: EndpointFormState,
  endpoints: Endpoint[],
  editingID: string | null,
  t: TranslateFn,
): EndpointInput {
  const port = Number(form.port.trim());
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(t("端口必须是 1 到 65535 之间的整数"));
  }
  if (endpoints.some((endpoint) => endpoint.id !== editingID && endpoint.port === port)) {
    throw new Error(t("端口 {{port}} 已被其他接入点使用", { port }));
  }

  const allowProxy = form.allow_http_forward || form.allow_http_reverse || form.allow_socks5;
  if (!form.allow_management && !allowProxy) {
    throw new Error(t("至少启用管理页面或一种代理能力"));
  }
  if (form.require_proxy_auth_info && !form.allow_http_forward && !form.allow_socks5) {
    throw new Error(t("强制客户端发送认证信息需要启用 HTTP 正向代理或 SOCKS5 代理"));
  }

  return {
    port,
    enabled: form.enabled,
    allow_management: form.allow_management,
    allow_proxy: allowProxy,
    require_proxy_auth_info: form.require_proxy_auth_info,
    allow_http_forward: form.allow_http_forward,
    allow_http_reverse: form.allow_http_reverse,
    allow_socks5: form.allow_socks5,
  };
}

type StatusTone = "neutral" | "signal" | "warn" | "alert";

function statusPresentation(status: string, t: TranslateFn): { label: string; tone: StatusTone } {
  switch (status) {
    case "active":
      return { label: t("运行中"), tone: "signal" };
    case "starting":
      return { label: t("启动中"), tone: "warn" };
    case "error":
      return { label: t("异常"), tone: "alert" };
    default:
      return { label: t("未运行"), tone: "neutral" };
  }
}

type EndpointFormProps = {
  endpoint: Endpoint | null;
  endpoints: Endpoint[];
  pending: boolean;
  onClose: () => void;
  onSubmit: (input: EndpointInput) => Promise<void>;
};

/**
 * The edit/create body is one form: the switch rows are grouped in fieldsets so a
 * screen reader hears "接入能力" before each toggle, and the submit button lives in
 * the sheet footer, tied back to this form by id.
 */
function EndpointForm({ endpoint, endpoints, pending, onClose, onSubmit }: EndpointFormProps) {
  const { t } = useI18n();
  const [form, setForm] = useState<EndpointFormState>(() => endpointToForm(endpoint));
  const [formError, setFormError] = useState("");
  const isEditing = Boolean(endpoint);
  const readOnly = endpoint?.read_only ?? false;
  const authPolicyAvailable = form.allow_http_forward || form.allow_socks5;
  const formID = isEditing ? "endpoint-edit-form" : "endpoint-create-form";

  const setProtocol = (
    field: "allow_http_forward" | "allow_http_reverse" | "allow_socks5",
    enabled: boolean,
  ) => {
    setForm((current) => {
      const next = { ...current, [field]: enabled };
      if (!next.allow_http_forward && !next.allow_socks5) {
        next.require_proxy_auth_info = false;
      }
      return next;
    });
    setFormError("");
  };

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (readOnly) {
      return;
    }
    let input: EndpointInput;
    try {
      input = parseEndpointForm(form, endpoints, endpoint?.id ?? null, t);
    } catch (error) {
      setFormError(error instanceof Error ? error.message : t("未知错误"));
      return;
    }
    setFormError("");
    void onSubmit(input).catch(() => undefined);
  };

  return (
    <form id={formID} className="flex flex-col gap-4" onSubmit={handleSubmit}>
      <div className="flex items-center justify-between gap-4 border-b border-rule pb-2">
        <label className="text-sm" htmlFor="endpoint-enabled">
          {t("启用")}
        </label>
        <Switch
          id="endpoint-enabled"
          checked={form.enabled}
          disabled={readOnly}
          onCheckedChange={(checked: boolean) => {
            setForm((current) => ({ ...current, enabled: checked }));
            setFormError("");
          }}
        />
      </div>

      <Fieldset label={t("监听端口")} htmlFor="endpoint-port">
        <Input
          id="endpoint-port"
          className="readout w-32"
          type="number"
          min={1}
          max={65535}
          step={1}
          inputMode="numeric"
          autoFocus={!readOnly}
          disabled={readOnly}
          value={form.port}
          onChange={(event) => {
            setForm((current) => ({ ...current, port: event.target.value }));
            setFormError("");
          }}
        />
      </Fieldset>

      <fieldset className="min-w-0">
        <legend className="label px-0">{t("接入能力")}</legend>
        <div className="mt-1 divide-y divide-rule border-y border-rule">
          <div className="flex items-center justify-between gap-4 py-1.5">
            <label className="text-sm" htmlFor="endpoint-management">
              {t("登录管理页面")}
            </label>
            <Switch
              id="endpoint-management"
              checked={form.allow_management}
              disabled={readOnly}
              onCheckedChange={(checked: boolean) => {
                setForm((current) => ({ ...current, allow_management: checked }));
                setFormError("");
              }}
            />
          </div>

          <div className="flex items-center justify-between gap-4 py-1.5">
            <label className="text-sm" htmlFor="endpoint-http-forward">
              {t("HTTP 正向代理")}
            </label>
            <Switch
              id="endpoint-http-forward"
              checked={form.allow_http_forward}
              disabled={readOnly}
              onCheckedChange={(checked: boolean) => setProtocol("allow_http_forward", checked)}
            />
          </div>

          <div className="flex items-center justify-between gap-4 py-1.5">
            <label className="text-sm" htmlFor="endpoint-http-reverse">
              {t("HTTP 反向代理")}
            </label>
            <Switch
              id="endpoint-http-reverse"
              checked={form.allow_http_reverse}
              disabled={readOnly}
              onCheckedChange={(checked: boolean) => setProtocol("allow_http_reverse", checked)}
            />
          </div>

          <div className="flex items-center justify-between gap-4 py-1.5">
            <label className="text-sm" htmlFor="endpoint-socks5">
              {t("SOCKS5 代理")}
            </label>
            <Switch
              id="endpoint-socks5"
              checked={form.allow_socks5}
              disabled={readOnly}
              onCheckedChange={(checked: boolean) => setProtocol("allow_socks5", checked)}
            />
          </div>
        </div>
      </fieldset>

      <fieldset className="min-w-0">
        <legend className="label px-0">{t("认证策略")}</legend>
        <div className="mt-1 flex items-center justify-between gap-4 border-y border-rule py-1.5">
          <span className="flex items-center gap-1.5">
            <label className="text-sm" htmlFor="endpoint-require-auth-info">
              {t(REQUIRE_PROXY_AUTH_LABEL)}
            </label>
            <TooltipProvider>
              <Tooltip content={t(REQUIRE_PROXY_AUTH_HINT)}>
                <span
                  className="text-ink-faint"
                  tabIndex={0}
                  aria-label={t(REQUIRE_PROXY_AUTH_LABEL)}
                >
                  <Info size={13} />
                </span>
              </Tooltip>
            </TooltipProvider>
          </span>
          <Switch
            id="endpoint-require-auth-info"
            checked={form.require_proxy_auth_info}
            disabled={readOnly || !authPolicyAvailable}
            onCheckedChange={(checked: boolean) =>
              setForm((current) => ({ ...current, require_proxy_auth_info: checked }))
            }
          />
        </div>
      </fieldset>

      {formError ? (
        <p className="flex items-start gap-1.5 text-xs leading-relaxed text-alert" role="alert">
          <AlertTriangle size={13} className="mt-px shrink-0" />
          <span>{formError}</span>
        </p>
      ) : null}

      {!isEditing ? (
        <div className="flex items-center justify-end gap-2">
          <Button variant="secondary" type="button" onClick={onClose} disabled={pending}>
            {t("取消")}
          </Button>
          <Button type="submit" disabled={pending} loading={pending}>
            {t("确认创建")}
          </Button>
        </div>
      ) : null}
    </form>
  );
}

/**
 * Offset pagination over the endpoint list.
 *
 * The page/count readout and the jump box stay together so the operator can read
 * the range and move to a page in one glance.
 */
function EndpointPagination({
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
  disabled?: boolean;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
}) {
  const { t } = useI18n();
  const pages = Math.max(1, totalPages);
  const current = Math.min(Math.max(0, page), pages - 1);
  const jump = (raw: string) => {
    const value = Number(raw);
    if (Number.isInteger(value) && value > 0) onPageChange(Math.max(0, Math.min(pages - 1, value - 1)));
  };
  return (
    <PanelFooter className="justify-between">
      <p className="text-xs text-ink-soft">
        {t("第 {{page}} / {{pages}} 页 · 显示 {{start}}-{{end}} / {{total}}", {
          page: current + 1,
          pages,
          start: totalItems ? current * pageSize + 1 : 0,
          end: Math.min((current + 1) * pageSize, totalItems),
          total: totalItems,
        })}
      </p>
      <div className="flex items-center gap-2">
        <label className="flex items-center gap-1.5 text-xs text-ink-soft">
          <span>{t("每页")}</span>
          <Select
            className="w-auto"
            value={pageSize}
            disabled={disabled}
            onChange={(event) => onPageSizeChange(Number(event.target.value))}
          >
            {PAGE_SIZE_OPTIONS.map((size) => (
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
            className="readout h-[var(--control-h)] w-14 px-1.5 text-xs"
            type="number"
            inputMode="numeric"
            min={1}
            max={pages}
            defaultValue={current + 1}
            aria-label={t("选择页码")}
            disabled={disabled}
            onKeyDown={(event) => {
              if (event.key === "Enter") jump(event.currentTarget.value);
            }}
            onBlur={(event) => {
              jump(event.currentTarget.value);
            }}
          />
        </label>
        <Button
          variant="secondary"
          size="sm"
          aria-label={t("上一页")}
          title={t("上一页")}
          disabled={disabled || current === 0}
          onClick={() => onPageChange(current - 1)}
        >
          {t("上一页")}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          aria-label={t("下一页")}
          title={t("下一页")}
          disabled={disabled || current >= pages - 1}
          onClick={() => onPageChange(current + 1)}
        >
          {t("下一页")}
        </Button>
      </div>
    </PanelFooter>
  );
}

/** A capability column: on/off read as a word, because these are compared down the column. */
function CapabilityCell({ enabled }: { enabled: boolean }) {
  const { t } = useI18n();
  return (
    <span className={enabled ? "text-xs text-signal-deep" : "text-xs text-ink-faint"}>
      {enabled ? t("已开启") : t("已关闭")}
    </span>
  );
}

export function EndpointsPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState<number>(20);
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const [editingEndpoint, setEditingEndpoint] = useState<Endpoint | null>(null);
  const [pendingEnabledStates, setPendingEnabledStates] = useState<Map<string, boolean>>(
    () => new Map(),
  );

  const endpointsQuery = useQuery({
    queryKey: ["endpoints", "page", page, pageSize],
    queryFn: () => listEndpoints({ limit: pageSize, offset: page * pageSize }),
    placeholderData: (previousData) => previousData,
    refetchInterval: 15_000,
  });
  const endpoints = endpointsQuery.data?.items ?? EMPTY_ENDPOINTS;
  const totalEndpoints = endpointsQuery.data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(totalEndpoints / pageSize));
  const currentPage = Math.min(page, totalPages - 1);

  const invalidateEndpoints = async () => {
    await queryClient.invalidateQueries({ queryKey: ["endpoints"] });
  };

  const createMutation = useMutation({
    mutationFn: createEndpoint,
    onSuccess: async (endpoint) => {
      await invalidateEndpoints();
      setCreateModalOpen(false);
      showToast("success", t("接入点 :{{port}} 已创建", { port: endpoint.port }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, input }: { id: string; input: EndpointInput }) => updateEndpoint(id, input),
    onSuccess: async (endpoint) => {
      await invalidateEndpoints();
      setEditingEndpoint(null);
      showToast("success", t("接入点 :{{port}} 已更新", { port: endpoint.port }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const toggleEnabledMutation = useMutation({
    mutationFn: ({ endpoint, enabled }: { endpoint: Endpoint; enabled: boolean }) =>
      updateEndpoint(endpoint.id, { enabled }),
    onSuccess: async (endpoint, { enabled }) => {
      await invalidateEndpoints();
      showToast(
        "success",
        enabled
          ? t("接入点 :{{port}} 已启用", { port: endpoint.port })
          : t("接入点 :{{port}} 已禁用", { port: endpoint.port }),
      );
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (endpoint: Endpoint) => {
      await deleteEndpoint(endpoint.id);
      return endpoint;
    },
    onSuccess: async (endpoint) => {
      if (endpoints.length === 1 && page > 0) {
        setPage(page - 1);
      }
      if (editingEndpoint?.id === endpoint.id) {
        setEditingEndpoint(null);
      }
      await invalidateEndpoints();
      showToast("success", t("接入点 :{{port}} 已删除", { port: endpoint.port }));
    },
    onError: (error) => {
      showToast("error", formatApiErrorMessage(error, t));
    },
  });

  const openCreateModal = () => {
    setEditingEndpoint(null);
    setCreateModalOpen(true);
  };

  const openEditDrawer = (endpoint: Endpoint) => {
    setCreateModalOpen(false);
    setEditingEndpoint(endpoint);
  };

  const closeCreateModal = () => {
    setCreateModalOpen(false);
  };

  const closeEditDrawer = () => {
    setEditingEndpoint(null);
  };

  const changePageSize = (nextPageSize: number) => {
    setPageSize(nextPageSize);
    setPage(0);
  };

  const submitCreateEndpoint = async (input: EndpointInput) => {
    await createMutation.mutateAsync(input);
  };

  const submitUpdateEndpoint = async (input: EndpointInput) => {
    if (!editingEndpoint || editingEndpoint.read_only) {
      return;
    }
    await updateMutation.mutateAsync({ id: editingEndpoint.id, input });
  };

  const handleDelete = async (endpoint: Endpoint) => {
    if (endpoint.read_only) {
      return;
    }
    const confirmed = window.confirm(t("确认删除接入点 :{{port}}？该端口将立即停止监听。", { port: endpoint.port }));
    if (!confirmed) {
      return;
    }
    await deleteMutation.mutateAsync(endpoint).catch(() => undefined);
  };

  const handleEnabledChange = async (endpoint: Endpoint, enabled: boolean) => {
    if (endpoint.read_only || pendingEnabledStates.has(endpoint.id)) {
      return;
    }
    setPendingEnabledStates((current) => new Map(current).set(endpoint.id, enabled));
    try {
      await toggleEnabledMutation.mutateAsync({ endpoint, enabled });
    } catch {
      // The mutation callback surfaces the API error.
    } finally {
      setPendingEnabledStates((current) => {
        const next = new Map(current);
        next.delete(endpoint.id);
        return next;
      });
    }
  };

  const showList = !endpointsQuery.isLoading && !endpointsQuery.isError;

  return (
    <Page bleed>
      <PageHeader
        title={t("接入点")}
        description={t("管理监听端口及其可用的接入能力。")}
        meta={
          <>
            <PageMeta label={t("接入点列表")} value={t("共 {{count}} 个接入点", { count: totalEndpoints })} />
            <PageMeta label={t("每页")} value={pageSize} />
          </>
        }
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="page-content page-content--fill">
        <Panel className="flex min-w-0 flex-col">
        <PanelHeader
          title={t("接入点列表")}
          description={t("共 {{count}} 个接入点", { count: totalEndpoints })}
          actions={
            <>
              <Button variant="secondary" size="sm" onClick={openCreateModal}>
                <Plus size={15} />
                {t("新建")}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void endpointsQuery.refetch()}
                disabled={endpointsQuery.isFetching}
              >
                <RefreshCw size={15} className={endpointsQuery.isFetching ? "animate-spin" : undefined} />
                {t("刷新")}
              </Button>
            </>
          }
        />

        {endpointsQuery.isLoading ? (
          <PanelBody>
            <LoadingState label={t("正在加载接入点...")} />
          </PanelBody>
        ) : null}

        {endpointsQuery.isError ? (
          <PanelBody>
            <ErrorState
              message={formatApiErrorMessage(endpointsQuery.error, t)}
              onRetry={() => void endpointsQuery.refetch()}
            />
          </PanelBody>
        ) : null}

        {showList && !endpoints.length ? (
          <PanelBody>
            <EmptyState
              title={t("暂无接入点")}
              action={
                <Button variant="secondary" size="sm" onClick={openCreateModal}>
                  <Plus size={15} />
                  {t("新建")}
                </Button>
              }
            />
          </PanelBody>
        ) : null}

        {showList && endpoints.length ? (
          <TableWrap>
            <Table>
              <THead>
                <TR>
                  <TH>{t("监听端口")}</TH>
                  <TH>{t("状态")}</TH>
                  <TH>{t("登录管理页面")}</TH>
                  <TH>{t("HTTP 正向代理")}</TH>
                  <TH>{t("HTTP 反向代理")}</TH>
                  <TH>{t("SOCKS5 代理")}</TH>
                  <TH>{t(REQUIRE_PROXY_AUTH_LABEL)}</TH>
                  <TH>{t("启用")}</TH>
                  <TH className="text-right">{t("操作")}</TH>
                </TR>
              </THead>
              <TBody>
                {endpoints.map((endpoint) => {
                  const status = statusPresentation(endpoint.status, t);
                  const displayedEnabled = pendingEnabledStates.get(endpoint.id) ?? endpoint.enabled;
                  const enabledToggleLabel = endpoint.read_only
                    ? t("默认接入点由环境端口定义，不可修改或删除")
                    : displayedEnabled
                      ? t("禁用接入点 :{{port}}", { port: endpoint.port })
                      : t("启用接入点 :{{port}}", { port: endpoint.port });

                  return (
                    <TR
                      key={endpoint.id}
                      selected={editingEndpoint?.id === endpoint.id}
                      className={endpoint.read_only ? undefined : "cursor-pointer"}
                      onClick={endpoint.read_only ? undefined : () => openEditDrawer(endpoint)}
                    >
                      <TD className="whitespace-nowrap">
                        <span className="flex items-center gap-2">
                          <span className="readout text-base font-medium">:{endpoint.port}</span>
                          {endpoint.read_only ? (
                            <Badge tone="outline" title={t("默认接入点由环境端口定义，不可修改或删除")}>
                              <LockKeyhole size={10} />
                              {t("默认 · 只读")}
                            </Badge>
                          ) : null}
                        </span>
                      </TD>
                      <TD className="whitespace-nowrap">
                        <span className="flex min-w-0 items-center gap-2 whitespace-nowrap">
                          <Badge tone={status.tone} dot>
                            {status.label}
                          </Badge>
                          {endpoint.last_error ? (
                            <span
                              className="max-w-[24ch] truncate text-2xs text-alert"
                              role="alert"
                              title={t("监听错误：{{message}}", { message: endpoint.last_error })}
                            >
                              {t("监听错误：{{message}}", { message: endpoint.last_error })}
                            </span>
                          ) : null}
                        </span>
                      </TD>
                      <TD>
                        <CapabilityCell enabled={endpoint.allow_management} />
                      </TD>
                      <TD>
                        <CapabilityCell enabled={endpoint.allow_http_forward} />
                      </TD>
                      <TD>
                        <CapabilityCell enabled={endpoint.allow_http_reverse} />
                      </TD>
                      <TD>
                        <CapabilityCell enabled={endpoint.allow_socks5} />
                      </TD>
                      <TD>
                        <CapabilityCell enabled={endpoint.require_proxy_auth_info} />
                      </TD>
                      <TD>
                        {/* The row itself opens the drawer, so the toggle must not bubble its click. */}
                        <div className="flex" onClick={(event) => event.stopPropagation()}>
                          <Switch
                            checked={displayedEnabled}
                            disabled={endpoint.read_only || pendingEnabledStates.has(endpoint.id)}
                            onCheckedChange={(checked: boolean) => void handleEnabledChange(endpoint, checked)}
                            title={enabledToggleLabel}
                            aria-label={enabledToggleLabel}
                          />
                        </div>
                      </TD>
                      <TD>
                        <div
                          className="flex items-center justify-end gap-1"
                          onClick={(event) => event.stopPropagation()}
                        >
                          <Button
                            variant="ghost"
                            size="icon"
                            onClick={() => openEditDrawer(endpoint)}
                            disabled={endpoint.read_only}
                            title={endpoint.read_only ? t("默认接入点由环境端口定义，不可修改或删除") : t("编辑接入点")}
                            aria-label={t("编辑接入点 :{{port}}", { port: endpoint.port })}
                          >
                            <Pencil size={14} />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="text-ink-faint hover:bg-alert-wash hover:text-alert"
                            onClick={() => void handleDelete(endpoint)}
                            disabled={endpoint.read_only || deleteMutation.isPending}
                            title={endpoint.read_only ? t("默认接入点由环境端口定义，不可修改或删除") : t("删除接入点")}
                            aria-label={t("删除接入点 :{{port}}", { port: endpoint.port })}
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

        <EndpointPagination
          page={currentPage}
          totalPages={totalPages}
          totalItems={totalEndpoints}
          pageSize={pageSize}
          onPageChange={setPage}
          onPageSizeChange={changePageSize}
        />
        </Panel>
      </div>

      <Sheet
        open={Boolean(editingEndpoint)}
        onOpenChange={(open) => {
          if (!open && !updateMutation.isPending) {
            closeEditDrawer();
          }
        }}
        title={editingEndpoint ? `:${editingEndpoint.port}` : ""}
        description={
          editingEndpoint ? <span className="readout text-xs text-ink-faint">{editingEndpoint.id}</span> : undefined
        }
        width="md"
        footer={
          editingEndpoint && !editingEndpoint.read_only ? (
            <div className="flex items-center justify-end gap-2">
              <Button type="submit" form="endpoint-edit-form" disabled={updateMutation.isPending} loading={updateMutation.isPending}>
                {t("保存配置")}
              </Button>
            </div>
          ) : undefined
        }
      >
        {editingEndpoint ? (
          <div className="flex flex-col gap-5">
            <section>
              <SectionTitle>{t("接入点配置")}</SectionTitle>
              <p className="text-xs leading-relaxed text-ink-soft">
                {editingEndpoint.read_only
                  ? t("默认接入点由环境端口定义，不可修改或删除")
                  : t("修改监听端口和接入能力后保存。")}
              </p>
              <div className="mt-3">
                <EndpointForm
                  key={editingEndpoint.id}
                  endpoint={editingEndpoint}
                  endpoints={endpoints}
                  pending={updateMutation.isPending}
                  onClose={closeEditDrawer}
                  onSubmit={submitUpdateEndpoint}
                />
              </div>
            </section>

            {!editingEndpoint.read_only ? (
              <section className="border-t border-rule pt-4">
                <SectionTitle>{t("运维操作")}</SectionTitle>
                <div className="mt-2 flex items-start justify-between gap-4">
                  <div className="min-w-0">
                    <h3 className="text-sm font-medium">{t("删除接入点")}</h3>
                    <p className="mt-0.5 max-w-[60ch] text-xs leading-relaxed text-ink-soft">
                      {t("删除接入点并停止监听，操作不可撤销。")}
                    </p>
                  </div>
                  <Button
                    variant="danger"
                    onClick={() => void handleDelete(editingEndpoint)}
                    disabled={deleteMutation.isPending}
                    loading={deleteMutation.isPending}
                  >
                    {t("删除")}
                  </Button>
                </div>
              </section>
            ) : null}
          </div>
        ) : null}
      </Sheet>

      <Sheet
        open={createModalOpen}
        onOpenChange={(open) => {
          if (!open && !createMutation.isPending) {
            closeCreateModal();
          }
        }}
        title={t("新建接入点")}
        width="sm"
        footer={
          <div className="flex items-center justify-end gap-2">
            <Button variant="secondary" onClick={closeCreateModal} disabled={createMutation.isPending}>
              {t("取消")}
            </Button>
            <Button type="submit" form="endpoint-create-form" disabled={createMutation.isPending} loading={createMutation.isPending}>
              {t("确认创建")}
            </Button>
          </div>
        }
      >
        <EndpointForm
          key="create"
          endpoint={null}
          endpoints={endpoints}
          pending={createMutation.isPending}
          onClose={closeCreateModal}
          onSubmit={submitCreateEndpoint}
        />
      </Sheet>
    </Page>
  );
}