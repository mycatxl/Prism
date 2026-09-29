import { useQuery, useQueryClient } from "@tanstack/react-query";
import { LogOut, Menu, PanelLeftClose, PanelLeftOpen, RefreshCw, X } from "lucide-react";
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { ApiError, apiRequest } from "../lib/api-client";
import { cn } from "../lib/cn";
import { navigation } from "../lib/navigation";
import { useAuthStore } from "../features/auth/auth-store";
import { getEnvConfig } from "../features/systemConfig/api";
import { useI18n } from "../i18n";
import { LanguageSwitcher } from "./LanguageSwitcher";
import { QuickSearch } from "./QuickSearch";
import { Badge } from "./ui/Badge";
import { Button } from "./ui/Button";

function subscribeOnline(callback: () => void) {
  window.addEventListener("online", callback);
  window.addEventListener("offline", callback);
  return () => {
    window.removeEventListener("online", callback);
    window.removeEventListener("offline", callback);
  };
}

const RAIL_COLLAPSED_KEY = "prism.rail-collapsed";

/**
 * The application frame: a left rail of destinations and a top bar that answers
 * "where am I" and "is this instance healthy".
 *
 * The rail is grouped by section with a hairline between groups rather than a
 * heading above each, so the grouping is visible without spending vertical space
 * on labels. It collapses to icons only, because the operator knows the icons once
 * they have used the panel for a day.
 */
