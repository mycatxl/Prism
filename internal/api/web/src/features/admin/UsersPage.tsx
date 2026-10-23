import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, RefreshCw, Search, Trash2, UserCheck, UserX, X } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { DialogContent, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from "../../components/ui/Dialog";
import { Fieldset, Input } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader, PanelToolbar } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import { Sheet } from "../../components/ui/Sheet";
import { Table, TableWrap, TBody, TD, TDClip, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro } from "../../lib/money";
import { formatRelativeTime } from "../../lib/time";
import { opsPaths } from "../../lib/paths";
import { adminPlansQueryKey, createUser, deleteUser, getUserBalance, listPlans, listUsers, updateUser } from "./api";
import type { AdminPlan, AdminUser, CreateUserInput } from "./types";

/*
 * The operator's account list, on the Signal Console rhythm: the page header
 * states the page and carries the one action that creates, one panel carries a
 * toolbar of search + status + role + refresh over a single data grid, and the
 * footer carries the pager. Identity is set in the mono face, the balance is a
 * readout with its unit set small, and a status is the dot-and-word pair. The
 * per-account work — plan, keys, usage, ledger — lives on the detail page.
 *
 * The balance is not part of the list contract, so each row reads the one
 * endpoint that answers for it (`getUserBalance`, ledger trimmed to 1) under a
 * row-scoped cache entry; the cell shows "--" until that answers and never
 * invents a figure.
 */

const PAGE_SIZE = 20;
const NANOS_PER_MILLI = 1_000_000;

function formatNsTime(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}

/** The activity column speaks in relative time; the stamp is kept as its title. */
function formatNsRelative(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  return formatRelativeTime(date.toISOString());
}

