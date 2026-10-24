import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, ExternalLink, Plus, RefreshCw, Search, Trash2, UserCheck, UserX } from "lucide-react";
import { useId, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  ConfirmDrawer,
  ControlButton,
  ControlInput,
  ControlPagination,
  ControlSelect,
  DetailFact,
  EmptyWorkState,
  FilterChip,
  InspectorDrawer,
  ObjectList,
  ObjectRow,
  PageFrame,
  QueryBadge,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { ToastContainer } from "../../components/ui/Toast";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro } from "../../lib/money";
import { opsPaths } from "../../lib/paths";
import { formatRelativeTime } from "../../lib/time";
import { adminPlansQueryKey, createUser, deleteUser, getUserBalance, listPlans, listUsers, updateUser } from "./api";
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
  const settledSearch = useDebouncedValue(search);
  const [statusFilter, setStatusFilter] = useState("all");
  const [roleFilter, setRoleFilter] = useState("all");
  const [page, setPage] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);
  const [selected, setSelected] = useState<AdminUser | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<AdminUser | null>(null);

  const queryParams = useMemo(
    () => ({
      q: settledSearch.trim() || undefined,
      status: statusFilter === "all" ? undefined : statusFilter,
      role: roleFilter === "all" ? undefined : roleFilter,
      limit: PAGE_SIZE,
      offset: page * PAGE_SIZE,
    }),
    [page, roleFilter, settledSearch, statusFilter],
  );
  const usersQuery = useQuery({
    queryKey: ["admin-users", queryParams],
    queryFn: () => listUsers(queryParams),
    placeholderData: (previous) => previous,
  });
  const plansQuery = useQuery({ queryKey: adminPlansQueryKey, queryFn: listPlans, staleTime: 60_000 });

  const toggleStatusMutation = useMutation({
    mutationFn: ({ id, nextStatus }: { id: string; nextStatus: string }) => updateUser(id, { status: nextStatus }),
    onSuccess: (updated) => {
      showToast("success", t("用户状态已更新"));
      if (selected?.id === updated.id) setSelected(updated);
      void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
      void queryClient.invalidateQueries({ queryKey: ["admin-user", updated.id] });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });
  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteUser(id),
    onSuccess: () => {
      showToast("success", t("用户已删除"));
      setDeleteTarget(null);
      setSelected(null);
      void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  const users = usersQuery.data?.items ?? [];
  const total = usersQuery.data?.total ?? 0;
  const filtersActive = search.trim() !== "" || statusFilter !== "all" || roleFilter !== "all";
  const clearFilters = () => {
    setSearch("");
    setStatusFilter("all");
    setRoleFilter("all");
    setPage(0);
  };
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
    void queryClient.invalidateQueries({ queryKey: adminPlansQueryKey });
  };
  const statusTone = usersQuery.isError && !usersQuery.data ? "warn" : usersQuery.data ? "live" : "neutral";

  return (
    <PageFrame
      eyebrow="Customers / user registry"
      title={t("用户")}
      description={t("管理系统用户、角色分配、套餐与访问权限。选择对象打开 inspector，完整账户工作面保留在用户详情页。")}
      meta={<StatusMark tone={statusTone} label={usersQuery.isError && !usersQuery.data ? t("读取失败") : usersQuery.data ? t("目录已同步") : t("等待目录")} />}
      actions={
        <>
          <ControlButton variant="secondary" size="sm" onPress={refresh} loading={usersQuery.isFetching}>
            <RefreshCw size={14} aria-hidden />{t("刷新")}
          </ControlButton>
          <ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}>
            <Plus size={14} aria-hidden />{t("新建用户")}
          </ControlButton>
        </>
      }
    >
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <section className="cp-command">
        <div className="cp-command__body">
          <ControlInput
            label={t("搜索用户")}
            value={search}
            onChange={(event) => {
              setSearch(event.target.value);
              setPage(0);
            }}
            placeholder={t("按用户名搜索")}
          />
          <ControlSelect
            label={t("状态")}
            value={statusFilter}
            onChange={(event) => {
              setStatusFilter(event.target.value);
              setPage(0);
            }}
          >
            <option value="all">{t("全部状态")}</option>
            <option value="active">{t("正常")}</option>
            <option value="disabled">{t("已停用")}</option>
          </ControlSelect>
          <ControlSelect
            label={t("角色")}
            value={roleFilter}
            onChange={(event) => {
              setRoleFilter(event.target.value);
              setPage(0);
            }}
          >
            <option value="all">{t("全部角色")}</option>
            <option value="user">{t("普通用户")}</option>
            <option value="admin">{t("管理员")}</option>
          </ControlSelect>
        </div>
        <div className="cp-command__actions">
          <Search size={14} aria-hidden />
          {filtersActive ? <ControlButton variant="ghost" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : null}
        </div>
      </section>

      {filtersActive ? (
        <div className="cp-active-filters" aria-label={t("当前筛选")}>
          {search.trim() ? <FilterChip label={`${t("关键词")}: ${search.trim()}`} onRemove={() => { setSearch(""); setPage(0); }} /> : null}
          {statusFilter !== "all" ? <FilterChip label={`${t("状态")}: ${statusFilter === "active" ? t("正常") : t("已停用")}`} onRemove={() => { setStatusFilter("all"); setPage(0); }} /> : null}
          {roleFilter !== "all" ? <FilterChip label={`${t("角色")}: ${roleFilter === "admin" ? t("管理员") : t("普通用户")}`} onRemove={() => { setRoleFilter("all"); setPage(0); }} /> : null}
        </div>
      ) : null}

      <WorkSurface
        eyebrow="Customer objects"
        title={t("用户目录")}
        meta={usersQuery.data ? t("共 {{count}} 个用户", { count: total }) : t("未加载")}
        description={t("余额、订阅、密钥数量和最近活动只显示真实账户数据；选择一行查看状态与管理动作。")}
      >
        {usersQuery.isError && !usersQuery.data ? (
          <EmptyWorkState title={t("用户目录读取失败")} detail={formatApiErrorMessage(usersQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void usersQuery.refetch()}>{t("重试")}</ControlButton>} />
        ) : null}
        {usersQuery.isLoading && !usersQuery.data ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载用户列表")}</div> : null}
        {!usersQuery.isLoading && !usersQuery.isError && users.length === 0 ? (
          <EmptyWorkState
            title={t(filtersActive ? "没有匹配的用户" : "暂无用户")}
            detail={t(filtersActive ? "调整搜索、状态或角色筛选后重试。" : "创建第一个用户后，账户关系会显示在这里。")}
            action={filtersActive ? <ControlButton variant="secondary" size="sm" onPress={clearFilters}>{t("清除筛选")}</ControlButton> : <ControlButton variant="primary" size="sm" onPress={() => setCreateOpen(true)}><Plus size={14} aria-hidden />{t("新建用户")}</ControlButton>}
          />
        ) : null}
        {users.length > 0 ? (
          <ObjectList>
            {users.map((user) => {
              const active = user.status === "active";
              return (
                <ObjectRow
                  key={user.id}
                  active={selected?.id === user.id}
                  onClick={() => setSelected(user)}
                  leading={<span className="cp-platform-row__mark" aria-hidden>{user.username.slice(0, 1).toUpperCase()}</span>}
                  title={user.username}
                  subtitle={`${user.role === "admin" ? t("管理员") : t("普通用户")} · ${t("{{count}} 个密钥", { count: user.key_count })}`}
                  facts={<><span>{t("余额")} <UserBalanceCell userId={user.id} /></span><span>{t("注册")} {formatNsTime(user.created_at_ns)}</span><span>{t("最近活动")} {formatNsRelative(user.last_login_ns)}</span></>}
                  status={<StatusMark tone={active ? "signal" : "alert"} label={active ? t("正常") : t("已停用")} />}
                  action={<ArrowRight size={14} aria-hidden />}
                />
              );
            })}
          </ObjectList>
        ) : null}
        {usersQuery.data ? (
          <ControlPagination
            page={page}
            totalItems={total}
            pageSize={PAGE_SIZE}
            disabled={usersQuery.isFetching}
            labels={{
              summary: ({ page: current, pages, start, end, total: count }) => t("第 {{page}} / {{pages}} 页 · {{start}}-{{end}} / {{total}}", { page: current, pages, start, end, total: count }),
              perPage: t("每页"),
              pageOption: (size) => `${size} / ${t("页")}`,
              previous: t("上一页"),
              next: t("下一页"),
            }}
            onPageChange={setPage}
            onPageSizeChange={() => undefined}
          />
        ) : null}
      </WorkSurface>

      <InspectorDrawer
        open={Boolean(selected) && !createOpen}
        onOpenChange={(open) => { if (!open) setSelected(null); }}
        title={selected?.username ?? t("用户")}
        eyebrow={t("Customer / user inspector")}
        footer={selected ? (
          <>
            <ControlButton variant="ghost" size="sm" onPress={() => setSelected(null)}>{t("关闭")}</ControlButton>
            <ControlButton variant="secondary" size="sm" onPress={() => navigate(`${opsPaths.users}/${selected.id}`)}><ExternalLink size={14} aria-hidden />{t("打开详情")}</ControlButton>
            <ControlButton
              variant="secondary"
              size="sm"
              isDisabled={toggleStatusMutation.isPending}
              onPress={() => toggleStatusMutation.mutate({ id: selected.id, nextStatus: selected.status === "active" ? "disabled" : "active" })}
              loading={toggleStatusMutation.isPending}
            >
              {selected.status === "active" ? <UserX size={14} aria-hidden /> : <UserCheck size={14} aria-hidden />}
              {selected.status === "active" ? t("停用") : t("启用")}
            </ControlButton>
            <ControlButton variant="danger" size="sm" onPress={() => setDeleteTarget(selected)}><Trash2 size={14} aria-hidden />{t("删除")}</ControlButton>
          </>
        ) : undefined}
      >
        {selected ? (
          <div className="cp-stack">
            <div className="cp-platform-inspector__identity">
              <span className="cp-platform-row__mark" aria-hidden>{selected.username.slice(0, 1).toUpperCase()}</span>
              <div><strong>{selected.username}</strong><code>{selected.id}</code></div>
              <StatusMark tone={selected.status === "active" ? "signal" : "alert"} label={selected.status === "active" ? t("正常") : t("已停用")} />
            </div>
            <dl className="cp-detail-facts">
              <DetailFact label={t("角色")} value={selected.role === "admin" ? t("管理员") : t("普通用户")} />
              <DetailFact label={t("余额")} value={<UserBalanceCell userId={selected.id} />} mono />
              <DetailFact label={t("访问密钥")} value={selected.key_count} mono />
              <DetailFact label={t("双因素认证")} value={selected.has_totp ? t("已启用") : t("未启用")} />
              <DetailFact label={t("注册时间")} value={formatNsTime(selected.created_at_ns)} mono />
              <DetailFact label={t("最近登录")} value={formatNsTime(selected.last_login_ns)} mono />
            </dl>
            <WorkSurface eyebrow="Subscription" title={t("当前套餐")}>
              {selected.subscription ? (
                <dl className="cp-detail-facts">
                  <DetailFact label={t("套餐")} value={selected.subscription.plan_name} />
                  <DetailFact label={t("状态")} value={selected.subscription.status} />
                  <DetailFact label={t("到期时间")} value={formatNsTime(selected.subscription.expires_at_ns)} mono />
                </dl>
              ) : <p className="cp-muted">{t("当前没有订阅")}</p>}
            </WorkSurface>
            <QueryBadge label={t("完整用量、账本、密钥和运营动作在用户详情页") } />
          </div>
        ) : null}
      </InspectorDrawer>

      <InspectorDrawer
        open={createOpen}
        onOpenChange={setCreateOpen}
        title={t("新建用户")}
        eyebrow={t("Administrator action")}
        footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => setCreateOpen(false)}>{t("取消")}</ControlButton><ControlButton type="submit" form="create-user-form" variant="primary" size="sm">{t("创建用户")}</ControlButton></div>}
      >
        {createOpen ? (
          <CreateUserContent
            plans={plansQuery.data?.items ?? []}
            onSuccess={() => {
              showToast("success", t("用户创建成功"));
              setCreateOpen(false);
              void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
            }}
          />
        ) : null}
      </InspectorDrawer>

      <ConfirmDrawer
        open={Boolean(deleteTarget)}
        onOpenChange={(open) => { if (!open) setDeleteTarget(null); }}
        title={t("确认删除用户")}
        eyebrow={t("Destructive action")}
        message={t("确定要删除用户 {{username}} 吗？此操作不可逆，将同时删除该用户的所有密钥与订阅。", { username: deleteTarget?.username ?? "" })}
        cancelLabel={t("取消")}
        confirmLabel={t("确认删除")}
        loading={deleteMutation.isPending}
        onConfirm={() => { if (deleteTarget) deleteMutation.mutate(deleteTarget.id); }}
      />
    </PageFrame>
  );
}

