import { zodResolver } from "@hookform/resolvers/zod";
import { Eye, EyeOff } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { z } from "zod";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { ErrorState } from "../../components/ui/QueryState";
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
    <main className="login-shell ground">
      <section className="login-card-shell">
        <div className="login-card panel">
          <div className="flex items-center justify-between gap-4">
            <div className="flex min-w-0 items-center gap-2.5">
              <img src={`${import.meta.env.BASE_URL}prism-mark.png`} alt="" width="36" height="36" className="size-9 rounded-control" />
              <div className="min-w-0">
                <span className="block text-base font-semibold tracking-tight">Prism</span>
                <span className="block text-xs text-ink-faint">{t("管理控制台")}</span>
              </div>
            </div>
            <LanguageSwitcher />
          </div>

          <div className="mt-5">
            <h1 className="text-xl font-semibold text-ink">{t("欢迎回来")}</h1>
            <p className="mt-1 text-sm leading-relaxed text-ink-soft">{t("使用你的 Prism 账号登录。")}</p>
          </div>

          <form className="mt-6 space-y-4" onSubmit={onSubmit} noValidate>
            <Fieldset label={t("用户名")} htmlFor="username">
              <Input
                id="username"
                autoComplete="username"
                autoCapitalize="none"
                invalid={Boolean(errors.username)}
                {...register("username")}
              />
              {errors.username?.message ? (
                <p role="alert" className="text-xs text-alert">
                  {t(errors.username.message)}
                </p>
              ) : null}
            </Fieldset>

            <Fieldset label={t("密码")} htmlFor="password">
              <div className="flex items-start gap-1.5">
                <Input
                  id="password"
                  className="min-w-0 flex-1"
                  type={isPasswordVisible ? "text" : "password"}
                  autoComplete="current-password"
                  invalid={Boolean(errors.password)}
                  {...register("password")}
                />
                <Button
                  type="button"
                  variant="quiet"
                  size="icon"
                  className="shrink-0 text-ink-faint hover:text-ink"
                  aria-label={isPasswordVisible ? t("隐藏密码") : t("显示密码")}
                  title={isPasswordVisible ? t("隐藏密码") : t("显示密码")}
                  onClick={() => setIsPasswordVisible((visible) => !visible)}
                >
                  {isPasswordVisible ? <EyeOff size={15} aria-hidden /> : <Eye size={15} aria-hidden />}
                </Button>
              </div>
              {errors.password?.message ? (
                <p role="alert" className="text-xs text-alert">
                  {t(errors.password.message)}
                </p>
              ) : null}
            </Fieldset>

            <Fieldset label={t("身份验证码")} hint={t("启用双因素认证后填写身份验证器中的 6 位验证码。")} htmlFor="totp">
              <Input
                id="totp"
                inputMode="numeric"
                autoComplete="one-time-code"
                className="font-mono tracking-[0.25em]"
                maxLength={6}
                invalid={Boolean(errors.totp)}
                {...register("totp")}
              />
              {errors.totp?.message ? (
                <p role="alert" className="text-xs text-alert">
                  {t(errors.totp.message)}
                </p>
              ) : null}
            </Fieldset>

            {submitError ? <ErrorState message={submitError} /> : null}
            <Button type="submit" variant="primary" className="w-full" disabled={isSubmitting} loading={isSubmitting}>
              {t("登录")}
            </Button>
          </form>

          <p className="mt-5 border-t border-rule pt-3 text-xs leading-relaxed text-ink-faint">
            {t("登录状态由安全 Cookie 保持，不会把密码或会话令牌写入浏览器存储。")}
          </p>

          {registrationMode === "open" || registrationMode === "invite" ? (
            <p className="mt-3 text-xs leading-relaxed text-ink-faint">
              {t("还没有账号？")}
              <Link className="ml-1 text-accent underline-offset-2 hover:underline" to="/register">
                {t("注册")}
              </Link>
            </p>
          ) : null}
        </div>
      </section>
    </main>
  );
}
