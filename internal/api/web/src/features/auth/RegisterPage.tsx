import { Card as HeroCard } from "@heroui/react";
import { Check, Eye, EyeOff } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { Link, useNavigate } from "react-router-dom";
import { z } from "zod";
import { zodResolver } from "@hookform/resolvers/zod";
import { ControlButton, ControlInput } from "../../components/control-plane";
import { PrismMark } from "../../components/PrismMark";
import { ErrorState } from "../../components/ui/QueryState";
import { Tooltip } from "../../components/ui/Tooltip";
import { LanguageSwitcher } from "../../components/LanguageSwitcher";
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
    <main className="access-shell">
      <div className="access-layout access-layout--register">
        <section aria-label="Prism" className="access-brief">
          <div className="access-brand">
            <PrismMark size={40} className="access-brand__mark" />
            <div className="access-brand__copy">
              <strong>Prism</strong>
              <span>{t("租户自助工作台")}</span>
            </div>
          </div>
          <div className="access-brief__content">
            <span className="access-brief__eyebrow">{t("自助接入")}</span>
            <h1>{t("创建账号，开始使用代理。")}</h1>
            <ul className="access-brief__list">
              {["订阅与配额", "访问密钥", "接入指南"].map((label) => (
                <li key={label}>
                  <Check size={15} aria-hidden />
                  <span>{t(label)}</span>
                </li>
              ))}
            </ul>
          </div>
          <p className="access-brief__footnote">{t("账号创建后即可登录控制台。")}</p>
        </section>

        <HeroCard className="access-card" variant="default">
          <HeroCard.Header className="access-card__header">
            <div className="access-card__utility">
              <span className="access-card__utility-label">{t("账户入口")}</span>
              <LanguageSwitcher />
            </div>
            <HeroCard.Title>{t("注册账号")}</HeroCard.Title>
            <HeroCard.Description>
              {open ? t("填写下面的信息创建你的账号。") : t("本部署未开放自助注册，请联系运营方开通账号。")}
            </HeroCard.Description>
          </HeroCard.Header>

          <HeroCard.Content>
            {open ? (
              <form className="access-form" onSubmit={onSubmit} noValidate>
                {needsInvite ? (
                  <ControlInput
                    label={t("邀请码")}
                    id="invite_code"
                    autoComplete="off"
                    autoCapitalize="none"
                    value={inviteCode}
                    onChange={(event) => setInviteCode(event.target.value)}
                  />
                ) : null}

                <ControlInput
                  label={t("用户名")}
                  id="username"
                  autoComplete="username"
                  autoCapitalize="none"
                  invalid={Boolean(errors.username)}
                  hint={errors.username?.message ? t(errors.username.message) : undefined}
                  {...field("username")}
                />

                <div className="access-password-field">
                  <ControlInput
                    label={t("密码")}
                    id="password"
                    type={isPasswordVisible ? "text" : "password"}
                    autoComplete="new-password"
                    invalid={Boolean(errors.password)}
                    hint={errors.password?.message ? t(errors.password.message) : undefined}
                    {...field("password")}
                  />
                  <Tooltip triggerClassName="access-password-toggle-trigger" content={isPasswordVisible ? t("隐藏密码") : t("显示密码")}>
                    <ControlButton
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="access-password-toggle"
                      aria-label={isPasswordVisible ? t("隐藏密码") : t("显示密码")}
                      onPress={() => setIsPasswordVisible((visible) => !visible)}
                    >
                      {isPasswordVisible ? <EyeOff size={15} aria-hidden /> : <Eye size={15} aria-hidden />}
                    </ControlButton>
                  </Tooltip>
                </div>

                <ControlInput
                  label={t("确认密码")}
                  id="confirm"
                  type={isPasswordVisible ? "text" : "password"}
                  autoComplete="new-password"
                  invalid={Boolean(errors.confirm)}
                  hint={errors.confirm?.message ? t(errors.confirm.message) : undefined}
                  {...field("confirm")}
                />

                {submitError ? <ErrorState message={submitError} /> : null}
                <ControlButton type="submit" variant="primary" className="access-submit" isDisabled={isSubmitting} loading={isSubmitting}>
                  {t("注册")}
                </ControlButton>
              </form>
            ) : (
              <div className="access-blocked">
                <span className="access-blocked__mark" aria-hidden />
                <strong>{t("暂未开放注册")}</strong>
                <p>{t("本部署未开放自助注册，请联系运营方开通账号。")}</p>
              </div>
            )}
          </HeroCard.Content>

          <HeroCard.Footer className="access-card__footer">
            <p>
              {t("已有账号？")}
              <Link to="/login">{t("去登录")}</Link>
            </p>
          </HeroCard.Footer>
        </HeroCard>
      </div>
    </main>
  );
}
