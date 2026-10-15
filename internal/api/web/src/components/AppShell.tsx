import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, useEffect, useSyncExternalStore } from "react";
import { LogOut, ShieldCheck } from "lucide-react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { ApiError, logout } from "../lib/api-client";
import { cn } from "../lib/cn";
import { navigation } from "../lib/navigation";
import { useAuthStore, clearSession } from "../features/auth/auth-store";
import { getEnvConfig } from "../features/systemConfig/api";
import { SYSTEM_INFO_QUERY_KEY, getSystemInfo } from "../features/systemInfo/api";
import { useI18n } from "../i18n";
import { LanguageSwitcher } from "./LanguageSwitcher";
import { ThemeSwitcher } from "./ThemeSwitcher";
import { Button } from "./ui/Button";

function subscribeOnline(callback: () => void) {
  window.addEventListener("online", callback);
  window.addEventListener("offline", callback);
  return () => {
    window.removeEventListener("online", callback);
    window.removeEventListener("offline", callback);
  };
}

export function AppShell() {
  const { t } = useI18n();
  const session = useAuthStore((state) => state.session);
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const online = useSyncExternalStore(subscribeOnline, () => navigator.onLine, () => true);

  const info = useQuery({
    queryKey: SYSTEM_INFO_QUERY_KEY,
    queryFn: getSystemInfo,
    refetchInterval: 30_000,
    retry: false,
  });
  const env = useQuery({
    queryKey: ["system-config-env", "shell"],
    queryFn: getEnvConfig,
    staleTime: 30_000,
  });

  const unauthorized = info.error instanceof ApiError && info.error.status === 401;
  const disconnected = !online || info.isError;
  const weakTokens = env.data && !env.data.proxy_token_set ? 1 : 0;

  useEffect(() => {
    const heading = document.querySelector<HTMLElement>("main h1");
    heading?.setAttribute("tabindex", "-1");
    heading?.focus({ preventScroll: true });
    document.querySelector(".shell-main")?.scrollTo({ top: 0, behavior: "instant" });
  }, [location.pathname]);

  const handleLogout = async () => {
    try {
      await logout();
    } catch {
      clearSession();
    }
    queryClient.clear();
    navigate("/login?reauth=1", { replace: true });
  };

  const statusText = unauthorized
    ? t("会话失效")
    : disconnected
      ? t("连接中断")
      : weakTokens > 0
        ? t("旧版令牌配置仍存在")
        : info.data
          ? t("实例在线")
          : t("连接中");
  const statusTone = unauthorized ? "alert" : disconnected || weakTokens > 0 ? "warn" : info.data ? "live" : "idle";
  const version = info.data?.version ? `v${info.data.version.replace(/^v/, "")}` : "";

  return (
    <div className="shell">
      <nav aria-label={t("主导航")} className="shell-nav">
        <div className="shell-brand">
          <img src={`${import.meta.env.BASE_URL}prism-mark.png`} alt="" aria-hidden className="shell-brand__mark" />
          <span>Prism</span>
        </div>
        {navigation.map((item, index) => {
          const active = location.pathname === item.path || location.pathname.startsWith(item.path + "/");
          const section = index === 0 || navigation[index - 1].section !== item.section ? item.section : null;
          return (
            <Fragment key={item.path}>
              {section && <div className="shell-nav__section">{t(section)}</div>}
              <NavLink to={item.path} aria-current={active ? "page" : undefined} className={cn("shell-nav__link", active && "is-active")}>
                {t(item.label)}
              </NavLink>
            </Fragment>
          );
        })}
        <div className="shell-nav__extra">
          <LanguageSwitcher collapsed />
          <Button variant="ghost" size="icon" onClick={() => navigate("/account-security")} aria-label={t("账户安全")} title={t("账户安全")} className="text-ink-soft hover:text-ink">
            <ShieldCheck size={14} aria-hidden />
          </Button>
          <Button variant="ghost" size="icon" onClick={() => void handleLogout()} aria-label={t("退出登录")} title={t("退出登录")} className="text-ink-soft hover:text-ink">
            <LogOut size={14} aria-hidden />
          </Button>
        </div>
        <div className="shell-nav__foot">
          <span className="shell-version" title={statusText} role="status">
            <span className={cn("shell-live", `shell-live--${statusTone}`)} aria-hidden />
            <span className="sr-only">{statusText}</span>
            <span className="shell-version__text">{version || "Prism"}</span>
          </span>
          <ThemeSwitcher className="shell-theme-toggle" />
        </div>
      </nav>

      <main className="shell-main">
        {session.authenticated || !session.auth_required ? <Outlet /> : <div className="p-6 text-sm text-ink-soft">{t("需要登录")}</div>}
      </main>
    </div>
  );
}
