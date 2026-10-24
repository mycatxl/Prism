import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Check, Copy, Pause, Play, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  ConfirmDrawer,
  ControlButton,
  ControlInput,
  ControlSelect,
  DetailFact,
  EmptyWorkState,
  InspectorDrawer,
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
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { copyToClipboard } from "../../lib/clipboard";
import { formatBytes } from "../../lib/bytes";
import { useNow } from "../../lib/clock";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro, parseAmountToMicro } from "../../lib/money";
import { formatGoDuration } from "../../lib/time";
import { opsPaths } from "../../lib/paths";
import {
  adjustUserBalance,
  adminPlansQueryKey,
  createSubscription,
  createUserKey,
  deleteUserKey,
  getUser,
  getUserUsage,
  listPlans,
  listUserKeys,
  updateSubscription,
  updateUser,
} from "./api";
import { adminUserBalanceQueryKey, useUserBalance } from "./userBalance";
import type { AdminBalanceEntry, AdminCreateKeyResponse, AdminKey, AdminPlan, AdminUserDetail } from "./types";

const NANOS_PER_MILLI = 1_000_000;
const SUBSCRIPTION_WARN_MS = 48 * 3_600_000;

type SubscriptionView = NonNullable<AdminUserDetail["subscription"]>;
type UsageDay = AdminUserDetail["usage_daily"][number];

