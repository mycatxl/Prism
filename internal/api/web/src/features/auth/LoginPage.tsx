import { zodResolver } from "@hookform/resolvers/zod";
import { Check, Eye, EyeOff } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useLocation, useNavigate } from "react-router-dom";
import { z } from "zod";
import { Button } from "../../components/ui/Button";
import { Fieldset, Input } from "../../components/ui/Input";
import { ErrorState } from "../../components/ui/QueryState";
import { LanguageSwitcher } from "../../components/LanguageSwitcher";
import { cn } from "../../lib/cn";
import { useAuthStore } from "./auth-store";
import { apiRequest, ApiError, fetchAuthMode } from "../../lib/api-client";
import { useI18n } from "../../i18n";

const formSchema = z.object({
  token: z.string().trim().min(1, "请输入 Admin Token"),
});

type LoginFormInput = z.infer<typeof formSchema>;

function destination(search: string) {
  const next = new URLSearchParams(search).get("next");
  return next?.startsWith("/") && !next.startsWith("//") && !next.includes("\\") ? next : "/dashboard";
}

/**
 * The entry door.
 *
 * Two columns on a wide screen: what Prism is on the paper, and the single field
 * that opens it on a raised sheet. The token is a credential, so it is typed in
 * mono, masked until the operator asks to see it, and its error is stated under
 * the field rather than colouring the whole column.
 */
export function LoginPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const location = useLocation();
  const setToken = useAuthStore((state) => state.setToken);
  const storedToken = useAuthStore((state) => state.token);
  const [submitError, setSubmitError] = useState("");
  const [isPasswordVisible, setIsPasswordVisible] = useState(false);

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<LoginFormInput>({
    resolver: zodResolver(formSchema),
    defaultValues: { token: "" },
  });

  useEffect(() => {
    const params = new URLSearchParams(location.search);
    const next = destination(location.search);

    if (storedToken) {
      navigate(next, { replace: true });
      return;
    }

    if (params.get("reauth")) return;

    let active = true;
    const controller = new AbortController();

    const checkAuthMode = async () => {
      try {
        // Anonymous access is a property of the deployment, not a failed request: a
        // tokenless probe of an authenticated endpoint answers 401, and the browser
        // logs that response as a console error on this very page.
        const authRequired = await fetchAuthMode(controller.signal);
        if (!active || authRequired) {
          return;
        }
        navigate(next, { replace: true });
      } catch {
        // Keep login page for secured deployments or temporary network errors.
      }
    };
    void checkAuthMode();

    return () => {
      active = false;
      controller.abort();
    };
  }, [location.search, navigate, storedToken]);

  const onSubmit = handleSubmit(async (values) => {
    setSubmitError("");

    try {
      await apiRequest("/api/v1/system/info", {
        auth: true,
        token: values.token,
      });
    } catch (error) {
      if (error instanceof ApiError) {
        setSubmitError(error.status === 401
          ? t("管理员令牌不正确，请检查后重试。")
          : t("登录失败：{{message}}", { message: error.message }));
      } else {
        setSubmitError(t("无法连接服务，请检查连接后重试。"));
      }
      return;
    }

    setToken(values.token);

    const next = destination(location.search);
    navigate(next, { replace: true });
  });

  const fieldErrorId = "token-error";

  return (
    <main className="login-shell ground flex min-h-dvh flex-col lg:flex-row">
      <section
        aria-label="Prism"
        className="login-intro flex flex-1 flex-col justify-between gap-10 px-6 py-10 lg:px-12 lg:py-14"
      >
        <div className="flex items-center gap-2.5">
          <img src={`${import.meta.env.BASE_URL}prism-mark.png`} alt="" width="40" height="40" className="size-10" />
          <span className="text-lg font-semibold tracking-tight">Prism</span>
        </div>

        <div className="max-w-[46ch]">
          <h1 className="text-2xl">{t("管理你的节点，读懂每个出口。")}</h1>
          <ul className="mt-5 space-y-2">
            {["订阅与节点", "出口类型与风险记录", "路由与请求日志"].map(label => (
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
              <p className="mt-1 text-sm leading-relaxed text-ink-soft">{t("使用部署时设置的管理员令牌。")}</p>
            </div>
            <div className="flex shrink-0 items-center gap-1">
              <LanguageSwitcher />
            </div>
          </div>

          <form className="mt-6 space-y-4" onSubmit={onSubmit} noValidate>
            <Fieldset label={t("管理员令牌")} htmlFor="token">
              <div className="flex items-start gap-1.5">
                <Input
                  id="token"
                  className={cn(
                    "min-w-0 flex-1 font-mono",
                    errors.token && "border-alert ring-1 ring-alert/40",
                  )}
                  aria-invalid={errors.token ? true : undefined}
                  aria-describedby={errors.token?.message ? fieldErrorId : undefined}
                  autoComplete="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  type={isPasswordVisible ? "text" : "password"}
                  {...register("token")}
                />
                <Button
                  type="button"
                  variant="quiet"
                  size="icon"
                  className="shrink-0 text-ink-faint hover:text-ink"
                  aria-label={isPasswordVisible ? t("隐藏管理员令牌") : t("显示管理员令牌")}
                  title={isPasswordVisible ? t("隐藏管理员令牌") : t("显示管理员令牌")}
                  onClick={() => setIsPasswordVisible((visible) => !visible)}
                >
                  {isPasswordVisible ? <EyeOff size={15} aria-hidden /> : <Eye size={15} aria-hidden />}
                </Button>
              </div>
              {errors.token?.message ? (
                <p id={fieldErrorId} role="alert" className="text-xs text-alert">
                  {t(errors.token.message)}
                </p>
              ) : null}
            </Fieldset>

            {submitError ? <ErrorState message={submitError} /> : null}

            <Button type="submit" variant="primary" className="w-full" disabled={isSubmitting} loading={isSubmitting}>
              {t("进入工作台")}
            </Button>
          </form>

          <p className="mt-5 border-t border-rule pt-3 text-xs leading-relaxed text-ink-faint">
            {t("令牌仅保留在当前标签页，关闭后需要重新登录。")}
          </p>
        </div>
      </section>
    </main>
  );
}