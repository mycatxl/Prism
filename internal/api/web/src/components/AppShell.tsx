import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  AlertTriangle,
  ArrowLeft,
  LogOut,
  Menu,
  RefreshCw,
  ShieldCheck,
  X,
} from "lucide-react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useEffect, useState, useSyncExternalStore } from "react";
import { Button } from "./ui/Button";
import { cn } from "../lib/cn";
import { apiRequest, ApiError } from "../lib/api-client";
import { useAuthStore } from "../features/auth/auth-store";
import { getEnvConfig } from "../features/systemConfig/api";
import { useI18n } from "../i18n";
import { LanguageSwitcher } from "./LanguageSwitcher";
import { QuickSearch } from "./QuickSearch";
import { ThemeMenu } from "./ThemeMenu";
import { navigation } from "../lib/navigation";
import { DialogSurface } from "./ui/DialogSurface";

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
  const token = useAuthStore((state) => state.token);
  const clearToken = useAuthStore((state) => state.clearToken);
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const [menuOpen, setMenuOpen] = useState(false);
  const online = useSyncExternalStore(
    subscribeOnline,
    () => navigator.onLine,
    () => true,
  );
  const info = useQuery({
    queryKey: ["system-info", "shell"],
    queryFn: () => apiRequest<{ version: string }>("/api/v1/system/info"),
    refetchInterval: 15_000,
    retry: false,
  });
  const env = useQuery({
    queryKey: ["system-config-env", "shell"],
    queryFn: getEnvConfig,
    staleTime: 30_000,
  });
  const current = navigation.find(
    (item) =>
      location.pathname === item.path ||
      location.pathname.startsWith(item.path + "/"),
  );
  const unauthorized =
    info.error instanceof ApiError && info.error.status === 401;
  const disconnected = !online || info.isError;
  const securityCount = env.data
    ? [
        !env.data.admin_token_set,
        !env.data.proxy_token_set,
        env.data.admin_token_weak,
        env.data.proxy_token_weak,
      ].filter(Boolean).length
    : 0;

  useEffect(() => {
    const heading = document.querySelector<HTMLElement>(
      ".content h1, .content h2",
    );
    heading?.setAttribute("tabindex", "-1");
    heading?.focus({ preventScroll: true });
    window.scrollTo({ top: 0, behavior: "instant" });
  }, [location.pathname]);

  const logout = () => {
    clearToken();
    queryClient.clear();
    navigate("/login?reauth=1", { replace: true });
  };

  const navContent = (
    <nav className="nav-list" aria-label={t("主导航")}>
      {["工作区", "观测与配置"].map((section) => (
        <div className="nav-section" key={section}>
          <p className="nav-section-label">{t(section)}</p>
          {navigation
            .filter((item) => item.section === section)
            .map((item) => (
              <NavLink
                key={item.path}
                to={item.path}
                title={t(item.label)}
                aria-label={t(item.label)}
                onClick={() => setMenuOpen(false)}
                className={({ isActive }) =>
                  cn("nav-item", isActive && "nav-item-active")
                }
              >
                <item.icon size={17} strokeWidth={1.75} />
                <span>{t(item.label)}</span>
              </NavLink>
            ))}
        </div>
      ))}
    </nav>
  );

  return (
    <div className="app-layout">
      <a className="skip-link" href="#main-content">
        {t("跳到内容")}
      </a>
      <aside className="sidebar">
        <NavLink to="/dashboard" className="brand" aria-label="Prism">
          <img
            className="brand-mark"
            src={`${import.meta.env.BASE_URL}prism-mark.png`}
            alt=""
            width="34"
            height="34"
          />
          <div className="brand-copy">
            <p className="brand-title">
              Prism<span className="brand-period">.</span>
            </p>
            <p className="brand-subtitle">{t("网络工作台")}</p>
          </div>
        </NavLink>
        <div className="sidebar-main">{navContent}</div>
        <div className="sidebar-bottom">
          <div className="sidebar-admin"><span><ShieldCheck size={18} /></span><div><strong>{t("本地管理员")}</strong><small>{t("私有实例")}</small></div></div>
          <div className="sidebar-instance">
            <span
              className={cn(
                "status-dot",
                disconnected
                  ? "status-dot-error"
                  : info.isPending
                    ? "status-dot-pending"
                    : "status-dot-live",
              )}
            />
            <span>
              {t(
                disconnected
                  ? "连接中断"
                  : info.isPending
                    ? "正在连接"
                    : "服务已连接",
              )}
            </span>
          </div>
          <div className="sidebar-tools">
            <LanguageSwitcher compact />
            {token && (
              <Button
                variant="ghost"
                size="sm"
                className="icon-button"
                onClick={logout}
                title={t("退出登录")}
                aria-label={t("退出登录")}
              >
                <LogOut size={16} />
              </Button>
            )}
          </div>
        </div>
      </aside>
      {menuOpen && (
        <DialogSurface
          title={t("主导航")}
          variant="navigation"
          onClose={() => setMenuOpen(false)}
        >
          <div className="mobile-nav-head">
            <strong>Prism</strong>
            <Button
              variant="ghost"
              className="icon-button"
              aria-label={t("关闭")}
              onClick={() => setMenuOpen(false)}
            >
              <X size={18} />
            </Button>
          </div>
          {navContent}
          <div className="mobile-nav-footer">
            <LanguageSwitcher />
            <ThemeMenu />
            {token && (
              <Button variant="secondary" size="sm" onClick={logout}>
                <LogOut size={15} />
                {t("退出登录")}
              </Button>
            )}
          </div>
        </DialogSurface>
      )}
      <main className="main" id="main-content">
        <div className="workspace-bar">
          <div className="workspace-context">
            <Button
              variant="ghost"
              className="icon-button mobile-menu-button"
              aria-label={t("打开导航")}
              onClick={() => setMenuOpen(true)}
            >
              <Menu size={19} />
            </Button>
            <Activity size={15} className="desktop-context-icon" />
            <span>{t(current?.section ?? "工作区")}</span>
            <span className="workspace-divider">/</span>
            <strong>{t(current?.label ?? "总览看板")}</strong>
            {location.pathname.startsWith("/platforms/") && (
              <Button
                variant="ghost"
                size="sm"
                className="icon-button"
                title={t("返回平台列表")}
                aria-label={t("返回平台列表")}
                onClick={() => navigate("/platforms")}
              >
                <ArrowLeft size={15} />
              </Button>
            )}
          </div>
          <QuickSearch />
          <div className="topbar-tools">
            <ThemeMenu />
            <span className="backend-version" title={t("服务版本")}>
              {info.data?.version || "Prism"}
            </span>
          </div>
        </div>
        <div className="content">
          {disconnected && (
            <div className="connection-banner" role="alert">
              <AlertTriangle size={17} />
              <div>
                <strong>
                  {t(
                    unauthorized
                      ? "登录已失效"
                      : !online
                        ? "当前处于离线状态"
                        : "无法连接服务",
                  )}
                </strong>
                <p>{t("已加载的数据可能不是最新状态。")}</p>
              </div>
              <Button
                variant="secondary"
                size="sm"
                onClick={
                  unauthorized
                    ? logout
                    : () => {
                        void queryClient.invalidateQueries();
                      }
                }
              >
                <RefreshCw size={14} />
                {t(unauthorized ? "重新登录" : "重新连接")}
              </Button>
            </div>
          )}
          {securityCount > 0 && (
            <div className="security-notice">
              <AlertTriangle size={15} />
              <span>{t("认证配置需要检查")}</span>
              <NavLink to="/system-config">{t("查看设置")}</NavLink>
            </div>
          )}
          <Outlet />
        </div>
      </main>
    </div>
  );
}
