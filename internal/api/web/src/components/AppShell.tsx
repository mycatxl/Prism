import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, Suspense, lazy, useEffect, useState, useSyncExternalStore } from "react";
import { Activity, Boxes, Cable, ChartNoAxesColumn, ChevronDown, ClipboardList, Coins, FileClock, KeyRound, ListChecks, LogOut, Menu as MenuIcon, Network, PackageCheck, ReceiptText, Search, ServerCog, Settings2, ShieldCheck, Tags, UserRound, UsersRound, WalletCards, Waypoints, type LucideIcon } from "lucide-react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { ApiError, logout } from "../lib/api-client";
import { cn } from "../lib/cn";
import { navigationFor, type NavigationItem } from "../lib/navigation";
import { ACCOUNT, OPS_ROOT, isCurrentPath, isExactPath, isWithinNamespace } from "../lib/paths";
import { isAdminRole } from "../lib/roles";
import { useAuthStore, clearSession } from "../features/auth/auth-store";
import { getEnvConfig, SYSTEM_ENV_CONFIG_QUERY_KEY, SYSTEM_ENV_STALE_MS } from "../features/systemConfig/api";
import { SYSTEM_INFO_QUERY_KEY, getSystemInfo } from "../features/systemInfo/api";
import { useI18n } from "../i18n";
import { LanguageSwitcher } from "./LanguageSwitcher";
import { ThemeSwitcher } from "./ThemeSwitcher";
import { CommandSurface, ControlButton, InspectorDrawer, StatusMark } from "./control-plane";

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

function RailContent({
  items,
  pathname,
  isAdmin,
  t,
  onNavigate,
}: {
  items: NavigationItem[];
  pathname: string;
  isAdmin: boolean;
  t: (text: string) => string;
  onNavigate?: () => void;
}) {
  return (
    <>
      <div className="cp-rail__brand">
        <img src={`${import.meta.env.BASE_URL}prism-mark.png`} alt="" aria-hidden className="cp-rail__mark" />
        <div className="cp-rail__brand-copy">
          <strong>Prism</strong>
          <span>{isAdmin ? t("运营控制平面") : t("租户自助工作台")}</span>
        </div>
      </div>
      <div className="cp-rail__scope">
        <span className="cp-rail__scope-mark" aria-hidden />
        <span>{isAdmin ? t("实例级操作") : t("账户级操作")}</span>
      </div>
      <div className="cp-rail__scroll">
        {items.map((item, index) => {
          const active = isCurrentPath(pathname, item.path);
          const opensGroup = Boolean(item.section) && items[index - 1]?.section !== item.section;
          const Icon = navIconFor(item.path);
          return (
            <Fragment key={item.path}>
              {opensGroup ? <div className="cp-rail__section">{t(item.section)}</div> : null}
              <NavLink
                to={item.path}
                end={isExactPath(item.path)}
                aria-current={active ? "page" : undefined}
                onClick={onNavigate}
                className={cn("cp-rail__link", active && "is-active")}
              >
                <span className="cp-rail__icon" aria-hidden><Icon size={15} /></span>
                <span className="cp-rail__label">{t(item.label)}</span>
                {active ? <span className="cp-rail__active-line" aria-hidden /> : null}
              </NavLink>
            </Fragment>
          );
        })}
      </div>
    </>
  );
}

