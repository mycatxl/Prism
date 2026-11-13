import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { KeyRound, Laptop, LogOut, RefreshCw, ShieldCheck, ShieldOff } from "lucide-react";
import { useNavigate, useSearchParams } from "react-router-dom";
import {
  ControlButton,
  ControlInput,
  ControlTextarea,
  EmptyWorkState,
  ObjectList,
  ObjectRow,
  WorkspacePage,
  QueryBadge,
  StatusMark,
  WorkspaceCard,
} from "../../components/control-plane";
import { useI18n } from "../../i18n";
import { useToast } from "../../hooks/useToast";
import { apiRequest, ApiError, logout } from "../../lib/api-client";
import { formatApiErrorMessage } from "../../lib/error-message";
import { getSessions, revokeSession } from "../me/api";
import { useAuthStore, clearSession } from "./auth-store";

type Enrolment = {
  secret: string;
  uri: string;
};

const NANOS_PER_MILLI = 1_000_000;

function formatSessionTime(ns: number) {
  if (!ns) return "-";
  return new Intl.DateTimeFormat(undefined, {
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(Math.round(ns / NANOS_PER_MILLI)));
}

function errorMessage(error: unknown, t: (text: string, options?: Record<string, unknown>) => string): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case "PASSWORD_MISMATCH": return t("当前密码不正确。");
      case "PASSWORD_TOO_SHORT": return t("新密码长度不足。");
      case "PASSWORD_UNCHANGED": return t("新密码不能与当前密码相同。");
      case "TOTP_INVALID": return t("验证码不正确，请检查身份验证器。");
      case "TOTP_ALREADY_ENROLLED": return t("账户已经启用双因素认证。");
      case "TOTP_NOT_ENROLLED": return t("账户尚未启用双因素认证。");
      case "NO_PEPPER": return t("服务未配置安全密钥，暂时无法保存双因素认证。");
      default: return `${error.code}: ${error.message}`;
    }
  }
  return error instanceof Error ? error.message : t("未知错误");
}

