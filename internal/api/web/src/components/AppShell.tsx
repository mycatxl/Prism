import { useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useEffect, useState, useSyncExternalStore } from "react";
import { Activity, ArrowUpRight, Boxes, Cable, ChartNoAxesColumn, ChevronDown, ClipboardList, Coins, FileClock, KeyRound, ListChecks, LogOut, Menu as MenuIcon, Network, PackageCheck, PanelLeftClose, PanelLeftOpen, ReceiptText, Search, ServerCog, Settings2, ShieldCheck, Tags, UsersRound, WalletCards, Waypoints, type LucideIcon } from "lucide-react";
import { Dropdown as HeroDropdown } from "@heroui/react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { ApiError, logout } from "../lib/api-client";
import { cn } from "../lib/cn";
import { PrismMark } from "./PrismMark";
import { navigationFor, type NavigationItem } from "../lib/navigation";
import { ACCOUNT, OPS_ROOT, isCurrentPath, isExactPath, isWithinNamespace } from "../lib/paths";
import { isAdminRole } from "../lib/roles";
import { useAuthStore, clearSession } from "../features/auth/auth-store";
import { getEnvConfig, SYSTEM_ENV_CONFIG_QUERY_KEY, SYSTEM_ENV_STALE_MS } from "../features/systemConfig/api";
import { SYSTEM_INFO_QUERY_KEY, getSystemInfo } from "../features/systemInfo/api";
import { useI18n } from "../i18n";
import { LanguageSwitcher } from "./LanguageSwitcher";
import { ThemeSwitcher } from "./ThemeSwitcher";
import { ControlButton, InspectorDrawer, StatusMark } from "./control-plane";
import { Tooltip } from "./ui/Tooltip";

const CommandPalette = lazy(() => import("./CommandPalette").then((module) => ({ default: module.CommandPalette })));

function navIconFor(path: string): LucideIcon {
  if (path === OPS_ROOT) return Activity;
  if (path === ACCOUNT) return ShieldCheck;
  if (path.includes("nodes")) return Boxes;
  if (path.includes("subscriptions")) return Cable;
  if (path.includes("jobs")) return ListChecks;
  if (path.includes("platforms")) return Network;
  if (path.includes("endpoints")) return ServerCog;
  if (path.includes("rules")) return Settings2;
  if (path.includes("request-logs")) return FileClock;
  if (path.includes("users")) return UsersRound;
  if (path.includes("plans")) return PackageCheck;
  if (path.includes("invites")) return Tags;
  if (path.includes("redeem-codes")) return Coins;
  if (path.includes("usage")) return ChartNoAxesColumn;
  if (path.includes("config") || path.includes("settings")) return Settings2;
  if (path.includes("resources")) return WalletCards;
  if (path.includes("audit")) return ReceiptText;
  if (path.includes("keys")) return KeyRound;
  if (path.includes("leases")) return Waypoints;
  return ClipboardList;
}

function subscribeOnline(callback: () => void) {
  window.addEventListener("online", callback);
  window.addEventListener("offline", callback);
  return () => {
    window.removeEventListener("online", callback);
    window.removeEventListener("offline", callback);
  };
}

function NavigationLinks({
  items,
  pathname,
  t,
  mobile = false,
  collapsed = false,
  onNavigate,
}: {
  items: NavigationItem[];
  pathname: string;
  t: (text: string) => string;
  mobile?: boolean;
  collapsed?: boolean;
  onNavigate?: () => void;
}) {
  return (
    <div className={cn("workspace-nav__groups", mobile && "workspace-nav__groups--mobile")}>
      {items.map((item, index) => {
        const active = isCurrentPath(pathname, item.path);
        const opensGroup = index === 0 || item.section !== items[index - 1]?.section;
        const Icon = navIconFor(item.path);
        return (
          <div key={item.path} className="workspace-nav__group">
            {opensGroup && (!collapsed || mobile) ? (
              <span className="workspace-nav__section">{item.section ? t(item.section) : t("工作台")}</span>
            ) : null}
            <NavLink
              to={item.path}
              end={isExactPath(item.path)}
              aria-current={active ? "page" : undefined}
              onClick={onNavigate}
              title={!mobile && collapsed ? t(item.label) : undefined}
              className={cn("workspace-nav__link", active && "is-active")}
            >
              <Icon size={mobile ? 17 : 18} aria-hidden />
              <span className="workspace-nav__link-label">{t(item.label)}</span>
              {mobile && active ? <ArrowUpRight size={14} aria-hidden /> : null}
            </NavLink>
          </div>
        );
      })}
    </div>
  );
}

