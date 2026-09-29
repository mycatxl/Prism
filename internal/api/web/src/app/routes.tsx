import { lazy, Suspense, type ReactNode } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { AppShell } from "../components/AppShell";
import { NotFoundPage } from "../components/NotFoundPage";
import { LoadingState } from "../components/ui/QueryState";
import { RequireAuth } from "../features/auth/RequireAuth";

const LoginPage = lazy(() => import("../features/auth/LoginPage").then((m) => ({ default: m.LoginPage })));
const WorkbenchPage = lazy(() => import("../features/dashboard/WorkbenchPage").then((m) => ({ default: m.WorkbenchPage })));
const EndpointsPage = lazy(() => import("../features/endpoints/EndpointsPage").then((m) => ({ default: m.EndpointsPage })));
const GeoIPPage = lazy(() => import("../features/geoip/GeoIPPage").then((m) => ({ default: m.GeoIPPage })));
const IntelSettingsPage = lazy(() => import("../features/intelSettings/IntelSettingsPage").then((m) => ({ default: m.IntelSettingsPage })));
const NodesPage = lazy(() => import("../features/nodes/NodesPage").then((m) => ({ default: m.NodesPage })));
const PlatformDetailPage = lazy(() => import("../features/platforms/PlatformDetailPage").then((m) => ({ default: m.PlatformDetailPage })));
const PlatformPage = lazy(() => import("../features/platforms/PlatformPage").then((m) => ({ default: m.PlatformPage })));
const RequestLogsPage = lazy(() => import("../features/requestLogs/RequestLogsPage").then((m) => ({ default: m.RequestLogsPage })));
const RulesPage = lazy(() => import("../features/rules/RulesPage").then((m) => ({ default: m.RulesPage })));
const SubscriptionPage = lazy(() => import("../features/subscriptions/SubscriptionPage").then((m) => ({ default: m.SubscriptionPage })));
const SystemConfigPage = lazy(() => import("../features/systemConfig/SystemConfigPage").then((m) => ({ default: m.SystemConfigPage })));
const JobsPage = lazy(() => import("../features/jobs/JobsPage").then((m) => ({ default: m.JobsPage })));
const ExportsPage = lazy(() => import("../features/exports/ExportsPage").then((m) => ({ default: m.ExportsPage })));
const AuditLogsPage = lazy(() => import("../features/audit/AuditLogsPage").then((m) => ({ default: m.AuditLogsPage })));

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
        <Route path="/" element={<Navigate to="/dashboard" replace />} />
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
        <Route path="/intel-settings" element={<Page><IntelSettingsPage /></Page>} />
        <Route path="/system-config" element={<Page><SystemConfigPage /></Page>} />
        <Route path="/jobs" element={<Page><JobsPage /></Page>} />
        <Route path="/exports" element={<Page><ExportsPage /></Page>} />
        <Route path="/audit" element={<Page><AuditLogsPage /></Page>} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  );
}