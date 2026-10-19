import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, Suspense, lazy, useEffect, useState, useSyncExternalStore } from "react";
import { ChevronDown, LogOut, Menu as MenuIcon, Search, ShieldCheck, UserRound, X } from "lucide-react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { ApiError, logout } from "../lib/api-client";
import { cn } from "../lib/cn";
import { navigationFor } from "../lib/navigation";
import { ACCOUNT, OPS_ROOT, isCurrentPath, isExactPath, isWithinNamespace } from "../lib/paths";
import { isAdminRole } from "../lib/roles";
import { useAuthStore, clearSession } from "../features/auth/auth-store";
import { getEnvConfig, SYSTEM_ENV_CONFIG_QUERY_KEY, SYSTEM_ENV_STALE_MS } from "../features/systemConfig/api";
import { SYSTEM_INFO_QUERY_KEY, getSystemInfo } from "../features/systemInfo/api";
import { useI18n } from "../i18n";
import { LanguageSwitcher } from "./LanguageSwitcher";
import { ThemeSwitcher } from "./ThemeSwitcher";
import { Button } from "./ui/Button";
import { Menu, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuTrigger } from "./ui/Menu";

/*
 * The palette is the one part of the shell that is loaded on demand: `cmdk` is
 * large, and an operator who never presses ⌘K should not pay for it on every
 * page. The shell keeps the trigger and the shortcut; the dialog arrives with
 * its own chunk the first time it is opened, and stays mounted afterwards.
 */
const CommandPalette = lazy(() => import("./CommandPalette").then((module) => ({ default: module.CommandPalette })));

function subscribeOnline(callback: () => void) {
  window.addEventListener("online", callback);
  window.addEventListener("offline", callback);
  return () => {
    window.removeEventListener("online", callback);
    window.removeEventListener("offline", callback);
  };
}

/**
 * The shell is three cells, not a sidebar with a header bolted on:
 *
 *   rail (brand, destinations, status foot)   instance bar
 *                                             work area
 *
 * The instance bar states where you are and what this browser can do from here;
 * the rail states the product and its state. Neither repeats the other's
 * controls, so there is exactly one place to look for a destination, one for the
 * account, and one for the instance's health.
 */