function AccountMenu({
  username,
  isAdmin,
  t,
  onAccount,
  onLogout,
}: {
  username: string;
  isAdmin: boolean;
  t: (text: string) => string;
  onAccount: () => void;
  onLogout: () => void;
}) {
  return (
    <HeroDropdown>
      <HeroDropdown.Trigger className="workspace-account-trigger" aria-label={t("账户菜单")}>
        <span className="workspace-account-trigger__avatar" aria-hidden>{(username[0] || "P").toUpperCase()}</span>
        <span className="workspace-account-trigger__copy">
          <strong>{username || t("账户")}</strong>
          <small>{isAdmin ? t("管理控制台") : t("用户中心")}</small>
        </span>
        <ChevronDown size={14} aria-hidden />
      </HeroDropdown.Trigger>
      <HeroDropdown.Popover placement="bottom end" className="workspace-account-menu">
        <HeroDropdown.Menu aria-label={t("账户菜单")} onAction={(key) => {
          if (String(key) === "account") onAccount();
          if (String(key) === "logout") onLogout();
        }}>
          <HeroDropdown.Item id="account">
            <ShieldCheck size={15} aria-hidden />
            <span>{t("账户安全")}</span>
          </HeroDropdown.Item>
          <HeroDropdown.Item id="logout">
            <LogOut size={15} aria-hidden />
            <span>{t("退出登录")}</span>
          </HeroDropdown.Item>
        </HeroDropdown.Menu>
      </HeroDropdown.Popover>
    </HeroDropdown>
  );
}

