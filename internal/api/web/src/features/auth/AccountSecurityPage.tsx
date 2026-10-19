import { useEffect, useState } from "react";
import { KeyRound, ShieldCheck, ShieldOff } from "lucide-react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input, Textarea } from "../../components/ui/Input";
import { Page, PageHeader } from "../../components/ui/PageHeader";
import { ErrorState } from "../../components/ui/QueryState";
import { Panel, PanelBody, PanelHeader } from "../../components/ui/Panel";
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
  // Removing the factor turns two credentials back into one, so the endpoint
  // behind the button requires the current password. Collecting it here is the
  // whole of the console's half: the server does the comparison, and a session
  // cookie on its own cannot perform the downgrade.
  const [disablePassword, setDisablePassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [enrolPromptSpent, setEnrolPromptSpent] = useState(false);

  const user = session.user;
  const hasTotp = user?.has_totp === true;

  /*
   * The `?enrol=1` flag is a one-shot prompt (RequireAuth sends a user whose
   * account still owes a TOTP enrolment here). It must be spent exactly once:
   * when this effect keyed off `enrolment` and the cancel button only cleared
   * that state, the URL still said `enrol=1`, the condition came straight back
   * and every cancel issued another POST /auth/totp/enrol — leaving a trail of
   * pending secrets on the server. `enrolPromptSpent` consumes the flag, and
   * cancelling also drops the flag from the URL so the panel stays closed.
   */
  useEffect(() => {
    if (searchParams.get("enrol") !== "1" || hasTotp || enrolPromptSpent) return;
    setEnrolPromptSpent(true);
    void startEnrolment();
  // `startEnrolment` is redefined every render; the guard above is what makes the
  // call once-only, not the dependency list.
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

  /** Drops the one-shot `?enrol=1` prompt so the effect above cannot fire again. */
  function clearEnrolFlag() {
    if (searchParams.get("enrol") === null) return;
    const next = new URLSearchParams(searchParams);
    next.delete("enrol");
    setSearchParams(next, { replace: true });
  }

  /** Closes the panel for good: no new pending secret, no surviving URL flag. */
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

  /*
   * The current password goes with the request every time: the endpoint refuses
   * without it, and a refusal here is shown in the panel rather than treated as
   * a broken session.
   */
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

  if (!user) {
    return null;
  }

  return (
    <Page>
      <PageHeader
        title={t("账户安全")}
        description={t("管理密码与双因素认证，保护控制台访问。")}
        meta={<span className="readout text-xs text-ink-soft">{user.username}</span>}
      />

      <div className="mx-auto grid w-full max-w-5xl gap-4 py-4 lg:grid-cols-2">
        <Panel>
          <PanelHeader title={t("修改密码")} description={t("修改后，账户的所有现有会话都会立即失效。")} />
          <PanelBody className="space-y-4">
            <Fieldset label={t("当前密码")} htmlFor="current-password">
              <Input id="current-password" type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} />
            </Fieldset>
            <Fieldset label={t("新密码")} hint={t("至少 12 个字符。")} htmlFor="new-password">
              <Input id="new-password" type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} />
            </Fieldset>
            <Fieldset label={t("确认新密码")} htmlFor="confirm-password">
              <Input id="confirm-password" type="password" autoComplete="new-password" value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} />
            </Fieldset>
            {passwordError ? <ErrorState message={passwordError} /> : null}
            {passwordMessage ? <p role="status" className="text-sm text-signal">{passwordMessage}</p> : null}
            <Button variant="primary" onClick={() => void changePassword()} disabled={busy || !currentPassword || !newPassword || !confirmPassword}>
              <KeyRound size={14} aria-hidden />
              {t("更新密码")}
            </Button>
          </PanelBody>
        </Panel>

        <Panel>
          <PanelHeader title={t("双因素认证")} description={t("使用身份验证器为登录增加一次性验证码。")} />
          <PanelBody className="space-y-4">
            {hasTotp ? (
              <>
                <div className="flex items-start gap-3 rounded-card-sm border border-signal/30 bg-signal/10 p-3 text-sm text-ink-soft">
                  <ShieldCheck size={18} className="mt-0.5 shrink-0 text-signal" aria-hidden />
                  <div>
                    <p className="font-medium text-ink">{t("双因素认证已启用")}</p>
                    <p className="mt-1 text-xs leading-relaxed">{t("每次登录都需要身份验证器生成的 6 位验证码。")}</p>
                  </div>
                </div>
                {totpError ? <ErrorState message={totpError} /> : null}
                <Fieldset
                  label={t("当前密码")}
                  hint={t("关闭双因素认证需要确认当前密码。")}
                  htmlFor="disable-current-password"
                >
                  <Input
                    id="disable-current-password"
                    type="password"
                    autoComplete="current-password"
                    value={disablePassword}
                    onChange={(event) => setDisablePassword(event.target.value)}
                  />
                </Fieldset>
                <Button variant="secondary" onClick={() => void disableTotp()} disabled={busy || !disablePassword}>
                  <ShieldOff size={14} aria-hidden />
                  {t("停用双因素认证")}
                </Button>
              </>
            ) : (
              <>
                <p className="text-sm leading-relaxed text-ink-soft">{t("尚未启用双因素认证。管理员可以在这里生成身份验证器配置并确认。")}</p>
                {enrolment ? (
                  <div className="space-y-4 rounded-card-sm border border-rule bg-paper-inset p-3">
                    <Fieldset label={t("身份验证器配置 URI")} htmlFor="totp-uri">
                      <Textarea id="totp-uri" readOnly value={enrolment.uri} className="font-mono text-xs" rows={3} />
                    </Fieldset>
                    <Fieldset label={t("手动设置密钥")} htmlFor="totp-secret">
                      <Input id="totp-secret" readOnly value={enrolment.secret} className="font-mono tracking-[0.12em]" />
                    </Fieldset>
                    <Fieldset label={t("确认验证码")} hint={t("在身份验证器中添加账户后，输入当前显示的 6 位验证码。")} htmlFor="totp-code">
                      <Input id="totp-code" inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={totpCode} onChange={(event) => setTotpCode(event.target.value)} className="font-mono tracking-[0.25em]" />
                    </Fieldset>
                    {totpError ? <ErrorState message={totpError} /> : null}
                    <div className="flex flex-wrap gap-2">
                      <Button variant="primary" onClick={() => void confirmEnrolment()} disabled={busy || !totpCode}>
                        {t("确认并启用")}
                      </Button>
                      <Button variant="ghost" onClick={cancelEnrolment} disabled={busy}>
                        {t("取消")}
                      </Button>
                    </div>
                  </div>
                ) : (
                  <>
                    {totpError ? <ErrorState message={totpError} /> : null}
                    {totpMessage ? <p role="status" className="text-sm text-signal">{totpMessage}</p> : null}
                    <Button variant="primary" onClick={() => void startEnrolment()} disabled={busy}>
                      <ShieldCheck size={14} aria-hidden />
                      {t("开始设置双因素认证")}
                    </Button>
                  </>
                )}
              </>
            )}
          </PanelBody>
        </Panel>
      </div>
    </Page>
  );
}
