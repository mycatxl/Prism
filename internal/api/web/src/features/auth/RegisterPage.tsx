import { zodResolver } from "@hookform/resolvers/zod";
import { Check, Eye, EyeOff } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { Link, useNavigate } from "react-router-dom";
import { z } from "zod";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { ErrorState } from "../../components/ui/QueryState";
import { LanguageSwitcher } from "../../components/LanguageSwitcher";
import { cn } from "../../lib/cn";
import { ApiError, fetchSession, register } from "../../lib/api-client";
import { homePathFor } from "../../lib/roles";
import { useAuthStore } from "./auth-store";
import { useI18n } from "../../i18n";

const formSchema = z
  .object({
    username: z.string().trim().min(1, "请输入用户名"),
    password: z.string().min(1, "请输入密码"),
    confirm: z.string().min(1, "请再次输入密码"),
  })
  .refine((values) => values.password === values.confirm, {
    path: ["confirm"],
    message: "两次输入的密码不一致",
  });

type RegisterFormInput = z.infer<typeof formSchema>;

function registerErrorMessage(error: ApiError, t: (text: string, options?: Record<string, unknown>) => string): string {
  switch (error.code) {
    case "REGISTRATION_CLOSED":
      return t("本部署未开放自助注册，请联系运营方开通账号。");
    // An unknown, disabled, lapsed or used-up invitation all answer the same
    // INVALID_ARGUMENT on purpose, so the form repeats that single answer.
    case "INVALID_ARGUMENT":
      return error.message.includes("invite_code")
        ? t("邀请码不可用。")
        : t("注册信息不符合要求：{{message}}", { message: error.message });
    case "CONFLICT":
      return t("该用户名已被占用。");
    case "RATE_LIMITED":
      return t("失败次数过多，请稍后重试。");
    case "HTTPS_REQUIRED":
      return t("控制台必须通过 HTTPS 访问，或在本机使用 HTTP。");
    default:
      return t("注册失败：{{message}}", { message: error.message });
  }
}

export function RegisterPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const session = useAuthStore((state) => state.session);
  const [submitError, setSubmitError] = useState("");
  const [isPasswordVisible, setIsPasswordVisible] = useState(false);

  // The server owns the policy: "open" needs no code, "invite" requires one,
  // and "off" must not offer the form at all.
  const mode = session.registration_mode ?? "off";
  const needsInvite = mode === "invite";
  const open = mode === "open" || needsInvite;

  const {
    register: field,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<RegisterFormInput>({
    resolver: zodResolver(formSchema),
    defaultValues: { username: "", password: "", confirm: "" },
  });

  useEffect(() => {
    if (session.authenticated) {
      navigate(homePathFor(session.user?.role), { replace: true });
      return;
    }
    if (session.registration_mode) return;

    const controller = new AbortController();
    void fetchSession(controller.signal).catch(() => {
      // Keep the form usable when the session probe cannot complete.
    });
    return () => controller.abort();
  }, [navigate, session.authenticated, session.registration_mode, session.user?.role]);

  const [inviteCode, setInviteCode] = useState("");

  const onSubmit = handleSubmit(async (values) => {
    setSubmitError("");
    try {
      const response = await register(needsInvite ? inviteCode.trim() : "", values.username, values.password);
      navigate(homePathFor(response.user.role), { replace: true });
    } catch (error) {
      if (error instanceof ApiError) {
        setSubmitError(registerErrorMessage(error, t));
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
          <h1 className="text-2xl">{t("创建账号，开始使用代理。")}</h1>
          <ul className="mt-5 space-y-2">
            {["订阅与配额", "访问密钥", "接入指南"].map((label) => (
              <li key={label} className="flex items-center gap-2 text-sm text-ink-soft">
                <Check size={14} className="shrink-0 text-signal" aria-hidden />
                {t(label)}
              </li>
            ))}
          </ul>
        </div>
        <p className="text-xs text-ink-faint">{t("账号创建后即可登录控制台。")}</p>
      </section>

      <section className="login-card-shell flex w-full shrink-0 items-center border-t border-rule bg-paper-inset px-6 py-10 lg:w-[min(42vw,32rem)] lg:border-t-0 lg:border-l lg:px-8">
        <div className="login-card panel mx-auto w-full max-w-md p-6 sm:p-8">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <h2 className="text-lg">{t("注册账号")}</h2>
              <p className="mt-1 text-sm leading-relaxed text-ink-soft">
                {open ? t("填写下面的信息创建你的账号。") : t("本部署未开放自助注册，请联系运营方开通账号。")}
              </p>
            </div>
            <LanguageSwitcher />
          </div>

          {open ? (
            <form className="mt-6 space-y-4" onSubmit={onSubmit} noValidate>
              {needsInvite ? (
                <Fieldset label={t("邀请码")} htmlFor="invite_code">
                  <Input
                    id="invite_code"
                    autoComplete="off"
                    autoCapitalize="none"
                    value={inviteCode}
                    onChange={(event) => setInviteCode(event.target.value)}
                  />
                </Fieldset>
              ) : null}

              <Fieldset label={t("用户名")} htmlFor="username">
                <Input
                  id="username"
                  autoComplete="username"
                  autoCapitalize="none"
                  aria-invalid={errors.username ? true : undefined}
                  {...field("username")}
                />
                {errors.username?.message ? <p className="text-xs text-alert">{t(errors.username.message)}</p> : null}
              </Fieldset>

              <Fieldset label={t("密码")} htmlFor="password">
                <div className="flex items-start gap-1.5">
                  <Input
                    id="password"
                    className={cn("min-w-0 flex-1", errors.password && "border-alert ring-1 ring-alert/40")}
                    type={isPasswordVisible ? "text" : "password"}
                    autoComplete="new-password"
                    aria-invalid={errors.password ? true : undefined}
                    {...field("password")}
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

              <Fieldset label={t("确认密码")} htmlFor="confirm">
                <Input
                  id="confirm"
                  type={isPasswordVisible ? "text" : "password"}
                  autoComplete="new-password"
                  aria-invalid={errors.confirm ? true : undefined}
                  {...field("confirm")}
                />
                {errors.confirm?.message ? <p className="text-xs text-alert">{t(errors.confirm.message)}</p> : null}
              </Fieldset>

              {submitError ? <ErrorState message={submitError} /> : null}
              <Button type="submit" variant="primary" className="w-full" disabled={isSubmitting} loading={isSubmitting}>
                {t("注册")}
              </Button>
            </form>
          ) : null}

          <p className="mt-5 border-t border-rule pt-3 text-xs leading-relaxed text-ink-faint">
            {t("已有账号？")}
            <Link className="ml-1 text-signal underline-offset-2 hover:underline" to="/login">
              {t("去登录")}
            </Link>
          </p>
        </div>
      </section>
    </main>
  );
}
