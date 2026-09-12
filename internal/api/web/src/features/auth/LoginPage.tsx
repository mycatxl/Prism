import { zodResolver } from "@hookform/resolvers/zod";
import { ArrowRight, Eye, EyeOff, Check } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useLocation, useNavigate } from "react-router-dom";
import { z } from "zod";
import { Card } from "../../components/ui/Card";
import { Input } from "../../components/ui/Input";
import { Button } from "../../components/ui/Button";
import { LanguageSwitcher } from "../../components/LanguageSwitcher";
import { ThemeMenu } from "../../components/ThemeMenu";
import { useAuthStore } from "./auth-store";
import { apiRequest, ApiError } from "../../lib/api-client";
import { useI18n } from "../../i18n";

const formSchema = z.object({
  token: z.string().trim().min(1, "请输入 Admin Token"),
});

type LoginFormInput = z.infer<typeof formSchema>;

function destination(search: string) {
  const next = new URLSearchParams(search).get("next");
  return next?.startsWith("/") && !next.startsWith("//") && !next.includes("\\") ? next : "/dashboard";
}

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
        await apiRequest("/api/v1/system/info", {
          auth: false,
          signal: controller.signal,
        });
        if (!active) {
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

  return (
    <main className="login-layout">
      <section className="login-introduction" aria-label="Prism">
        <div className="login-wordmark">
          <img src={`${import.meta.env.BASE_URL}prism-mark.png`} alt="" width="40" height="40" />
          <span>Prism</span>
        </div>
        <div className="prism-glass-art" aria-hidden="true">
          <span className="glass-pane glass-pane-back" />
          <span className="glass-pane glass-pane-middle" />
          <span className="glass-pane glass-pane-front" />
          <span className="glass-beam" />
        </div>
        <div className="login-message">
          <h1>{t("管理你的节点，读懂每个出口。")}</h1>
          <ul className="login-features">
            {["订阅与节点", "出口类型与风险记录", "路由与请求日志"].map(label =>
              <li key={label}><Check size={14} />{t(label)}</li>)}
          </ul>
        </div>
        <p className="login-brand-note">{t("节点、出口与质量，一处掌握。")}</p>
      </section>
      <Card className="login-card">
        <div className="login-header">
          <div className="login-heading-copy">
            <h2 className="login-title">{t("欢迎回来")}</h2>
            <p className="login-note">{t("使用部署时设置的管理员令牌。")}</p>
          </div>
          <div className="login-tools">
            <ThemeMenu />
            <LanguageSwitcher className="login-locale" />
          </div>
        </div>

        <form className="login-form" onSubmit={onSubmit}>
          <label className="field-label field-label-with-info login-token-label" htmlFor="token">
            <span>{t("管理员令牌")}</span>
          </label>
          <div className="login-input-wrap">
            <Input
              id="token"
              className="login-token-input"
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              type={isPasswordVisible ? "text" : "password"}
              invalid={Boolean(errors.token)}
              {...register("token")}
            />
            <Button
              variant="ghost"
              size="sm"
              className="password-visibility-toggle"
              aria-label={isPasswordVisible ? t("隐藏管理员令牌") : t("显示管理员令牌")}
              title={isPasswordVisible ? t("隐藏管理员令牌") : t("显示管理员令牌")}
              onClick={() => setIsPasswordVisible((visible) => !visible)}
            >
              {isPasswordVisible ? <EyeOff size={16} /> : <Eye size={16} />}
            </Button>
          </div>

          {errors.token?.message ? <p className="field-error">{t(errors.token.message)}</p> : null}
          {submitError ? <p className="field-error" role="alert">{submitError}</p> : null}

          <Button type="submit" className="w-full login-submit" disabled={isSubmitting}>
            {isSubmitting ? t("校验中...") : t("进入工作台")}<ArrowRight size={16} />
          </Button>
        </form>
        <p className="login-session-note">{t("令牌仅保留在当前标签页，关闭后需要重新登录。")}</p>
      </Card>
    </main>
  );
}