function formatNsTime(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat(isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}

function formatNsDate(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat(isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(date);
}

function statusTone(status: string): "signal" | "alert" | "warn" | "neutral" {
  if (status === "active" || status === "succeeded") return "signal";
  if (status === "disabled" || status === "failed" || status === "expired") return "alert";
  return "warn";
}

function statusLabel(status: string, t: (value: string, values?: Record<string, unknown>) => string): string {
  switch (status) {
    case "active": return t("正常");
    case "disabled": return t("已停用");
    case "expired": return t("已过期");
    case "paused": return t("已暂停");
    default: return status || t("未知");
  }
}

function remainingPercent(remainingMs: number, windowMs: number): number {
  if (windowMs <= 0 || remainingMs <= 0) return 0;
  return Math.max(0, Math.min(100, (remainingMs / windowMs) * 100));
}

function formatRemaining(remainingMs: number): string {
  if (remainingMs <= 0) return "-";
  return formatGoDuration(`${Math.max(1, Math.ceil(remainingMs / 3_600_000))}h`);
}

function BalanceLedger({ userId }: { userId: string }) {
  const { t } = useI18n();
  const balanceQuery = useUserBalance(userId);
  const entries: AdminBalanceEntry[] = balanceQuery.data?.ledger ?? [];
  if (balanceQuery.isLoading) return <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载余额流水")}</div>;
  if (balanceQuery.isError) return <EmptyWorkState title={t("余额流水读取失败")} detail={formatApiErrorMessage(balanceQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void balanceQuery.refetch()}>{t("重试")}</ControlButton>} />;
  if (entries.length === 0) return <EmptyWorkState title={t("暂无余额变动")} detail={t("兑换码充值或运营调整后，流水会显示在这里。")} />;

  return (
    <ObjectList>
      {entries.map((entry) => (
        <ObjectRow
          key={entry.id}
          leading={<span className={`cp-platform-row__mark ${entry.delta_micro >= 0 ? "is-live" : "is-alert"}`} aria-hidden>{entry.delta_micro >= 0 ? "+" : "−"}</span>}
          title={<span className="readout">{entry.delta_micro >= 0 ? "+" : ""}{formatMicro(entry.delta_micro)} USDT</span>}
          subtitle={`${entry.reason || t("余额变动")} · ${formatNsTime(entry.created_at_ns)}`}
          facts={<><span>{t("变动后余额")} <span className="readout">{formatMicro(entry.balance_after_micro)} USDT</span></span><span>{entry.note || entry.actor || "-"}</span></>}
          status={<QueryBadge label={entry.request_id || t("无请求号")} />}
        />
      ))}
    </ObjectList>
  );
}

function UserSummary({ user, balance }: { user: AdminUserDetail; balance: ReturnType<typeof useUserBalance> }) {
  const { t } = useI18n();
  return (
    <WorkSurface
      eyebrow="Customer / account"
      title={t("账户概要")}
      meta={<StatusMark tone={user.status === "active" ? "signal" : "alert"} label={statusLabel(user.status, t)} />}
      description={t("账户状态、角色、余额和关键时间均来自管理员账户接口。")}
    >
      <div className="cp-readout-grid">
        <DetailFact label={t("当前余额")} value={balance.isSuccess ? `${formatMicro(balance.data?.balance_micro ?? 0)} USDT` : "—"} mono />
        <DetailFact label={t("角色")} value={user.role === "admin" ? t("管理员") : t("普通用户")} />
        <DetailFact label={t("注册时间")} value={formatNsTime(user.created_at_ns)} mono />
        <DetailFact label={t("最近登录")} value={formatNsTime(user.last_login_ns)} mono />
        <DetailFact label={t("更新时间")} value={formatNsTime(user.updated_at_ns)} mono />
        <DetailFact label={t("双因素认证")} value={user.has_totp ? t("已启用") : t("未启用")} />
      </div>
    </WorkSurface>
  );
}

function SubscriptionSurface({
  user,
  onAssign,
  onExtend,
  onToggle,
  pending,
}: {
  user: AdminUserDetail;
  onAssign: () => void;
  onExtend: () => void;
  onToggle: () => void;
  pending: boolean;
}) {
  const { t } = useI18n();
  const now = useNow();
  const sub = user.subscription;
  if (!sub) {
    return <WorkSurface eyebrow="Customer / plan" title={t("套餐与订阅")} actions={<ControlButton variant="primary" size="sm" onPress={onAssign}><Plus size={14} aria-hidden />{t("分配套餐")}</ControlButton>}><EmptyWorkState title={t("暂无活跃订阅")} detail={t("该用户当前未绑定任何套餐，点击上方按钮进行分配。")} /></WorkSurface>;
  }
  const startsMs = Math.round(sub.starts_at_ns / NANOS_PER_MILLI);
  const expiresMs = Math.round(sub.expires_at_ns / NANOS_PER_MILLI);
  const remainingMs = expiresMs - now;
  const windowMs = expiresMs - startsMs;
  const remaining = sub.remaining_bytes ?? Math.max(0, sub.traffic_bytes - sub.period_used_bytes);
  const hasQuota = sub.traffic_bytes > 0;
  return (
    <WorkSurface
      eyebrow="Customer / plan"
      title={sub.plan_name}
      meta={<StatusMark tone={statusTone(sub.status)} label={statusLabel(sub.status, t)} />}
      description={t("管理员可以暂停、恢复、分配或续期当前订阅；配额和时间只显示服务端快照。")}
      actions={<><ControlButton variant="secondary" size="sm" onPress={onToggle} loading={pending}>{sub.status === "active" ? <Pause size={14} aria-hidden /> : <Play size={14} aria-hidden />}{sub.status === "active" ? t("暂停") : t("恢复")}</ControlButton><ControlButton variant="secondary" size="sm" onPress={onExtend}>{t("续期")}</ControlButton><ControlButton variant="primary" size="sm" onPress={onAssign}>{t("更换套餐")}</ControlButton></>}
    >
      <div className="cp-readout-grid">
        <DetailFact label={t("订阅 ID")} value={sub.id} mono />
        <DetailFact label={t("生效时间")} value={formatNsTime(sub.starts_at_ns)} mono />
        <DetailFact label={t("到期时间")} value={formatNsTime(sub.expires_at_ns)} mono />
        <DetailFact label={t("周期已用流量")} value={formatBytes(sub.period_used_bytes)} mono />
        <DetailFact label={t("配额上限")} value={hasQuota ? formatBytes(sub.traffic_bytes) : t("不限")} mono />
        <DetailFact label={t("剩余流量")} value={hasQuota ? formatBytes(remaining) : t("不限")} mono />
      </div>
      <div className="cp-inset-stack">
        <div className="cp-lease-line">
          <div><span>{t("剩余时间")}</span><strong>{remainingMs > 0 ? formatRemaining(remainingMs) : t("已过期")}</strong></div>
          <span className="cp-lease-track" aria-hidden><span className={`cp-lease-fill ${remainingMs <= 0 ? "is-alert" : remainingMs <= SUBSCRIPTION_WARN_MS ? "is-warn" : ""}`} style={{ width: `${remainingMs <= 0 ? 100 : remainingPercent(remainingMs, windowMs)}%` }} /></span>
        </div>
        {hasQuota ? <div className="cp-lease-line"><div><span>{t("周期剩余流量")}</span><strong>{formatBytes(Math.max(0, remaining))} / {formatBytes(sub.traffic_bytes)}</strong></div><span className="cp-lease-track" aria-hidden><span className={`cp-lease-fill ${remaining <= 0 ? "is-alert" : remaining / sub.traffic_bytes <= 0.25 ? "is-warn" : ""}`} style={{ width: `${Math.max(0, Math.min(100, (remaining / sub.traffic_bytes) * 100))}%` }} /></span></div> : null}
      </div>
    </WorkSurface>
  );
}

function KeysSurface({ keys, onCreate, onDelete }: { keys: AdminKey[]; onCreate: () => void; onDelete: (key: AdminKey) => void }) {
  const { t } = useI18n();
  return (
    <WorkSurface eyebrow="Customer / access" title={t("访问密钥")} meta={t("{{count}} 个密钥", { count: keys.length })} actions={<ControlButton variant="primary" size="sm" onPress={onCreate}><Plus size={14} aria-hidden />{t("新建密钥")}</ControlButton>}>
      {keys.length === 0 ? <EmptyWorkState title={t("暂无访问密钥")} detail={t("点击上方按钮为该用户生成新的访问凭据。")} action={<ControlButton variant="primary" size="sm" onPress={onCreate}><Plus size={14} aria-hidden />{t("新建密钥")}</ControlButton>} /> : <ObjectList>{keys.map((key) => <ObjectRow key={key.id} leading={<span className="cp-platform-row__mark" aria-hidden>KEY</span>} title={key.name} subtitle={`${key.credential_hint || "****"} · ${key.platforms?.length ? key.platforms.join(", ") : t("全部平台")}`} facts={<><span>{t("创建")} {formatNsTime(key.created_at_ns)}</span><span>{t("最后使用")} {formatNsTime(key.last_used_ns)}</span></>} status={<StatusMark tone={statusTone(key.status)} label={statusLabel(key.status, t)} />} action={<ControlButton variant="ghost" size="sm" isIconOnly aria-label={t("删除密钥")} onPress={() => onDelete(key)}><Trash2 size={14} aria-hidden /></ControlButton>} />)}</ObjectList>}
    </WorkSurface>
  );
}

function UsageSurface({ days }: { days: UsageDay[] }) {
  const { t } = useI18n();
  const total = days.reduce((sum, day) => sum + day.total_bytes, 0);
  return (
    <WorkSurface eyebrow="Customer / usage" title={t("最近 30 天用量")} meta={days.length ? `${days.length} ${t("天")}` : t("未提供")} description={t("显示接口返回的每日入站、出站和合计流量，不在前端补算缺失数据。")}>
      {days.length === 0 ? <EmptyWorkState title={t("暂无历史用量数据")} detail={t("当前周期内未产生入站或出站流量。")} /> : <ObjectList>{days.map((day) => <ObjectRow key={day.day_ns} leading={<span className="cp-platform-row__mark" aria-hidden>DAY</span>} title={formatNsDate(day.day_ns)} subtitle={t("每日流量证据")} facts={<><span>{t("入站")} <span className="readout">{formatBytes(day.ingress_bytes)}</span></span><span>{t("出站")} <span className="readout">{formatBytes(day.egress_bytes)}</span></span><span>{t("合计")} <span className="readout">{formatBytes(day.total_bytes)}</span></span></>} status={day.total_bytes > 0 ? <QueryBadge label={formatBytes(day.total_bytes)} tone="signal" /> : <QueryBadge label={t("无流量")} />} />)}</ObjectList>}
      {days.length ? <p className="cp-muted">{t("当前返回周期合计")} <span className="readout">{formatBytes(total)}</span></p> : null}
    </WorkSurface>
  );
}

function AttributesSurface({ user }: { user: AdminUserDetail }) {
  const { t } = useI18n();
  return <WorkSurface eyebrow="Customer / identity" title={t("账户属性")}><dl className="cp-detail-facts"><DetailFact label={t("用户 ID")} value={user.id} mono /><DetailFact label={t("密钥数")} value={user.key_count} mono /><DetailFact label={t("双因素认证")} value={user.has_totp ? t("已启用") : t("未启用")} /></dl></WorkSurface>;
}

function AdminActionsSurface({ user, onRoleChange, rolePending, onBalance, onPassword, onStatus, statusPending }: { user: AdminUserDetail; onRoleChange: (role: string) => void; rolePending: boolean; onBalance: () => void; onPassword: () => void; onStatus: () => void; statusPending: boolean }) {
  const { t } = useI18n();
  return <WorkSurface eyebrow="Customer / operations" title={t("运营操作")} description={t("这些动作直接修改账户或账本，结果会通过服务端状态回读。")}>
    <div className="cp-form-stack">
      <ControlSelect label={t("用户角色")} value={user.role} disabled={rolePending} onChange={(event) => onRoleChange(event.target.value)}><option value="user">{t("普通用户")}</option><option value="admin">{t("管理员")}</option></ControlSelect>
      <div className="cp-drawer-actions"><ControlButton variant="secondary" size="sm" onPress={onBalance}><Plus size={14} aria-hidden />{t("调整余额")}</ControlButton><ControlButton variant="secondary" size="sm" onPress={onPassword}>{t("重置密码")}</ControlButton><ControlButton variant="danger" size="sm" onPress={onStatus} loading={statusPending}>{user.status === "active" ? t("停用账户") : t("启用账户")}</ControlButton></div>
    </div>
  </WorkSurface>;
}

export function UserDetailPage() {
  const { t } = useI18n();
  const { userId = "" } = useParams<{ userId: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();
  const [assignOpen, setAssignOpen] = useState(false);
  const [extendOpen, setExtendOpen] = useState(false);
  const [createKeyOpen, setCreateKeyOpen] = useState(false);
  const [secret, setSecret] = useState<AdminCreateKeyResponse | null>(null);
  const [resetOpen, setResetOpen] = useState(false);
  const [balanceOpen, setBalanceOpen] = useState(false);
  const [deleteKey, setDeleteKey] = useState<AdminKey | null>(null);
  const [statusTargetOpen, setStatusTargetOpen] = useState(false);

  const userQuery = useQuery({ queryKey: ["admin-user", userId], queryFn: () => getUser(userId), enabled: Boolean(userId) });
  const keysQuery = useQuery({ queryKey: ["admin-user-keys", userId], queryFn: () => listUserKeys(userId), enabled: Boolean(userId) });
  const usageQuery = useQuery({ queryKey: ["admin-user-usage", userId], queryFn: () => getUserUsage(userId, 30), enabled: Boolean(userId) });
  const plansQuery = useQuery({ queryKey: adminPlansQueryKey, queryFn: listPlans, staleTime: 60_000 });
  const balanceQuery = useUserBalance(userId);
  const user = userQuery.data;

  const invalidateUser = () => {
    void queryClient.invalidateQueries({ queryKey: ["admin-user", userId] });
    void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
    void queryClient.invalidateQueries({ queryKey: ["admin-user-keys", userId] });
    void queryClient.invalidateQueries({ queryKey: ["admin-user-usage", userId] });
    void queryClient.invalidateQueries({ queryKey: adminUserBalanceQueryKey(userId) });
  };
  const refresh = () => invalidateUser();

  const roleMutation = useMutation({ mutationFn: (role: string) => updateUser(userId, { role }), onSuccess: () => { showToast("success", t("用户角色已更新")); invalidateUser(); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const statusMutation = useMutation({ mutationFn: () => updateUser(userId, { status: user?.status === "active" ? "disabled" : "active" }), onSuccess: () => { setStatusTargetOpen(false); showToast("success", t("用户状态已更新")); invalidateUser(); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const subscriptionMutation = useMutation({ mutationFn: ({ id, status }: { id: string; status: string }) => updateSubscription(id, { status }), onSuccess: () => { showToast("success", t("订阅状态已更新")); invalidateUser(); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });
  const deleteKeyMutation = useMutation({ mutationFn: (keyId: string) => deleteUserKey(userId, keyId), onSuccess: () => { setDeleteKey(null); showToast("success", t("访问密钥已删除")); invalidateUser(); }, onError: (error) => showToast("error", formatApiErrorMessage(error, t)) });

  if (!userId) return <PageFrame eyebrow="Customers / user" title={t("用户详情")}><EmptyWorkState title={t("用户 ID 缺失")} detail={t("无法加载用户详情。")} /></PageFrame>;

  return (
    <PageFrame eyebrow="Customers / user detail" title={user?.username ?? userId} description={<code>{userId}</code>} meta={user ? <StatusMark tone={user.status === "active" ? "signal" : "alert"} label={statusLabel(user.status, t)} /> : <StatusMark tone="neutral" label={t("等待账户")}/>} actions={<><ControlButton variant="secondary" size="sm" onPress={() => navigate(opsPaths.users)}><ArrowLeft size={14} aria-hidden />{t("返回用户列表")}</ControlButton><ControlButton variant="secondary" size="sm" onPress={refresh} loading={userQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton></>}>
      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
      {userQuery.isError && !user ? <EmptyWorkState title={t("用户详情读取失败")} detail={formatApiErrorMessage(userQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void userQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
      {userQuery.isLoading && !user ? <div className="cp-register-state"><span className="cp-spinner" aria-hidden />{t("正在加载用户详情")}</div> : null}
      {user ? <div className="cp-detail-grid">
        <div className="cp-stack"><UserSummary user={user} balance={balanceQuery}/><SubscriptionSurface user={user} onAssign={() => setAssignOpen(true)} onExtend={() => setExtendOpen(true)} onToggle={() => { if (user.subscription) subscriptionMutation.mutate({ id: user.subscription.id, status: user.subscription.status === "active" ? "disabled" : "active" }); }} pending={subscriptionMutation.isPending}/><KeysSurface keys={keysQuery.data?.items ?? user.keys} onCreate={() => setCreateKeyOpen(true)} onDelete={setDeleteKey}/><UsageSurface days={usageQuery.data?.items ?? user.usage_daily}/><WorkSurface eyebrow="Customer / ledger" title={t("余额流水")} meta={balanceQuery.isSuccess ? `${formatMicro(balanceQuery.data.balance_micro)} USDT` : "—"}><BalanceLedger userId={userId}/></WorkSurface></div>
        <div className="cp-stack"><AttributesSurface user={user}/><AdminActionsSurface user={user} onRoleChange={(role) => roleMutation.mutate(role)} rolePending={roleMutation.isPending} onBalance={() => setBalanceOpen(true)} onPassword={() => setResetOpen(true)} onStatus={() => setStatusTargetOpen(true)} statusPending={statusMutation.isPending}/></div>
      </div> : null}

      <AssignPlanDrawer open={assignOpen} onOpenChange={setAssignOpen} userId={userId} currentSub={user?.subscription} plans={plansQuery.data?.items ?? []} onSuccess={() => { setAssignOpen(false); showToast("success", t("套餐已成功分配")); invalidateUser(); }}/>
      <ExtendSubscriptionDrawer open={extendOpen} onOpenChange={setExtendOpen} subscriptionId={user?.subscription?.id ?? ""} onSuccess={() => { setExtendOpen(false); showToast("success", t("订阅续期成功")); invalidateUser(); }}/>
      <CreateKeyDrawer open={createKeyOpen} onOpenChange={setCreateKeyOpen} userId={userId} onCreated={(result) => { setCreateKeyOpen(false); setSecret(result); invalidateUser(); }}/>
      <SecretDrawer data={secret} onClose={() => setSecret(null)}/>
      <ResetPasswordDrawer open={resetOpen} onOpenChange={setResetOpen} userId={userId} onSuccess={() => { setResetOpen(false); showToast("success", t("用户密码已重置")); }}/>
      <AdjustBalanceDrawer open={balanceOpen} onOpenChange={setBalanceOpen} userId={userId} onSuccess={() => { setBalanceOpen(false); showToast("success", t("余额已调整")); invalidateUser(); }}/>
      <ConfirmDrawer open={Boolean(deleteKey)} onOpenChange={(open) => { if (!open) setDeleteKey(null); }} title={t("确认删除访问密钥")} eyebrow={t("Destructive action")} message={t("确定要删除密钥 {{name}} 吗？删除后使用该密钥的客户端将立即无法访问。", { name: deleteKey?.name ?? "" })} cancelLabel={t("取消")} confirmLabel={t("确认删除")} loading={deleteKeyMutation.isPending} onConfirm={() => { if (deleteKey) deleteKeyMutation.mutate(deleteKey.id); }}/>
      <ConfirmDrawer open={statusTargetOpen} onOpenChange={setStatusTargetOpen} title={user?.status === "active" ? t("确认停用账户") : t("确认启用账户")} eyebrow={t("Account status")} message={user?.status === "active" ? t("停用后该账户将无法继续使用已授权的接入能力。") : t("启用后该账户可以恢复访问已授权的接入能力。")} cancelLabel={t("取消")} confirmLabel={user?.status === "active" ? t("确认停用") : t("确认启用")} loading={statusMutation.isPending} onConfirm={() => statusMutation.mutate()}/>
    </PageFrame>
  );
}

function AssignPlanDrawer({ open, onOpenChange, userId, currentSub, plans, onSuccess }: { open: boolean; onOpenChange: (open: boolean) => void; userId: string; currentSub?: SubscriptionView | null; plans: AdminPlan[]; onSuccess: () => void }) {
  const { t } = useI18n();
  const [planId, setPlanId] = useState("");
  const [days, setDays] = useState("30");
  const [error, setError] = useState("");
  const mutation = useMutation({ mutationFn: () => currentSub ? updateSubscription(currentSub.id, { plan_id: planId || plans[0]?.id || "", duration_days: Number(days) || 30 }) : createSubscription({ user_id: userId, plan_id: planId || plans[0]?.id || "", duration_days: Number(days) || 30 }), onSuccess: () => { setPlanId(""); setDays("30"); setError(""); onSuccess(); }, onError: (err) => setError(formatApiErrorMessage(err, t)) });
  const effectivePlanId = planId || plans[0]?.id || "";
  return <InspectorDrawer open={open} onOpenChange={onOpenChange} title={currentSub ? t("更换套餐") : t("分配套餐")} eyebrow="Customer / plan" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => onOpenChange(false)}>{t("取消")}</ControlButton><ControlButton variant="primary" size="sm" type="submit" form="assign-plan-form" loading={mutation.isPending}>{t("确定")}</ControlButton></div>}>
    <form id="assign-plan-form" className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); if (!effectivePlanId) { setError(t("请选择套餐")); return; } setError(""); mutation.mutate(); }}>
      {error ? <p className="cp-form-error">{error}</p> : null}
      <ControlSelect label={t("选择套餐")} value={effectivePlanId} onChange={(event) => setPlanId(event.target.value)}><option value="">{t("请选择套餐")}</option>{plans.map((plan) => <option key={plan.id} value={plan.id}>{plan.name} ({plan.period})</option>)}</ControlSelect>
      <ControlInput label={t("有效天数")} type="number" min="1" value={days} onChange={(event) => setDays(event.target.value)}/>
    </form>
  </InspectorDrawer>;
}

function ExtendSubscriptionDrawer({ open, onOpenChange, subscriptionId, onSuccess }: { open: boolean; onOpenChange: (open: boolean) => void; subscriptionId: string; onSuccess: () => void }) {
  const { t } = useI18n();
  const [days, setDays] = useState("30");
  const [error, setError] = useState("");
  const mutation = useMutation({ mutationFn: () => updateSubscription(subscriptionId, { extend_days: Number(days) || 30 }), onSuccess: () => { setDays("30"); setError(""); onSuccess(); }, onError: (err) => setError(formatApiErrorMessage(err, t)) });
  return <InspectorDrawer open={open} onOpenChange={onOpenChange} title={t("续期套餐")} eyebrow="Customer / subscription" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => onOpenChange(false)}>{t("取消")}</ControlButton><ControlButton variant="primary" size="sm" type="submit" form="extend-subscription-form" loading={mutation.isPending}>{t("确定续期")}</ControlButton></div>}>
    <form id="extend-subscription-form" className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); setError(""); mutation.mutate(); }}>
      {error ? <p className="cp-form-error">{error}</p> : null}<ControlInput label={t("延长天数")} type="number" min="1" value={days} onChange={(event) => setDays(event.target.value)}/>
    </form>
  </InspectorDrawer>;
}

function CreateKeyDrawer({ open, onOpenChange, userId, onCreated }: { open: boolean; onOpenChange: (open: boolean) => void; userId: string; onCreated: (result: AdminCreateKeyResponse) => void }) {
  const { t } = useI18n();
  const [name, setName] = useState("");
  const [platforms, setPlatforms] = useState("");
  const [allowlist, setAllowlist] = useState("");
  const [expires, setExpires] = useState("");
  const [error, setError] = useState("");
  const mutation = useMutation({ mutationFn: () => createUserKey(userId, { name: name.trim(), platforms: platforms.split(",").map((value) => value.trim()).filter(Boolean), ip_allowlist: allowlist.split(",").map((value) => value.trim()).filter(Boolean), expires_in_days: expires ? Number(expires) : undefined }), onSuccess: (result) => { setName(""); setPlatforms(""); setAllowlist(""); setExpires(""); setError(""); onCreated(result); }, onError: (err) => setError(formatApiErrorMessage(err, t)) });
  return <InspectorDrawer open={open} onOpenChange={onOpenChange} title={t("新建访问密钥")} eyebrow="Sensitive operation" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => onOpenChange(false)}>{t("取消")}</ControlButton><ControlButton variant="primary" size="sm" type="submit" form="create-admin-key-form" loading={mutation.isPending}>{t("生成密钥")}</ControlButton></div>}>
    <form id="create-admin-key-form" className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); if (!name.trim()) { setError(t("密钥名称不能为空")); return; } setError(""); mutation.mutate(); }}>
      {error ? <p className="cp-form-error">{error}</p> : null}
      <ControlInput label={t("密钥名称")} value={name} onChange={(event) => setName(event.target.value)} placeholder={t("例如: 生产环境客户端")}/>
      <ControlInput label={t("平台限制") } hint={t("留空不限制，多个值用逗号分隔。")} value={platforms} onChange={(event) => setPlatforms(event.target.value)}/>
      <ControlInput label={t("IP 白名单")} hint={t("留空不限制，多个值用逗号分隔。")} value={allowlist} onChange={(event) => setAllowlist(event.target.value)}/>
      <ControlInput label={t("有效天数")} hint={t("留空则永不过期")} type="number" min="1" value={expires} onChange={(event) => setExpires(event.target.value)}/>
    </form>
  </InspectorDrawer>;
}