export function UsersPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  const [search, setSearch] = useState("");
  // The input binds to the raw value; the request and its cache key use the
  // settled one, so one keystroke is never one request and no character typed is
  // dropped while the value is still settling.
  const settledSearch = useDebouncedValue(search);
  const [statusFilter, setStatusFilter] = useState("all");
  const [roleFilter, setRoleFilter] = useState("all");
  const [offset, setOffset] = useState(0);

  const [createSheetOpen, setCreateSheetOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<AdminUser | null>(null);

  const queryParams = useMemo(
    () => ({
      q: settledSearch.trim() || undefined,
      status: statusFilter === "all" ? undefined : statusFilter,
      role: roleFilter === "all" ? undefined : roleFilter,
      limit: PAGE_SIZE,
      offset,
    }),
    [settledSearch, statusFilter, roleFilter, offset],
  );

  const {
    data: usersData,
    isLoading,
    isError,
    error,
    isFetching,
  } = useQuery({
    queryKey: ["admin-users", queryParams],
    queryFn: () => listUsers(queryParams),
  });

  const { data: plansData } = useQuery({
    queryKey: adminPlansQueryKey,
    queryFn: () => listPlans(),
  });

  const toggleStatusMutation = useMutation({
    mutationFn: ({ id, nextStatus }: { id: string; nextStatus: string }) =>
      updateUser(id, { status: nextStatus }),
    onSuccess: (updated) => {
      showToast("success", t("用户状态已更新"));
      void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
      void queryClient.invalidateQueries({ queryKey: ["admin-user", updated.id] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteUser(id),
    onSuccess: () => {
      showToast("success", t("用户已删除"));
      setDeleteTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  const users = usersData?.items ?? [];
  const total = usersData?.total ?? 0;
  const currentPage = Math.floor(offset / PAGE_SIZE) + 1;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const filtersActive =
    search.trim() !== "" || statusFilter !== "all" || roleFilter !== "all";

  // Clearing filters clears filters only — the pager keeps its place and no row
  // state of any kind is touched by the button.
  const clearFilters = () => {
    setSearch("");
    setStatusFilter("all");
    setRoleFilter("all");
    setOffset(0);
  };

  // One action, every query the page reads: the list itself and the plan
  // catalogue the create sheet picks from.
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
    void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });
  };

  return (
    <Page bleed>
      <PageHeader
        title={t("用户")}
        description={t("管理系统用户、角色分配、套餐与访问权限。")}
        meta={
          <PageMeta
            label={t("用户总数")}
            value={usersData ? total.toLocaleString() : "--"}
          />
        }
        actions={
          <Button variant="primary" onClick={() => setCreateSheetOpen(true)}>
            <Plus size={16} />
            <span>{t("新建用户")}</span>
          </Button>
        }
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="page-content page-content--fill">
        <Panel className="flex min-w-0 flex-col">
          <PanelHeader
            title={t("全部用户")}
            meta={usersData ? t("共 {{count}} 个用户", { count: total }) : undefined}
          />

          <PanelToolbar>
            <label htmlFor="users-search" className="relative block min-w-48 sm:w-64">
              <Search
                size={13}
                aria-hidden
                className="pointer-events-none absolute top-1/2 left-2 -translate-y-1/2 text-ink-faint"
              />
              <Input
                id="users-search"
                className="pl-7 text-xs"
                placeholder={t("按用户名搜索...")}
                aria-label={t("按用户名搜索...")}
                value={search}
                onChange={(event) => {
                  setSearch(event.target.value);
                  setOffset(0);
                }}
              />
            </label>
            <Select
              className="w-32"
              aria-label={t("状态")}
              value={statusFilter}
              onChange={(event) => {
                setStatusFilter(event.target.value);
                setOffset(0);
              }}
            >
              <option value="all">{t("全部状态")}</option>
              <option value="active">{t("正常")}</option>
              <option value="disabled">{t("已停用")}</option>
            </Select>
            <Select
              className="w-32"
              aria-label={t("角色")}
              value={roleFilter}
              onChange={(event) => {
                setRoleFilter(event.target.value);
                setOffset(0);
              }}
            >
              <option value="all">{t("全部角色")}</option>
              <option value="user">{t("普通用户")}</option>
              <option value="admin">{t("管理员")}</option>
            </Select>
            {filtersActive && (
              <Button variant="ghost" size="sm" onClick={clearFilters}>
                <X size={14} aria-hidden />
                {t("清除筛选")}
              </Button>
            )}
            <div className="ml-auto flex items-center gap-1.5">
              <Button
                variant="secondary"
                size="sm"
                onClick={refresh}
                disabled={isFetching}
                title={t("刷新")}
              >
                <RefreshCw
                  size={15}
                  className={isFetching ? "animate-spin" : undefined}
                />
                <span>{t("刷新")}</span>
              </Button>
            </div>
          </PanelToolbar>

          {isLoading ? (
            <PanelBody>
              <LoadingState label={t("正在加载用户列表...")} />
            </PanelBody>
          ) : isError ? (
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(error, t)}
                onRetry={refresh}
              />
            </PanelBody>
          ) : users.length === 0 ? (
            <PanelBody>
              <EmptyState
                title={filtersActive ? t("没有匹配的用户") : t("暂无用户")}
                hint={
                  filtersActive
                    ? t("调整搜索或状态筛选，或清除筛选查看全部。")
                    : t("未找到符合条件的用户，可点击右上角新建。")
                }
                action={
                  filtersActive ? (
                    <Button variant="secondary" onClick={clearFilters}>
                      {t("清除筛选")}
                    </Button>
                  ) : (
                    <Button variant="primary" onClick={() => setCreateSheetOpen(true)}>
                      <Plus size={14} />
                      <span>{t("新建用户")}</span>
                    </Button>
                  )
                }
              />
            </PanelBody>
          ) : (
            <TableWrap aria-busy={isFetching}>
              <Table className="min-w-[880px]">
                <THead>
                  <TR>
                    <TH>{t("用户名")}</TH>
                    <TH>{t("角色")}</TH>
                    <TH className="text-right">{t("余额")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH>{t("注册时间")}</TH>
                    <TH>{t("最近活动")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {users.map((user) => {
                    const active = user.status === "active";
                    const isPendingAction =
                      toggleStatusMutation.isPending &&
                      toggleStatusMutation.variables?.id === user.id;

                    return (
                      <TR
                        key={user.id}
                        className="cursor-pointer"
                        onClick={() => navigate(`${opsPaths.users}/${user.id}`)}
                      >
                        <TDClip className="mono font-medium text-ink" title={user.username}>
                          {user.username}
                        </TDClip>
                        <TD>
                          <Badge tone={user.role === "admin" ? "accent" : "neutral"}>
                            {user.role === "admin" ? t("管理员") : t("普通用户")}
                          </Badge>
                        </TD>
                        <TDNum className="whitespace-nowrap">
                          <UserBalanceCell userId={user.id} />
                        </TDNum>
                        <TD className="whitespace-nowrap">
                          <span className="inline-flex items-center gap-2">
                            <span
                              aria-hidden
                              className={active ? "state-dot state-dot--signal" : "state-dot"}
                            />
                            <span className="state-text">
                              {active ? t("正常") : t("已停用")}
                            </span>
                          </span>
                        </TD>
                        <TD className="readout whitespace-nowrap text-xs text-ink-soft">
                          {formatNsTime(user.created_at_ns)}
                        </TD>
                        <TD
                          className="readout whitespace-nowrap text-xs text-ink-soft"
                          title={formatNsTime(user.last_login_ns)}
                        >
                          {formatNsRelative(user.last_login_ns)}
                        </TD>
                        <TD
                          className="text-right"
                          onClick={(e) => e.stopPropagation()}
                        >
                          <div className="flex items-center justify-end gap-1">
                            <Button
                              variant="ghost"
                              size="icon"
                              disabled={isPendingAction}
                              onClick={() => {
                                toggleStatusMutation.mutate({
                                  id: user.id,
                                  nextStatus: active ? "disabled" : "active",
                                });
                              }}
                              title={active ? t("停用用户") : t("启用用户")}
                              aria-label={active ? t("停用用户") : t("启用用户")}
                            >
                              {active ? <UserX size={14} /> : <UserCheck size={14} />}
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="text-ink-faint hover:bg-alert-wash hover:text-alert"
                              onClick={() => setDeleteTarget(user)}
                              title={t("删除用户")}
                              aria-label={t("删除用户")}
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
          )}

          {totalPages > 1 && (
            <PanelFooter>
              <span className="readout text-xs text-ink-soft">
                {t("第 {{current}} / {{total}} 页 (共 {{count}} 条)", {
                  current: currentPage,
                  total: totalPages,
                  count: total,
                })}
              </span>
              <div className="flex items-center gap-2">
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={offset === 0}
                  onClick={() => setOffset((prev) => Math.max(0, prev - PAGE_SIZE))}
                >
                  {t("上一页")}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={offset + PAGE_SIZE >= total}
                  onClick={() => setOffset((prev) => prev + PAGE_SIZE)}
                >
                  {t("下一页")}
                </Button>
              </div>
            </PanelFooter>
          )}
        </Panel>
      </div>

      <CreateUserSheet
        open={createSheetOpen}
        onOpenChange={setCreateSheetOpen}
        plans={plansData?.items ?? []}
        onSuccess={() => {
          showToast("success", t("用户创建成功"));
          setCreateSheetOpen(false);
          void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
        }}
      />

      <DialogRoot
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md">
            <div className="border-b border-rule px-5 py-4">
              <DialogTitle>{t("确认删除用户")}</DialogTitle>
            </div>
            <div className="px-5 py-4 text-sm leading-relaxed text-ink-soft">
              {t("确定要删除用户 {{username}} 吗？此操作不可逆，将同时删除该用户的所有密钥与订阅。", {
                username: deleteTarget?.username ?? "",
              })}
            </div>
            <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
              <Button
                variant="secondary"
                onClick={() => setDeleteTarget(null)}
                disabled={deleteMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                variant="danger"
                loading={deleteMutation.isPending}
                onClick={() => {
                  if (deleteTarget) deleteMutation.mutate(deleteTarget.id);
                }}
              >
                {t("确认删除")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>
    </Page>
  );
}

/**
 * One row's balance, read from the endpoint that answers for a single account.
 * The ledger is trimmed to its first entry because the cell shows the figure
 * only; the full ledger is the detail page's panel.
 */
function UserBalanceCell({ userId }: { userId: string }) {
  const balanceQuery = useQuery({
    queryKey: ["admin-user-balance", userId, "row"],
    queryFn: () => getUserBalance(userId, 1),
    enabled: Boolean(userId),
    staleTime: 30_000,
  });

  if (!balanceQuery.isSuccess || !balanceQuery.data) {
    return <span className="text-ink-faint">--</span>;
  }
  return (
    <span className="readout">
      {formatMicro(balanceQuery.data.balance_micro)}
      <small>USDT</small>
    </span>
  );
}

function CreateUserSheet({
  open,
  onOpenChange,
  plans,
  onSuccess,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  plans: AdminPlan[];
  onSuccess: () => void;
}) {
  const { t } = useI18n();
  const usernameId = useId();
  const passwordId = useId();
  const roleId = useId();
  const planId = useId();
  const durationId = useId();

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState("user");
  const [selectedPlanId, setSelectedPlanId] = useState("");
  const [durationDays, setDurationDays] = useState("30");
  const [errorMsg, setErrorMsg] = useState("");

  const createMutation = useMutation({
    mutationFn: (input: CreateUserInput) => createUser(input),
    onSuccess: () => {
      setUsername("");
      setPassword("");
      setRole("user");
      setSelectedPlanId("");
      setDurationDays("30");
      setErrorMsg("");
      onSuccess();
    },
    onError: (err) => {
      setErrorMsg(formatApiErrorMessage(err, t));
    },
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!username.trim()) {
      setErrorMsg(t("用户名不能为空"));
      return;
    }
    setErrorMsg("");
    createMutation.mutate({
      username: username.trim(),
      password: password || undefined,
      role,
      plan_id: selectedPlanId || undefined,
      duration_days: selectedPlanId ? Number(durationDays) || 30 : undefined,
    });
  };

  return (
    <Sheet
      open={open}
      onOpenChange={(next) => {
        if (!createMutation.isPending) {
          setErrorMsg("");
          onOpenChange(next);
        }
      }}
      title={t("新建用户")}
      description={t("添加一个系统用户并为其分配角色和可选套餐。")}
      width="md"
      footer={
        <div className="flex items-center justify-end gap-2">
          <Button
            variant="secondary"
            onClick={() => onOpenChange(false)}
            disabled={createMutation.isPending}
          >
            {t("取消")}
          </Button>
          <Button
            variant="primary"
            onClick={handleSubmit}
            loading={createMutation.isPending}
          >
            {t("创建用户")}
          </Button>
        </div>
      }
    >
      <form onSubmit={handleSubmit} className="space-y-4 p-6">
        {errorMsg && (
          <div className="rounded-control border border-alert/30 bg-alert-wash p-3 text-xs text-alert">
            {errorMsg}
          </div>
        )}

        <Fieldset label={t("用户名")} htmlFor={usernameId}>
          <Input
            id={usernameId}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder={t("输入用户名")}
            required
          />
        </Fieldset>

        <Fieldset
          label={t("初始密码")}
          hint={t("留空则由系统随机生成或无需初始密码")}
          htmlFor={passwordId}
        >
          <Input
            id={passwordId}
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Fieldset>

        <Fieldset label={t("用户角色")} htmlFor={roleId}>
          <Select id={roleId} value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="user">{t("普通用户")}</option>
            <option value="admin">{t("管理员")}</option>
          </Select>
        </Fieldset>

        <Fieldset label={t("绑定套餐 (可选)")} htmlFor={planId}>
          <Select
            id={planId}
            value={selectedPlanId}
            onChange={(e) => setSelectedPlanId(e.target.value)}
          >
            <option value="">{t("暂不绑定套餐")}</option>
            {plans.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name} ({p.period})
              </option>
            ))}
          </Select>
        </Fieldset>

        {selectedPlanId && (
          <Fieldset label={t("有效天数")} htmlFor={durationId}>
            <Input
              id={durationId}
              type="number"
              min="1"
              value={durationDays}
              onChange={(e) => setDurationDays(e.target.value)}
              placeholder="30"
            />
          </Fieldset>
        )}
      </form>
    </Sheet>
  );
}
