import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useState, useSyncExternalStore } from "react";
import { LogOut, Menu, PanelLeftClose, PanelLeftOpen, X } from "lucide-react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { ApiError } from "../lib/api-client";
import { cn } from "../lib/cn";
import { navigation } from "../lib/navigation";
import { useAuthStore } from "../features/auth/auth-store";
import { getEnvConfig } from "../features/systemConfig/api";
import { SYSTEM_INFO_QUERY_KEY, getSystemInfo } from "../features/systemInfo/api";
import { useI18n } from "../i18n";
import { LanguageSwitcher } from "./LanguageSwitcher";
import { QuickSearch } from "./QuickSearch";
import { ThemeSwitcher } from "./ThemeSwitcher";
import { Badge } from "./ui/Badge";
import { Button } from "./ui/Button";
import { Tooltip, TooltipProvider } from "./ui/Tooltip";

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
 * The application frame: a translucent glass rail (248px expanded / 64px collapsed)
 * and a 56px sticky glass top bar over the deep-navy glowing canvas.
 */
export function AppShell() {
  const { t } = useI18n();
  const token = useAuthStore((state) => state.token);
  const clearToken = useAuthStore((state) => state.clearToken);
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [railCollapsed, setRailCollapsed] = useState(
    () => window.localStorage.getItem(RAIL_COLLAPSED_KEY) === "1",
  );
  const [narrow, setNarrow] = useState(
    () => window.matchMedia("(max-width: 1439px)").matches,
  );
  useEffect(() => {
    const query = window.matchMedia("(max-width: 1439px)");
    const onChange = (event: MediaQueryListEvent) => setNarrow(event.matches);
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);
  const collapsed = narrow || railCollapsed;
  const online = useSyncExternalStore(
    subscribeOnline,
    () => navigator.onLine,
    () => true,
  );

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
    setRailCollapsed((previous) => {
      const next = !previous;
      window.localStorage.setItem(RAIL_COLLAPSED_KEY, next ? "1" : "0");
      return next;
    });
  }, []);

  useEffect(() => {
    setMobileNavOpen(false);
  }, [location.pathname]);

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

  const statusBadge = unauthorized ? (
    <Badge tone="alert" dot>
      {t("令牌失效")}
    </Badge>
  ) : disconnected ? (
    <Badge tone="warn" dot>
      {t("连接中断")}
    </Badge>
  ) : weakTokens > 0 ? (
    <Badge tone="warn" title={t("部分令牌未设置")}>
      {t("令牌未设置")}
    </Badge>
  ) : info.data ? (
    <Badge tone="signal" dot title={`${info.data.version ?? ""}`}>
      {t("实例在线")}
      <span className="readout ml-1 font-normal opacity-80">
        {info.data.version ?? ""}
      </span>
    </Badge>
  ) : (
    <Badge tone="neutral" dot>
      {t("连接中")}
    </Badge>
  );

  const statusText = unauthorized
    ? t("令牌失效")
    : disconnected
      ? t("连接中断")
      : info.data
        ? t("实例在线")
        : t("连接中");

  /*
   * One rail for every page. The board used to carry its own replica of the
   * reference rail above 1536px, with its geometry in a breakpoint layer in
   * `design.css`; the layer is gone and the board now reads the same shell as
   * every other route, so the replica is gone with it.
   */
  const rail = (
    <TooltipProvider delayDuration={180}>
      <nav
        aria-label={t("主导航")}
        className={cn(
          "glass-rail flex h-full flex-col border-r border-glass-edge",
          collapsed ? "w-[var(--shell-rail-w-collapsed)]" : "w-[var(--shell-rail-w)]",
        )}
      >
        <div className="flex h-full flex-col">
          <div
            className={cn(
              "flex h-[var(--shell-bar-h)] shrink-0 items-center",
              collapsed ? "justify-center px-2" : "gap-2.5 px-3.5",
            )}
          >
            <img
              src={`${import.meta.env.BASE_URL}prism-mark.png`}
              alt=""
              aria-hidden
              className="size-8 shrink-0 object-contain"
            />
            {!collapsed && (
              <>
                <div className="min-w-0 flex-1">
                  <span className="block truncate text-lg font-bold tracking-tight text-ink">
                    Prism
                  </span>
                  <span className="block truncate text-2xs text-ink-faint">
                    {info.data?.version ? `v${info.data.version.replace(/^v/, "")}` : t("控制台")}
                  </span>
                </div>
                {!narrow && (
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={toggleRail}
                    className="hidden shrink-0 lg:inline-flex"
                    aria-label={t("收起导航")}
                  >
                    <PanelLeftClose size={15} />
                  </Button>
                )}
              </>
            )}
          </div>

          {collapsed && !narrow && (
            <Button
              variant="ghost"
              size="icon"
              onClick={toggleRail}
              className="mx-auto mt-2.5 hidden shrink-0 lg:inline-flex"
              aria-label={t("展开导航")}
            >
              <PanelLeftOpen size={15} />
            </Button>
          )}

          <div className="min-h-0 flex-1 overflow-y-auto py-3">
            {Object.entries(sections).map(([section, items], index) => (
              <div key={section} className={cn(index > 0 && "mt-4")}>
                {collapsed ? (
                  index > 0 && <div className="mx-3 mb-2.5 border-t border-rule-faint" />
                ) : (
                  <div className="micro px-6 pb-1.5">{t(section)}</div>
                )}
                {items.map((item) => {
                  const Icon = item.icon;
                  const active =
                    location.pathname === item.path ||
                    location.pathname.startsWith(item.path + "/");
                  const link = (
                    <NavLink
                      key={item.path}
                      to={item.path}
                      title={collapsed ? t(item.label) : undefined}
                      aria-current={active ? "page" : undefined}
                      className={cn(
                        "rail-link action mx-3 my-1 flex h-[var(--control-h-xl)] items-center gap-3 rounded-card-sm text-sm",
                        collapsed ? "justify-center px-0" : "px-3",
                        active
                          ? "bg-accent-wash font-semibold text-accent-deep"
                          : "text-ink-soft hover:bg-paper-sunk hover:text-ink",
                      )}
                    >
                      <Icon size={17} className="shrink-0" />
                      {!collapsed && <span className="truncate">{t(item.label)}</span>}
                    </NavLink>
                  );
                  return collapsed ? (
                    <Tooltip key={item.path} content={t(item.label)} side="right">
                      {link}
                    </Tooltip>
                  ) : (
                    link
                  );
                })}
              </div>
            ))}
          </div>

          <div
            className={cn(
              "shrink-0 p-3",
              collapsed ? "flex flex-col items-center gap-1.5" : "flex flex-col gap-2",
            )}
          >
            {!collapsed && (
              <div className="rail-status flex items-center gap-2.5 rounded-card-sm border border-rule-faint bg-paper-inset px-3 py-2.5">
                <span
                  aria-hidden
                  className={cn(
                    "rail-status__dot size-2.5 shrink-0",
                    unauthorized ? "bg-alert" : disconnected ? "bg-warn" : info.data ? "bg-signal" : "bg-rule-strong",
                  )}
                />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-xs font-semibold text-ink">{statusText}</span>
                  {info.data?.version && (
                    <span className="readout block truncate text-2xs text-ink-faint">
                      {info.data.version}
                    </span>
                  )}
                </span>
              </div>
            )}
            <div
              className={cn(
                "flex items-center sm:hidden",
                collapsed ? "flex-col gap-1.5" : "justify-between gap-2",
              )}
            >
              <div className={cn("flex items-center gap-1", collapsed && "flex-col gap-1")}>
                <LanguageSwitcher collapsed={collapsed} />
                <ThemeSwitcher collapsed={collapsed} />
              </div>
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
          </div>
        </div>
      </nav>
    </TooltipProvider>
  );

  return (
    <div className="wb-shell-root flex h-dvh overflow-hidden bg-transparent text-ink">
      <div className="hidden min-[1024px]:flex">{rail}</div>

      {mobileNavOpen && (
        <div className="fixed inset-0 z-50 flex min-[1024px]:hidden">
          <div className="w-[var(--shell-rail-w)]">{rail}</div>
          <button
            type="button"
            aria-label={t("关闭导航")}
            className="flex-1 bg-ink/30"
            onClick={() => setMobileNavOpen(false)}
          />
        </div>
      )}

      <div className="wb-main-zone flex min-w-0 flex-1 flex-col">
        <header className="glass-bar sticky top-0 z-20 flex h-[var(--shell-bar-h)] shrink-0 items-center gap-3 border-b border-glass-edge px-4 lg:px-6">
          <Button
            variant="ghost"
            size="icon"
            className="min-[1024px]:hidden"
            onClick={() => setMobileNavOpen((open) => !open)}
            aria-label={t("打开导航")}
            aria-expanded={mobileNavOpen}
          >
            {mobileNavOpen ? <X size={16} /> : <Menu size={16} />}
          </Button>


          <div className="flex min-w-0 items-baseline gap-2 sm:hidden">
            <span className="truncate text-sm font-semibold text-ink">
              {t(current?.label ?? "工作区")}
            </span>
          </div>

          <QuickSearch />

          <div className="wb-topbar-actions ml-auto flex items-center gap-2">
            {statusBadge}


            <div className="hidden items-center gap-1.5 sm:flex">
              <ThemeSwitcher />
              <LanguageSwitcher />
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
          </div>
        </header>

        <main className="wb-main-scroll min-h-0 flex-1 overflow-y-auto">
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
