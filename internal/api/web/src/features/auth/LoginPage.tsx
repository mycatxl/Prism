import { Eye, EyeOff, LockKeyhole, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { z } from "zod";
import { zodResolver } from "@hookform/resolvers/zod";
import { PrismMark } from "../../components/PrismMark";
import { ControlButton, ControlInput } from "../../components/control-plane";
import { ErrorState } from "../../components/ui/QueryState";
import { Tooltip } from "../../components/ui/Tooltip";
import { LanguageSwitcher } from "../../components/LanguageSwitcher";
import { ApiError, fetchSession, login } from "../../lib/api-client";
import { homePathFor } from "../../lib/roles";
import { useAuthStore } from "./auth-store";
import { useI18n } from "../../i18n";

const formSchema = z.object({
  username: z.string().trim().min(1, "请输入用户名"),
  password: z.string().min(1, "请输入密码"),
  // One backslash, not two: `\\d` would be the escape for a literal backslash
  // followed by a literal "d", so every real six-digit code would be rejected
  // before handleSubmit ever ran.
  totp: z.string().trim().refine((value) => value === "" || /^\d{6}$/.test(value), "请输入 6 位验证码"),
});

type LoginFormInput = z.infer<typeof formSchema>;

function destination(search: string, role: string | undefined) {
  const next = new URLSearchParams(search).get("next");
  const fallback = homePathFor(role);
  return next?.startsWith("/") && !next.startsWith("//") && !next.includes("\\") ? next : fallback;
}

function loginErrorMessage(error: ApiError, t: (text: string, options?: Record<string, unknown>) => string): string {
  switch (error.code) {
    case "TOTP_REQUIRED":
      return t("请输入身份验证器中的 6 位验证码。");
    case "TOTP_INVALID":
      return t("验证码不正确，请检查身份验证器后重试。");
    case "RATE_LIMITED":
      return t("失败次数过多，请稍后重试。");
    case "HTTPS_REQUIRED":
      return t("控制台必须通过 HTTPS 访问，或在本机使用 HTTP。");
    case "UNAUTHORIZED":
      return t("用户名或密码不正确。");
    default:
      return t("登录失败：{{message}}", { message: error.message });
  }
}

export function LoginPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const location = useLocation();
  const session = useAuthStore((state) => state.session);
  const [submitError, setSubmitError] = useState("");
  const [isPasswordVisible, setIsPasswordVisible] = useState(false);
  const registrationMode = session.registration_mode ?? "off";

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<LoginFormInput>({
    resolver: zodResolver(formSchema),
    defaultValues: { username: "", password: "", totp: "" },
  });

  useEffect(() => {
    if (session.authenticated || !session.auth_required) {
      navigate(destination(location.search, session.user?.role), { replace: true });
      return;
    }

    let active = true;
    const controller = new AbortController();
    void fetchSession(controller.signal).then((next) => {
      if (active && (next.authenticated || !next.auth_required)) {
        navigate(destination(location.search, next.user?.role), { replace: true });
      }
    }).catch(() => {
      // Keep the form visible when the session probe cannot complete.
    });
    return () => {
      active = false;
      controller.abort();
    };
  }, [location.search, navigate, session.auth_required, session.authenticated, session.user?.role]);

  const onSubmit = handleSubmit(async (values) => {
    setSubmitError("");
    try {
      const response = await login(values.username, values.password, values.totp);
      navigate(destination(location.search, response.user.role), { replace: true });
    } catch (error) {
      if (error instanceof ApiError) {
        setSubmitError(loginErrorMessage(error, t));
      } else {
        setSubmitError(t("无法连接服务，请检查连接后重试。"));
      }
    }
  });

  return (
    <main className="access-shell access-shell--login">
      <div className="access-login">
        <header className="access-login__topbar">
          <div className="access-login__brand" aria-label="Prism">
            <PrismMark size={30} className="access-login__mark" />
            <strong>Prism</strong>
          </div>
          <LanguageSwitcher />
        </header>

        <section className="access-login__panel" aria-labelledby="login-title">
          <div className="access-login__heading">
            <span className="access-login__eyebrow">{t("账户入口")}</span>
            <h1 id="login-title">{t("欢迎回来")}</h1>
            <p>{t("使用你的 Prism 账号登录。")}</p>
          </div>

          <div className="access-login__signals" aria-label={t("账户入口")}>
            <span><ShieldCheck size={15} aria-hidden />{t("安全 Cookie 会话")}</span>
            <span><LockKeyhole size={15} aria-hidden />{t("双因素认证可用")}</span>
          </div>

          <form className="access-form access-login__form" onSubmit={onSubmit} noValidate>
            <ControlInput
              label={t("用户名")}
              id="username"
              autoComplete="username"
              autoCapitalize="none"
              invalid={Boolean(errors.username)}
              hint={errors.username?.message ? t(errors.username.message) : undefined}
              {...register("username")}
            />

            <div className="access-login__password">
              <ControlInput
                label={t("密码")}
                id="password"
                type={isPasswordVisible ? "text" : "password"}
                className="access-login__password-field"
                autoComplete="current-password"
                invalid={Boolean(errors.password)}
                hint={errors.password?.message ? t(errors.password.message) : undefined}
                {...register("password")}
              />
              <Tooltip triggerClassName="access-login__password-toggle-trigger" content={isPasswordVisible ? t("隐藏密码") : t("显示密码")}>
                <ControlButton
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="workspace-button access-login__password-toggle"
                  aria-label={isPasswordVisible ? t("隐藏密码") : t("显示密码")}
                  onPress={() => setIsPasswordVisible((visible) => !visible)}
                >
                  {isPasswordVisible ? <EyeOff size={15} aria-hidden /> : <Eye size={15} aria-hidden />}
                </ControlButton>
              </Tooltip>
            </div>

            <ControlInput
              label={t("身份验证码")}
              id="totp"
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={6}
              invalid={Boolean(errors.totp)}
              hint={errors.totp?.message ? t(errors.totp.message) : t("启用双因素认证后填写身份验证器中的 6 位验证码。")}
              {...register("totp")}
            />

            {submitError ? <ErrorState message={submitError} /> : null}
            <ControlButton
              type="submit"
              variant="primary"
              className="workspace-button access-login__submit"
              isDisabled={isSubmitting}
              loading={isSubmitting}
              aria-busy={isSubmitting || undefined}
            >
              {t("登录")}
            </ControlButton>
          </form>

          <footer className="access-login__footer">
            <p>{t("登录状态由安全 Cookie 保持，不会把密码或会话令牌写入浏览器存储。")}</p>
            {registrationMode === "open" || registrationMode === "invite" ? (
              <p>
                {t("还没有账号？")}
                <Link to="/register">{t("注册")}</Link>
              </p>
            ) : null}
          </footer>
        </section>
      </div>
    </main>
  );
}