function UserBalanceCell({ userId }: { userId: string }) {
  const balanceQuery = useQuery({
    queryKey: ["admin-user-balance", userId, "row"],
    queryFn: () => getUserBalance(userId, 1),
    enabled: Boolean(userId),
    staleTime: 30_000,
  });
  if (!balanceQuery.isSuccess || !balanceQuery.data) return <span className="cp-muted">—</span>;
  return <span className="readout">{formatMicro(balanceQuery.data.balance_micro)}<small>USDT</small></span>;
}

function CreateUserContent({ plans, onSuccess }: { plans: AdminPlan[]; onSuccess: () => void }) {
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
    onError: (error) => setErrorMsg(formatApiErrorMessage(error, t)),
  });
  const handleSubmit = (event: React.FormEvent) => {
    event.preventDefault();
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
    <form id="create-user-form" className="cp-form-stack" onSubmit={handleSubmit}>
      {errorMsg ? <p className="cp-form-error">{errorMsg}</p> : null}
      <ControlInput id={usernameId} label={t("用户名")} value={username} onChange={(event) => setUsername(event.target.value)} placeholder={t("输入用户名")} />
      <ControlInput id={passwordId} label={t("初始密码")} hint={t("留空则由系统随机生成或无需初始密码")} type="password" value={password} onChange={(event) => setPassword(event.target.value)} />
      <ControlSelect id={roleId} label={t("用户角色")} value={role} onChange={(event) => setRole(event.target.value)}>
        <option value="user">{t("普通用户")}</option>
        <option value="admin">{t("管理员")}</option>
      </ControlSelect>
      <ControlSelect id={planId} label={t("绑定套餐 (可选)")} value={selectedPlanId} onChange={(event) => setSelectedPlanId(event.target.value)}>
        <option value="">{t("暂不绑定套餐")}</option>
        {plans.map((plan) => <option key={plan.id} value={plan.id}>{plan.name} ({plan.period})</option>)}
      </ControlSelect>
      {selectedPlanId ? <ControlInput id={durationId} label={t("有效天数")} type="number" min="1" value={durationDays} onChange={(event) => setDurationDays(event.target.value)} /> : null}
      {createMutation.isPending ? <p className="cp-muted">{t("正在创建用户")}</p> : null}
    </form>
  );
}