export function AppShell() {
  const { t } = useI18n();
  const session = useAuthStore((state) => state.session);
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const online = useSyncExternalStore(subscribeOnline, () => navigator.onLine, () => true);
  const [railOpen, setRailOpen] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [paletteReady, setPaletteReady] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);

  const isAdmin = isAdminRole(session.user?.role);
  const operatorSurface = isAdmin && isWithinNamespace(location.pathname, OPS_ROOT);
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
  const env = useQuery({
    queryKey: SYSTEM_ENV_CONFIG_QUERY_KEY,
    queryFn: getEnvConfig,
    staleTime: SYSTEM_ENV_STALE_MS,
    enabled: operatorSurface,
  });
  const unauthorized = info.error instanceof ApiError && info.error.status === 401;
  const disconnected = !online || (operatorSurface && info.isError);
  const weakTokens = operatorSurface && env.data && !env.data.proxy_token_set;

  useEffect(() => {
    setRailOpen(false);
    setAccountOpen(false);
    document.querySelector(".cp-main")?.scrollTo({ top: 0, behavior: "instant" });
  }, [location.pathname]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setRailOpen(false);
        setAccountOpen(false);
      }
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
  const current = navItems
    .filter((item) => isCurrentPath(location.pathname, item.path))
    .sort((a, b) => b.path.length - a.path.length)[0];
  const username = session.user?.username ?? "";
  const railTitle = isAdmin ? t("运营控制平面") : t("租户自助工作台");

  return (
    <div className={cn("cp-shell", isAdmin ? "cp-shell--ops" : "cp-shell--me")} data-tier={isAdmin ? "ops" : "me"}>
      <a className="cp-skip" href="#cp-main">{t("跳到正文")}</a>
      <nav className="cp-rail" aria-label={t("主导航")}>
        <RailContent items={navItems} pathname={location.pathname} isAdmin={isAdmin} t={t} />
        <div className="cp-rail__foot">
          <StatusMark tone={status.tone} label={status.label} />
          <span className="cp-rail__version">{version || (online ? "ready" : "offline")}</span>
          <div className="cp-rail__prefs"><LanguageSwitcher collapsed /><ThemeSwitcher /></div>
        </div>
      </nav>

      <InspectorDrawer open={railOpen} onOpenChange={setRailOpen} title={railTitle} eyebrow="Prism" placement="left">
        <nav className="cp-mobile-rail" aria-label={t("主导航")}>
          <RailContent items={navItems} pathname={location.pathname} isAdmin={isAdmin} t={t} onNavigate={() => setRailOpen(false)} />
        </nav>
      </InspectorDrawer>

      <div className="cp-shell__column">
        <header className="cp-context">
          <div className="cp-context__left">
            <ControlButton
              variant="ghost"
              size="sm"
              isIconOnly
              className="cp-context__menu"
              onPress={() => setRailOpen(true)}
              aria-label={t("打开导航")}
              aria-expanded={railOpen}
            >
              <MenuIcon size={17} aria-hidden />
            </ControlButton>
            <div className="cp-context__crumb" aria-label={t("当前位置")}>
              <span className="cp-context__surface">{isAdmin ? t("Control Plane") : t("Self Service")}</span>
              <span aria-hidden className="cp-context__slash">/</span>
              <strong>{current ? t(current.label) : "Prism"}</strong>
            </div>
          </div>
          <div className="cp-context__right">
            <div className="cp-context__signal"><StatusMark tone={status.tone} label={status.label} />{version ? <span className="cp-context__version">{version}</span> : null}</div>
            <ControlButton
              variant="ghost"
              size="sm"
              className="cp-context__search"
              onPress={() => { setPaletteReady(true); setPaletteOpen(true); }}
              aria-keyshortcuts="Meta+K Control+K"
              aria-haspopup="dialog"
            >
              <Search size={15} aria-hidden />
              <span>{t("快速定位")}</span>
              <kbd>⌘K</kbd>
            </ControlButton>
            <div className="cp-account-wrap">
              <ControlButton
                variant="ghost"
                size="sm"
                className="cp-account-button"
                onPress={() => setAccountOpen((open) => !open)}
                aria-expanded={accountOpen}
                aria-haspopup="menu"
              >
                <UserRound size={15} aria-hidden />
                <span>{username}</span>
                <ChevronDown size={13} aria-hidden />
              </ControlButton>
              {accountOpen ? (
                <div className="cp-account-menu" aria-label={t("账户菜单")}>
                  <div className="cp-account-menu__identity"><strong>{username}</strong><span>{isAdmin ? t("管理控制台") : t("用户中心")}</span></div>
                  <ControlButton variant="ghost" size="sm" className="cp-account-menu__item" onPress={() => { setAccountOpen(false); navigate(ACCOUNT); }}><ShieldCheck size={14} aria-hidden />{t("账户安全")}</ControlButton>
                  <ControlButton variant="ghost" size="sm" className="cp-account-menu__item" onPress={() => void handleLogout()}><LogOut size={14} aria-hidden />{t("退出登录")}</ControlButton>
                </div>
              ) : null}
            </div>
          </div>
        </header>
        <div className="cp-context__statusbar">
          <CommandSurface>
            <span className="cp-context__descriptor">{operatorSurface ? t("实例工作区") : t("账户工作区")}</span>
            <span>{t("数据、关系和动作在同一工作面内展开")}</span>
          </CommandSurface>
        </div>
        <main id="cp-main" className="cp-main" tabIndex={-1}>
          {session.authenticated || !session.auth_required ? <Outlet /> : <div className="cp-auth-required">{t("需要登录")}</div>}
        </main>
      </div>

      {paletteReady ? (
        <Suspense fallback={null}>
          <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} items={navItems} onSignOut={() => void handleLogout()} />
        </Suspense>
      ) : null}
    </div>
  );
}
