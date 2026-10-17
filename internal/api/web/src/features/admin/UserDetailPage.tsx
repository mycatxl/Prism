import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  Check,
  Copy,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { useId, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import {
  DialogContent,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "../../components/ui/Dialog";
import { Input } from "../../components/ui/Input";
import { PageHeader } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Select } from "../../components/ui/Select";
import { Sheet } from "../../components/ui/Sheet";
import { Sparkline } from "../../components/ui/Sparkline";
import { Table, TableWrap, TBody, TD, TDClip, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { copyToClipboard } from "../../lib/clipboard";
import { formatBytes } from "../../lib/bytes";
import { formatApiErrorMessage } from "../../lib/error-message";
import { UserBalancePanel } from "./UserBalancePanel";
import {
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
import type { AdminCreateKeyResponse, AdminKey, AdminPlan, AdminUserDetail } from "./types";

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

function formatNsDate(ns?: number | null): string {
  if (!ns || ns <= 0) return "-";
  const date = new Date(Math.round(ns / NANOS_PER_MILLI));
  if (Number.isNaN(date.getTime())) return "-";
  const locale = isEnglishLocale(getCurrentLocale()) ? "en-US" : "zh-CN";
  return new Intl.DateTimeFormat(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(date);
}

export function UserDetailPage() {
  const { t } = useI18n();
  const { id = "" } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  // Modals / Sheets
  const [assignPlanOpen, setAssignPlanOpen] = useState(false);
  const [extendSubOpen, setExtendSubOpen] = useState(false);
  const [createKeyOpen, setCreateKeyOpen] = useState(false);
  const [resetPwdOpen, setResetPwdOpen] = useState(false);
  const [newKeySecretModal, setNewKeySecretModal] = useState<AdminCreateKeyResponse | null>(null);
  const [deleteKeyTarget, setDeleteKeyTarget] = useState<AdminKey | null>(null);

  const {
    data: user,
    isLoading: userLoading,
    isError: userError,
    error: userErr,
    refetch: refetchUser,
  } = useQuery<AdminUserDetail>({
    queryKey: ["admin-user", id],
    queryFn: () => getUser(id),
    enabled: Boolean(id),
  });

  const { data: keysData, refetch: refetchKeys } = useQuery({
    queryKey: ["admin-user-keys", id],
    queryFn: () => listUserKeys(id),
    enabled: Boolean(id),
  });

  const { data: usageData } = useQuery({
    queryKey: ["admin-user-usage", id],
    queryFn: () => getUserUsage(id, 30),
    enabled: Boolean(id),
  });

  const { data: plansData } = useQuery({
    queryKey: ["admin-plans-list"],
    queryFn: () => listPlans(),
  });

  // Role update mutation
  const updateRoleMutation = useMutation({
    mutationFn: (newRole: string) => updateUser(id, { role: newRole }),
    onSuccess: () => {
      showToast("success", t("用户角色已更新"));
      void queryClient.invalidateQueries({ queryKey: ["admin-user", id] });
      void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  // Toggle sub status mutation
  const toggleSubStatusMutation = useMutation({
    mutationFn: ({ subId, nextStatus }: { subId: string; nextStatus: string }) =>
      updateSubscription(subId, { status: nextStatus }),
    onSuccess: () => {
      showToast("success", t("订阅状态已更新"));
      void queryClient.invalidateQueries({ queryKey: ["admin-user", id] });
      void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  // Delete key mutation
  const deleteKeyMutation = useMutation({
    mutationFn: (keyId: string) => deleteUserKey(id, keyId),
    onSuccess: () => {
      showToast("success", t("访问密钥已删除"));
      setDeleteKeyTarget(null);
      void queryClient.invalidateQueries({ queryKey: ["admin-user-keys", id] });
      void queryClient.invalidateQueries({ queryKey: ["admin-user", id] });
    },
    onError: (err) => {
      showToast("error", formatApiErrorMessage(err, t));
    },
  });

  if (userLoading) {
    return <LoadingState label={t("正在加载用户详情...")} className="py-24" />;
  }

  if (userError || !user) {
    return (
      <div className="space-y-4">
        <Button variant="secondary" onClick={() => navigate("/admin/users")}>
          <ArrowLeft size={14} />
          <span>{t("返回用户列表")}</span>
        </Button>
        <ErrorState
          message={formatApiErrorMessage(userErr, t)}
          onRetry={() => void refetchUser()}
          className="py-16"
        />
      </div>
    );
  }

  const sub = user.subscription;
  const keysList = keysData?.items ?? user.keys ?? [];
  const dailyList = usageData?.items ?? user.usage_daily ?? [];
  const sparklineValues = dailyList.map((d) => d.total_bytes);

  return (
    <div className="space-y-6">
      {/* Back button and page header */}
      <div>
        <Button
          variant="ghost"
          onClick={() => navigate("/admin/users")}
          className="mb-2 -ml-2 text-ink-soft hover:text-ink"
        >
          <ArrowLeft size={14} />
          <span>{t("返回用户列表")}</span>
        </Button>

        <PageHeader
          title={user.username}
          description={t("创建于 {{date}} · 最后登录: {{lastLogin}}", {
            date: formatNsTime(user.created_at_ns),
            lastLogin: formatNsTime(user.last_login_ns),
          })}
          actions={
            <div className="flex items-center gap-2">
              <Badge tone={user.status === "active" ? "signal" : "warn"} dot>
                {user.status === "active" ? t("正常") : t("已停用")}
              </Badge>
              <Badge tone={user.role === "admin" ? "accent" : "neutral"}>
                {user.role === "admin" ? t("管理员") : t("普通用户")}
              </Badge>
              <Button
                variant="secondary"
                onClick={() => {
                  void refetchUser();
                  void refetchKeys();
                }}
              >
                <RefreshCw size={14} />
                <span>{t("刷新")}</span>
              </Button>
            </div>
          }
        />
      </div>

      {/* Grid of Sections */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        {/* Section B: 订阅 Panel */}
        <Panel>
          <PanelHeader
            title={t("套餐与订阅")}
            actions={
              <div className="flex items-center gap-2">
                {sub && (
                  <>
                    <Button
                      variant="ghost"
                      onClick={() =>
                        toggleSubStatusMutation.mutate({
                          subId: sub.id,
                          nextStatus: sub.status === "active" ? "disabled" : "active",
                        })
                      }
                      disabled={toggleSubStatusMutation.isPending}
                      title={sub.status === "active" ? t("暂停订阅") : t("恢复订阅")}
                    >
                      {sub.status === "active" ? (
                        <>
                          <Pause size={14} className="text-warn" />
                          <span>{t("暂停")}</span>
                        </>
                      ) : (
                        <>
                          <Play size={14} className="text-signal" />
                          <span>{t("恢复")}</span>
                        </>
                      )}
                    </Button>
                    <Button variant="secondary" onClick={() => setExtendSubOpen(true)}>
                      {t("续期")}
                    </Button>
                  </>
                )}
                <Button variant="primary" onClick={() => setAssignPlanOpen(true)}>
                  {sub ? t("更换套餐") : t("分配套餐")}
                </Button>
              </div>
            }
          />

          <PanelBody className="space-y-4">
            {sub ? (
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <span className="micro block text-ink-faint">{t("当前套餐")}</span>
                  <span className="mt-1 block font-semibold text-ink">
                    {sub.plan_name}
                  </span>
                </div>
                <div>
                  <span className="micro block text-ink-faint">{t("订阅状态")}</span>
                  <div className="mt-1">
                    <Badge tone={sub.status === "active" ? "signal" : "warn"} dot>
                      {sub.status === "active" ? t("正常") : t("已暂停")}
                    </Badge>
                  </div>
                </div>
                <div>
                  <span className="micro block text-ink-faint">{t("生效时间")}</span>
                  <span className="readout mt-1 block text-xs text-ink-soft">
                    {formatNsTime(sub.starts_at_ns)}
                  </span>
                </div>
                <div>
                  <span className="micro block text-ink-faint">{t("到期时间")}</span>
                  <span className="readout mt-1 block text-xs text-ink-soft">
                    {formatNsTime(sub.expires_at_ns)}
                  </span>
                </div>
                <div>
                  <span className="micro block text-ink-faint">{t("周期已用流量")}</span>
                  <span className="readout mt-1 block text-xs font-semibold text-ink">
                    {formatBytes(sub.period_used_bytes)}
                  </span>
                </div>
                <div>
                  <span className="micro block text-ink-faint">{t("配额上限 / 剩余")}</span>
                  <span className="readout mt-1 block text-xs text-ink-soft">
                    {sub.traffic_bytes > 0 ? formatBytes(sub.traffic_bytes) : t("不限")}{" "}
                    /{" "}
                    {sub.remaining_bytes != null
                      ? formatBytes(sub.remaining_bytes)
                      : t("不限")}
                  </span>
                </div>
              </div>
            ) : (
              <EmptyState
                title={t("暂无活跃订阅")}
                hint={t("该用户当前未绑定任何套餐，点击上方按钮进行分配。")}
                className="py-8"
              />
            )}
          </PanelBody>
        </Panel>

        {/* Section E: 安全 Panel */}
        <Panel>
          <PanelHeader
            title={t("账户与安全")}
            actions={
              <Button variant="secondary" onClick={() => setResetPwdOpen(true)}>
                {t("重置密码")}
              </Button>
            }
          />

          <PanelBody className="space-y-4">
            <div className="grid grid-cols-2 gap-4">
              <div>
                <span className="micro block text-ink-faint">{t("用户角色")}</span>
                <div className="mt-1 max-w-[10rem]">
                  <Select
                    value={user.role}
                    onChange={(e) => updateRoleMutation.mutate(e.target.value)}
                    disabled={updateRoleMutation.isPending}
                  >
                    <option value="user">{t("普通用户")}</option>
                    <option value="admin">{t("管理员")}</option>
                  </Select>
                </div>
              </div>

              <div>
                <span className="micro block text-ink-faint">{t("双因素认证 (TOTP)")}</span>
                <div className="mt-1">
                  <Badge tone={user.has_totp ? "signal" : "neutral"} dot>
                    {user.has_totp ? t("已启用") : t("未启用")}
                  </Badge>
                </div>
              </div>

              <div>
                <span className="micro block text-ink-faint">{t("创建时间")}</span>
                <span className="readout mt-1 block text-xs text-ink-soft">
                  {formatNsTime(user.created_at_ns)}
                </span>
              </div>

              <div>
                <span className="micro block text-ink-faint">{t("更新时间")}</span>
                <span className="readout mt-1 block text-xs text-ink-soft">
                  {formatNsTime(user.updated_at_ns)}
                </span>
              </div>
            </div>
          </PanelBody>
        </Panel>
      </div>

      <UserBalancePanel userId={id} />

      {/* Section C: 访问密钥 Panel */}
      <Panel>
        <PanelHeader
          title={t("访问密钥")}
          meta={<Badge tone="neutral">{keysList.length}</Badge>}
          actions={
            <Button variant="primary" onClick={() => setCreateKeyOpen(true)}>
              <Plus size={14} />
              <span>{t("新建密钥")}</span>
            </Button>
          }
        />

        <PanelBody className="p-0">
          {keysList.length === 0 ? (
            <EmptyState
              title={t("暂无访问密钥")}
              hint={t("点击右上角为该用户生成新的访问密钥。")}
              className="py-12"
            />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("密钥名称")}</TH>
                    <TH>{t("凭据提示")}</TH>
                    <TH>{t("状态")}</TH>
                    <TH>{t("平台限制")}</TH>
                    <TH>{t("创建时间")}</TH>
                    <TH>{t("最后使用")}</TH>
                    <TH className="text-right">{t("操作")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {keysList.map((key) => (
                    <TR key={key.id}>
                      <TD>
                        <TDClip className="font-semibold text-ink">{key.name}</TDClip>
                      </TD>
                      <TD>
                        <code className="rounded-control bg-glass px-1.5 py-0.5 font-mono text-xs text-ink">
                          {key.credential_hint || "****"}
                        </code>
                      </TD>
                      <TD>
                        <Badge tone={key.status === "active" ? "signal" : "neutral"} dot>
                          {key.status === "active" ? t("正常") : t("已停用")}
                        </Badge>
                      </TD>
                      <TD>
                        <TDClip className="text-xs text-ink-soft">
                          {key.platforms && key.platforms.length > 0
                            ? key.platforms.join(", ")
                            : t("全部平台")}
                        </TDClip>
                      </TD>
                      <TD className="readout text-xs">
                        {formatNsTime(key.created_at_ns)}
                      </TD>
                      <TD className="readout text-xs">
                        {formatNsTime(key.last_used_ns)}
                      </TD>
                      <TD className="text-right">
                        <Button
                          variant="ghost"
                          onClick={() => setDeleteKeyTarget(key)}
                          title={t("删除密钥")}
                        >
                          <Trash2 size={14} className="text-alert" />
                          <span>{t("删除")}</span>
                        </Button>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          )}
        </PanelBody>
      </Panel>

      {/* Section D: 用量 Panel */}
      <Panel>
        <PanelHeader
          title={t("最近 30 天用量明细")}
          description={t("展示近一个月的每日入站、出站及合计流量消耗。")}
          actions={
            sparklineValues.length >= 2 ? (
              <div className="w-36">
                <Sparkline values={sparklineValues} tone="accent" height={28} />
              </div>
            ) : undefined
          }
        />

        <PanelBody className="p-0">
          {dailyList.length === 0 ? (
            <EmptyState
              title={t("暂无历史用量数据")}
              hint={t("当前周期内未产生入站或出站流量。")}
              className="py-12"
            />
          ) : (
            <TableWrap>
              <Table>
                <THead>
                  <TR>
                    <TH>{t("日期")}</TH>
                    <TH className="text-right">{t("入站流量")}</TH>
                    <TH className="text-right">{t("出站流量")}</TH>
                    <TH className="text-right">{t("合计流量")}</TH>
                  </TR>
                </THead>
                <TBody>
                  {dailyList.map((day) => (
                    <TR key={day.day_ns}>
                      <TD className="readout font-mono text-xs">
                        {formatNsDate(day.day_ns)}
                      </TD>
                      <TD className="readout text-right text-xs text-ink-soft">
                        {formatBytes(day.ingress_bytes)}
                      </TD>
                      <TD className="readout text-right text-xs text-ink-soft">
                        {formatBytes(day.egress_bytes)}
                      </TD>
                      <TD className="readout text-right text-xs font-semibold text-ink">
                        {formatBytes(day.total_bytes)}
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          )}
        </PanelBody>
      </Panel>

      {/* Dialog: 分配套餐 */}
      <AssignPlanDialog
        open={assignPlanOpen}
        onOpenChange={setAssignPlanOpen}
        userId={id}
        currentSubId={sub?.id}
        plans={plansData?.items ?? []}
        onSuccess={() => {
          showToast("success", t("套餐已成功分配"));
          setAssignPlanOpen(false);
          void queryClient.invalidateQueries({ queryKey: ["admin-user", id] });
          void queryClient.invalidateQueries({ queryKey: ["admin-users"] });
        }}
      />

      {/* Dialog: 续期 */}
      <ExtendSubDialog
        open={extendSubOpen}
        onOpenChange={setExtendSubOpen}
        subId={sub?.id ?? ""}
        onSuccess={() => {
          showToast("success", t("订阅续期成功"));
          setExtendSubOpen(false);
          void queryClient.invalidateQueries({ queryKey: ["admin-user", id] });
        }}
      />

      {/* Sheet: 新建密钥 */}
      <CreateKeySheet
        open={createKeyOpen}
        onOpenChange={setCreateKeyOpen}
        userId={id}
        onCreated={(res) => {
          setCreateKeyOpen(false);
          setNewKeySecretModal(res);
          void queryClient.invalidateQueries({ queryKey: ["admin-user-keys", id] });
          void queryClient.invalidateQueries({ queryKey: ["admin-user", id] });
        }}
      />

      {/* Dialog: 展示一次性明文密钥 */}
      <SecretOnceDialog
        data={newKeySecretModal}
        onClose={() => setNewKeySecretModal(null)}
      />

      {/* Dialog: 重置密码 */}
      <ResetPasswordDialog
        open={resetPwdOpen}
        onOpenChange={setResetPwdOpen}
        userId={id}
        onSuccess={() => {
          showToast("success", t("用户密码已重置"));
          setResetPwdOpen(false);
        }}
      />

      {/* Dialog: 删除密钥确认 */}
      <DialogRoot
        open={Boolean(deleteKeyTarget)}
        onOpenChange={(open) => !open && setDeleteKeyTarget(null)}
      >
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md p-6">
            <DialogTitle className="text-base font-semibold text-ink">
              {t("确认删除访问密钥")}
            </DialogTitle>
            <p className="mt-3 text-sm text-ink-soft">
              {t("确定要删除密钥 {{name}} 吗？删除后使用该密钥的客户端将立即无法访问。", {
                name: deleteKeyTarget?.name ?? "",
              })}
            </p>
            <div className="mt-6 flex items-center justify-end gap-3">
              <Button
                variant="secondary"
                onClick={() => setDeleteKeyTarget(null)}
                disabled={deleteKeyMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                variant="primary"
                onClick={() => {
                  if (deleteKeyTarget) deleteKeyMutation.mutate(deleteKeyTarget.id);
                }}
                disabled={deleteKeyMutation.isPending}
              >
                {deleteKeyMutation.isPending ? t("正在删除...") : t("确认删除")}
              </Button>
            </div>
          </DialogContent>
        </DialogPortal>
      </DialogRoot>

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Dialog: 分配套餐
// ---------------------------------------------------------------------------
function AssignPlanDialog({
  open,
  onOpenChange,
  userId,
  currentSubId,
  plans,
  onSuccess,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  userId: string;
  currentSubId?: string;
  plans: AdminPlan[];
  onSuccess: () => void;
}) {
  const { t } = useI18n();
  const planIdField = useId();
  const durationField = useId();

  /*
   * `plans` arrives from its own query, so the first render can see an empty list.
   * Seeding `planId` from `plans[0]` at mount time therefore left it as "" while the
   * native select displayed its first option regardless — submitting then answered
   * "请选择套餐" with a plan visibly selected. The selection is derived instead, so
   * `planId` holds nothing but an explicit choice: until the operator picks one, the
   * first plan that has arrived is both what the select shows and what the form
   * submits, and the two can never disagree. Deriving it also keeps a `setState` out
   * of an effect, which would only have bought a cascading render.
   */
  const [planId, setPlanId] = useState("");
  const [durationDays, setDurationDays] = useState("30");
  const [errorMsg, setErrorMsg] = useState("");
  const effectivePlanId = planId || plans[0]?.id || "";

  const mutation = useMutation({
    mutationFn: async () => {
      const days = Number(durationDays) || 30;
      if (currentSubId) {
        return updateSubscription(currentSubId, {
          plan_id: effectivePlanId,
          duration_days: days,
        });
      }
      return createSubscription({
        user_id: userId,
        plan_id: effectivePlanId,
        duration_days: days,
      });
    },
    onSuccess: () => {
      // The dialog closes on success; the next open starts from a clean slate.
      setPlanId("");
      setDurationDays("30");
      setErrorMsg("");
      onSuccess();
    },
    onError: (err) => setErrorMsg(formatApiErrorMessage(err, t)),
  });

  return (
    <DialogRoot open={open} onOpenChange={onOpenChange}>
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="w-full max-w-md p-6">
          <DialogTitle className="text-base font-semibold text-ink">
            {currentSubId ? t("更换套餐") : t("分配套餐")}
          </DialogTitle>

          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (!effectivePlanId) {
                setErrorMsg(t("请选择套餐"));
                return;
              }
              setErrorMsg("");
              mutation.mutate();
            }}
            className="mt-4 space-y-4"
          >
            {errorMsg && (
              <div className="rounded-control border border-alert/30 bg-alert-wash p-3 text-xs text-alert">
                {errorMsg}
              </div>
            )}

            <div className="space-y-1.5">
              <label htmlFor={planIdField} className="block text-xs font-medium text-ink-soft">
                {t("选择套餐")}
              </label>
              <Select
                id={planIdField}
                value={effectivePlanId}
                onChange={(e) => setPlanId(e.target.value)}
              >
                <option value="">{t("请选择套餐")}</option>
                {plans.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name} ({p.period}) - {formatBytes(p.traffic_bytes)}
                  </option>
                ))}
              </Select>
            </div>

            <div className="space-y-1.5">
              <label htmlFor={durationField} className="block text-xs font-medium text-ink-soft">
                {t("有效天数")}
              </label>
              <Input
                id={durationField}
                type="number"
                min="1"
                value={durationDays}
                onChange={(e) => setDurationDays(e.target.value)}
              />
            </div>

            <div className="mt-6 flex items-center justify-end gap-3">
              <Button
                type="button"
                variant="secondary"
                onClick={() => onOpenChange(false)}
                disabled={mutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button type="submit" variant="primary" disabled={mutation.isPending}>
                {mutation.isPending ? t("正在保存...") : t("确定")}
              </Button>
            </div>
          </form>
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}

// ---------------------------------------------------------------------------
// Dialog: 续期
// ---------------------------------------------------------------------------
function ExtendSubDialog({
  open,
  onOpenChange,
  subId,
  onSuccess,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  subId: string;
  onSuccess: () => void;
}) {
  const { t } = useI18n();
  const daysField = useId();
  const [days, setDays] = useState("30");
  const [errorMsg, setErrorMsg] = useState("");

  const mutation = useMutation({
    mutationFn: () =>
      updateSubscription(subId, {
        extend_days: Number(days) || 30,
      }),
    onSuccess,
    onError: (err) => setErrorMsg(formatApiErrorMessage(err, t)),
  });

  return (
    <DialogRoot open={open} onOpenChange={onOpenChange}>
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="w-full max-w-md p-6">
          <DialogTitle className="text-base font-semibold text-ink">
            {t("续期套餐")}
          </DialogTitle>

          <form
            onSubmit={(e) => {
              e.preventDefault();
              setErrorMsg("");
              mutation.mutate();
            }}
            className="mt-4 space-y-4"
          >
            {errorMsg && (
              <div className="rounded-control border border-alert/30 bg-alert-wash p-3 text-xs text-alert">
                {errorMsg}
              </div>
            )}

            <div className="space-y-1.5">
              <label htmlFor={daysField} className="block text-xs font-medium text-ink-soft">
                {t("延长天数")}
              </label>
              <Input
                id={daysField}
                type="number"
                min="1"
                value={days}
                onChange={(e) => setDays(e.target.value)}
              />
            </div>

            <div className="mt-6 flex items-center justify-end gap-3">
              <Button
                type="button"
                variant="secondary"
                onClick={() => onOpenChange(false)}
                disabled={mutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button type="submit" variant="primary" disabled={mutation.isPending}>
                {mutation.isPending ? t("正在续期...") : t("确定续期")}
              </Button>
            </div>
          </form>
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}

// ---------------------------------------------------------------------------
// Sheet: 新建访问密钥
// ---------------------------------------------------------------------------
function CreateKeySheet({
  open,
  onOpenChange,
  userId,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  userId: string;
  onCreated: (res: AdminCreateKeyResponse) => void;
}) {
  const { t } = useI18n();
  const nameField = useId();
  const platformsField = useId();
  const ipField = useId();
  const expiresField = useId();

  const [name, setName] = useState("");
  const [platformsRaw, setPlatformsRaw] = useState("");
  const [ipRaw, setIpRaw] = useState("");
  const [expiresInDays, setExpiresInDays] = useState("");
  const [errorMsg, setErrorMsg] = useState("");

  const mutation = useMutation({
    mutationFn: () =>
      createUserKey(userId, {
        name: name.trim(),
        platforms: platformsRaw
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        ip_allowlist: ipRaw
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean),
        expires_in_days: expiresInDays ? Number(expiresInDays) : undefined,
      }),
    onSuccess: (res) => {
      setName("");
      setPlatformsRaw("");
      setIpRaw("");
      setExpiresInDays("");
      setErrorMsg("");
      onCreated(res);
    },
    onError: (err) => setErrorMsg(formatApiErrorMessage(err, t)),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setErrorMsg(t("密钥名称不能为空"));
      return;
    }
    setErrorMsg("");
    mutation.mutate();
  };

  return (
    <Sheet
      open={open}
      onOpenChange={(next) => {
        if (!mutation.isPending) {
          setErrorMsg("");
          onOpenChange(next);
        }
      }}
      title={t("新建访问密钥")}
      description={t("为该用户签发一条新的访问凭据。生成后请立即复制妥善保存。")}
      width="md"
      footer={
        <div className="flex items-center justify-end gap-2">
          <Button
            variant="secondary"
            onClick={() => onOpenChange(false)}
            disabled={mutation.isPending}
          >
            {t("取消")}
          </Button>
          <Button
            variant="primary"
            onClick={handleSubmit}
            disabled={mutation.isPending}
          >
            {mutation.isPending ? t("正在生成...") : t("生成密钥")}
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
          <label htmlFor={nameField} className="block text-xs font-medium text-ink-soft">
            {t("密钥名称")} *
          </label>
          <Input
            id={nameField}
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("例如: 生产环境客户端")}
            required
          />
        </div>

        <div className="space-y-1.5">
          <label htmlFor={platformsField} className="block text-xs font-medium text-ink-soft">
            {t("平台限制 (可选，多个逗号分隔)")}
          </label>
          <Input
            id={platformsField}
            value={platformsRaw}
            onChange={(e) => setPlatformsRaw(e.target.value)}
            placeholder={t("留空不限制，或填: openai, anthropic")}
          />
        </div>

        <div className="space-y-1.5">
          <label htmlFor={ipField} className="block text-xs font-medium text-ink-soft">
            {t("IP 白名单 (可选，多个逗号分隔)")}
          </label>
          <Input
            id={ipField}
            value={ipRaw}
            onChange={(e) => setIpRaw(e.target.value)}
            placeholder={t("留空不限制，或填: 1.2.3.4, 5.6.7.8/24")}
          />
        </div>

        <div className="space-y-1.5">
          <label htmlFor={expiresField} className="block text-xs font-medium text-ink-soft">
            {t("有效天数 (可选)")}
          </label>
          <Input
            id={expiresField}
            type="number"
            min="1"
            value={expiresInDays}
            onChange={(e) => setExpiresInDays(e.target.value)}
            placeholder={t("留空则永不过期")}
          />
        </div>
      </form>
    </Sheet>
  );
}

// ---------------------------------------------------------------------------
// Dialog: 展示一次性明文密钥
// ---------------------------------------------------------------------------
function SecretOnceDialog({
  data,
  onClose,
}: {
  data: AdminCreateKeyResponse | null;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState("");

  const handleCopy = () => {
    if (!data?.credential) return;
    // `navigator.clipboard` is absent on a plain-HTTP page; calling it there threw
    // before any failure notice could render.
    void copyToClipboard(data.credential).then((ok) => {
      if (!ok) {
        setCopied(false);
        setCopyError(t("复制失败，请手动选择复制"));
        return;
      }
      setCopyError("");
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  };

  return (
    <DialogRoot open={Boolean(data)} onOpenChange={(open) => !open && onClose()}>
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="w-full max-w-lg p-6">
          <DialogTitle className="text-base font-semibold text-ink">
            {t("密钥凭据已生成")}
          </DialogTitle>

          <div className="mt-3 rounded-control border border-warn/30 bg-warn-wash p-3 text-xs text-warn">
            {t("请立即复制并妥善保管此凭据！出于安全考虑，该密钥将不再向任何人展示。")}
          </div>

          <div className="mt-4 space-y-1.5">
            <span className="micro block text-ink-faint">{t("明文访问凭据")}</span>
            <div className="flex items-center gap-2">
              <Input
                readOnly
                value={data?.credential || ""}
                className="font-mono text-xs"
              />
              <Button variant="secondary" onClick={handleCopy} className="shrink-0">
                {copied ? <Check size={14} className="text-signal" /> : <Copy size={14} />}
                <span>{copied ? t("已复制") : t("复制")}</span>
              </Button>
            </div>
            {copyError ? <ErrorState message={copyError} className="mt-2" /> : null}
          </div>

          <div className="mt-6 flex items-center justify-end">
            <Button variant="primary" onClick={onClose}>
              {t("我已保存，关闭")}
            </Button>
          </div>
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}

// ---------------------------------------------------------------------------
// Dialog: 重置密码
// ---------------------------------------------------------------------------
function ResetPasswordDialog({
  open,
  onOpenChange,
  userId,
  onSuccess,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  userId: string;
  onSuccess: () => void;
}) {
  const { t } = useI18n();
  const pwdField = useId();
  const [newPassword, setNewPassword] = useState("");
  const [errorMsg, setErrorMsg] = useState("");

  const mutation = useMutation({
    mutationFn: () => updateUser(userId, { password: newPassword }),
    onSuccess: () => {
      setNewPassword("");
      setErrorMsg("");
      onSuccess();
    },
    onError: (err) => setErrorMsg(formatApiErrorMessage(err, t)),
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!newPassword.trim()) {
      setErrorMsg(t("新密码不能为空"));
      return;
    }
    setErrorMsg("");
    mutation.mutate();
  };

  return (
    <DialogRoot open={open} onOpenChange={onOpenChange}>
      <DialogPortal>
        <DialogOverlay />
        <DialogContent className="w-full max-w-md p-6">
          <DialogTitle className="text-base font-semibold text-ink">
            {t("重置用户密码")}
          </DialogTitle>

          <form onSubmit={handleSubmit} className="mt-4 space-y-4">
            {errorMsg && (
              <div className="rounded-control border border-alert/30 bg-alert-wash p-3 text-xs text-alert">
                {errorMsg}
              </div>
            )}

            <div className="space-y-1.5">
              <label htmlFor={pwdField} className="block text-xs font-medium text-ink-soft">
                {t("新密码")}
              </label>
              <Input
                id={pwdField}
                type="password"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                placeholder={t("输入新的登录密码")}
                required
              />
            </div>

            <div className="mt-6 flex items-center justify-end gap-3">
              <Button
                type="button"
                variant="secondary"
                onClick={() => onOpenChange(false)}
                disabled={mutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button type="submit" variant="primary" disabled={mutation.isPending}>
                {mutation.isPending ? t("正在重置...") : t("确认重置")}
              </Button>
            </div>
          </form>
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}
