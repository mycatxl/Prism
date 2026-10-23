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
import { Link, useParams } from "react-router-dom";
import { Badge } from "../../components/ui/Badge";
import { Button } from "../../components/ui/Button";
import {
  DialogContent,
  DialogOverlay,
  DialogPortal,
  DialogRoot,
  DialogTitle,
} from "../../components/ui/Dialog";
import { Fieldset, Input } from "../../components/ui/Input";
import { Page, PageHeader, PageMeta } from "../../components/ui/PageHeader";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
import { EmptyState, ErrorState, LoadingState } from "../../components/ui/QueryState";
import { Readout } from "../../components/ui/Readout";
import { Select } from "../../components/ui/Select";
import { Sheet } from "../../components/ui/Sheet";
import { Sparkline } from "../../components/ui/Sparkline";
import { Table, TableWrap, TBody, TD, TDClip, TDNum, TH, THead, TR } from "../../components/ui/Table";
import { ToastContainer } from "../../components/ui/Toast";
import { useToast } from "../../hooks/useToast";
import { useI18n } from "../../i18n";
import { getCurrentLocale, isEnglishLocale } from "../../i18n/locale";
import { copyToClipboard } from "../../lib/clipboard";
import { formatBytes } from "../../lib/bytes";
import { useNow } from "../../lib/clock";
import { formatApiErrorMessage } from "../../lib/error-message";
import { formatMicro } from "../../lib/money";
import { formatGoDuration } from "../../lib/time";
import { opsPaths } from "../../lib/paths";
import {
  AdjustBalanceDialog,
  UserBalanceLedgerPanel,
} from "./UserBalancePanel";
import { adminUserBalanceQueryKey, useUserBalance } from "./userBalance";
import {
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
import type { AdminCreateKeyResponse, AdminKey, AdminPlan, AdminUserDetail } from "./types";

/*
 * One account, on the Signal Console rhythm: the header carries the account's
 * name, its state and role, and the page-level actions. The main column states
 * how the account stands — the summary with its balance and key times, the
 * subscription as a lease with its remaining life, then keys, usage and the
 * ledger. The side column holds the record (attributes) and the operator's
 * writes over it (role, balance adjustment, password reset). Every mutation and
 * confirmation dialog the page had is kept; only the arrangement changed.
 */

const NANOS_PER_MILLI = 1_000_000;
/** "Soon" for a subscription is the console-wide 48h rule. */
const SUBSCRIPTION_WARN_MS = 48 * 3_600_000;

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

function statusDotClass(active: boolean): string {
  return active ? "state-dot state-dot--signal" : "state-dot";
}

/**
 * The share of a window that is still ahead, 0..100; a lapsed window reads as
 * an expired bar, never as a guessed fraction.
 */
function remainingPercent(remainingMs: number, windowMs: number): number {
  if (windowMs <= 0 || remainingMs <= 0) return 0;
  return Math.max(0, Math.min(100, (remainingMs / windowMs) * 100));
}

function remainingTone(ratio: number): "signal" | "warn" | "alert" {
  if (ratio <= 0) return "alert";
  if (ratio <= 0.05) return "alert";
  if (ratio <= 0.25) return "warn";
  return "signal";
}

/** A remaining span in the house duration voice, re-encoded through hours. */
function formatRemainingDuration(remainingMs: number): string {
  const hours = Math.max(1, Math.ceil(remainingMs / 3_600_000));
  return formatGoDuration(`${hours}h`);
}

/** One labelled fact in a summary grid: micro-caps label over its value. */
function Fact({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="min-w-0">
      <span className="micro block">{label}</span>
      <div className="mt-1 min-w-0">{children}</div>
    </div>
  );
}

/**
 * The remaining-life bar: the length is what is still usable, the tone is how
 * urgent that is, and the figure beside it says the same thing in words.
 */
function LeaseBar({
  label,
  detail,
  percent,
  tone,
  expired,
}: {
  label: string;
  detail: string;
  percent: number;
  tone: "signal" | "warn" | "alert";
  expired: boolean;
}) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between gap-3">
        <span className="micro">{label}</span>
        <span className={`readout whitespace-nowrap text-xs ${expired ? "text-alert" : "text-ink-soft"}`}>
          {detail}
        </span>
      </div>
      <span className="lease-track block" aria-hidden>
        <span
          className={`block ${tone === "signal" ? "lease-fill" : `lease-fill lease-fill--${tone}`}`}
          style={{ width: `${expired ? 100 : percent}%` }}
        />
      </span>
    </div>
  );
}

