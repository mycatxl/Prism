import { zodResolver } from "@hookform/resolvers/zod";
import { Check, Eye, EyeOff } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { z } from "zod";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { ErrorState } from "../../components/ui/QueryState";
import { LanguageSwitcher } from "../../components/LanguageSwitcher";
import { cn } from "../../lib/cn";
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
    <main className="login-shell ground flex min-h-dvh flex-col lg:flex-row">
      <section aria-label="Prism" className="login-intro flex flex-1 flex-col justify-between gap-10 px-6 py-10 lg:px-12 lg:py-14">
        <div className="flex items-center gap-2.5">
          <img src={`${import.meta.env.BASE_URL}prism-mark.png`} alt="" width="40" height="40" className="size-10" />
          <span className="text-lg font-semibold tracking-tight">Prism</span>
        </div>
        <div className="max-w-[46ch]">
          <h1 className="text-2xl">{t("管理你的节点，读懂每个出口。")}</h1>
          <ul className="mt-5 space-y-2">
            {["订阅与节点", "出口类型与风险记录", "路由与请求日志"].map((label) => (
              <li key={label} className="flex items-center gap-2 text-sm text-ink-soft">
                <Check size={14} className="shrink-0 text-signal" aria-hidden />
                {t(label)}
              </li>
            ))}
          </ul>
        </div>
        <p className="text-xs text-ink-faint">{t("节点、出口与质量，一处掌握。")}</p>
      </section>

      <section className="login-card-shell flex w-full shrink-0 items-center border-t border-rule bg-paper-inset px-6 py-10 lg:w-[min(42vw,32rem)] lg:border-t-0 lg:border-l lg:px-8">
        <div className="login-card panel mx-auto w-full max-w-md p-6 sm:p-8">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <h2 className="text-lg">{t("欢迎回来")}</h2>
              <p className="mt-1 text-sm leading-relaxed text-ink-soft">{t("使用你的 Prism 账号登录。")}</p>
            </div>
            <LanguageSwitcher />
          </div>

          <form className="mt-6 space-y-4" onSubmit={onSubmit} noValidate>
            <Fieldset label={t("用户名")} htmlFor="username">
              <Input
                id="username"
                autoComplete="username"
                autoCapitalize="none"
                aria-invalid={errors.username ? true : undefined}
                {...register("username")}
              />
              {errors.username?.message ? <p className="text-xs text-alert">{t(errors.username.message)}</p> : null}
            </Fieldset>

            <Fieldset label={t("密码")} htmlFor="password">
              <div className="flex items-start gap-1.5">
                <Input
                  id="password"
                  className={cn("min-w-0 flex-1", errors.password && "border-alert ring-1 ring-alert/40")}
                  type={isPasswordVisible ? "text" : "password"}
                  autoComplete="current-password"
                  aria-invalid={errors.password ? true : undefined}
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
              {errors.password?.message ? <p className="text-xs text-alert">{t(errors.password.message)}</p> : null}
            </Fieldset>

            <Fieldset label={t("身份验证码")} hint={t("启用双因素认证后填写身份验证器中的 6 位验证码。")} htmlFor="totp">
              <Input
                id="totp"
                inputMode="numeric"
                autoComplete="one-time-code"
                className="font-mono tracking-[0.25em]"
                maxLength={6}
                aria-invalid={errors.totp ? true : undefined}
                {...register("totp")}
              />
              {errors.totp?.message ? <p className="text-xs text-alert">{t(errors.totp.message)}</p> : null}
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
              <Link className="ml-1 text-signal underline-offset-2 hover:underline" to="/register">
                {t("注册")}
              </Link>
            </p>
          ) : null}
        </div>
      </section>
    </main>
  );
}
