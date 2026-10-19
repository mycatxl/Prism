import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, RefreshCw, Search, Trash2, UserCheck, UserX } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import { DialogContent, DialogOverlay, DialogPortal, DialogRoot, DialogTitle } from "../../components/ui/Dialog";
import { Input } from "../../components/ui/Input";
import { PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelFooter, PanelHeader, PanelToolbar } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import { Sheet } from "../../components/ui/Sheet";
import { Table, TableWrap, TBody, TD, TDClip, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import { opsPaths } from "../../lib/paths";
import { adminPlansQueryKey, createUser, deleteUser, listPlans, listUsers, updateUser } from "./api";
import type { AdminPlan, AdminUser, CreateUserInput } from "./types";

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
    refetch,
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

  const filteredUsers = useMemo(() => {
    return (usersData?.items ?? []).filter((u) => {
      if (roleFilter !== "all" && u.role !== roleFilter) return false;
      return true;
    });
  }, [usersData?.items, roleFilter]);

  const total = usersData?.total ?? 0;
  const currentPage = Math.floor(offset / PAGE_SIZE) + 1;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("用户")}
        description={t("管理系统用户、角色分配、套餐与访问权限。")}
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="secondary"
              onClick={() => void refetch()}
              disabled={isFetching}
            >
              <RefreshCw className={isFetching ? "animate-spin" : ""} size={14} />
              <span>{t("刷新")}</span>
            </Button>
            <Button variant="primary" onClick={() => setCreateSheetOpen(true)}>
              <Plus size={14} />
              <span>{t("新建用户")}</span>
            </Button>
          </div>
        }
      />
      <Panel>
        <PanelHeader
          title={t("全部用户")}
          meta={<Badge tone="neutral">{total}</Badge>}
        />
        <PanelToolbar className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-1 flex-wrap items-center gap-3">
            <div className="relative min-w-[14rem] flex-1 max-w-sm">
              <Search
                size={14}
                className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-ink-faint"
              />
              <Input
                value={search}
                onChange={(e) => {
                  setSearch(e.target.value);
                  setOffset(0);
                }}
                placeholder={t("按用户名搜索...")}
                className="pl-9"
              />
            </div>
            <div className="w-36">
              <Select
                value={statusFilter}
                onChange={(e) => {
                  setStatusFilter(e.target.value);
                  setOffset(0);
                }}
              >
                <option value="all">{t("全部状态")}</option>
                <option value="active">{t("正常")}</option>
                <option value="disabled">{t("已停用")}</option>
              </Select>
            </div>
            <div className="w-36">
              <Select
                value={roleFilter}
                onChange={(e) => {
                  setRoleFilter(e.target.value);
                  setOffset(0);
                }}
              >
                <option value="all">{t("全部角色")}</option>
                <option value="user">{t("普通用户")}</option>
                <option value="admin">{t("管理员")}</option>
              </Select>
            </div>
          </div>
        </PanelToolbar>

        <PanelBody className="p-0">
          {isLoading ? (
            <LoadingState label={t("正在加载用户列表...")} className="py-16" />
          ) : isError ? (
            <ErrorState
              message={formatApiErrorMessage(error, t)}
              onRetry={() => void refetch()}
              className="py-16"
            />
          ) : filteredUsers.length === 0 ? (
            <EmptyState
              title={t("暂无用户")}
              hint={t("未找到符合条件的用户，可点击右上角新建。")}
              className="py-16"
            />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("用户名")}</TH>
                    <TH>{t("角色")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH>{t("套餐")}</TH>
                    <TH>{t("到期时间")}</TH>
                    <TH>{t("密钥数")}</TH>
                    <TH>{t("周期用量")}</TH>
                    <TH>{t("创建时间")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {filteredUsers.map((user) => {
                    const sub = user.subscription;
                    const usedBytes = sub?.period_used_bytes ?? 0;
                    const totalQuota = sub?.traffic_bytes ?? 0;
                    const isPendingAction =
                      toggleStatusMutation.isPending &&
                      toggleStatusMutation.variables?.id === user.id;

                    return (
                      <TR
                        key={user.id}
                        className="cursor-pointer hover:bg-hover"
                        onClick={() => navigate(`${opsPaths.users}/${user.id}`)}
                      >
                        <TD>
                          <TDClip className="font-semibold text-ink">
                            {user.username}
                          </TDClip>
                        </TD>
                        <TD>
                          <Badge tone={user.role === "admin" ? "accent" : "neutral"}>
                            {user.role === "admin" ? t("管理员") : t("普通用户")}
                          </Badge>
                        </TD>
                        <TD>
                          <Badge
                            tone={user.status === "active" ? "signal" : "warn"}
                            dot
                          >
                            {user.status === "active" ? t("正常") : t("已停用")}
                          </Badge>
                        </TD>
                        <TD>
                          <TDClip>{sub?.plan_name || "-"}</TDClip>
                        </TD>
                        <TD className="readout text-xs">
                          {formatNsTime(sub?.expires_at_ns)}
                        </TD>
                        <TD className="readout">{user.key_count ?? 0}</TD>
                        <TD className="readout text-xs">
                          {sub ? (
                            <span>
                              {formatBytes(usedBytes)} /{" "}
                              {totalQuota > 0 ? formatBytes(totalQuota) : t("不限")}
                            </span>
                          ) : (
                            "-"
                          )}
                        </TD>
                        <TD className="readout text-xs">
                          {formatNsTime(user.created_at_ns)}
                        </TD>
                        <TD
                          className="text-right"
                          onClick={(e) => e.stopPropagation()}
                        >
                          <div className="flex items-center justify-end gap-1.5">
                            <Button
                              variant="ghost"
                              disabled={isPendingAction}
                              onClick={() => {
                                const nextStatus =
                                  user.status === "active" ? "disabled" : "active";
                                toggleStatusMutation.mutate({
                                  id: user.id,
                                  nextStatus,
                                });
                              }}
                              title={
                                user.status === "active"
                                  ? t("停用用户")
                                  : t("启用用户")
                              }
                            >
                              {user.status === "active" ? (
                                <UserX size={14} className="text-warn" />
                              ) : (
                                <UserCheck size={14} className="text-signal" />
                              )}
                              <span>
                                {user.status === "active"
                                  ? t("停用")
                                  : t("启用")}
                              </span>
                            </Button>
                            <Button
                              variant="ghost"
                              onClick={() => setDeleteTarget(user)}
                              title={t("删除用户")}
                            >
                              <Trash2 size={14} className="text-alert" />
                              <span>{t("删除")}</span>
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
        </PanelBody>

        {totalPages > 1 && (
          <PanelFooter className="flex items-center justify-between">
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
                disabled={offset === 0}
                onClick={() => setOffset((prev) => Math.max(0, prev - PAGE_SIZE))}
              >
                {t("上一页")}
              </Button>
              <Button
                variant="secondary"
                disabled={offset + PAGE_SIZE >= total}
                onClick={() => setOffset((prev) => prev + PAGE_SIZE)}
              >
                {t("下一页")}
              </Button>
            </div>
          </PanelFooter>
        )}
      </Panel>

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
          <DialogContent className="w-full max-w-md p-6">
            <DialogTitle className="text-base font-semibold text-ink">
              {t("确认删除用户")}
            </DialogTitle>
            <p className="mt-3 text-sm text-ink-soft">
              {t("确定要删除用户 {{username}} 吗？此操作不可逆，将同时删除该用户的所有密钥与订阅。", {
                username: deleteTarget?.username ?? "",
              })}
            </p>
            <div className="mt-6 flex items-center justify-end gap-3">
              <Button
                variant="secondary"
                onClick={() => setDeleteTarget(null)}
                disabled={deleteMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                variant="primary"
                onClick={() => {
                  if (deleteTarget) deleteMutation.mutate(deleteTarget.id);
                }}
                disabled={deleteMutation.isPending}
              >
                {deleteMutation.isPending ? t("正在删除...") : t("确认删除")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
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
            disabled={createMutation.isPending}
          >
            {createMutation.isPending ? t("正在创建...") : t("创建用户")}
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

        <div className="space-y-1.5">
          <label htmlFor={usernameId} className="block text-xs font-medium text-ink-soft">
            {t("用户名")} *
          </label>
          <Input
            id={usernameId}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder={t("输入用户名")}
            required
          />
        </div>

        <div className="space-y-1.5">
          <label htmlFor={passwordId} className="block text-xs font-medium text-ink-soft">
            {t("初始密码")}
          </label>
          <Input
            id={passwordId}
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t("留空则由系统随机生成或无需初始密码")}
          />
        </div>

        <div className="space-y-1.5">
          <label htmlFor={roleId} className="block text-xs font-medium text-ink-soft">
            {t("用户角色")}
          </label>
          <Select id={roleId} value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="user">{t("普通用户")}</option>
            <option value="admin">{t("管理员")}</option>
          </Select>
        </div>

        <div className="space-y-1.5">
          <label htmlFor={planId} className="block text-xs font-medium text-ink-soft">
            {t("绑定套餐 (可选)")}
          </label>
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
        </div>

        {selectedPlanId && (
          <div className="space-y-1.5">
            <label htmlFor={durationId} className="block text-xs font-medium text-ink-soft">
              {t("有效天数")}
            </label>
            <Input
              id={durationId}
              type="number"
              min="1"
              value={durationDays}
              onChange={(e) => setDurationDays(e.target.value)}
              placeholder="30"
            />
          </div>
        )}
      </form>
    </Sheet>
  );
}
