import { useEffect, useState } from "react";
import { KeyRound, ShieldCheck, ShieldOff } from "lucide-react";
import { useNavigate, useSearchParams } from "react-router-dom";
import {
  ControlButton,
  ControlInput,
  ControlTextarea,
  PageFrame,
  QueryBadge,
  StatusMark,
  WorkSurface,
} from "../../components/control-plane";
import { useI18n } from "../../i18n";
import { apiRequest, ApiError, fetchSession } from "../../lib/api-client";
import { useAuthStore, clearSession, setSession, type SessionInfo } from "./auth-store";

type Enrolment = {
  secret: string;
  uri: string;
};

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

function updateUserSession(session: SessionInfo, hasTotp: boolean): void {
  if (!session.user) return;
  setSession({
    ...session,
    user: { ...session.user, has_totp: hasTotp },
  });
}

export function AccountSecurityPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
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
      const next = await fetchSession();
      updateUserSession(next, true);
      setEnrolment(null);
      clearEnrolFlag();
      setTotpCode("");
      setTotpMessage(t("双因素认证已启用。"));
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

  if (!user) return null;

  return (
    <PageFrame
      eyebrow="Account / security"
      title={t("账户安全")}
      description={t("管理密码与双因素认证，保护控制台访问。")}
      meta={<QueryBadge label={user.username} tone="neutral" />}
    >
      <div className="workbench-layout">
        <WorkSurface
          eyebrow="Credential policy"
          title={t("修改密码")}
          description={t("修改后，账户的所有现有会话都会立即失效。")}
          actions={<StatusMark tone="warn" label={t("修改后需要重新登录")} />}
        >
          <form className="cp-form-stack" onSubmit={(event) => { event.preventDefault(); void changePassword(); }}>
            <ControlInput label={t("当前密码")} type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} />
            <ControlInput label={t("新密码")} hint={t("至少 12 个字符。")} type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} />
            <ControlInput label={t("确认新密码")} type="password" autoComplete="new-password" value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} />
            {passwordError ? <p role="alert" className="cp-form-error">{passwordError}</p> : null}
            {passwordMessage ? <p role="status" className="cp-form-success">{passwordMessage}</p> : null}
            <div className="cp-drawer-actions">
              <ControlButton type="submit" variant="primary" size="sm" isDisabled={busy || !currentPassword || !newPassword || !confirmPassword} loading={busy}>
                <KeyRound size={14} aria-hidden />
                {t("更新密码")}
              </ControlButton>
            </div>
          </form>
        </WorkSurface>

        <WorkSurface
          eyebrow="Authenticator"
          title={t("双因素认证")}
          description={t("使用身份验证器为登录增加一次性验证码。")}
          actions={<StatusMark tone={hasTotp ? "signal" : "warn"} label={hasTotp ? t("已启用") : t("未启用")} />}
        >
          {hasTotp ? (
            <div className="cp-stack">
              <div className="cp-notice cp-notice--signal">
                <ShieldCheck size={18} aria-hidden />
                <div><strong>{t("双因素认证已启用")}</strong><p>{t("每次登录都需要身份验证器生成的 6 位验证码。")}</p></div>
              </div>
              {totpError ? <p role="alert" className="cp-form-error">{totpError}</p> : null}
              <ControlInput label={t("当前密码")} hint={t("关闭双因素认证需要确认当前密码。") } type="password" autoComplete="current-password" value={disablePassword} onChange={(event) => setDisablePassword(event.target.value)} />
              <ControlButton variant="secondary" size="sm" onPress={() => void disableTotp()} isDisabled={busy || !disablePassword} loading={busy}>
                <ShieldOff size={14} aria-hidden />
                {t("停用双因素认证")}
              </ControlButton>
            </div>
          ) : (
            <div className="cp-stack">
              <p className="cp-muted">{t("尚未启用双因素认证。管理员可以在这里生成身份验证器配置并确认。")}</p>
              {enrolment ? (
                <div className="cp-inset-stack">
                  <ControlTextarea label={t("身份验证器配置 URI")} readOnly value={enrolment.uri} rows={3} className="font-mono" />
                  <ControlInput label={t("手动设置密钥")} readOnly value={enrolment.secret} className="font-mono" />
                  <ControlInput label={t("确认验证码")} hint={t("在身份验证器中添加账户后，输入当前显示的 6 位验证码。") } inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={totpCode} onChange={(event) => setTotpCode(event.target.value)} className="font-mono" />
                  {totpError ? <p role="alert" className="cp-form-error">{totpError}</p> : null}
                  <div className="cp-drawer-actions">
                    <ControlButton variant="primary" size="sm" onPress={() => void confirmEnrolment()} isDisabled={busy || !totpCode} loading={busy}>{t("确认并启用")}</ControlButton>
                    <ControlButton variant="ghost" size="sm" onPress={cancelEnrolment} isDisabled={busy}>{t("取消")}</ControlButton>
                  </div>
                </div>
              ) : (
                <>
                  {totpError ? <p role="alert" className="cp-form-error">{totpError}</p> : null}
                  {totpMessage ? <p role="status" className="cp-form-success">{totpMessage}</p> : null}
                  <ControlButton variant="primary" size="sm" onPress={() => void startEnrolment()} isDisabled={busy} loading={busy}>
                    <ShieldCheck size={14} aria-hidden />
                    {t("开始设置双因素认证")}
                  </ControlButton>
                </>
              )}
            </div>
          )}
        </WorkSurface>
      </div>
    </PageFrame>
  );
}
