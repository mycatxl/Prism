import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, useEffect, useSyncExternalStore } from "react";
import { LogOut } from "lucide-react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { ApiError } from "../lib/api-client";
import { cn } from "../lib/cn";
import { navigation } from "../lib/navigation";
import { useAuthStore } from "../features/auth/auth-store";
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

/**
 * The application frame: a 136px glass rail beside the page on wide screens, and
 * a horizontally scrolling glass bar above it below 1100px. The rail carries the
 * page list, grouped by section, and a footer with the instance's live state and
 * version on the left and the theme toggle on the right.
 */
export function AppShell() {
  const { t } = useI18n();
  const token = useAuthStore((state) => state.token);
  const clearToken = useAuthStore((state) => state.clearToken);
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
  const weakTokens = env.data ? [!env.data.admin_token_set, !env.data.proxy_token_set].filter(Boolean).length : 0;

  useEffect(() => {
    const heading = document.querySelector<HTMLElement>("main h1");
    heading?.setAttribute("tabindex", "-1");
    heading?.focus({ preventScroll: true });
    document.querySelector(".shell-main")?.scrollTo({ top: 0, behavior: "instant" });
  }, [location.pathname]);

  const logout = () => {
    clearToken();
    queryClient.clear();
    navigate("/login?reauth=1", { replace: true });
  };

  const statusText = unauthorized
    ? t("令牌失效")
    : disconnected
      ? t("连接中断")
      : weakTokens > 0
        ? t("令牌未设置")
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
              <NavLink
                to={item.path}
                aria-current={active ? "page" : undefined}
                className={cn("shell-nav__link", active && "is-active")}
              >
                {t(item.label)}
              </NavLink>
            </Fragment>
          );
        })}
        <div className="shell-nav__extra">
          <LanguageSwitcher collapsed />
          <Button variant="ghost" size="icon" onClick={logout} aria-label={t("退出登录")} title={t("退出登录")} className="text-ink-soft hover:text-ink">
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
        {token ? <Outlet /> : <div className="p-6 text-sm text-ink-soft">{t("需要管理员令牌")}</div>}
      </main>
    </div>
  );
}