function SecretDrawer({ data, onClose }: { data: AdminCreateKeyResponse | null; onClose: () => void }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState("");
  const copy = () => { if (!data?.credential) return; void copyToClipboard(data.credential).then((ok) => { if (!ok) { setError(t("复制失败，请手动选择复制")); setCopied(false); return; } setError(""); setCopied(true); window.setTimeout(() => setCopied(false), 1800); }); };
  return <InspectorDrawer open={Boolean(data)} onOpenChange={(open) => { if (!open) onClose(); }} title={t("密钥凭据已生成")} eyebrow="One-time secret" footer={<div className="cp-drawer-actions"><ControlButton variant="primary" size="sm" onPress={onClose}>{t("我已保存，关闭")}</ControlButton></div>}>
    <div className="cp-form-stack">{data ? <><p className="cp-notice cp-notice--warn">{t("请立即复制并妥善保管此凭据！出于安全考虑，该密钥将不再向任何人展示。")}</p><ControlInput label={t("明文访问凭据")} readOnly value={data.credential}/><div className="cp-drawer-actions"><ControlButton variant="secondary" size="sm" onPress={copy}>{copied ? <Check size={14} aria-hidden /> : <Copy size={14} aria-hidden />}{copied ? t("已复制") : t("复制")}</ControlButton></div>{error ? <p className="cp-form-error">{error}</p> : null}</> : null}</div>
  </InspectorDrawer>;
}