export function AppShell() {
  const { t } = useI18n();
  const token = useAuthStore((state) => state.token);
  const clearToken = useAuthStore((state) => state.clearToken);
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [collapsed, setCollapsed] = useState(
    () => window.localStorage.getItem(RAIL_COLLAPSED_KEY) === "1",
  );
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
      location.pathname === item.path || location.pathname.startsWith(item.path + "/"),
  );

  const unauthorized = info.error instanceof ApiError && info.error.status === 401;
  const disconnected = !online || info.isError;
  const weakTokens = env.data
    ? [!env.data.admin_token_set, !env.data.proxy_token_set].filter(Boolean).length
    : 0;

  const toggleRail = useCallback(() => {
    setCollapsed((previous) => {
      const next = !previous;
      window.localStorage.setItem(RAIL_COLLAPSED_KEY, next ? "1" : "0");
      return next;
    });
  }, []);

  // Closing the mobile drawer on navigation keeps the destination the only thing
  // that changed on screen.
  useEffect(() => {
    setMobileNavOpen(false);
  }, [location.pathname]);

  // Move focus to the page heading after a navigation so keyboard users land in
  // the new content instead of at the top of the chrome.
  useEffect(() => {
    const heading = document.querySelector<HTMLElement>("main h1");
    heading?.setAttribute("tabindex", "-1");
    heading?.focus({ preventScroll: true });
    window.scrollTo({ top: 0, behavior: "instant" });
  }, [location.pathname]);

  const logout = () => {
    clearToken();
    queryClient.clear();
    navigate("/login?reauth=1", { replace: true });
  };

  const sections = navigation.reduce<Record<string, typeof navigation>>((acc, item) => {
    (acc[item.section] ??= []).push(item);
    return acc;
  }, {});

  const rail = (
    <nav
      aria-label={t("主导航")}
      className={cn(
        "flex h-full flex-col border-r border-rule bg-paper-raised",
        collapsed ? "w-14" : "w-56",
      )}
    >
      <div
        className={cn(
          "flex h-12 shrink-0 items-center border-b border-rule",
          collapsed ? "justify-center px-2" : "gap-2 px-3",
        )}
      >
        <span
          aria-hidden
          className="grid size-6 shrink-0 place-items-center rounded-control bg-signal text-2xs font-semibold text-white"
        >
          P
        </span>
        {!collapsed && (
          <span className="min-w-0 flex-1 truncate text-sm font-semibold tracking-tight">
            Prism
          </span>
        )}
        <Button
          variant="ghost"
          size="icon"
          onClick={toggleRail}
          className={cn("hidden shrink-0 lg:inline-flex", collapsed && "hidden")}
          aria-label={t("收起导航")}
        >
          <PanelLeftClose size={15} />
        </Button>
      </div>

      {collapsed && (
        <Button
          variant="ghost"
          size="icon"
          onClick={toggleRail}
          className="mx-auto mt-2 hidden shrink-0 lg:inline-flex"
          aria-label={t("展开导航")}
        >
          <PanelLeftOpen size={15} />
        </Button>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto py-2">
        {Object.entries(sections).map(([section, items], index) => (
          <div key={section} className={cn(index > 0 && "mt-2 border-t border-rule pt-2")}>
            {items.map((item) => {
              const Icon = item.icon;
              const active =
                location.pathname === item.path ||
                location.pathname.startsWith(item.path + "/");
              return (
                <NavLink
                  key={item.path}
                  to={item.path}
                  title={collapsed ? t(item.label) : undefined}
                  aria-current={active ? "page" : undefined}
                  className={cn(
                    "relative flex items-center gap-2.5 py-1.5 text-sm transition-colors",
                    collapsed ? "justify-center px-0" : "px-3",
                    active
                      ? "bg-signal-wash/70 font-medium text-signal-deep"
                      : "text-ink-soft hover:bg-paper-sunk hover:text-ink",
                  )}
                >
                  {active && (
                    <span
                      aria-hidden
                      className="absolute inset-y-0 left-0 w-0.5 bg-signal"
                    />
                  )}
                  <Icon size={15} className="shrink-0" />
                  {!collapsed && <span className="truncate">{t(item.label)}</span>}
                </NavLink>
              );
            })}
          </div>
        ))}
      </div>

      <div
        className={cn(
          "shrink-0 border-t border-rule p-2",
          collapsed ? "flex flex-col items-center gap-1" : "flex items-center justify-between gap-2",
        )}
      >
        <LanguageSwitcher collapsed={collapsed} />
        <Button
          variant="ghost"
          size="icon"
          onClick={logout}
          aria-label={t("退出登录")}
          title={t("退出登录")}
        >
          <LogOut size={15} />
        </Button>
      </div>
    </nav>
  );

  return (
    <div className="flex h-dvh overflow-hidden bg-paper">
      <div className="hidden lg:flex">{rail}</div>

      {mobileNavOpen && (
        <div className="fixed inset-0 z-50 flex lg:hidden">
          <div className="w-56">{rail}</div>
          <button
            type="button"
            aria-label={t("关闭导航")}
            className="flex-1 bg-ink/25"
            onClick={() => setMobileNavOpen(false)}
          />
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-12 shrink-0 items-center gap-3 border-b border-rule bg-paper-raised px-3 lg:px-4">
          <Button
            variant="ghost"
            size="icon"
            className="lg:hidden"
            onClick={() => setMobileNavOpen((open) => !open)}
            aria-label={t("打开导航")}
            aria-expanded={mobileNavOpen}
          >
            {mobileNavOpen ? <X size={16} /> : <Menu size={16} />}
          </Button>

          <div className="flex min-w-0 items-baseline gap-2">
            <span className="text-sm font-medium text-ink">
              {t(current?.label ?? "工作区")}
            </span>
            {current && current.path !== "/dashboard" && (
              <span className="hidden truncate text-xs text-ink-faint sm:inline">
                {t(current.section)}
              </span>
            )}
          </div>

          <div className="ml-auto flex items-center gap-2">
            <QuickSearch />

            {unauthorized ? (
              <Badge tone="alert" dot>
                {t("令牌失效")}
              </Badge>
            ) : disconnected ? (
              <Badge tone="warn" dot pulse>
                {t("连接中断")}
              </Badge>
            ) : weakTokens > 0 ? (
              <Badge tone="warn" title={t("部分令牌未设置")}>
                {t("令牌未设置")}
              </Badge>
            ) : info.data ? (
              <Badge tone="signal" dot title={`${info.data.version ?? ""}`}>
                {t("实例在线")}
              </Badge>
            ) : (
              <Badge tone="neutral" dot pulse>
                {t("连接中")}
              </Badge>
            )}

            <Button
              variant="ghost"
              size="icon"
              onClick={() => queryClient.invalidateQueries()}
              aria-label={t("刷新数据")}
              title={t("刷新数据")}
            >
              <RefreshCw size={15} className={cn(info.isFetching && "animate-spin")} />
            </Button>
          </div>
        </header>

        <main className="min-h-0 flex-1 overflow-y-auto">
          {token ? (
            <Outlet />
          ) : (
            <div className="p-6 text-sm text-ink-soft">{t("需要管理员令牌")}</div>
          )}
        </main>
      </div>
    </div>
  );
}