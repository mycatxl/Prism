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
import { ThemeSwitcher } from "./ThemeSwitcher";
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
 * The application frame: a rail of destinations and a bar that answers "where am I"
 * and "is this instance healthy".
 *
 * Geometry is fixed by tokens (rail 232 expanded / 56 collapsed, bar 48) rather than
 * chosen per screen, because the whole point of a console is that the frame never
 * moves. 232 is what Vben, Tabler and shadcn-admin converge on; 56 is the width at
 * which a 15px icon still has a 36px hit target.
 *
 * The rail sits on its own neutral layer (`--color-rail`) so the frame and the sheet
 * the data is read on never blur into one field.
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
  // A 232px rail on a laptop is a fifth of the screen. Below 1440 the rail folds to
  // its icon width on its own, so navigation stays reachable and the data gets the
  // room; above that the operator's own preference applies. Nobody's console is one
  // device, so this is a width the layout reacts to rather than a reference viewport.
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
    setRailCollapsed((previous) => {
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
        "flex h-full flex-col border-r border-rule bg-rail shadow-[inset_-1px_0_0_0_var(--color-rule)]",
        collapsed ? "w-[var(--shell-rail-w-collapsed)]" : "w-[var(--shell-rail-w)]",
      )}
    >
      <div
        className={cn(
          "flex h-[var(--shell-bar-h)] shrink-0 items-center border-b border-rule",
          collapsed ? "justify-center px-2" : "gap-2.5 px-3",
        )}
      >
        <span
          aria-hidden
          className="grid size-7 shrink-0 place-items-center rounded-[6px] bg-ink text-2xs font-semibold tracking-tight text-paper"
        >
          P
        </span>
        {!collapsed && (
          <>
            <span className="min-w-0 flex-1 truncate text-sm font-semibold tracking-tight">
              Prism
            </span>
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
          className="mx-auto mt-2 hidden shrink-0 lg:inline-flex"
          aria-label={t("展开导航")}
        >
          <PanelLeftOpen size={15} />
        </Button>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto py-2">
        {Object.entries(sections).map(([section, items], index) => (
          <div key={section} className={cn(index > 0 && "mt-3")}>
            {/* A rail group label, not an eyebrow over a heading: it names the set
                of destinations under it, which is the one job micro-caps have. */}
            {collapsed ? (
              index > 0 && <div className="mx-2.5 mb-2 border-t border-rule" />
            ) : (
              <div className="micro px-3 pb-1.5">{t(section)}</div>
            )}
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
                    // The current destination is a filled pill. A coloured edge
                    // stripe on a list row is the loudest generic-UI tell there is,
                    // and it says nothing the fill does not already say.
                    "mx-2 flex h-[var(--row-h)] items-center gap-2.5 rounded-control text-sm transition-colors",
                    collapsed ? "justify-center px-0" : "px-2.5",
                    active
                      ? "bg-paper-raised font-semibold text-accent-deep shadow-xs"
                      : "text-ink-soft hover:bg-paper-raised/70 hover:text-ink",
                  )}
                >
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
    </nav>
  );

  return (
    <div className="flex h-dvh overflow-hidden bg-paper">
      <div className="hidden lg:flex">{rail}</div>

      {mobileNavOpen && (
        <div className="fixed inset-0 z-50 flex lg:hidden">
          <div className="w-[var(--shell-rail-w)]">{rail}</div>
          <button
            type="button"
            aria-label={t("关闭导航")}
            className="flex-1 bg-ink/30"
            onClick={() => setMobileNavOpen(false)}
          />
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-[var(--shell-bar-h)] shrink-0 items-center gap-3 border-b border-rule bg-paper-raised px-3 lg:px-4">
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
            <span className="truncate text-sm font-semibold text-ink">
              {t(current?.label ?? "工作区")}
            </span>
            {current && (
              <span className="hidden truncate text-2xs tracking-[0.06em] text-ink-faint uppercase sm:inline">
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
                <span className="readout ml-1 font-normal opacity-70">
                  {info.data.version ?? ""}
                </span>
              </Badge>
            ) : (
              <Badge tone="neutral" dot>
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