function ResetPasswordDrawer({ open, onOpenChange, userId, onSuccess }: { open: boolean; onOpenChange: (open: boolean) => void; userId: string; onSuccess: () => void }) {
  const { t } = useI18n();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const mutation = useMutation({ mutationFn: () => updateUser(userId, { password }), onSuccess: () => { setPassword(""); setError(""); onSuccess(); }, onError: (err) => setError(formatApiErrorMessage(err, t)) });
  return <InspectorDrawer open={open} onOpenChange={onOpenChange} title={t("重置用户密码")} eyebrow="Sensitive operation" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => onOpenChange(false)}>{t("取消")}</ControlButton><ControlButton variant="primary" size="sm" type="submit" form="reset-admin-password-form" loading={mutation.isPending}>{t("确认重置")}</ControlButton></div>}>
    <form id="reset-admin-password-form" className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); if (!password.trim()) { setError(t("新密码不能为空")); return; } setError(""); mutation.mutate(); }}>
      {error ? <p className="cp-form-error">{error}</p> : null}<ControlInput label={t("新密码")} type="password" value={password} onChange={(event) => setPassword(event.target.value)} />
    </form>
  </InspectorDrawer>;
}

function AdjustBalanceDrawer({ open, onOpenChange, userId, onSuccess }: { open: boolean; onOpenChange: (open: boolean) => void; userId: string; onSuccess: () => void }) {
  const { t } = useI18n();
  const [amount, setAmount] = useState("");
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const mutation = useMutation({ mutationFn: () => { const raw = amount.trim(); const negative = raw.startsWith("-"); const parsed = parseAmountToMicro(negative ? raw.slice(1) : raw); if (parsed === null || parsed === 0) throw new Error(t("请输入非零金额，最多六位小数。")); if (!note.trim()) throw new Error(t("手动调整必须填写备注。")); return adjustUserBalance(userId, negative ? -parsed : parsed, note.trim()); }, onSuccess: () => { setAmount(""); setNote(""); setError(""); onSuccess(); }, onError: (err) => setError(err instanceof Error ? err.message : formatApiErrorMessage(err, t)) });
  return <InspectorDrawer open={open} onOpenChange={onOpenChange} title={t("调整余额")} eyebrow="Sensitive operation" footer={<div className="cp-drawer-actions"><ControlButton variant="ghost" size="sm" onPress={() => onOpenChange(false)}>{t("取消")}</ControlButton><ControlButton variant="primary" size="sm" type="submit" form="adjust-admin-balance-form" loading={mutation.isPending}>{t("提交调整")}</ControlButton></div>}>
    <form id="adjust-admin-balance-form" className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); setError(""); mutation.mutate(); }}>
      {error ? <p className="cp-form-error">{error}</p> : null}<ControlInput label={t("变动金额 (USDT)")} hint={t("正数增加余额，负数扣减；最多六位小数。")} value={amount} onChange={(event) => setAmount(event.target.value)} inputMode="decimal" placeholder="10.000000"/><ControlInput label={t("调整备注")} value={note} onChange={(event) => setNote(event.target.value)} placeholder={t("说明这次调整的原因")}/>
    </form>
  </InspectorDrawer>;
}

export default UserDetailPage;