export function AppShell() {
  const { t } = useI18n();
  const session = useAuthStore((state) => state.session);
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const online = useSyncExternalStore(subscribeOnline, () => navigator.onLine, () => true);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [navCollapsed, setNavCollapsed] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [paletteReady, setPaletteReady] = useState(false);

  const isAdmin = isAdminRole(session.user?.role);
  const operatorSurface = isAdmin && isWithinNamespace(location.pathname, OPS_ROOT);
  const { data: adminItems } = useQuery({
    queryKey: ["navigation-admin"],
    queryFn: () => import("../lib/navigation-admin").then((module) => module.adminItems),
    enabled: isAdmin,
    staleTime: Infinity,
  });
  const navItems = navigationFor(session.user?.role, adminItems);
  const currentNavigationItem = navItems.find((item) => isCurrentPath(location.pathname, item.path));
  const info = useQuery({
    queryKey: SYSTEM_INFO_QUERY_KEY,
    queryFn: getSystemInfo,
    refetchInterval: 30_000,
    retry: false,
    enabled: operatorSurface,
  });
  const env = useQuery({
    queryKey: SYSTEM_ENV_CONFIG_QUERY_KEY,
    queryFn: getEnvConfig,
    staleTime: SYSTEM_ENV_STALE_MS,
    enabled: operatorSurface,
  });
  const unauthorized = info.error instanceof ApiError && info.error.status === 401;
  const disconnected = !online || (operatorSurface && info.isError);
  const weakTokens = operatorSurface && env.data && !env.data.proxy_token_set;
  const status = !operatorSurface
    ? online
      ? { tone: "live" as const, label: t("服务已连接") }
      : { tone: "warn" as const, label: t("当前处于离线状态") }
    : unauthorized
      ? { tone: "alert" as const, label: t("会话失效") }
      : disconnected
        ? { tone: "warn" as const, label: t("连接中断") }
        : weakTokens
          ? { tone: "warn" as const, label: t("令牌配置需要检查") }
          : info.data
            ? { tone: "live" as const, label: t("实例在线") }
            : { tone: "neutral" as const, label: t("连接中") };
  const version = (() => {
    const raw = (info.data?.version ?? "").trim().replace(/^v/, "");
    return raw ? (/^\d/.test(raw) ? `v${raw}` : raw) : "";
  })();
  const username = session.user?.username ?? "";
  const workspaceLabel = isAdmin ? t("运营控制平面") : t("租户自助工作台");

  useEffect(() => {
    setMobileNavOpen(false);
    document.querySelector(".workspace-main")?.scrollTo({ top: 0, behavior: "instant" });
  }, [location.pathname]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setMobileNavOpen(false);
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

  return (
    <div className={cn("workspace-shell", isAdmin ? "workspace-shell--ops" : "workspace-shell--me", navCollapsed && "is-nav-collapsed")} data-tier={isAdmin ? "ops" : "me"}>
      <a className="workspace-skip" href="#workspace-main">{t("跳到正文")}</a>
      <aside className={cn("workspace-nav", navCollapsed && "is-collapsed")}>
        <div className="workspace-nav__brand">
          <PrismMark size={26} variant="onDark" />
          <div className="workspace-nav__wordmark">
            <strong>Prism</strong>
            <span>{workspaceLabel}</span>
          </div>
          <div className="workspace-nav__brand-tools">
            <Tooltip content={navCollapsed ? t("展开侧边导航") : t("收起侧边导航")}>
              <ControlButton
                variant="ghost"
                size="icon"
                className="workspace-sidebar-toggle"
                onPress={() => setNavCollapsed((collapsed) => !collapsed)}
                aria-label={navCollapsed ? t("展开侧边导航") : t("收起侧边导航")}
                aria-expanded={!navCollapsed}
              >
                {navCollapsed ? <PanelLeftOpen size={18} aria-hidden /> : <PanelLeftClose size={18} aria-hidden />}
              </ControlButton>
            </Tooltip>
          </div>
        </div>
        <nav className="workspace-nav__scroll" aria-label={t("主导航")}>
          <NavigationLinks items={navItems} pathname={location.pathname} t={t} collapsed={navCollapsed} />
        </nav>
        <div className="workspace-nav__footer">
          <StatusMark tone={status.tone} label={status.label} />
          <span className="workspace-version">{version || (online ? "ready" : "offline")}</span>
        </div>
      </aside>

      <div className="workspace-workarea">
        <header className="workspace-topbar">
          <div className="workspace-topbar__lead">
            <ControlButton
              variant="ghost"
              size="icon"
              className="workspace-mobile-menu"
              onPress={() => setMobileNavOpen(true)}
              aria-label={t("打开导航")}
              aria-expanded={mobileNavOpen}
            >
              <MenuIcon size={18} aria-hidden />
            </ControlButton>
            <div className="workspace-topbar__trail" aria-label={t("当前位置")}>
              <span className="workspace-topbar__trail-root">Prism</span>
              <span className="workspace-topbar__trail-separator" aria-hidden>/</span>
              <strong>{currentNavigationItem ? t(currentNavigationItem.label) : workspaceLabel}</strong>
            </div>
            <ControlButton
              variant="ghost"
              size="sm"
              className="workspace-search"
              onPress={() => { setPaletteReady(true); setPaletteOpen(true); }}
              aria-keyshortcuts="Meta+K Control+K"
              aria-haspopup="dialog"
            >
              <Search size={15} aria-hidden />
              <span>{t("快速定位")}</span>
              <kbd>⌘K</kbd>
            </ControlButton>
          </div>
          <div className="workspace-topbar__tools">
            <div className="workspace-topbar__connection">
              <StatusMark tone={status.tone} label={status.label} />
              {version ? <span className="workspace-version">{version}</span> : null}
            </div>
            <LanguageSwitcher collapsed className="workspace-topbar__icon-button" />
            <ThemeSwitcher className="workspace-topbar__icon-button" />
            <AccountMenu
              username={username}
              isAdmin={isAdmin}
              t={t}
              onAccount={() => navigate(ACCOUNT)}
              onLogout={() => void handleLogout()}
            />
          </div>
        </header>

        <main id="workspace-main" className="workspace-main" tabIndex={-1}>
          {session.authenticated || !session.auth_required ? <Outlet /> : <div className="workspace-auth-required">{t("需要登录")}</div>}
        </main>
      </div>

      <InspectorDrawer open={mobileNavOpen} onOpenChange={setMobileNavOpen} title={workspaceLabel} eyebrow="Prism" placement="left">
        <nav className="workspace-mobile-nav" aria-label={t("主导航")}>
          <NavigationLinks items={navItems} pathname={location.pathname} t={t} mobile onNavigate={() => setMobileNavOpen(false)} />
          <div className="workspace-mobile-nav__footer">
            <StatusMark tone={status.tone} label={status.label} />
            <span className="workspace-version">{version || (online ? "ready" : "offline")}</span>
            <div className="workspace-mobile-nav__preferences"><LanguageSwitcher /><ThemeSwitcher /></div>
          </div>
        </nav>
      </InspectorDrawer>

      {paletteReady ? (
        <Suspense fallback={null}>
          <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} items={navItems} onSignOut={() => void handleLogout()} />
        </Suspense>
      ) : null}
    </div>
  );
}