export function AppShell() {
  const { t } = useI18n();
  const session = useAuthStore((state) => state.session);
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const online = useSyncExternalStore(subscribeOnline, () => navigator.onLine, () => true);
  const [railOpen, setRailOpen] = useState(false);
  // `paletteReady` latches on the first open, so the chunk is requested once and
  // the dialog stays mounted after that.
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [paletteReady, setPaletteReady] = useState(false);

  // The console answers in two namespaces (src/lib/paths.ts): `/ops/*` is the
  // operator surface, `/me/*` and `/account` are the customer's. The instance
  // probes and the verdict they produce belong to the operator surface only —
  // they are refused to a tenant by the role gate, and an operator reading their
  // own account is not looking at the instance.
  const isAdmin = isAdminRole(session.user?.role);
  const operatorSurface = isAdmin && isWithinNamespace(location.pathname, OPS_ROOT);
  // The operator rail names management paths, so it is fetched rather than
  // bundled: a tenant's browser never holds them. Until it arrives the rail is
  // empty for an operator and shows the self-service set for a tenant.
  const { data: adminItems } = useQuery({
    queryKey: ["navigation-admin"],
    queryFn: () => import("../lib/navigation-admin").then((m) => m.adminItems),
    enabled: isAdmin,
    staleTime: Infinity,
  });
  const navItems = navigationFor(session.user?.role, adminItems);

  const info = useQuery({
    queryKey: SYSTEM_INFO_QUERY_KEY,
    queryFn: getSystemInfo,
    refetchInterval: 30_000,
    retry: false,
    enabled: operatorSurface,
  });
  // The shared env entry. It used to carry a "shell" suffix, which meant the
  // console fetched the same url for itself and could not be refreshed by a save
  // on another page; the reader here still refetches on an explicit invalidate,
  // which is the only way these boot-time values ever change.
  const env = useQuery({
    queryKey: SYSTEM_ENV_CONFIG_QUERY_KEY,
    queryFn: getEnvConfig,
    staleTime: SYSTEM_ENV_STALE_MS,
    enabled: operatorSurface,
  });

  const unauthorized = info.error instanceof ApiError && info.error.status === 401;
  const disconnected = !online || (operatorSurface && info.isError);
  const weakTokens = operatorSurface && env.data && !env.data.proxy_token_set;

  // A route change is a new page: focus its heading so a keyboard or screen
  // reader starts at the top of the document, not where the previous page left
  // the caret. The drawer closes on the same event, since it covers the page.
  useEffect(() => {
    setRailOpen(false);
    const heading = document.querySelector<HTMLElement>("main h1");
    heading?.setAttribute("tabindex", "-1");
    heading?.focus({ preventScroll: true });
    document.querySelector(".shell-main")?.scrollTo({ top: 0, behavior: "instant" });
  }, [location.pathname]);

  useEffect(() => {
    if (!railOpen) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setRailOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [railOpen]);

  /*
   * ⌘K / Ctrl+K anywhere in the app. The modifier is required, so it never
   * steals a `k` from a field the operator is typing in.
   */
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== "k") return;
      event.preventDefault();
      setPaletteReady(true);
      setPaletteOpen((open) => !open);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  const handleLogout = async () => {
    try {
      await logout();
    } catch {
      clearSession();
    }
    queryClient.clear();
    navigate("/login?reauth=1", { replace: true });
  };

  /*
   * The status is stated in words, always. Outside the operator namespace there
   * is no instance probe to read — a tenant is refused one, and `/me` and
   * `/account` are that customer's own pages — so the line reports what this
   * browser can know: whether it can reach the server. "Connecting…" shown
   * forever was the old behaviour, and an unknown status reading as a pending one
   * is exactly the failure this line exists to avoid.
   */
  const status = !operatorSurface
    ? online
      ? { text: t("服务已连接"), tone: "live" as const }
      : { text: t("当前处于离线状态"), tone: "warn" as const }
    : unauthorized
      ? { text: t("会话失效"), tone: "alert" as const }
      : disconnected
        ? { text: t("连接中断"), tone: "warn" as const }
        : weakTokens
          ? { text: t("旧版令牌配置仍存在"), tone: "warn" as const }
          : info.data
            ? { text: t("实例在线"), tone: "live" as const }
            : { text: t("连接中"), tone: "idle" as const };
  // A release reads `v0.3.0`; a source build reports `dev`, and `vdev` is not a
  // version — so the `v` is only added when a number follows it.
  const version = (() => {
    const raw = (info.data?.version ?? "").trim().replace(/^v/, "");
    if (!raw) return "";
    return /^\d/.test(raw) ? `v${raw}` : raw;
  })();

  // The rail's groups, numbered in the order they appear. The numeral is the
  // index an operator can quote, so it is derived from the same array the rail
  // renders rather than kept as a second list.
  const sections: string[] = [];
  for (const item of navItems) {
    if (item.section && !sections.includes(item.section)) sections.push(item.section);
  }
  const current = navItems
    .filter((item) => isCurrentPath(location.pathname, item.path))
    .sort((a, b) => b.path.length - a.path.length)[0];
  const username = session.user?.username ?? "";

  return (
    <div className={cn("shell", isAdmin ? "shell--desk" : "shell--self")}>
      <a className="shell-skip" href="#sheet">
        {t("跳到正文")}
      </a>

      <header className="shell-bar">
        <Button
          variant="ghost"
          size="icon"
          className="shell-bar__menu"
          onClick={() => setRailOpen(true)}
          aria-label={t("打开导航")}
          aria-expanded={railOpen}
          aria-controls="shell-nav"
        >
          <MenuIcon size={18} aria-hidden />
        </Button>

        <nav aria-label={t("当前位置")} className="shell-crumb">
          {current?.section && (
            <>
              <span className="shell-crumb__section">{t(current.section)}</span>
              <span className="shell-crumb__sep" aria-hidden>
                /
              </span>
            </>
          )}
          <span className="shell-crumb__page">{current ? t(current.label) : "Prism"}</span>
        </nav>

        <div className="shell-bar__tools">
          {/* The trigger is shell, the dialog is a chunk. `aria-keyshortcuts`
              advertises the shortcut the window listener above implements. */}
          <Button
            variant="ghost"
            size="md"
            className="gap-2"
            onClick={() => {
              setPaletteReady(true);
              setPaletteOpen(true);
            }}
            aria-keyshortcuts="Meta+K Control+K"
            aria-haspopup="dialog"
            aria-label={t("快速定位")}
          >
            <Search size={15} aria-hidden />
            <span className="shell-bar__searchlabel">{t("快速定位")}</span>
            <kbd className="shell-kbd" aria-hidden>
              ⌘K
            </kbd>
          </Button>

          <Menu>
            <MenuTrigger asChild>
              <Button variant="ghost" size="md" className="gap-1.5" aria-label={t("账户菜单")}>
                <UserRound size={15} aria-hidden />
                <span className="shell-account__name">{username}</span>
                <ChevronDown size={13} aria-hidden className="text-ink-faint" />
              </Button>
            </MenuTrigger>
            <MenuContent>
              <MenuLabel>
                <span className="block truncate font-medium text-ink">{username}</span>
                <span className="shell-account__role">{isAdmin ? t("管理控制台") : t("用户中心")}</span>
              </MenuLabel>
              <MenuSeparator />
              <MenuItem onSelect={() => navigate(ACCOUNT)}>
                <span>{t("账户安全")}</span>
                <ShieldCheck size={14} aria-hidden />
              </MenuItem>
              <MenuItem onSelect={() => void handleLogout()}>
                <span>{t("退出登录")}</span>
                <LogOut size={14} aria-hidden />
              </MenuItem>
            </MenuContent>
          </Menu>
        </div>
      </header>

      {railOpen && <div className="shell-scrim" aria-hidden onClick={() => setRailOpen(false)} />}

      <nav aria-label={t("主导航")} id="shell-nav" className={cn("shell-nav", railOpen && "is-open")}>
        <div className="shell-brand">
          <img src={`${import.meta.env.BASE_URL}prism-mark.png`} alt="" aria-hidden className="shell-brand__mark" />
          <span className="shell-brand__name">Prism</span>
          <span className="shell-brand__role">{isAdmin ? t("管理控制台") : t("用户中心")}</span>
          <Button
            variant="ghost"
            size="icon"
            className="shell-nav__close"
            onClick={() => setRailOpen(false)}
            aria-label={t("关闭导航")}
          >
            <X size={16} aria-hidden />
          </Button>
        </div>

        <div className="shell-nav__scroll">
          {navItems.map((item, index) => {
            const active = isCurrentPath(location.pathname, item.path);
            const opensGroup = Boolean(item.section) && navItems[index - 1]?.section !== item.section;
            const ordinal = String(sections.indexOf(item.section) + 1).padStart(2, "0");
            return (
              <Fragment key={item.path}>
                {opensGroup && (
                  <div className="shell-nav__section">
                    <span className="shell-nav__ord" aria-hidden>
                      {ordinal}
                    </span>
                    <span className="shell-nav__section-label">{t(item.section)}</span>
                  </div>
                )}
                <NavLink
                  to={item.path}
                  aria-current={active ? "page" : undefined}
                  // The router's own match writes `aria-current`; a namespace root
                  // is an exact address, so it must not claim every page beneath it.
                  end={isExactPath(item.path)}
                  className={cn("shell-nav__link", active && "is-active")}
                >
                  {t(item.label)}
                </NavLink>
              </Fragment>
            );
          })}
        </div>

        <div className="shell-nav__foot">
          <span className={cn("shell-status", `shell-status--${status.tone}`)} role="status">
            <span className="shell-status__dot" aria-hidden />
            <span className="shell-status__text">{status.text}</span>
            {version && <span className="shell-version readout">{version}</span>}
          </span>
          <div className="shell-nav__prefs">
            <LanguageSwitcher collapsed />
            <ThemeSwitcher />
          </div>
        </div>
      </nav>

      <main id="sheet" className="shell-main" tabIndex={-1}>
        {session.authenticated || !session.auth_required ? <Outlet /> : <div className="p-6 text-sm text-ink-soft">{t("需要登录")}</div>}
      </main>

      {paletteReady && (
        <Suspense fallback={null}>
          <CommandPalette
            open={paletteOpen}
            onOpenChange={setPaletteOpen}
            items={navItems}
            onSignOut={() => void handleLogout()}
          />
        </Suspense>
      )}
    </div>
  );
}