export function UserDetailPage() {
  const { t } = useI18n();
  const { id = "" } = useParams<{ id: string }>();
  const queryClient = useQueryClient();
  const { toasts, showToast, dismissToast } = useToast();

  // Modals / Sheets
  const [assignPlanOpen, setAssignPlanOpen] = useState(false);
  const [extendSubOpen, setExtendSubOpen] = useState(false);
  const [createKeyOpen, setCreateKeyOpen] = useState(false);
  const [resetPwdOpen, setResetPwdOpen] = useState(false);
  const [adjustBalanceOpen, setAdjustBalanceOpen] = useState(false);
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

  const { data: keysData } = useQuery({
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
    queryKey: adminPlansQueryKey,
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

  // One action, every query the page reads: the account, its keys, its usage
  // and its balance view.
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["admin-user", id] });
    void queryClient.invalidateQueries({ queryKey: ["admin-user-keys", id] });
    void queryClient.invalidateQueries({ queryKey: ["admin-user-usage", id] });
    void queryClient.invalidateQueries({ queryKey: adminUserBalanceQueryKey(id) });
  };

  const balanceQuery = useUserBalance(id);

  if (!id) {
    return (
      <Page bleed>
        <PageHeader title={t("用户")} />
        <ToastContainer toasts={toasts} onDismiss={dismissToast} />
        <div className="page-content page-content--split">
          <Panel>
            <PanelBody>
              <ErrorState message={t("用户 ID 缺失，无法加载详情。")} />
            </PanelBody>
          </Panel>
        </div>
      </Page>
    );
  }

  const userView = (
    <Page bleed>
      <PageHeader
        title={user ? user.username : id}
        description={<code className="mono text-xs text-ink-faint">{id}</code>}
        meta={
          user ? (
            <>
              <Badge tone={user.status === "active" ? "signal" : "warn"} dot>
                {user.status === "active" ? t("正常") : t("已停用")}
              </Badge>
              <Badge tone={user.role === "admin" ? "accent" : "neutral"}>
                {user.role === "admin" ? t("管理员") : t("普通用户")}
              </Badge>
              <PageMeta label={t("最近活动")} value={formatNsTime(user.last_login_ns)} />
            </>
          ) : undefined
        }
        actions={
          <>
            <Button variant="secondary" size="sm" asChild>
              <Link to={opsPaths.users}>
                <ArrowLeft size={15} aria-hidden />
                {t("返回用户列表")}
              </Link>
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={refresh}
              disabled={userLoading}
            >
              <RefreshCw size={15} aria-hidden />
              {t("刷新")}
            </Button>
          </>
        }
      />

      <ToastContainer toasts={toasts} onDismiss={dismissToast} />

      <div className="page-content page-content--split">
        {userError && !user ? (
          <Panel>
            <PanelBody>
              <ErrorState
                message={formatApiErrorMessage(userErr, t)}
                onRetry={() => void refetchUser()}
              />
            </PanelBody>
          </Panel>
        ) : userLoading && !user ? (
          <Panel>
            <PanelBody>
              <LoadingState label={t("正在加载用户详情...")} />
            </PanelBody>
          </Panel>
        ) : user ? (
          <>
            <div className="flex min-w-0 flex-col gap-4">
              <SummaryPanel user={user} balanceQuery={balanceQuery} />
              <SubscriptionPanel
                user={user}
                toggleSubStatusMutation={toggleSubStatusMutation}
                onAssignPlan={() => setAssignPlanOpen(true)}
                onExtend={() => setExtendSubOpen(true)}
              />
              <KeysPanel
                keys={keysData?.items ?? user.keys}
                onCreateKey={() => setCreateKeyOpen(true)}
                onDeleteKey={setDeleteKeyTarget}
              />
              <UsagePanel days={usageData?.items ?? user.usage_daily} />
              <UserBalanceLedgerPanel userId={id} />
            </div>

            <div className="flex min-w-0 flex-col gap-4">
              <AttributesPanel user={user} />
              <OperationsPanel
                user={user}
                updateRoleMutation={updateRoleMutation}
                onAdjustBalance={() => setAdjustBalanceOpen(true)}
                onResetPassword={() => setResetPwdOpen(true)}
              />
            </div>
          </>
        ) : null}
      </div>

      {/* Dialog: 分配套餐 */}
      <AssignPlanDialog
        open={assignPlanOpen}
        onOpenChange={setAssignPlanOpen}
        userId={id}
        currentSubId={user?.subscription?.id}
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
        subId={user?.subscription?.id ?? ""}
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

      {/* Dialog: 调整余额 */}
      <AdjustBalanceDialog
        open={adjustBalanceOpen}
        onOpenChange={setAdjustBalanceOpen}
        userId={id}
        onSuccess={() => {
          showToast("success", t("余额已调整"));
          setAdjustBalanceOpen(false);
          void queryClient.invalidateQueries({ queryKey: adminUserBalanceQueryKey(id) });
        }}
      />

      {/* Dialog: 删除密钥确认 */}
      <DialogRoot
        open={Boolean(deleteKeyTarget)}
        onOpenChange={(open) => !open && setDeleteKeyTarget(null)}
      >
        <DialogPortal>
          <DialogOverlay />
          <DialogContent className="w-full max-w-md">
            <div className="border-b border-rule px-5 py-4">
              <DialogTitle>{t("确认删除访问密钥")}</DialogTitle>
            </div>
            <div className="px-5 py-4 text-sm leading-relaxed text-ink-soft">
              {t("确定要删除密钥 {{name}} 吗？删除后使用该密钥的客户端将立即无法访问。", {
                name: deleteKeyTarget?.name ?? "",
              })}
            </div>
            <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
              <Button
                variant="secondary"
                onClick={() => setDeleteKeyTarget(null)}
                disabled={deleteKeyMutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button
                variant="danger"
                loading={deleteKeyMutation.isPending}
                onClick={() => {
                  if (deleteKeyTarget) deleteKeyMutation.mutate(deleteKeyTarget.id);
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

  return userView;
}

// ---------------------------------------------------------------------------
// Panel: 账户概要 — balance, state, the key times
// ---------------------------------------------------------------------------

function SummaryPanel({
  user,
  balanceQuery,
}: {
  user: AdminUserDetail;
  balanceQuery: ReturnType<typeof useUserBalance>;
}) {
  const { t } = useI18n();
  const active = user.status === "active";
  const balance = balanceQuery.data?.balance_micro ?? 0;

  return (
    <Panel>
      <PanelHeader title={t("账户概要")} />
      <PanelBody className="space-y-4">
        <Readout
          label={t("当前余额")}
          value={balanceQuery.isSuccess ? formatMicro(balance) : "--"}
          unit="USDT"
          tone={balance > 0 ? "signal" : "muted"}
          hint={t("金额以 USDT 计，保留六位小数。")}
        />
        <div className="grid grid-cols-2 gap-4 border-t border-rule-faint pt-4 sm:grid-cols-4">
          <Fact label={t("状态")}>
            <span className="inline-flex items-center gap-2">
              <span aria-hidden className={statusDotClass(active)} />
              <span className="state-text">{active ? t("正常") : t("已停用")}</span>
            </span>
          </Fact>
          <Fact label={t("注册时间")}>
            <span className="readout block text-xs text-ink-soft">
              {formatNsTime(user.created_at_ns)}
            </span>
          </Fact>
          <Fact label={t("最近登录")}>
            <span className="readout block text-xs text-ink-soft">
              {formatNsTime(user.last_login_ns)}
            </span>
          </Fact>
          <Fact label={t("更新时间")}>
            <span className="readout block text-xs text-ink-soft">
              {formatNsTime(user.updated_at_ns)}
            </span>
          </Fact>
        </div>
      </PanelBody>
    </Panel>
  );
}

// ---------------------------------------------------------------------------
// Panel: 套餐与订阅 — the subscription as a lease with its remaining life
// ---------------------------------------------------------------------------

function SubscriptionPanel({
  user,
  toggleSubStatusMutation,
  onAssignPlan,
  onExtend,
}: {
  user: AdminUserDetail;
  toggleSubStatusMutation: {
    isPending: boolean;
    mutate: (variables: { subId: string; nextStatus: string }) => void;
  };
  onAssignPlan: () => void;
  onExtend: () => void;
}) {
  const { t } = useI18n();
  const sub = user.subscription;

  return (
    <Panel>
      <PanelHeader
        title={t("套餐与订阅")}
        meta={sub ? sub.plan_name : undefined}
        actions={
          <div className="flex items-center gap-2">
            {sub && (
              <>
                <Button
                  variant="ghost"
                  size="sm"
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
                      <Pause size={14} aria-hidden />
                      <span>{t("暂停")}</span>
                    </>
                  ) : (
                    <>
                      <Play size={14} aria-hidden />
                      <span>{t("恢复")}</span>
                    </>
                  )}
                </Button>
                <Button variant="secondary" size="sm" onClick={onExtend}>
                  {t("续期")}
                </Button>
              </>
            )}
            <Button variant="primary" size="sm" onClick={onAssignPlan}>
              {sub ? t("更换套餐") : t("分配套餐")}
            </Button>
          </div>
        }
      />

      <PanelBody className="space-y-4">
        {sub ? (
          <>
            <div className="grid grid-cols-2 gap-4 sm:grid-cols-3">
              <Fact label={t("订阅状态")}>
                <span className="inline-flex items-center gap-2">
                  <span
                    aria-hidden
                    className={sub.status === "active" ? "state-dot state-dot--signal" : "state-dot"}
                  />
                  <span className="state-text">
                    {sub.status === "active" ? t("正常") : t("已暂停")}
                  </span>
                </span>
              </Fact>
              <Fact label={t("生效时间")}>
                <span className="readout block text-xs text-ink-soft">
                  {formatNsTime(sub.starts_at_ns)}
                </span>
              </Fact>
              <Fact label={t("到期时间")}>
                <span className="readout block text-xs text-ink-soft">
                  {formatNsTime(sub.expires_at_ns)}
                </span>
              </Fact>
              <Fact label={t("周期已用流量")}>
                <span className="readout block text-xs text-ink">
                  {formatBytes(sub.period_used_bytes)}
                </span>
              </Fact>
              <Fact label={t("配额上限 / 剩余")}>
                <span className="readout block text-xs text-ink-soft">
                  {sub.traffic_bytes > 0 ? formatBytes(sub.traffic_bytes) : t("不限")}{" "}
                  /{" "}
                  {sub.remaining_bytes != null
                    ? formatBytes(sub.remaining_bytes)
                    : t("不限")}
                </span>
              </Fact>
            </div>

            <div className="space-y-4 border-t border-rule-faint pt-4">
              <TimeLeaseRow sub={sub} />
              {sub.traffic_bytes > 0 && <QuotaLeaseRow sub={sub} />}
            </div>
          </>
        ) : (
          <EmptyState
            title={t("暂无活跃订阅")}
            hint={t("该用户当前未绑定任何套餐，点击上方按钮进行分配。")}
          />
        )}
      </PanelBody>
    </Panel>
  );
}

/** The subscription's remaining life against its own window. */
function TimeLeaseRow({ sub }: { sub: NonNullable<AdminUserDetail["subscription"]> }) {
  const { t } = useI18n();
  const now = useNow();
  const startsMs = Math.round(sub.starts_at_ns / NANOS_PER_MILLI);
  const expiresMs = Math.round(sub.expires_at_ns / NANOS_PER_MILLI);
  const windowMs = expiresMs - startsMs;
  const remainingMs = expiresMs - now;
  const expired = remainingMs <= 0;

  // A window the record cannot produce is a bar that would guess at its
  // denominator, so it is left out rather than drawn.
  if (windowMs <= 0) {
    return (
      <div className="flex items-baseline justify-between gap-3">
        <span className="micro">{t("剩余时间")}</span>
        <span className="readout text-xs text-ink-soft">
          {expired ? t("已过期") : "-"}
        </span>
      </div>
    );
  }

  return (
    <LeaseBar
      label={t("剩余时间")}
      detail={expired ? t("已过期") : formatRemainingDuration(remainingMs)}
      percent={remainingPercent(remainingMs, windowMs)}
      tone={remainingMs <= 0 ? "alert" : remainingMs <= SUBSCRIPTION_WARN_MS ? "warn" : "signal"}
      expired={expired}
    />
  );
}

/** The period quota's remaining share; unlimited quotas draw no bar. */
function QuotaLeaseRow({ sub }: { sub: NonNullable<AdminUserDetail["subscription"]> }) {
  const { t } = useI18n();
  const remaining = sub.remaining_bytes ?? sub.traffic_bytes - sub.period_used_bytes;
  const ratio = remaining / sub.traffic_bytes;
  const expired = remaining <= 0;

  return (
    <LeaseBar
      label={t("周期剩余流量")}
      detail={`${formatBytes(Math.max(0, remaining))} / ${formatBytes(sub.traffic_bytes)}`}
      percent={Math.max(0, Math.min(100, ratio * 100))}
      tone={remainingTone(ratio)}
      expired={expired}
    />
  );
}

// ---------------------------------------------------------------------------
// Panel: 访问密钥
// ---------------------------------------------------------------------------

function KeysPanel({
  keys,
  onCreateKey,
  onDeleteKey,
}: {
  keys: AdminKey[];
  onCreateKey: () => void;
  onDeleteKey: (key: AdminKey) => void;
}) {
  const { t } = useI18n();

  return (
    <Panel>
      <PanelHeader
        title={t("访问密钥")}
        meta={<Badge tone="neutral">{keys.length}</Badge>}
        actions={
          <Button variant="primary" size="sm" onClick={onCreateKey}>
            <Plus size={14} aria-hidden />
            <span>{t("新建密钥")}</span>
          </Button>
        }
      />

      <PanelBody className="p-0">
        {keys.length === 0 ? (
          <EmptyState
            title={t("暂无访问密钥")}
            hint={t("点击右上角为该用户生成新的访问密钥。")}
            className="py-10"
          />
        ) : (
          <TableWrap>
            <Table className="min-w-[720px]">
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
                {keys.map((key) => (
                  <TR key={key.id}>
                    <TDClip className="font-medium text-ink">{key.name}</TDClip>
                    <TD>
                      <code className="mono rounded-control bg-paper-inset px-1.5 py-0.5 text-xs text-ink">
                        {key.credential_hint || "****"}
                      </code>
                    </TD>
                    <TD className="whitespace-nowrap">
                      <span className="inline-flex items-center gap-2">
                        <span
                          aria-hidden
                          className={statusDotClass(key.status === "active")}
                        />
                        <span className="state-text">
                          {key.status === "active" ? t("正常") : t("已停用")}
                        </span>
                      </span>
                    </TD>
                    <TDClip className="text-xs text-ink-soft">
                      {key.platforms && key.platforms.length > 0
                        ? key.platforms.join(", ")
                        : t("全部平台")}
                    </TDClip>
                    <TD className="readout whitespace-nowrap text-xs text-ink-soft">
                      {formatNsTime(key.created_at_ns)}
                    </TD>
                    <TD className="readout whitespace-nowrap text-xs text-ink-soft">
                      {formatNsTime(key.last_used_ns)}
                    </TD>
                    <TD className="text-right">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="text-ink-faint hover:bg-alert-wash hover:text-alert"
                        onClick={() => onDeleteKey(key)}
                        title={t("删除密钥")}
                        aria-label={t("删除密钥")}
                      >
                        <Trash2 size={14} />
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
  );
}

// ---------------------------------------------------------------------------
// Panel: 最近 30 天用量明细
// ---------------------------------------------------------------------------

function UsagePanel({
  days,
}: {
  days: { day_ns: number; ingress_bytes: number; egress_bytes: number; total_bytes: number }[];
}) {
  const { t } = useI18n();
  const sparklineValues = days.map((d) => d.total_bytes);

  return (
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
        {days.length === 0 ? (
          <EmptyState
            title={t("暂无历史用量数据")}
            hint={t("当前周期内未产生入站或出站流量。")}
            className="py-10"
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
                {days.map((day) => (
                  <TR key={day.day_ns}>
                    <TD className="readout text-xs text-ink-soft">
                      {formatNsDate(day.day_ns)}
                    </TD>
                    <TDNum className="whitespace-nowrap text-xs">
                      <span className="readout text-ink-soft">{formatBytes(day.ingress_bytes)}</span>
                    </TDNum>
                    <TDNum className="whitespace-nowrap text-xs">
                      <span className="readout text-ink-soft">{formatBytes(day.egress_bytes)}</span>
                    </TDNum>
                    <TDNum className="whitespace-nowrap text-xs">
                      <span className="readout">{formatBytes(day.total_bytes)}</span>
                    </TDNum>
                  </TR>
                ))}
              </TBody>
            </Table>
          </TableWrap>
        )}
      </PanelBody>
    </Panel>
  );
}

// ---------------------------------------------------------------------------
// Panel: 账户属性 — the record, read-only
// ---------------------------------------------------------------------------

function AttributesPanel({ user }: { user: AdminUserDetail }) {
  const { t } = useI18n();

  return (
    <Panel>
      <PanelHeader title={t("账户属性")} />
      <PanelBody>
        <dl className="space-y-3">
          <div className="flex items-center justify-between gap-3">
            <dt className="micro shrink-0">{t("用户 ID")}</dt>
            <dd className="mono min-w-0 truncate text-xs text-ink" title={user.id}>
              {user.id}
            </dd>
          </div>
          <div className="flex items-center justify-between gap-3">
            <dt className="micro shrink-0">{t("双因素认证 (TOTP)")}</dt>
            <dd>
              <Badge tone={user.has_totp ? "signal" : "neutral"} dot>
                {user.has_totp ? t("已启用") : t("未启用")}
              </Badge>
            </dd>
          </div>
          <div className="flex items-center justify-between gap-3">
            <dt className="micro shrink-0">{t("密钥数")}</dt>
            <dd className="readout text-xs text-ink">{user.key_count}</dd>
          </div>
        </dl>
      </PanelBody>
    </Panel>
  );
}

// ---------------------------------------------------------------------------
// Panel: 运营操作 — the writes the operator drives from here
// ---------------------------------------------------------------------------

function OperationsPanel({
  user,
  updateRoleMutation,
  onAdjustBalance,
  onResetPassword,
}: {
  user: AdminUserDetail;
  updateRoleMutation: {
    isPending: boolean;
    mutate: (role: string) => void;
  };
  onAdjustBalance: () => void;
  onResetPassword: () => void;
}) {
  const { t } = useI18n();
  const roleId = useId();

  return (
    <Panel>
      <PanelHeader title={t("运营操作")} />
      <PanelBody className="space-y-4">
        <Fieldset label={t("用户角色")} htmlFor={roleId}>
          <Select
            id={roleId}
            value={user.role}
            onChange={(e) => updateRoleMutation.mutate(e.target.value)}
            disabled={updateRoleMutation.isPending}
          >
            <option value="user">{t("普通用户")}</option>
            <option value="admin">{t("管理员")}</option>
          </Select>
        </Fieldset>

        <div className="grid gap-2 border-t border-rule-faint pt-4">
          <Button variant="secondary" onClick={onAdjustBalance}>
            <Plus size={14} aria-hidden />
            <span>{t("调整余额")}</span>
          </Button>
          <Button variant="secondary" onClick={onResetPassword}>
            <span>{t("重置密码")}</span>
          </Button>
        </div>
      </PanelBody>
    </Panel>
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
        <DialogContent className="w-full max-w-md">
          <div className="border-b border-rule px-5 py-4">
            <DialogTitle>{currentSubId ? t("更换套餐") : t("分配套餐")}</DialogTitle>
          </div>

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
          >
            <div className="space-y-4 px-5 py-4">
              {errorMsg && (
                <div className="rounded-control border border-alert/30 bg-alert-wash p-3 text-xs text-alert">
                  {errorMsg}
                </div>
              )}

              <Fieldset label={t("选择套餐")} htmlFor={planIdField}>
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
              </Fieldset>

              <Fieldset label={t("有效天数")} htmlFor={durationField}>
                <Input
                  id={durationField}
                  type="number"
                  min="1"
                  value={durationDays}
                  onChange={(e) => setDurationDays(e.target.value)}
                />
              </Fieldset>
            </div>

            <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
              <Button
                type="button"
                variant="secondary"
                onClick={() => onOpenChange(false)}
                disabled={mutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button type="submit" variant="primary" loading={mutation.isPending}>
                {t("确定")}
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
        <DialogContent className="w-full max-w-md">
          <div className="border-b border-rule px-5 py-4">
            <DialogTitle>{t("续期套餐")}</DialogTitle>
          </div>

          <form
            onSubmit={(e) => {
              e.preventDefault();
              setErrorMsg("");
              mutation.mutate();
            }}
          >
            <div className="space-y-4 px-5 py-4">
              {errorMsg && (
                <div className="rounded-control border border-alert/30 bg-alert-wash p-3 text-xs text-alert">
                  {errorMsg}
                </div>
              )}

              <Fieldset label={t("延长天数")} htmlFor={daysField}>
                <Input
                  id={daysField}
                  type="number"
                  min="1"
                  value={days}
                  onChange={(e) => setDays(e.target.value)}
                />
              </Fieldset>
            </div>

            <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
              <Button
                type="button"
                variant="secondary"
                onClick={() => onOpenChange(false)}
                disabled={mutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button type="submit" variant="primary" loading={mutation.isPending}>
                {t("确定续期")}
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
            loading={mutation.isPending}
          >
            {t("生成密钥")}
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

        <Fieldset label={t("密钥名称")} htmlFor={nameField}>
          <Input
            id={nameField}
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("例如: 生产环境客户端")}
            required
          />
        </Fieldset>

        <Fieldset
          label={t("平台限制 (可选，多个逗号分隔)")}
          hint={t("留空不限制，或填: openai, anthropic")}
          htmlFor={platformsField}
        >
          <Input
            id={platformsField}
            value={platformsRaw}
            onChange={(e) => setPlatformsRaw(e.target.value)}
          />
        </Fieldset>

        <Fieldset
          label={t("IP 白名单 (可选，多个逗号分隔)")}
          hint={t("留空不限制，或填: 1.2.3.4, 5.6.7.8/24")}
          htmlFor={ipField}
        >
          <Input
            id={ipField}
            value={ipRaw}
            onChange={(e) => setIpRaw(e.target.value)}
          />
        </Fieldset>

        <Fieldset
          label={t("有效天数 (可选)")}
          hint={t("留空则永不过期")}
          htmlFor={expiresField}
        >
          <Input
            id={expiresField}
            type="number"
            min="1"
            value={expiresInDays}
            onChange={(e) => setExpiresInDays(e.target.value)}
          />
        </Fieldset>
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
        <DialogContent className="w-full max-w-lg">
          <div className="border-b border-rule px-5 py-4">
            <DialogTitle>{t("密钥凭据已生成")}</DialogTitle>
          </div>

          <div className="space-y-4 px-5 py-4">
            <div className="rounded-control border border-warn/30 bg-warn-wash p-3 text-xs text-warn">
              {t("请立即复制并妥善保管此凭据！出于安全考虑，该密钥将不再向任何人展示。")}
            </div>

            <Fieldset label={t("明文访问凭据")}>
              <div className="flex items-center gap-2">
                <Input
                  readOnly
                  value={data?.credential || ""}
                  className="mono text-xs"
                />
                <Button variant="secondary" onClick={handleCopy} className="shrink-0">
                  {copied ? <Check size={14} className="text-signal" /> : <Copy size={14} />}
                  <span>{copied ? t("已复制") : t("复制")}</span>
                </Button>
              </div>
              {copyError ? <ErrorState message={copyError} className="mt-2" /> : null}
            </Fieldset>
          </div>

          <div className="flex items-center justify-end border-t border-rule bg-rail px-5 py-3">
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
        <DialogContent className="w-full max-w-md">
          <div className="border-b border-rule px-5 py-4">
            <DialogTitle>{t("重置用户密码")}</DialogTitle>
          </div>

          <form onSubmit={handleSubmit}>
            <div className="space-y-4 px-5 py-4">
              {errorMsg && (
                <div className="rounded-control border border-alert/30 bg-alert-wash p-3 text-xs text-alert">
                  {errorMsg}
                </div>
              )}

              <Fieldset label={t("新密码")} htmlFor={pwdField}>
                <Input
                  id={pwdField}
                  type="password"
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  placeholder={t("输入新的登录密码")}
                  required
                />
              </Fieldset>
            </div>

            <div className="flex items-center justify-end gap-2 border-t border-rule bg-rail px-5 py-3">
              <Button
                type="button"
                variant="secondary"
                onClick={() => onOpenChange(false)}
                disabled={mutation.isPending}
              >
                {t("取消")}
              </Button>
              <Button type="submit" variant="primary" loading={mutation.isPending}>
                {t("确认重置")}
              </Button>
            </div>
          </form>
        </DialogContent>
      </DialogPortal>
    </DialogRoot>
  );
}
