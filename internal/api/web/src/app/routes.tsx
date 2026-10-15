import { lazy, Suspense, type ReactElement, type ReactNode } from "react";
import { Navigate, Outlet, Route, Routes, useLocation } from "react-router-dom";
import { AppShell } from "../components/AppShell";
import { NotFoundPage } from "../components/NotFoundPage";
import { LoadingState } from "../components/ui/QueryState";
import { RequireAdmin } from "../features/auth/RequireAdmin";
import { RequireAuth } from "../features/auth/RequireAuth";
import { useAuthStore } from "../features/auth/auth-store";
import { homePathFor } from "../lib/roles";
const AdminInvitesPage = lazy(() => import("../features/admin/InvitesPage").then((m) => ({ default: m.InvitesPage })));
const AdminPlansPage = lazy(() => import("../features/admin/PlansPage").then((m) => ({ default: m.PlansPage })));
const AdminSettingsPage = lazy(() => import("../features/admin/SettingsPage").then((m) => ({ default: m.SettingsPage })));
const AdminUsagePage = lazy(() => import("../features/admin/UsagePage").then((m) => ({ default: m.UsagePage })));
const AdminUserDetailPage = lazy(() => import("../features/admin/UserDetailPage").then((m) => ({ default: m.UserDetailPage })));
const AdminUsersPage = lazy(() => import("../features/admin/UsersPage").then((m) => ({ default: m.UsersPage })));
const AccountSecurityPage = lazy(() => import("../features/auth/AccountSecurityPage").then((m) => ({ default: m.AccountSecurityPage })));
const LoginPage = lazy(() => import("../features/auth/LoginPage").then((m) => ({ default: m.LoginPage })));
const WorkbenchPage = lazy(() => import("../features/dashboard/WorkbenchPage").then((m) => ({ default: m.WorkbenchPage })));
const EndpointsPage = lazy(() => import("../features/endpoints/EndpointsPage").then((m) => ({ default: m.EndpointsPage })));
const GeoIPPage = lazy(() => import("../features/geoip/GeoIPPage").then((m) => ({ default: m.GeoIPPage })));
const NodesPage = lazy(() => import("../features/nodes/NodesPage").then((m) => ({ default: m.NodesPage })));
const PlatformDetailPage = lazy(() => import("../features/platforms/PlatformDetailPage").then((m) => ({ default: m.PlatformDetailPage })));
const PlatformPage = lazy(() => import("../features/platforms/PlatformPage").then((m) => ({ default: m.PlatformPage })));
const RequestLogsPage = lazy(() => import("../features/requestLogs/RequestLogsPage").then((m) => ({ default: m.RequestLogsPage })));
const RulesPage = lazy(() => import("../features/rules/RulesPage").then((m) => ({ default: m.RulesPage })));
const SubscriptionPage = lazy(() => import("../features/subscriptions/SubscriptionPage").then((m) => ({ default: m.SubscriptionPage })));
const SystemConfigPage = lazy(() => import("../features/systemConfig/SystemConfigPage").then((m) => ({ default: m.SystemConfigPage })));
const JobsPage = lazy(() => import("../features/jobs/JobsPage").then((m) => ({ default: m.JobsPage })));
const AuditLogsPage = lazy(() => import("../features/audit/AuditLogsPage").then((m) => ({ default: m.AuditLogsPage })));
const MeGuidePage = lazy(() => import("../features/me/GuidePage").then((m) => ({ default: m.GuidePage })));
const MeKeysPage = lazy(() => import("../features/me/KeysPage").then((m) => ({ default: m.KeysPage })));
const MeOverviewPage = lazy(() => import("../features/me/OverviewPage").then((m) => ({ default: m.OverviewPage })));

/**
 * Route boundary.
 *
 * The `key` on the fallback replaces the whole region when the destination
 * changes, so a slow route never shows the previous page's content under a new
 * title. Focus movement after navigation is handled once, in `AppShell`.
 */
function Page({ children }: { children: ReactNode }) {
  const location = useLocation();
  return (
    <Suspense
      key={location.pathname}
      fallback={<LoadingState label="载入页面" className="py-24" />}
    >
      {children}
    </Suspense>
  );
}

/**
 * Landing route: the workbench for operators, the self-service surface for a
 * tenant session. Both roles share one bundle, so the choice is made here from
 * the session the server handed us.
 */
function HomeRedirect() {
  const session = useAuthStore((state) => state.session);
  return <Navigate to={homePathFor(session.user?.role)} replace />;
}

function LegacyQualityRedirect() {
  const location = useLocation();
  const previous = new URLSearchParams(location.search);
  const next = new URLSearchParams({ view: "exits" });
  for (const [oldKey, newKey] of [
    ["ip", "quality_ip"],
    ["q", "quality_q"],
    ["page", "quality_page"],
  ]) {
    const value = previous.get(oldKey);
    if (value) next.set(newKey, value);
  }
  return <Navigate to={"/nodes?" + next.toString()} replace />;
}

/** Management surfaces: refused to a tenant session by the server, kept out of their routing here. */
function AdminOnly({ children }: { children: ReactElement }) {
  return <RequireAdmin>{children}</RequireAdmin>;
}

export function AppRoutes() {
  return (
    <Routes>
      <Route
        path="/login"
        element={
          <Page>
            <LoginPage />
          </Page>
        }
      />
      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route path="/" element={<HomeRedirect />} />
        <Route
          element={
            <AdminOnly>
              <Outlet />
            </AdminOnly>
          }
        >
          <Route path="/dashboard" element={<Page><WorkbenchPage /></Page>} />
          <Route path="/platforms" element={<Page><PlatformPage /></Page>} />
          <Route path="/platforms/:platformId" element={<Page><PlatformDetailPage /></Page>} />
          <Route path="/subscriptions" element={<Page><SubscriptionPage /></Page>} />
          <Route path="/nodes" element={<Page><NodesPage /></Page>} />
          <Route path="/quality" element={<LegacyQualityRedirect />} />
          <Route path="/endpoints" element={<Page><EndpointsPage /></Page>} />
          <Route path="/rules" element={<Page><RulesPage /></Page>} />
          <Route path="/request-logs" element={<Page><RequestLogsPage /></Page>} />
          <Route path="/resources" element={<Page><GeoIPPage /></Page>} />
          <Route path="/system-config" element={<Page><SystemConfigPage /></Page>} />
          <Route path="/jobs" element={<Page><JobsPage /></Page>} />
          <Route path="/audit" element={<Page><AuditLogsPage /></Page>} />
          <Route path="/admin/users" element={<Page><AdminUsersPage /></Page>} />
          <Route path="/admin/users/:userId" element={<Page><AdminUserDetailPage /></Page>} />
          <Route path="/admin/plans" element={<Page><AdminPlansPage /></Page>} />
          <Route path="/admin/invites" element={<Page><AdminInvitesPage /></Page>} />
          <Route path="/admin/usage" element={<Page><AdminUsagePage /></Page>} />
          <Route path="/admin/settings" element={<Page><AdminSettingsPage /></Page>} />
        </Route>
        <Route path="/me" element={<Page><MeOverviewPage /></Page>} />
        <Route path="/me/keys" element={<Page><MeKeysPage /></Page>} />
        <Route path="/me/guide" element={<Page><MeGuidePage /></Page>} />
        <Route path="/account-security" element={<Page><AccountSecurityPage /></Page>} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  );
}