export function AccountSecurityPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showToast } = useToast();
  const [searchParams, setSearchParams] = useSearchParams();
  const session = useAuthStore((state) => state.session);
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [passwordMessage, setPasswordMessage] = useState("");
  const [passwordError, setPasswordError] = useState("");
  const [enrolment, setEnrolment] = useState<Enrolment | null>(null);
  const [totpCode, setTotpCode] = useState("");
  const [totpError, setTotpError] = useState("");
  const [totpMessage, setTotpMessage] = useState("");
  const [disablePassword, setDisablePassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [enrolPromptSpent, setEnrolPromptSpent] = useState(false);

  const user = session.user;
  const hasTotp = user?.has_totp === true;
  const sessionsQuery = useQuery({ queryKey: ["me-sessions"], queryFn: getSessions, enabled: Boolean(user) });
  const sessions = sessionsQuery.data?.items ?? [];
  const revokeMutation = useMutation({
    mutationFn: revokeSession,
    onSuccess: () => {
      showToast("success", t("会话已撤销"));
      void queryClient.invalidateQueries({ queryKey: ["me-sessions"] });
    },
    onError: (error) => showToast("error", formatApiErrorMessage(error, t)),
  });

  useEffect(() => {
    if (searchParams.get("enrol") !== "1" || hasTotp || enrolPromptSpent) return;
    setEnrolPromptSpent(true);
    void startEnrolment();
    // `startEnrolment` is intentionally guarded by the one-shot URL state.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchParams, hasTotp, enrolPromptSpent]);

  async function startEnrolment() {
    setTotpError("");
    setTotpMessage("");
    setBusy(true);
    try {
      const next = await apiRequest<Enrolment>("/api/v1/auth/totp/enrol", { method: "POST", body: {} });
      setEnrolment(next);
    } catch (error) {
      setTotpError(errorMessage(error, t));
    } finally {
      setBusy(false);
    }
  }

  function clearEnrolFlag() {
    if (searchParams.get("enrol") === null) return;
    const next = new URLSearchParams(searchParams);
    next.delete("enrol");
    setSearchParams(next, { replace: true });
  }

  function cancelEnrolment() {
    setEnrolment(null);
    setTotpCode("");
    setTotpError("");
    clearEnrolFlag();
  }

  async function confirmEnrolment() {
    if (!enrolment || !/^\d{6}$/.test(totpCode.trim())) {
      setTotpError(t("请输入身份验证器中的 6 位验证码。"));
      return;
    }
    setTotpError("");
    setTotpMessage("");
    setBusy(true);
    try {
      await apiRequest<{ has_totp: boolean }>("/api/v1/auth/totp/confirm", {
        method: "POST",
        body: { secret: enrolment.secret, code: totpCode.trim() },
      });
      clearSession();
      navigate("/login?reauth=1", { replace: true });
    } catch (error) {
      setTotpError(errorMessage(error, t));
    } finally {
      setBusy(false);
    }
  }

  async function disableTotp() {
    setTotpError("");
    setTotpMessage("");
    setBusy(true);
    try {
      await apiRequest<{ has_totp: boolean }>("/api/v1/auth/totp/disable", {
        method: "POST",
        body: { current_password: disablePassword },
      });
      clearSession();
      navigate("/login?reauth=1", { replace: true });
    } catch (error) {
      setTotpError(errorMessage(error, t));
    } finally {
      setBusy(false);
    }
  }

  async function changePassword() {
    setPasswordError("");
    setPasswordMessage("");
    if (newPassword !== confirmPassword) {
      setPasswordError(t("两次输入的新密码不一致。"));
      return;
    }
    if (newPassword.length < 12) {
      setPasswordError(t("新密码至少需要 12 个字符。"));
      return;
    }
    setBusy(true);
    try {
      await apiRequest<void>("/api/v1/auth/password", {
        method: "POST",
        body: { current_password: currentPassword, new_password: newPassword },
      });
      clearSession();
      navigate("/login?reauth=1", { replace: true });
    } catch (error) {
      setPasswordError(errorMessage(error, t));
    } finally {
      setBusy(false);
    }
  }

  async function signOut() {
    try {
      await logout();
    } finally {
      clearSession();
      queryClient.clear();
      navigate("/login", { replace: true });
    }
  }

  if (!user) return null;

  return (
    <WorkspacePage
      eyebrow="Account / security"
      title={t("账户与安全")}
      description={t("管理密码与双因素认证，保护控制台访问。")}
      meta={<QueryBadge label={user.username} tone="neutral" />}
    >
      <div className="workbench-layout">
        <WorkspaceCard
          eyebrow="Credential policy"
          title={t("修改密码")}
          description={t("修改后，账户的所有现有会话都会立即失效。")}
          actions={<StatusMark tone="warn" label={t("修改后需要重新登录")} />}
        >
          <form className="workspace-form-stack" onSubmit={(event) => { event.preventDefault(); void changePassword(); }}>
            <ControlInput label={t("当前密码")} type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} />
            <ControlInput label={t("新密码")} hint={t("至少 12 个字符。")} type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} />
            <ControlInput label={t("确认新密码")} type="password" autoComplete="new-password" value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} />
            {passwordError ? <p role="alert" className="workspace-form-error">{passwordError}</p> : null}
            {passwordMessage ? <p role="status" className="workspace-form-success">{passwordMessage}</p> : null}
            <div className="workspace-drawer-actions">
              <ControlButton type="submit" variant="primary" size="sm" isDisabled={busy || !currentPassword || !newPassword || !confirmPassword} loading={busy}>
                <KeyRound size={14} aria-hidden />
                {t("更新密码")}
              </ControlButton>
            </div>
          </form>
        </WorkspaceCard>

        <WorkspaceCard
          eyebrow="Authenticator"
          title={t("双因素认证")}
          description={t("使用身份验证器为登录增加一次性验证码。")}
          actions={<StatusMark tone={hasTotp ? "signal" : "warn"} label={hasTotp ? t("已启用") : t("未启用")} />}
        >
          {hasTotp ? (
            <div className="workspace-stack">
              <div className="workspace-notice workspace-notice--signal">
                <ShieldCheck size={18} aria-hidden />
                <div><strong>{t("双因素认证已启用")}</strong><p>{t("每次登录都需要身份验证器生成的 6 位验证码。")}</p></div>
              </div>
              {totpError ? <p role="alert" className="workspace-form-error">{totpError}</p> : null}
              <ControlInput label={t("当前密码")} hint={t("关闭双因素认证需要确认当前密码。") } type="password" autoComplete="current-password" value={disablePassword} onChange={(event) => setDisablePassword(event.target.value)} />
              <ControlButton variant="secondary" size="sm" onPress={() => void disableTotp()} isDisabled={busy || !disablePassword} loading={busy}>
                <ShieldOff size={14} aria-hidden />
                {t("停用双因素认证")}
              </ControlButton>
            </div>
          ) : (
            <div className="workspace-stack">
              <p className="workspace-muted">{t("尚未启用双因素认证。管理员可以在这里生成身份验证器配置并确认。")}</p>
              {enrolment ? (
                <div className="workspace-inset-stack">
                  <ControlTextarea label={t("身份验证器配置 URI")} readOnly value={enrolment.uri} rows={3} className="font-mono" />
                  <ControlInput label={t("手动设置密钥")} readOnly value={enrolment.secret} className="font-mono" />
                  <ControlInput label={t("确认验证码")} hint={t("在身份验证器中添加账户后，输入当前显示的 6 位验证码。") } inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={totpCode} onChange={(event) => setTotpCode(event.target.value)} className="font-mono" />
                  {totpError ? <p role="alert" className="workspace-form-error">{totpError}</p> : null}
                  <div className="workspace-drawer-actions">
                    <ControlButton variant="primary" size="sm" onPress={() => void confirmEnrolment()} isDisabled={busy || !totpCode} loading={busy}>{t("确认并启用")}</ControlButton>
                    <ControlButton variant="ghost" size="sm" onPress={cancelEnrolment} isDisabled={busy}>{t("取消")}</ControlButton>
                  </div>
                </div>
              ) : (
                <>
                  {totpError ? <p role="alert" className="workspace-form-error">{totpError}</p> : null}
                  {totpMessage ? <p role="status" className="workspace-form-success">{totpMessage}</p> : null}
                  <ControlButton variant="primary" size="sm" onPress={() => void startEnrolment()} isDisabled={busy} loading={busy}>
                    <ShieldCheck size={14} aria-hidden />
                    {t("开始设置双因素认证")}
                  </ControlButton>
                </>
              )}
            </div>
          )}
        </WorkspaceCard>

        <WorkspaceCard
          className="account-sessions-card"
          eyebrow="Account sessions"
          title={t("登录会话")}
          description={t("查看已登录设备并撤销不再使用的会话。")}
          actions={<ControlButton variant="ghost" size="sm" onPress={() => void sessionsQuery.refetch()} loading={sessionsQuery.isFetching}><RefreshCw size={14} aria-hidden />{t("刷新")}</ControlButton>}
        >
          <div className="workspace-stack">
            {sessionsQuery.isError ? <EmptyWorkState embedded title={t("读取失败")} detail={formatApiErrorMessage(sessionsQuery.error, t)} action={<ControlButton variant="secondary" size="sm" onPress={() => void sessionsQuery.refetch()}>{t("重试")}</ControlButton>} /> : null}
            {!sessionsQuery.isLoading && !sessionsQuery.isError && sessions.length === 0 ? <EmptyWorkState embedded title={t("暂无活跃会话")} /> : null}
            {sessions.length > 0 ? (
              <ObjectList>
                {sessions.map((item) => (
                  <ObjectRow
                    key={item.id}
                    leading={<Laptop size={15} aria-hidden />}
                    title={item.ip || "-"}
                    subtitle={item.user_agent || "-"}
                    facts={<><span>{t("登录时间")} {formatSessionTime(item.created_at_ns)}</span><span>{t("会话到期")} {formatSessionTime(item.expires_at_ns)}</span></>}
                    status={<StatusMark tone={item.current ? "signal" : "neutral"} label={item.current ? t("当前设备") : t("已登录")} />}
                    action={item.current ? <span className="workspace-muted">{t("当前会话")}</span> : <ControlButton variant="ghost" size="sm" isDisabled={revokeMutation.isPending} onPress={() => revokeMutation.mutate(item.id)}>{t("撤销")}</ControlButton>}
                  />
                ))}
              </ObjectList>
            ) : null}
            <div className="workspace-danger-row workspace-danger-row--warn">
              <div><strong>{t("退出当前账户")}</strong><p>{t("结束当前浏览器中的登录会话。")}</p></div>
              <ControlButton variant="danger" size="sm" onPress={() => void signOut()}><LogOut size={14} aria-hidden />{t("退出登录")}</ControlButton>
            </div>
          </div>
        </WorkspaceCard>
      </div>
    </WorkspacePage>
  );
}
